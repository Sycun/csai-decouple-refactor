package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func newEmbeddingsStore(t *testing.T) *KnowledgeEmbeddings {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "embeddings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS knowledge_base_items (
		id TEXT PRIMARY KEY, category TEXT NOT NULL, title TEXT NOT NULL,
		file_path TEXT NOT NULL, content TEXT, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
	);`); err != nil {
		t.Fatal(err)
	}
	return NewKnowledgeEmbeddings(db)
}

func embeddingColumnExists(t *testing.T, store *KnowledgeEmbeddings, column string) bool {
	t.Helper()
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('knowledge_embeddings') WHERE name = ?`, column).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestKnowledgeEmbeddingsEnsureSchemaIsIdempotent(t *testing.T) {
	store := newEmbeddingsStore(t)
	for i := 0; i < 2; i++ {
		if err := store.EnsureSchema(); err != nil {
			t.Fatalf("EnsureSchema pass %d: %v", i, err)
		}
	}
	for _, column := range []string{"sub_indexes", "embedding_model", "embedding_dim"} {
		if !embeddingColumnExists(t, store, column) {
			t.Fatalf("%s missing after EnsureSchema", column)
		}
	}
	var index string
	if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_knowledge_embeddings_item_id'`).Scan(&index); err != nil {
		t.Fatalf("the item_id index was not created: %v", err)
	}
}

// The whole reason the column backfill exists: databases written before sub_indexes / embedding_model /
// embedding_dim arrived have to gain them, and both old copies of this rule were guarded by
// pragma_table_info - now there is one guard in one place.
func TestKnowledgeEmbeddingsEnsureColumnsBackfillsALegacyTable(t *testing.T) {
	store := newEmbeddingsStore(t)
	if _, err := store.db.Exec(`CREATE TABLE knowledge_embeddings (
		id TEXT PRIMARY KEY, item_id TEXT NOT NULL, chunk_index INTEGER NOT NULL,
		chunk_text TEXT NOT NULL, embedding TEXT NOT NULL, created_at DATETIME NOT NULL
	);`); err != nil {
		t.Fatal(err)
	}
	if embeddingColumnExists(t, store, "sub_indexes") {
		t.Fatal("the legacy fixture already has the new column")
	}
	if err := store.EnsureColumns(); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"sub_indexes", "embedding_model", "embedding_dim"} {
		if !embeddingColumnExists(t, store, column) {
			t.Fatalf("%s was not backfilled", column)
		}
	}
	if err := store.EnsureColumns(); err != nil {
		t.Fatalf("second EnsureColumns must be a no-op: %v", err)
	}
}

// EnsureColumns runs on the indexer's construction path, sometimes before anything exists in the
// file: a missing table is not an error there.
func TestKnowledgeEmbeddingsEnsureColumnsSkipsAbsentTable(t *testing.T) {
	store := newEmbeddingsStore(t)
	if err := store.EnsureColumns(); err != nil {
		t.Fatalf("EnsureColumns with no table: %v", err)
	}
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='knowledge_embeddings'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("EnsureColumns must not create the table")
	}
}

func TestKnowledgeEmbeddingsWithoutDatabaseIsRefused(t *testing.T) {
	none := NewKnowledgeEmbeddings(nil)
	if err := none.EnsureSchema(); err == nil {
		t.Fatal("a connectionless store created the table")
	}
	if err := none.EnsureColumns(); err == nil {
		t.Fatal("a connectionless store ran a migration")
	}
}
