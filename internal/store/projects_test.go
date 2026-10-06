package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// The project row - create / list / access filters / delete with its four cross-domain unlinks, the
// counters, the dashboard aggregate and the last-activity stamp - moved out of the connection
// wrapper. These cases run against a real database and build the schema through the same store
// phases the boot path calls, so the fixture walks the three phases it is claiming.

func testProjectsStore(t *testing.T) (*Projects, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "projects.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := NewProjects(db).EnsureSchema(); err != nil {
		t.Fatalf("projects schema: %v", err)
	}
	if err := NewProjects(db).MigrateLateColumns(); err != nil {
		t.Fatalf("projects late columns: %v", err)
	}
	if err := NewProjects(db).EnsureIndexes(); err != nil {
		t.Fatalf("projects indexes: %v", err)
	}
	if err := NewConversations(db).EnsureSchema(); err != nil {
		t.Fatalf("conversations schema: %v", err)
	}
	if err := NewConversations(db).MigrateLateColumns(); err != nil {
		t.Fatalf("conversations late columns: %v", err)
	}
	if err := NewRBAC(db).EnsureSchema(); err != nil {
		t.Fatalf("rbac schema: %v", err)
	}
	return NewProjects(db), db
}

func TestProjectsCrudAndLastActivity(t *testing.T) {
	s, _ := testProjectsStore(t)
	created, err := s.CreateProject(&Project{Name: "alpha"})
	if err != nil || created == nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.Status != "active" {
		t.Fatalf("create did not fill defaults: %#v", created)
	}
	got, err := s.GetProject(created.ID)
	if err != nil || got == nil || got.Name != "alpha" {
		t.Fatalf("get: %#v/%v", got, err)
	}
	name, err := s.GetProjectName(created.ID)
	if err != nil || name != "alpha" {
		t.Fatalf("name = %q/%v", name, err)
	}
	created.Name = "renamed"
	created.Pinned = true
	if err := s.UpdateProject(created); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = s.GetProject(created.ID)
	if got.Name != "renamed" || !got.Pinned {
		t.Fatalf("update lost: %#v", got)
	}
	activity, ok, err := s.ProjectLastActivity(created.ID)
	if err != nil || !ok || activity.IsZero() {
		t.Fatalf("last activity = %v/%v/%v", activity, ok, err)
	}
	if _, ok, _ := s.ProjectLastActivity("missing"); ok {
		t.Fatal("a missing project must answer ok=false")
	}
}

func TestProjectsAccessFiltersRespectOwnershipAndAssignments(t *testing.T) {
	s, db := testProjectsStore(t)
	r := NewRBAC(db)
	alice, err := r.CreateRBACUser("alice", "Alice", "h", true, nil)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	owned, _ := s.CreateProject(&Project{Name: "owned"})
	assigned, _ := s.CreateProject(&Project{Name: "assigned"})
	hidden, _ := s.CreateProject(&Project{Name: "hidden"})
	if _, err := db.Exec(`UPDATE projects SET owner_user_id = ? WHERE id = ?`, alice.ID, owned.ID); err != nil {
		t.Fatalf("stamp owner: %v", err)
	}
	if err := r.AssignResourceToUser(alice.ID, "project", assigned.ID); err != nil {
		t.Fatalf("assign: %v", err)
	}
	list, err := s.ListProjectsForAccess("", "", 10, 0, alice.ID, ScopeAssigned)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %#v/%v, want the owned and the assigned one", list, err)
	}
	total, err := s.CountProjectsForAccess("", "", alice.ID, ScopeAssigned)
	if err != nil || total != 2 {
		t.Fatalf("count = %d/%v", total, err)
	}
	all, err := s.ListProjectsForAccess("", "", 10, 0, "", ScopeAll)
	if err != nil || len(all) != 3 {
		t.Fatalf("scope all = %d/%v", len(all), err)
	}
	if _, err := s.GetProject(hidden.ID); err != nil {
		t.Fatalf("the row itself stays readable: %v", err)
	}
}

func TestProjectsStatsAndDashboardCounts(t *testing.T) {
	s, db := testProjectsStore(t)
	if err := NewFacts(db).EnsureSchema(); err != nil {
		t.Fatalf("facts schema: %v", err)
	}
	if err := NewVulnerabilities(db, nil).EnsureSchema(); err != nil {
		t.Fatalf("findings schema: %v", err)
	}
	project, _ := s.CreateProject(&Project{Name: "p"})
	if _, err := db.Exec(`INSERT INTO vulnerabilities (id, title, severity, project_id) VALUES ('v1', 'x', 'high', ?)`, project.ID); err != nil {
		t.Fatalf("seed vulnerability: %v", err)
	}
	conv, err := NewConversations(db).CreateConversation("c", ConversationCreateMeta{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	stats, err := s.GetProjectStatsCounts(project.ID)
	if err != nil || stats == nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.VulnCount != 1 || stats.ConversationCount != 1 || stats.FactCount != 0 {
		t.Fatalf("stats = %#v", stats)
	}
	if _, err := s.GetProjectStatsCounts("missing"); err == nil {
		t.Fatal("stats for a missing project must fail")
	}
	convs, err := NewConversations(db).ListConversationsByProjectID(project.ID, 10, 0)
	if err != nil || len(convs) != 1 || convs[0].ID != conv.ID {
		t.Fatalf("by project = %#v/%v", convs, err)
	}
	if n, err := NewConversations(db).CountConversationsByProjectID(project.ID); err != nil || n != 1 {
		t.Fatalf("count by project = %d/%v", n, err)
	}
	summary, err := s.GetProjectDashboardSummaryForAccess(5, "", ScopeAll)
	if err != nil || summary == nil || summary.Totals.ActiveProjects != 1 {
		t.Fatalf("dashboard = %#v/%v", summary, err)
	}
}

func TestProjectsDeleteUnlinksTheOtherDomains(t *testing.T) {
	s, db := testProjectsStore(t)
	// The four domains DeleteProject reaches are the stores that own their tables' SQL; each needs
	// its own schema for the unlink to run against.
	if err := NewVulnerabilities(db, nil).EnsureSchema(); err != nil {
		t.Fatalf("findings schema: %v", err)
	}
	if err := NewAssets(db).EnsureSchema(); err != nil {
		t.Fatalf("assets schema: %v", err)
	}
	if err := NewWebshell(db).EnsureSchema(); err != nil {
		t.Fatalf("webshell schema: %v", err)
	}
	if err := NewC2(db).EnsureSchema(); err != nil {
		t.Fatalf("c2 schema: %v", err)
	}
	project, err := s.CreateProject(&Project{Name: "doomed"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO vulnerabilities (id, title, severity, project_id) VALUES ('v1', 'x', 'high', ?)`, project.ID); err != nil {
		t.Fatalf("seed vulnerability: %v", err)
	}
	if err := NewC2(db).CreateC2Listener(&C2Listener{ID: "l1", ProjectID: project.ID, Name: "l", Type: "http_beacon", BindPort: 9001}); err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	if err := s.DeleteProject(project.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetProject(project.ID); err == nil {
		t.Fatal("the project row survived the delete")
	}
	var projectID sql.NullString
	if err := db.QueryRow(`SELECT project_id FROM vulnerabilities WHERE id = 'v1'`).Scan(&projectID); err != nil {
		t.Fatalf("read vulnerability: %v", err)
	}
	if projectID.Valid && projectID.String != "" {
		t.Fatalf("the finding kept its project link: %q", projectID.String)
	}
	listener, err := NewC2(db).GetC2Listener("l1")
	if err != nil || listener == nil || listener.ProjectID != "" {
		t.Fatalf("the listener kept its project link: %#v/%v", listener, err)
	}
}
