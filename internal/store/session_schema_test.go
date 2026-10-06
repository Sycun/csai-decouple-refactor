package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// openSessionStoreDB opens a real database in a temp dir; foreign keys are left off the way a bare
// sql.Open does, so these cases build one table at a time without needing the conversation row.
func openSessionStoreDB(t *testing.T, name string) (*Session, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewSession(db), db
}

// The session store builds the two tables it is the only writer of. These cases read the schema back
// rather than trusting the store's own returns, and the second one starts from the shape a database
// written before the two late columns existed still has.

func TestSessionEnsureSchemaBuildsBothTablesAndThreeIndexes(t *testing.T) {
	s, db := openSessionStoreDB(t, "session-schema.db")
	if err := s.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := s.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	for _, table := range []string{"messages", "process_details"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s present %d times", table, count)
		}
	}
	var indexes []string
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND (name LIKE 'idx_messages%' OR name LIKE 'idx_process_details%') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, name)
	}
	rows.Close()
	if len(indexes) != 3 {
		t.Fatalf("indexes = %v, want the three the store owns", indexes)
	}
	// The cascade is part of the contract the conversation delete relies on.
	for _, expect := range []struct{ table, ref string }{{"messages", "conversations"}, {"process_details", "messages"}} {
		ok, err := sessionFKCascades(db, expect.table, expect.ref)
		if err != nil {
			t.Fatalf("read %s foreign keys: %v", expect.table, err)
		}
		if !ok {
			t.Fatalf("%s lost its ON DELETE CASCADE onto %s", expect.table, expect.ref)
		}
	}
}

func sessionFKCascades(db *sql.DB, table, ref string) (bool, error) {
	rows, err := db.Query("PRAGMA foreign_key_list(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var target, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return false, err
		}
		if target == ref && strings.EqualFold(onDelete, "CASCADE") {
			return true, nil
		}
	}
	return false, rows.Err()
}

func TestSessionMigrateMessageColumnsRepairsTheLegacyShape(t *testing.T) {
	s, db := openSessionStoreDB(t, "session-legacy.db")
	// messages as it stood before updated_at / reasoning_content existed.
	if _, err := db.Exec(`CREATE TABLE messages (
		id TEXT PRIMARY KEY,
		conversation_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		mcp_execution_ids TEXT,
		created_at DATETIME NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy messages: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO messages (id, conversation_id, role, content, created_at)
		VALUES ('m1', 'c1', 'assistant', '正文', '2026-01-02 03:04:05')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := s.MigrateMessageColumns(); err != nil {
		t.Fatalf("MigrateMessageColumns: %v", err)
	}
	if err := s.MigrateMessageColumns(); err != nil {
		t.Fatalf("second MigrateMessageColumns (all duplicates now): %v", err)
	}

	for _, column := range []string{"updated_at", "reasoning_content"} {
		count, err := schemaColumnCount(db, "messages", column)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("column %s was not backfilled", column)
		}
	}
	// The row that predates updated_at gets at least its creation time, and nothing is stamped "now".
	var updated string
	if err := db.QueryRow(`SELECT COALESCE(updated_at,'') FROM messages WHERE id='m1'`).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(updated, "2026-01-02") {
		t.Fatalf("legacy row updated_at=%q, want the created_at value", updated)
	}
}

func TestSessionSchemaRefusesAConnectionlessHandle(t *testing.T) {
	var missing *Session
	value := NewSession(nil)
	want := "store: session requires a database"
	for i, handle := range []*Session{missing, value} {
		if err := handle.EnsureSchema(); err == nil || err.Error() != want {
			t.Fatalf("handle %d EnsureSchema answered %v, want %q", i, err, want)
		}
		if err := handle.MigrateMessageColumns(); err == nil || err.Error() != want {
			t.Fatalf("handle %d MigrateMessageColumns answered %v", i, err)
		}
	}
	// A schema probe against a missing table is reported, not treated as "column absent":
	// silently skipping the ALTER would leave the next read failing on a column nobody mentioned.
	if ok, err := SchemaHasColumn(value.db, "no_such_table", "x"); ok || err == nil {
		t.Fatalf("probe on a missing table=(%v,%v), want an error", ok, err)
	}
}
