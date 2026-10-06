package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func newRobotSessionsStore(t *testing.T) (*RobotSessions, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "robot.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE conversations (id TEXT PRIMARY KEY, title TEXT);`); err != nil {
		t.Fatal(err)
	}
	sessions := NewRobotSessions(db)
	if err := sessions.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	return sessions, db
}

func TestRobotSessionsEnsureSchemaIsIdempotent(t *testing.T) {
	sessions, db := newRobotSessionsStore(t)
	if err := sessions.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	var index string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_robot_user_sessions_updated_at'`).Scan(&index); err != nil {
		t.Fatalf("the updated_at index was not created: %v", err)
	}
}

// The table is older than its agent_mode column, so a base written before that field arrived has to
// gain it - the read below selects the column unconditionally.
func TestRobotSessionsEnsureSchemaBackfillsAgentMode(t *testing.T) {
	sessions, db := newRobotSessionsStore(t)
	if _, err := db.Exec(`DROP TABLE robot_user_sessions`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE robot_user_sessions (
		session_key TEXT PRIMARY KEY, conversation_id TEXT NOT NULL,
		role_name TEXT NOT NULL DEFAULT '默认', updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`); err != nil {
		t.Fatal(err)
	}
	if err := sessions.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('robot_user_sessions') WHERE name='agent_mode'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("agent_mode was not backfilled on a legacy table")
	}
}

func TestRobotSessionsGetSeparatesAbsentFromEmpty(t *testing.T) {
	sessions, _ := newRobotSessionsStore(t)
	got, err := sessions.Get("wxwork/never-seen")
	if err != nil || got != nil {
		t.Fatalf("an unknown thread read as %+v (%v), want nil", got, err)
	}
	// An empty key is what a caller whose platform identity is missing hands over; it is not an error.
	if got, err := sessions.Get("  "); err != nil || got != nil {
		t.Fatalf("an empty key read as %+v (%v), want nil and no error", got, err)
	}
}

func TestRobotSessionsUpsertRoundTripAndDefaults(t *testing.T) {
	sessions, db := newRobotSessionsStore(t)
	if _, err := db.Exec(`INSERT INTO conversations (id, title) VALUES ('c-1','one')`); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Upsert("wxwork/u-1", "c-1", "审计员", "deep"); err != nil {
		t.Fatal(err)
	}
	got, err := sessions.Get("wxwork/u-1")
	if err != nil || got == nil {
		t.Fatalf("Get: %+v (%v)", got, err)
	}
	if got.ConversationID != "c-1" || got.RoleName != "审计员" || got.AgentMode != "deep" {
		t.Fatalf("row came back changed: %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("updated_at did not read back as an instant")
	}
	// Empty role and mode fall back to the defaults on the way in, so a reader never sees a blank.
	if err := sessions.Upsert("wxwork/u-2", "c-1", "  ", ""); err != nil {
		t.Fatal(err)
	}
	got2, err := sessions.Get("wxwork/u-2")
	if err != nil || got2 == nil {
		t.Fatalf("Get u-2: %+v (%v)", got2, err)
	}
	if got2.RoleName != DefaultRoleName || got2.AgentMode != DefaultAgentMode {
		t.Fatalf("defaults not applied: %+v", got2)
	}
}

// One thread points at one conversation: pointing it elsewhere replaces the row rather than adding a
// second one the next read could pick either of.
func TestRobotSessionsUpsertReplacesTheSameKey(t *testing.T) {
	sessions, db := newRobotSessionsStore(t)
	for _, id := range []string{"c-1", "c-2"} {
		if _, err := db.Exec(`INSERT INTO conversations (id, title) VALUES (?, ?)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions.Upsert("wxwork/u-1", "c-1", "one", "eino_single"); err != nil {
		t.Fatal(err)
	}
	first, _ := sessions.Get("wxwork/u-1")
	if err := sessions.Upsert("wxwork/u-1", "c-2", "two", "deep"); err != nil {
		t.Fatal(err)
	}
	second, _ := sessions.Get("wxwork/u-1")
	if second.ConversationID != "c-2" || second.RoleName != "two" || second.AgentMode != "deep" {
		t.Fatalf("the rewrite did not take: %+v", second)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM robot_user_sessions`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d rows after rewriting one thread, want 1", rows)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("updated_at did not move: %v then %v", first.UpdatedAt, second.UpdatedAt)
	}
}

// A turn with no conversation has nothing to remember: silence, not an error the reply path would have
// to explain to a chat user.
func TestRobotSessionsWritesIgnoreNothingToRemember(t *testing.T) {
	sessions, db := newRobotSessionsStore(t)
	if _, err := db.Exec(`INSERT INTO conversations (id, title) VALUES ('c-1','one')`); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"", "c-1"}, {"  ", "c-1"}, {"wxwork/u-1", ""}, {"wxwork/u-1", "  "}} {
		if err := sessions.Upsert(pair[0], pair[1], "r", "m"); err != nil {
			t.Fatalf("Upsert(%q,%q) returned %v, want silence", pair[0], pair[1], err)
		}
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM robot_user_sessions`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("%d rows written for turns that had nothing to remember", rows)
	}
	// Deleting is equally quiet, both for a key that was never there and for an empty one.
	if err := sessions.Delete("wxwork/gone"); err != nil {
		t.Fatalf("Delete of an absent thread: %v", err)
	}
	if err := sessions.Delete(""); err != nil {
		t.Fatalf("Delete of an empty key: %v", err)
	}
}

// The row is a pointer into a conversation, so deleting the conversation deletes the pointer. Without
// the foreign key this would be the orphan that makes a robot answer into a base nobody can open.
func TestRobotSessionsBindingGoesWithItsConversation(t *testing.T) {
	sessions, db := newRobotSessionsStore(t)
	if _, err := db.Exec(`INSERT INTO conversations (id, title) VALUES ('c-1','one')`); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Upsert("wxwork/u-1", "c-1", "r", "m"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM conversations WHERE id='c-1'`); err != nil {
		t.Fatal(err)
	}
	got, err := sessions.Get("wxwork/u-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("the binding outlived its conversation: %+v", got)
	}
}

func TestRobotSessionsRefusesNoConnection(t *testing.T) {
	none := NewRobotSessions(nil)
	if err := none.EnsureSchema(); err == nil {
		t.Fatal("a connectionless store created the table")
	}
	if _, err := none.Get("wxwork/u-1"); err == nil {
		t.Fatal("a connectionless store returned a binding")
	}
	if err := none.Upsert("wxwork/u-1", "c-1", "r", "m"); err == nil {
		t.Fatal("a connectionless store wrote a binding")
	}
	if err := none.Delete("wxwork/u-1"); err == nil {
		t.Fatal("a connectionless store deleted a binding")
	}
}
