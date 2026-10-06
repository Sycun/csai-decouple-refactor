package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// The RBAC ledger - accounts, roles, permissions, assignments and the access questions - moved out
// of the connection wrapper with its schema. These cases run against a real database: the schema
// guard proves a fresh install builds every object, and the roundtrips pin the SQL that moved
// verbatim (bootstrap, role resolution, the owner/assigned/parent access chain, atomic batches).

func testRBACStore(t *testing.T) (*RBAC, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "rbac.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r := NewRBAC(db)
	if err := r.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return r, db
}

func TestRBACEnsureSchemaIsIdempotentAndBuildsEveryObject(t *testing.T) {
	r, db := testRBACStore(t)
	if err := r.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	for _, object := range []struct{ kind, name string }{
		{"table", "rbac_users"}, {"table", "rbac_roles"}, {"table", "rbac_permissions"},
		{"table", "rbac_role_permissions"}, {"table", "rbac_user_roles"}, {"table", "rbac_resource_assignments"},
		{"index", "idx_rbac_user_roles_user"}, {"index", "idx_rbac_role_permissions_role"},
		{"index", "idx_rbac_assignments_user_resource"}, {"index", "idx_rbac_assignments_resource"},
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`,
			object.kind, object.name).Scan(&count); err != nil {
			t.Fatalf("look up %s %s: %v", object.kind, object.name, err)
		}
		if count != 1 {
			t.Fatalf("%s %s present %d times, want 1", object.kind, object.name, count)
		}
	}
}

func TestRBACBootstrapSeedsAdminAndSystemRoles(t *testing.T) {
	r, _ := testRBACStore(t)
	needs, err := r.RBACNeedsAdminPassword()
	if err != nil || !needs {
		t.Fatalf("RBACNeedsAdminPassword = %v/%v before bootstrap, want true", needs, err)
	}
	if err := r.BootstrapRBAC("hash-admin", map[string]string{"auth:self": "self", "project:read": "read"}); err != nil {
		t.Fatalf("BootstrapRBAC: %v", err)
	}
	if err := r.BootstrapRBAC("", map[string]string{"auth:self": "self", "project:read": "read"}); err != nil {
		t.Fatalf("second BootstrapRBAC: %v", err)
	}
	needs, err = r.RBACNeedsAdminPassword()
	if err != nil || needs {
		t.Fatalf("RBACNeedsAdminPassword = %v/%v after bootstrap, want false", needs, err)
	}
	admin, err := r.GetRBACUserByUsername("ADMIN")
	if err != nil || admin == nil || !admin.IsBuiltin || !admin.Enabled {
		t.Fatalf("admin user = %#v / %v", admin, err)
	}
	access, err := r.ResolveRBACAccess(admin.ID)
	if err != nil || access == nil {
		t.Fatalf("resolve admin: %v", err)
	}
	if !access.Permissions["auth:self"] || access.Scope != ScopeAll {
		t.Fatalf("admin access = %#v, want every seeded permission at scope all", access)
	}
}

func TestRBACAccessFollowsOwnerAssignedAndParentChains(t *testing.T) {
	r, db := testRBACStore(t)
	// The tables the parent chains read belong to other domains; seed the minimum they touch.
	for _, ddl := range []string{
		`CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT, status TEXT DEFAULT 'active', updated_at DATETIME, owner_user_id TEXT)`,
		`CREATE TABLE conversations (id TEXT PRIMARY KEY, project_id TEXT, title TEXT DEFAULT '', owner_user_id TEXT)`,
		`CREATE TABLE messages (id TEXT PRIMARY KEY, conversation_id TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("seed table: %v", err)
		}
	}
	alice, err := r.CreateRBACUser("alice", "Alice", "h", true, nil)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if err := r.SetResourceOwner("project", "p1", alice.ID); err != nil {
		t.Fatalf("set owner: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, owner_user_id) VALUES ('p1', 'P1', ?)`, alice.ID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO conversations (id, project_id) VALUES ('c1', 'p1')`); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO messages (id, conversation_id) VALUES ('m1', 'c1')`); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	if !r.UserCanAccessResource(alice.ID, ScopeOwn, "project", "p1") {
		t.Fatal("the owner must reach their own project")
	}
	if !r.UserCanAccessResource(alice.ID, ScopeOwn, "conversation", "c1") {
		t.Fatal("the conversation must be reachable through the owned project")
	}
	if !r.UserCanAccessMessage(alice.ID, ScopeOwn, "m1") {
		t.Fatal("the message must be reachable through its conversation's owned project")
	}
	// A second user reaches it only once assigned.
	bob, err := r.CreateRBACUser("bob", "Bob", "h", true, nil)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	if r.UserCanAccessResource(bob.ID, ScopeAssigned, "conversation", "c1") {
		t.Fatal("an unassigned user must not reach the conversation")
	}
	if _, err := r.AssignResourcesToUser(bob.ID, "conversation", []string{"c1"}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if !r.UserCanAccessResource(bob.ID, ScopeAssigned, "conversation", "c1") {
		t.Fatal("the assigned user must reach the conversation")
	}
	if r.UserCanAccessResource("", ScopeAll, "conversation", "c1") {
		t.Fatal("an empty user id fails closed even under scope all")
	}
}

func TestRBACAssignmentBatchIsAllOrNothing(t *testing.T) {
	r, _ := testRBACStore(t)
	user, err := r.CreateRBACUser("carol", "Carol", "h", true, nil)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	// No projects table at all: every probe fails, and nothing may be written.
	if _, err := r.AssignResourcesToUser(user.ID, "project", []string{"p1", "p2"}); err == nil {
		t.Fatal("assignment against a missing table must fail")
	}
	var count int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM rbac_resource_assignments`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("assignments = %d after a failed batch, want none", count)
	}
	if _, err := r.AssignResourcesToUser("missing-user", "project", []string{"p1"}); err == nil {
		t.Fatal("assignment for a missing user must fail")
	}
}

func TestRBACRoleCRUDRespectsSystemRoles(t *testing.T) {
	r, _ := testRBACStore(t)
	if err := r.BootstrapRBAC("hash", map[string]string{"auth:self": "self"}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	role, err := r.UpsertRBACRole("", "custom", "custom role", ScopeAssigned, []string{"auth:self"})
	if err != nil || role == nil {
		t.Fatalf("upsert: %v", err)
	}
	keys, err := r.ListRBACRolePermissionKeys(role.ID)
	if err != nil || len(keys) != 1 || keys[0] != "auth:self" {
		t.Fatalf("permission keys = %v / %v", keys, err)
	}
	if _, err := r.UpsertRBACRole(role.ID, "custom", "custom role", ScopeOwn, []string{"no:such"}); err == nil {
		t.Fatal("an unknown permission must be refused")
	}
	if err := r.DeleteRBACRole(RBACSystemRoleAdmin); err == nil {
		t.Fatal("a system role must not be deletable")
	}
	if err := r.DeleteRBACRole(role.ID); err != nil {
		t.Fatalf("delete custom role: %v", err)
	}
	roles, err := r.ListRBACRoles()
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	for _, listed := range roles {
		if listed.ID == role.ID {
			t.Fatalf("the deleted role is still listed: %#v", listed)
		}
	}
}

func TestRBACResourcePickerPageAndCountAgree(t *testing.T) {
	r, db := testRBACStore(t)
	if _, err := db.Exec(`CREATE TABLE assets (id TEXT PRIMARY KEY, host TEXT, domain TEXT, ip TEXT, protocol TEXT, port INTEGER, updated_at DATETIME)`); err != nil {
		t.Fatalf("seed assets: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO assets (id, host, protocol, port, updated_at) VALUES ('a1', '10.0.0.1', 'https', 443, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	options, err := r.ListAssignableRBACResourcesPage("asset", "10.0.0.1", 10, 0)
	if err != nil || len(options) != 1 {
		t.Fatalf("options = %#v / %v", options, err)
	}
	if options[0].ID != "a1" || options[0].Label == "" {
		t.Fatalf("option = %#v, want a1 with a normalised label", options[0])
	}
	total, err := r.CountAssignableRBACResources("asset", "10.0.0.1")
	if err != nil || total != 1 {
		t.Fatalf("count = %d / %v, want 1", total, err)
	}
	if _, err := r.ListAssignableRBACResourcesPage("secret_table", "", 10, 0); err == nil {
		t.Fatal("an unknown resource type must be refused")
	}
}
