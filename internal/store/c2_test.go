package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// The C2 ledger - listeners, sessions, tasks, files, events and malleable profiles - moved out of
// the connection wrapper together with its schema. These cases run against a real database: the
// schema guard proves a fresh install builds every object (including the index that sits on a
// backfilled column), and the roundtrips pin the SQL that moved verbatim.

func testC2Store(t *testing.T) (*C2, *sql.DB) {
	t.Helper()
	// The cascade cases need foreign keys enforced, which is what the production DSN sets.
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "c2.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := NewC2(db)
	if err := c.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := c.MigrateListenerColumns(); err != nil {
		t.Fatalf("MigrateListenerColumns: %v", err)
	}
	if err := c.EnsureIndexes(); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	return c, db
}

func TestC2EnsureSchemaIsIdempotentAndBuildsEveryObject(t *testing.T) {
	c, db := testC2Store(t)
	if err := c.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	if err := c.MigrateListenerColumns(); err != nil {
		t.Fatalf("second MigrateListenerColumns: %v", err)
	}
	if err := c.EnsureIndexes(); err != nil {
		t.Fatalf("second EnsureIndexes: %v", err)
	}
	for _, object := range []struct{ kind, name string }{
		{"table", "c2_listeners"}, {"table", "c2_sessions"}, {"table", "c2_tasks"},
		{"table", "c2_files"}, {"table", "c2_events"}, {"table", "c2_profiles"},
		{"index", "idx_c2_listeners_created_at"}, {"index", "idx_c2_listeners_project_id"},
		{"index", "idx_c2_listeners_status"}, {"index", "idx_c2_sessions_listener"},
		{"index", "idx_c2_sessions_status"}, {"index", "idx_c2_sessions_last_check_in"},
		{"index", "idx_c2_tasks_session"}, {"index", "idx_c2_tasks_status"},
		{"index", "idx_c2_tasks_created_at"}, {"index", "idx_c2_tasks_conversation"},
		{"index", "idx_c2_files_session"}, {"index", "idx_c2_events_created_at"},
		{"index", "idx_c2_events_category"}, {"index", "idx_c2_events_session"},
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

func TestC2MigrateListenerColumnsAddsTheLateColumn(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "c2.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := NewC2(db)
	// A database written by the first release: c2_listeners without the project binding.
	if _, err := db.Exec(`CREATE TABLE c2_listeners (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, bind_port INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT 'stopped', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed legacy table: %v", err)
	}
	if err := c.MigrateListenerColumns(); err != nil {
		t.Fatalf("MigrateListenerColumns: %v", err)
	}
	if err := c.MigrateListenerColumns(); err != nil {
		t.Fatalf("second MigrateListenerColumns: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('c2_listeners') WHERE name = 'project_id'`).Scan(&count); err != nil {
		t.Fatalf("read pragma: %v", err)
	}
	if count != 1 {
		t.Fatalf("project_id present %d times after the backfill, want 1", count)
	}
}

func TestC2SessionUpsertKeepsOneRowPerImplant(t *testing.T) {
	c, _ := testC2Store(t)
	if err := c.CreateC2Listener(&C2Listener{ID: "l_1", Name: "l", Type: "http_beacon", BindPort: 9001}); err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	first := time.Now().Add(-time.Minute)
	if err := c.UpsertC2Session(&C2Session{ID: "s_first", ListenerID: "l_1", ImplantUUID: "u-1",
		Hostname: "old", Status: "active", FirstSeenAt: first, LastCheckIn: first}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := c.UpsertC2Session(&C2Session{ID: "s_second", ListenerID: "l_1", ImplantUUID: "u-1",
		Hostname: "new", Status: "sleeping", FirstSeenAt: first, LastCheckIn: time.Now()}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	session, err := c.GetC2SessionByImplantUUID("u-1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if session == nil || session.ID != "s_first" || session.Hostname != "new" || session.Status != "sleeping" {
		t.Fatalf("upsert kept %#v, want the first id with the second hostname/status", session)
	}
	list, err := c.ListC2Sessions(ListC2SessionsFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("sessions = %d, want one row per implant", len(list))
	}
}

func TestC2DeleteListenerCascadesToChildren(t *testing.T) {
	c, _ := testC2Store(t)
	now := time.Now()
	if err := c.CreateC2Listener(&C2Listener{ID: "l_1", Name: "l", Type: "http_beacon", BindPort: 9001}); err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	if err := c.UpsertC2Session(&C2Session{ID: "s_1", ListenerID: "l_1", ImplantUUID: "u-1",
		Status: "active", FirstSeenAt: now, LastCheckIn: now}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := c.CreateC2Task(&C2Task{ID: "t_1", SessionID: "s_1", TaskType: "shell"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if err := c.CreateC2File(&C2File{ID: "f_1", SessionID: "s_1", Direction: "download"}); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := c.DeleteC2Listener("l_1"); err != nil {
		t.Fatalf("delete listener: %v", err)
	}
	for _, table := range []string{"c2_sessions", "c2_tasks", "c2_files"} {
		var count int
		if err := c.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s kept %d rows after the listener was deleted, want the cascade to empty it", table, count)
		}
	}
}

func TestC2PopQueuedTasksClaimsAtomically(t *testing.T) {
	c, _ := testC2Store(t)
	now := time.Now()
	if err := c.CreateC2Listener(&C2Listener{ID: "l_1", Name: "l", Type: "http_beacon", BindPort: 9001}); err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	if err := c.UpsertC2Session(&C2Session{ID: "s_1", ListenerID: "l_1", ImplantUUID: "u-1",
		Status: "active", FirstSeenAt: now, LastCheckIn: now}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	for _, id := range []string{"t_1", "t_2"} {
		if err := c.CreateC2Task(&C2Task{ID: id, SessionID: "s_1", TaskType: "shell"}); err != nil {
			t.Fatalf("seed task %s: %v", id, err)
		}
	}
	if err := c.CreateC2Task(&C2Task{ID: "t_3", SessionID: "s_1", TaskType: "shell", Status: "success"}); err != nil {
		t.Fatalf("seed finished task: %v", err)
	}
	claimed, err := c.PopQueuedC2Tasks("s_1", 10)
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d tasks, want the two queued ones", len(claimed))
	}
	for _, task := range claimed {
		if task.Status != "sent" || task.SentAt == nil {
			t.Fatalf("claimed task %s came back as %s/%v, want sent with a timestamp", task.ID, task.Status, task.SentAt)
		}
	}
	again, err := c.PopQueuedC2Tasks("s_1", 10)
	if err != nil {
		t.Fatalf("second pop: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second pop claimed %d tasks, want none - the first claim is durable", len(again))
	}
}

func TestC2ProfileRoundTripsItsJSONColumns(t *testing.T) {
	c, _ := testC2Store(t)
	if err := c.CreateC2Profile(&C2Profile{ID: "p_1", Name: "profile", UserAgent: "ua",
		URIs:           []string{"/a", "/b"},
		RequestHeaders: map[string]string{"X-Test": "1"}}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	profile, err := c.GetC2Profile("p_1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if profile == nil || len(profile.URIs) != 2 || profile.RequestHeaders["X-Test"] != "1" {
		t.Fatalf("profile round-tripped as %#v", profile)
	}
}

func TestC2UnlinkProjectClearsTheBindingOnly(t *testing.T) {
	c, _ := testC2Store(t)
	if err := c.CreateC2Listener(&C2Listener{ID: "l_1", ProjectID: "p_1", Name: "l",
		Type: "http_beacon", BindPort: 9001}); err != nil {
		t.Fatalf("seed listener: %v", err)
	}
	if err := c.UnlinkProject("p_1"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	listener, err := c.GetC2Listener("l_1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if listener == nil {
		t.Fatalf("the listener disappeared with the project, want it kept unbound")
	}
	if listener.ProjectID != "" {
		t.Fatalf("listener project = %q, want the binding cleared", listener.ProjectID)
	}
}
