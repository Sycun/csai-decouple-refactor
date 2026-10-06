package database_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// The findings table had two copies of its visibility rule: the data layer's own
// appendVulnerabilityAccessFilter and store.ConstrainFinding, used by the notification digest.
// Byte-for-byte the same six reachability paths - except that the data layer's copy added **no
// constraint** when the caller had no user id. These cases are why one of them could be deleted
// rather than paraphrased a third time.

// legacyFindingClause is the wording the extraction replaced, pinned here verbatim so the
// comparison is against what was in production, not against the new code's own shape.
const legacyFindingClause = ` AND (
	owner_user_id = ?
	OR EXISTS (
		SELECT 1 FROM rbac_resource_assignments ra
		WHERE ra.user_id = ? AND ra.resource_type = 'vulnerability' AND ra.resource_id = vulnerabilities.id
	)
	OR (
		project_id IS NOT NULL AND project_id <> '' AND (
			EXISTS (SELECT 1 FROM projects p WHERE p.id = vulnerabilities.project_id AND p.owner_user_id = ?)
			OR EXISTS (
				SELECT 1 FROM rbac_resource_assignments pra
				WHERE pra.user_id = ? AND pra.resource_type = 'project' AND pra.resource_id = vulnerabilities.project_id
			)
		)
	)
	OR (
		conversation_id IS NOT NULL AND conversation_id <> '' AND (
			EXISTS (SELECT 1 FROM conversations c WHERE c.id = vulnerabilities.conversation_id AND c.owner_user_id = ?)
			OR EXISTS (
				SELECT 1 FROM rbac_resource_assignments cra
				WHERE cra.user_id = ? AND cra.resource_type = 'conversation' AND cra.resource_id = vulnerabilities.conversation_id
			)
		)
	)
)`

func newFindingParityBase(t *testing.T) (*database.DB, map[string]string) {
	t.Helper()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "access-parity.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.BootstrapRBAC("hash", security.PermissionCatalog); err != nil {
		t.Fatal(err)
	}
	users := map[string]string{}
	for _, name := range []string{"finding-owner", "finding-assigned", "project-owner", "project-assigned", "conv-owner", "conv-assigned", "nobody"} {
		u, err := db.CreateRBACUser(name, name, "hash", true, nil)
		if err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		users[name] = u.ID
	}

	projectOwned, err := db.CreateProject(&database.Project{Name: "owned project"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetResourceOwner("project", projectOwned.ID, users["project-owner"]); err != nil {
		t.Fatal(err)
	}
	projectAssigned, err := db.CreateProject(&database.Project{Name: "assigned project"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AssignResourceToUser(users["project-assigned"], "project", projectAssigned.ID); err != nil {
		t.Fatal(err)
	}
	convOwned, err := db.CreateConversation("owned conversation", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetResourceOwner("conversation", convOwned.ID, users["conv-owner"]); err != nil {
		t.Fatal(err)
	}
	convAssigned, err := db.CreateConversation("assigned conversation", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AssignResourceToUser(users["conv-assigned"], "conversation", convAssigned.ID); err != nil {
		t.Fatal(err)
	}

	// tag is stored in the title so the expectation per row is readable in the seed itself.
	seed := []struct {
		title     string
		owner     string
		project   string
		conv      string
		assignees []string
	}{
		{title: "f-direct", owner: users["finding-owner"]},
		{title: "f-assigned", assignees: []string{users["finding-assigned"]}},
		{title: "f-project-owned", project: projectOwned.ID},
		{title: "f-project-assigned", project: projectAssigned.ID},
		{title: "f-conv-owned", conv: convOwned.ID},
		{title: "f-conv-assigned", conv: convAssigned.ID},
		{title: "f-orphan"},
	}
	for _, s := range seed {
		v, err := database.NewFindings(db).Create(&store.Vulnerability{Title: s.title, Severity: "high", ProjectID: s.project, ConversationID: s.conv})
		if err != nil {
			t.Fatalf("seed %s: %v", s.title, err)
		}
		if s.owner != "" {
			if err := db.SetResourceOwner("vulnerability", v.ID, s.owner); err != nil {
				t.Fatalf("owner %s: %v", s.title, err)
			}
		}
		for _, a := range s.assignees {
			if err := db.AssignResourceToUser(a, "vulnerability", v.ID); err != nil {
				t.Fatalf("assign %s: %v", s.title, err)
			}
		}
	}
	return db, users
}

// Identical answers for every identified caller, under both scopes the console uses.
func TestFindingAccessClausesAnswerTheSame(t *testing.T) {
	db, users := newFindingParityBase(t)
	byUser := map[string][]string{
		"finding-owner":    {"f-direct"},
		"finding-assigned": {"f-assigned"},
		"project-owner":    {"f-project-owned"},
		"project-assigned": {"f-project-assigned"},
		"conv-owner":       {"f-conv-owned"},
		"conv-assigned":    {"f-conv-assigned"},
		"nobody":           {},
	}
	for name, want := range byUser {
		for _, scope := range []string{database.RBACScopeOwn, database.RBACScopeAssigned} {
			access := store.Access{UserID: users[name], Scope: scope}
			got, err := database.NewFindings(db).List(100, 0, store.VulnerabilityListFilter{}, access)
			if err != nil {
				t.Fatalf("%s/%s: %v", name, scope, err)
			}
			titles := []string{}
			for _, v := range got {
				titles = append(titles, v.Title)
			}
			sort.Strings(titles)
			if strings.Join(titles, ",") != strings.Join(want, ",") {
				t.Fatalf("%s/%s: the store clause answered %v, want %v", name, scope, titles, want)
			}
			legacy := legacyVisibleTitles(t, db, users[name])
			if strings.Join(legacy, ",") != strings.Join(titles, ",") {
				t.Fatalf("%s/%s: the two wordings disagree: store %v, extracted %v", name, scope, titles, legacy)
			}
		}
	}
}

func legacyVisibleTitles(t *testing.T, db *database.DB, userID string) []string {
	t.Helper()
	rows, err := db.DB.Query(`SELECT title FROM vulnerabilities WHERE 1=1`+legacyFindingClause,
		userID, userID, userID, userID, userID, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatal(err)
		}
		out = append(out, title)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// An unrestricted scope is the same answer in both wordings: no clause at all.
func TestFindingAccessUnrestrictedScopeMatches(t *testing.T) {
	db, users := newFindingParityBase(t)
	all, err := database.NewFindings(db).List(100, 0, store.VulnerabilityListFilter{}, store.Access{Scope: database.RBACScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 7 {
		t.Fatalf("scope all saw %d findings, want all 7 without needing a user id", len(all))
	}
	if legacy := legacyVisibleTitles(t, db, users["nobody"]); len(legacy) != 0 {
		t.Fatalf("the seed leaked to a stranger: %v", legacy)
	}
}

// The one deliberate difference. With no user id, the extracted wording added nothing and the caller
// read the whole base; the store's clause answers 1=0. Routes authenticate first, so this is not a
// reachable path today - it is the SQL-level default being moved to fail closed.
func TestFindingAccessWithoutAnIdentityFailsClosed(t *testing.T) {
	db, _ := newFindingParityBase(t)
	got, err := database.NewFindings(db).List(100, 0, store.VulnerabilityListFilter{}, store.Access{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a caller with no identity read %d findings, want none", len(got))
	}
	// What the extracted code actually did with an empty id: it added no clause at all, so the caller
	// read the whole base. That is the behaviour being narrowed, and it is worth keeping visible here
	// rather than describing it in a comment nobody can run.
	unconstrained, err := db.DB.Query(`SELECT title FROM vulnerabilities WHERE 1=1`)
	if err != nil {
		t.Fatal(err)
	}
	defer unconstrained.Close()
	var wouldHaveSeen []string
	for unconstrained.Next() {
		var title string
		if err := unconstrained.Scan(&title); err != nil {
			t.Fatal(err)
		}
		wouldHaveSeen = append(wouldHaveSeen, title)
	}
	if err := unconstrained.Err(); err != nil {
		t.Fatal(err)
	}
	if len(wouldHaveSeen) != 7 {
		t.Fatalf("the extracted wording would have returned %d findings for an empty id, want the whole base", len(wouldHaveSeen))
	}
}
