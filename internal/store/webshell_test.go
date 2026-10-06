package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// The webshell connection and state rows came over from the connection wrapper together with both
// tables' schema. These cases run against a real database, and the legacy-row case is the reason the
// read list COALESCEs four columns.

func newWebshellStore(t *testing.T, dsn string) (*Webshell, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w := NewWebshell(db)
	if err := w.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return w, db
}

func testWebshellStore(t *testing.T) (*Webshell, *sql.DB) {
	t.Helper()
	return newWebshellStore(t, filepath.Join(t.TempDir(), "webshell.db"))
}

func putConnection(t *testing.T, w *Webshell, id, projectID, url, remark string, created time.Time) {
	t.Helper()
	if err := w.Create(&WebShellConnection{ID: id, ProjectID: projectID, URL: url, Password: "p",
		Type: "php", Method: "post", Remark: remark, CreatedAt: created}); err != nil {
		t.Fatalf("seed connection %s: %v", id, err)
	}
}

func TestWebshellEnsureSchemaIsIdempotentAndBuildsEveryObject(t *testing.T) {
	w, db := testWebshellStore(t)
	if err := w.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	for _, object := range []struct{ kind, name string }{
		{"table", "webshell_connections"}, {"table", "webshell_connection_states"},
		{"index", "idx_webshell_connections_created_at"}, {"index", "idx_webshell_connections_project_id"},
		{"index", "idx_webshell_connection_states_updated_at"},
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

func TestWebshellStateReads(t *testing.T) {
	w, _ := testWebshellStore(t)
	// Absent answers "{}" rather than an error or an empty string: the front end parses the object.
	if got, err := w.GetState("ws_missing"); err != nil || got != "{}" {
		t.Fatalf("absent state = %q (%v), want \"{}\"", got, err)
	}
	if err := w.UpsertState("ws_1", `{"tabs":["a"]}`); err != nil {
		t.Fatalf("upsert state: %v", err)
	}
	got, err := w.GetState("ws_1")
	if err != nil || got != `{"tabs":["a"]}` {
		t.Fatalf("state = %q (%v)", got, err)
	}
	// The same key overwrites: it is an upsert, not an append-only history.
	if err := w.UpsertState("ws_1", `{"tabs":[]}`); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if got, _ = w.GetState("ws_1"); got != `{"tabs":[]}` {
		t.Fatalf("state after overwrite = %q", got)
	}
	// An empty payload is stored as the empty object, so a later read never has to special-case it.
	if err := w.UpsertState("ws_2", ""); err != nil {
		t.Fatalf("upsert empty: %v", err)
	}
	if got, err = w.GetState("ws_2"); err != nil || got != "{}" {
		t.Fatalf("empty payload read back as %q (%v), want \"{}\"", got, err)
	}
}

func TestWebshellCRUDRoundTripAndUnknownIDAnswers(t *testing.T) {
	w, _ := testWebshellStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	putConnection(t, w, "ws_a", "p1", "http://a/shell", "first", now)

	got, err := w.Get("ws_a")
	if err != nil || got == nil {
		t.Fatalf("get = %+v (%v)", got, err)
	}
	if got.ProjectID != "p1" || got.Remark != "first" || !got.CreatedAt.Equal(now) {
		t.Fatalf("get echoed %+v, want the seeded values", got)
	}
	// Unknown id is (nil, nil): the authorization helpers decide 403 vs 404 from that.
	missing, err := w.Get("ws_none")
	if err != nil || missing != nil {
		t.Fatalf("unknown id = %+v (%v), want (nil, nil)", missing, err)
	}

	got.Remark = "renamed"
	got.Encoding = "gbk"
	if err := w.Update(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, err := w.Get("ws_a")
	if err != nil || after.Remark != "renamed" || after.Encoding != "gbk" {
		t.Fatalf("after update = %+v (%v)", after, err)
	}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"update unknown", w.Update(&WebShellConnection{ID: "ws_none", URL: "u"})},
		{"delete unknown", w.Delete("ws_none")},
	} {
		if tc.err != sql.ErrNoRows {
			t.Fatalf("%s = %v, want sql.ErrNoRows", tc.name, tc.err)
		}
	}
	if err := w.Delete("ws_a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if after, err := w.Get("ws_a"); err != nil || after != nil {
		t.Fatalf("deleted connection still readable: %+v (%v)", after, err)
	}
}

// TestWebshellStateCascadesWithTheConnection is the reason both tables have one owner: the delete is
// expected to leave no state row behind, and the foreign key is in this store's own schema.
func TestWebshellStateCascadesWithTheConnection(t *testing.T) {
	w, db := newWebshellStore(t, "file:"+filepath.Join(t.TempDir(), "webshell-fk.db")+"?_foreign_keys=1")
	putConnection(t, w, "ws_a", "", "http://a", "a", time.Now())
	if err := w.UpsertState("ws_a", `{"x":1}`); err != nil {
		t.Fatalf("upsert state: %v", err)
	}
	if err := w.Delete("ws_a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM webshell_connection_states`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d state rows left after the connection was deleted, want none", left)
	}
}

func TestWebshellListFiltersByProjectAndAccess(t *testing.T) {
	w, _ := testWebshellStore(t)
	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	putConnection(t, w, "ws_p1", "p1", "http://1", "in p1", base.Add(-time.Hour))
	putConnection(t, w, "ws_unbound", "  ", "http://2", "no project", base)
	putConnection(t, w, "ws_p2", "p2", "http://3", "in p2", base.Add(-2*time.Hour))
	if _, err := w.db.Exec(`UPDATE webshell_connections SET owner_user_id = 'u1' WHERE id IN ('ws_p1','ws_unbound')`); err != nil {
		// owner_user_id is not part of this store's schema: it is added by the RBAC migration.
		if !strings.Contains(err.Error(), "owner_user_id") {
			t.Fatalf("stamp owner: %v", err)
		}
	}

	// owner_user_id and the assignment table are added by the RBAC migration, not by this store's
	// schema - the visibility predicate below therefore depends on another domain's column. Stamped
	// here the same way that migration does.
	if _, err := w.db.Exec(`ALTER TABLE webshell_connections ADD COLUMN owner_user_id TEXT`); err != nil {
		t.Fatalf("add the ownership column: %v", err)
	}
	if _, err := w.db.Exec(`CREATE TABLE rbac_resource_assignments (user_id TEXT, resource_type TEXT, resource_id TEXT)`); err != nil {
		t.Fatalf("create the assignment table: %v", err)
	}
	if _, err := w.db.Exec(`UPDATE webshell_connections SET owner_user_id = 'u1' WHERE id IN ('ws_p1','ws_unbound')`); err != nil {
		t.Fatalf("stamp owners: %v", err)
	}
	if _, err := w.db.Exec(`INSERT INTO rbac_resource_assignments (user_id, resource_type, resource_id) VALUES ('u2','webshell','ws_p2')`); err != nil {
		t.Fatalf("assign a connection: %v", err)
	}

	all := Access{UserID: "u1", Scope: ScopeAll}
	if got, err := w.List(all, ""); err != nil || len(got) != 3 {
		t.Fatalf("scope=all, no project filter = %d rows (%v), want 3", len(got), err)
	}
	// Newest first, whatever the insertion order was.
	got, err := w.List(all, "")
	if err != nil || got[0].ID != "ws_unbound" || got[2].ID != "ws_p2" {
		t.Fatalf("order = %+v (%v), want created_at DESC", got, err)
	}
	if one, err := w.List(all, "p1"); err != nil || len(one) != 1 || one[0].ID != "ws_p1" {
		t.Fatalf("project p1 = %+v (%v), want only ws_p1", one, err)
	}
	unbound, err := w.List(all, ProjectUnbound)
	if err != nil || len(unbound) != 1 || unbound[0].ID != "ws_unbound" {
		t.Fatalf("unbound = %+v (%v), want the blank-project row", unbound, err)
	}

	// Owner sees their two; the assigned caller sees the one assigned to them; an unrelated caller
	// sees nothing.
	if mine, err := w.List(Access{UserID: "u1", Scope: ScopeOwn}, ""); err != nil || len(mine) != 2 {
		t.Fatalf("u1 own = %+v (%v), want its two connections", mine, err)
	}
	if theirs, err := w.List(Access{UserID: "u2", Scope: ScopeAssigned}, ""); err != nil || len(theirs) != 1 || theirs[0].ID != "ws_p2" {
		t.Fatalf("u2 assigned = %+v (%v), want only ws_p2", theirs, err)
	}
	if nobody, err := w.List(Access{UserID: "u3", Scope: ScopeOwn}, ""); err != nil || len(nobody) != 0 {
		t.Fatalf("unrelated caller = %+v (%v), want none", nobody, err)
	}
	// Pinned as it behaves today, not as it should: an empty user id adds no predicate at all, so a
	// caller with no session but a non-"all" scope sees every connection. The shared access helpers
	// answer the opposite (ConstrainConversation turns an empty id into 1=0), so the two paths
	// disagree. Recorded in the research doc as a defect to fix on its own, in a change that is
	// allowed to alter reachability.
	if empty, err := w.List(Access{UserID: "  ", Scope: ScopeOwn}, ""); err != nil || len(empty) != 3 {
		t.Fatalf("empty user id = %d rows (%v), want the pre-existing unrestricted answer of 3", len(empty), err)
	}
}

// TestWebshellLegacyRowWithoutTheAddedColumnsStillLoads pins the COALESCEs. A row written before
// encoding/os/project_id existed holds NULL; scanning that into string fails the row, and a listing
// that drops rows on a scan error is how connections disappear from the page with a warning as the
// only trace.
func TestWebshellLegacyRowWithoutTheAddedColumnsStillLoads(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE webshell_connections (
		id TEXT PRIMARY KEY, url TEXT NOT NULL, password TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT 'php', method TEXT NOT NULL DEFAULT 'post',
		cmd_param TEXT NOT NULL DEFAULT '', remark TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create the legacy table: %v", err)
	}
	w := NewWebshell(db)
	// The row predates the added columns: project_id arrives as NULL, because that ALTER carries no
	// default. encoding and os arrive NOT NULL DEFAULT '', so they are the empty string here - which
	// is why the read list COALESCEs them anyway: a row written by an older build of this schema is
	// exactly what a scan into string would refuse.
	if _, err := db.Exec(`INSERT INTO webshell_connections (id, url, created_at) VALUES ('ws_old', 'http://old', '2026-01-01 00:00:00')`); err != nil {
		t.Fatalf("insert a legacy row: %v", err)
	}
	if err := w.MigrateConnectionsTable(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	list, err := w.List(Access{Scope: ScopeAll}, "")
	if err != nil {
		t.Fatalf("list over a legacy row: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d rows, want the legacy row kept", len(list))
	}
	if list[0].Encoding != "" || list[0].OS != "" || list[0].ProjectID != "" {
		t.Fatalf("legacy nulls read as %+v, want empty strings", list[0])
	}
	// Idempotent: the columns are already there.
	if err := w.MigrateConnectionsTable(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestWebshellUnlinkProjectClearsOnlyThatProject(t *testing.T) {
	w, _ := testWebshellStore(t)
	now := time.Now()
	putConnection(t, w, "ws_p1", "p1", "http://1", "a", now)
	putConnection(t, w, "ws_p1b", "p1", "http://1b", "b", now)
	putConnection(t, w, "ws_p2", "p2", "http://2", "c", now)

	if err := w.UnlinkProject("p1"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	for _, tc := range []struct{ id, want string }{{"ws_p1", ""}, {"ws_p1b", ""}, {"ws_p2", "p2"}} {
		got, err := w.Get(tc.id)
		if err != nil || got == nil {
			t.Fatalf("read %s: %+v (%v)", tc.id, got, err)
		}
		if got.ProjectID != tc.want {
			t.Fatalf("%s project = %q, want %q", tc.id, got.ProjectID, tc.want)
		}
	}
}

func TestWebshellRefusesAConnectionlessHandle(t *testing.T) {
	w := NewWebshell(nil)
	if err := w.EnsureSchema(); err == nil {
		t.Fatal("EnsureSchema answered no error")
	}
	if _, err := w.Get("ws_1"); err == nil {
		t.Fatal("Get answered no error")
	}
	if _, err := w.GetState("ws_1"); err == nil {
		t.Fatal("GetState answered no error")
	}
	if _, err := w.List(Access{Scope: ScopeAll}, ""); err == nil {
		t.Fatal("List answered no error")
	}
	if err := w.Create(&WebShellConnection{ID: "ws_1"}); err == nil {
		t.Fatal("Create answered no error")
	}
	if err := w.Update(&WebShellConnection{ID: "ws_1"}); err == nil {
		t.Fatal("Update answered no error")
	}
	if err := w.Delete("ws_1"); err == nil {
		t.Fatal("Delete answered no error")
	}
	if err := w.UpsertState("ws_1", "{}"); err == nil {
		t.Fatal("UpsertState answered no error")
	}
	if err := w.UnlinkProject("p1"); err == nil {
		t.Fatal("UnlinkProject answered no error")
	}
	if err := w.MigrateConnectionsTable(); err == nil {
		t.Fatal("MigrateConnectionsTable answered no error")
	}
}
