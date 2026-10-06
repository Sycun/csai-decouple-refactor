package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// The conversations and projects DDL used to boot from the data layer's initTables, with their late
// columns added by two backfill methods and their five indexes in a global sweep. These cases walk
// the three phases exactly the way the boot path now calls them - EnsureSchema, MigrateLateColumns,
// EnsureIndexes - and pin what a fresh install gets, plus that a second boot over the same file
// changes nothing (that is the upgrade path: every statement is idempotent).

func testSchemaDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), name)+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sqliteIndexExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("look up index %s: %v", name, err)
	}
	return count > 0
}

func TestConversationsSchemaPhasesBuildColumnsAndIndexes(t *testing.T) {
	db := testSchemaDB(t, "conversations-schema.db")
	c := NewConversations(db)

	if err := c.EnsureSchema(); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	// The CREATE ships the first release's columns; project_id and owner_user_id are the backfills'.
	if !columnExists(t, db, "conversations", "last_react_output") {
		t.Fatal("the CREATE did not ship last_react_output")
	}
	if columnExists(t, db, "conversations", "project_id") || columnExists(t, db, "conversations", "owner_user_id") {
		t.Fatal("a late column appeared before its phase ran")
	}

	if err := c.MigrateLateColumns(); err != nil {
		t.Fatalf("migrate late columns: %v", err)
	}
	for _, column := range []string{"pinned", "webshell_connection_id", "project_id", "owner_user_id"} {
		if !columnExists(t, db, "conversations", column) {
			t.Fatalf("late column %s is missing", column)
		}
	}

	if err := c.EnsureIndexes(); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	for _, index := range []string{"idx_conversations_updated_at", "idx_conversations_pinned", "idx_conversations_project_id"} {
		if !sqliteIndexExists(t, db, index) {
			t.Fatalf("index %s is missing", index)
		}
	}

	// The upgrade path boots over an existing file: every phase must survive a second run.
	if err := c.EnsureSchema(); err != nil {
		t.Fatalf("second ensure schema: %v", err)
	}
	if err := c.MigrateLateColumns(); err != nil {
		t.Fatalf("second migrate late columns: %v", err)
	}
	if err := c.EnsureIndexes(); err != nil {
		t.Fatalf("second ensure indexes: %v", err)
	}
}

func TestProjectsSchemaPhasesBuildOwnerColumnAndIndexes(t *testing.T) {
	db := testSchemaDB(t, "projects-schema.db")
	s := NewProjects(db)

	if err := s.EnsureSchema(); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if columnExists(t, db, "projects", "owner_user_id") {
		t.Fatal("owner_user_id appeared before its phase ran")
	}

	if err := s.MigrateLateColumns(); err != nil {
		t.Fatalf("migrate late columns: %v", err)
	}
	if !columnExists(t, db, "projects", "owner_user_id") {
		t.Fatal("owner_user_id is missing after its phase")
	}

	if err := s.EnsureIndexes(); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	for _, index := range []string{"idx_projects_status", "idx_projects_updated_at"} {
		if !sqliteIndexExists(t, db, index) {
			t.Fatalf("index %s is missing", index)
		}
	}

	if err := s.EnsureSchema(); err != nil {
		t.Fatalf("second ensure schema: %v", err)
	}
	if err := s.MigrateLateColumns(); err != nil {
		t.Fatalf("second migrate late columns: %v", err)
	}
	if err := s.EnsureIndexes(); err != nil {
		t.Fatalf("second ensure indexes: %v", err)
	}
}

func TestFactsDropLegacyTablesRemovesTheVersionArchive(t *testing.T) {
	db := testSchemaDB(t, "facts-legacy.db")
	if _, err := db.Exec(`CREATE TABLE project_fact_versions (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("seed legacy table: %v", err)
	}
	facts := NewFacts(db)
	if err := facts.DropLegacyTables(); err != nil {
		t.Fatalf("drop legacy tables: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='project_fact_versions'`).Scan(&count); err != nil {
		t.Fatalf("look up legacy table: %v", err)
	}
	if count != 0 {
		t.Fatal("the version archive survived the drop")
	}
	// Idempotent: the boot path calls this on every start, including fresh installs.
	if err := facts.DropLegacyTables(); err != nil {
		t.Fatalf("second drop: %v", err)
	}
}
