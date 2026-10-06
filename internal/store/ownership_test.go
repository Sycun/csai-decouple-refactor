package store

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The data-layer item of the refactor plan is about ownership, not about where a
// file happens to sit: a table written from several places cannot be changed,
// tested or reasoned about as one thing. These are the tables this package owns,
// and the statements that reach them must exist here and nowhere else in
// production code.
//
// Test files in other packages are allowed to create fixtures directly: they set up
// a database, they are not a second write path.
// robot_user_bindings is deliberately NOT on this list yet: the alert-recipient query in
// internal/database/vulnerability_alert.go joins it to know which platform accounts to notify, and that
// query belongs to the vulnerability alert domain. It moves together with vulnerability_alert_subscriptions,
// so the table is claimed here once - by the store that will own both sides of the join - rather than
// half-claimed now and then reported as a leak.
func TestOwnedTablesAreOnlyWrittenFromThisPackage(t *testing.T) {
	root := moduleRoot(t)
	owned := []string{"hitl_interrupts", "hitl_conversation_configs", "notification_reads_by_user", "skill_stats", "chat_upload_artifacts", "audit_logs",
		"knowledge_retrieval_logs", "knowledge_base_items", "knowledge_embeddings", "model_token_usage",
		"robot_user_sessions", "robot_binding_codes", "c2_payload_artifacts"}
	pattern := regexp.MustCompile(`(?i)\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM|FROM|JOIN)\s+` + `(` + strings.Join(owned, "|") + `)`)

	var offenders []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "generated" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			inStore := strings.HasPrefix(filepath.ToSlash(rel), "internal/store/")
			if inStore || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if hit := pattern.FindString(string(data)); hit != "" {
				offenders = append(offenders, rel+": "+hit)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", dir, err)
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("statements against tables owned by internal/store leaked into production code: %v. "+
			"Add a method to the owning store instead of writing the table from elsewhere.", offenders)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test working directory")
		}
		dir = parent
	}
}

// TestFindingsTableHasOneWriter claims the findings table by writer rather than by reader.
//
// The owned-table scan above cannot carry this table: findings rows are also read by four other
// domains' aggregate queries (the asset risk trend, the project statistics, the RBAC resource search
// and the batch-task join inside the listing itself), and a claim that bans those reads would either
// be wrong or would have to be widened until it means nothing. So this table is claimed by the
// narrower, stronger question - who may change it.
//
// The count on the store side is asserted too, because a gate that passes on an empty set proves
// nothing: if the findings writes ever stop being here, that is the leak this test exists to see.
func TestFindingsTableHasOneWriter(t *testing.T) {
	root := moduleRoot(t)
	// The pattern is SQL-shaped on purpose: the permission catalogue in internal/security/rbac.go
	// literally says "Create and update vulnerabilities", and a keyword-plus-table match reports that
	// prose as a write. Requiring the syntax that follows the table name (the VALUES list, the SET
	// clause, the DELETE's own end of statement) is what keeps the scan about statements.
	// The word boundary belongs inside each alternative, not around the group: the insert is followed by
	// its column list, and a `\b` after a "(" never matches. That blind spot was found by the exact
	// count below, not by the leak check - the insert is the very statement this gate claims to see.
	writer := regexp.MustCompile(`(?i)\b(?:INSERT\s+INTO\s+vulnerabilities\s*\(|UPDATE\s+vulnerabilities\s+SET|DELETE\s+FROM\s+vulnerabilities\b)`)

	var offenders []string
	ownWrites := 0
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "generated" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			hits := writer.FindAllString(string(data), -1)
			if strings.HasPrefix(filepath.ToSlash(rel), "internal/store/") {
				ownWrites += len(hits)
				return nil
			}
			for _, hit := range hits {
				offenders = append(offenders, rel+": "+hit)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", dir, err)
		}
	}

	// Six statements as this slice landed: the insert, the update, the two deletes (single row and by
	// filter), the retired-conversation source stamp and the project unlink. The count is an exact
	// expectation rather than a floor because the claim being pinned is "these and only these change
	// the table"; a new write belongs here deliberately, with the sentence above it updated.
	if ownWrites != 6 {
		t.Fatalf("internal/store writes the findings table in %d statements, want exactly 6 - "+
			"a scan that finds fewer (or none) here is not a claim about ownership", ownWrites)
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("the findings table is written from outside internal/store: %v. "+
			"The lifecycle steps that need a finding touched (conversation retirement, project deletion) "+
			"call store.Vulnerabilities for it; add a method there instead of writing the table here.", offenders)
	}
}
