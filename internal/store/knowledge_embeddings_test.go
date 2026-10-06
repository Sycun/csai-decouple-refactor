package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

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

// The vector rows are read by joining the item they belong to, so a faithful fixture is two real
// stores over one connection opened the way NewKnowledgeDB opens it (foreign keys on).
func newVectorStores(t *testing.T) (*KnowledgeEmbeddings, *KnowledgeItems) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "knowledge.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	items := NewKnowledgeItems(db)
	if err := items.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	vectors := NewKnowledgeEmbeddings(db)
	if err := vectors.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	return vectors, items
}

func seedItem(t *testing.T, items *KnowledgeItems, id, category, title string) {
	t.Helper()
	if err := items.Insert(id, category, title, "/kb/"+id+".md", "body of "+id, time.Now()); err != nil {
		t.Fatalf("seed item %s: %v", id, err)
	}
}

func chunk(id, itemID string, index int, text, embedding, subIndexes, model string, dim int) NewChunk {
	return NewChunk{ID: id, ItemID: itemID, ChunkIndex: index, ChunkText: text, Embedding: embedding, SubIndexes: subIndexes, Model: model, Dim: dim}
}

func TestKnowledgeEmbeddingsInsertChunksRoundTripsThroughSearchable(t *testing.T) {
	vectors, items := newVectorStores(t)
	seedItem(t, items, "i-1", "risk", "Title One")
	if err := vectors.InsertChunks(context.Background(), []NewChunk{
		chunk("c-2", "i-1", 1, "second chunk", "[1,0]", "", "text-emb", 2),
		chunk("c-1", "i-1", 0, "first chunk", "[0,1]", "", "text-emb", 2),
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := vectors.Searchable(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	byID := map[string]VectorChunk{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	got := byID["c-1"]
	if got.ChunkIndex != 0 || got.ChunkText != "first chunk" || got.Embedding != "[0,1]" || got.Dim != 2 {
		t.Fatalf("chunk columns came back changed: %+v", got)
	}
	// The two item columns are what the join exists for: retrieval names the source of a hit.
	if got.ItemID != "i-1" || got.Category != "risk" || got.Title != "Title One" {
		t.Fatalf("joined item columns wrong: %+v", got)
	}
	var created string
	if err := vectors.db.QueryRow(`SELECT created_at FROM knowledge_embeddings WHERE id = 'c-1'`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(created) == "" {
		t.Fatal("datetime('now') did not fill created_at, so the index-status page has no age to show")
	}
}

// The category predicate is TRIM(?) COLLATE NOCASE because risk types are typed by hand in the
// console and stored with varying case and padding.
func TestKnowledgeEmbeddingsSearchableCategoryFilter(t *testing.T) {
	vectors, items := newVectorStores(t)
	seedItem(t, items, "i-1", " Risk ", "Padded")
	seedItem(t, items, "i-2", "other", "Other")
	if err := vectors.InsertChunks(context.Background(), []NewChunk{
		chunk("c-1", "i-1", 0, "a", "[1]", "", "", 1),
		chunk("c-2", "i-2", 0, "b", "[1]", "", "", 1),
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := vectors.Searchable(context.Background(), "risk", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "c-1" {
		t.Fatalf("category filter matched %v, want only c-1", rows)
	}
}

// A chunk with no sub-index belongs to every tag; a tag is matched after lowering and stripping
// spaces, and only inside the comma list - so "ab" must not match "a,b".
func TestKnowledgeEmbeddingsSearchableSubIndexTag(t *testing.T) {
	vectors, items := newVectorStores(t)
	seedItem(t, items, "i-1", "risk", "One")
	if err := vectors.InsertChunks(context.Background(), []NewChunk{
		chunk("c-untagged", "i-1", 0, "a", "[1]", "", "", 1),
		chunk("c-tagged", "i-1", 1, "b", "[1]", "A, B ", "", 1),
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tag   string
		want  []string
		title string
	}{
		{" a ", []string{"c-tagged", "c-untagged"}, "spaces around the tag are stripped before matching"},
		{"ab", []string{"c-untagged"}, "a tag that is two entries glued together matches nothing"},
		{"c", []string{"c-untagged"}, "an untagged row is never filtered out"},
		{"", []string{"c-tagged", "c-untagged"}, "no tag means no filter at all"},
	} {
		rows, err := vectors.Searchable(context.Background(), "", tc.tag)
		if err != nil {
			t.Fatalf("%s: %v", tc.title, err)
		}
		got := make([]string, 0, len(rows))
		for _, r := range rows {
			got = append(got, r.ID)
		}
		sort.Strings(got)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.title, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: got %v, want %v", tc.title, got, tc.want)
			}
		}
	}
}

// Retrieval reads through an inner join, so a vector whose item row is gone is not scored and not
// reported as a hit with an empty title. Orphans are real: a base written before the foreign key
// existed keeps its vectors when the item goes.
func TestKnowledgeEmbeddingsSearchableDropsOrphanVectors(t *testing.T) {
	vectors, items := newVectorStores(t)
	seedItem(t, items, "i-1", "risk", "One")
	if err := vectors.InsertChunks(context.Background(), []NewChunk{chunk("c-1", "i-1", 0, "a", "[1]", "", "", 1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := vectors.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := vectors.db.Exec(`DELETE FROM knowledge_base_items WHERE id = 'i-1'`); err != nil {
		t.Fatal(err)
	}
	var orphans int
	if err := vectors.db.QueryRow(`SELECT COUNT(*) FROM knowledge_embeddings`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 1 {
		t.Fatalf("the fixture has %d vector rows, want the orphan left behind", orphans)
	}
	rows, err := vectors.Searchable(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("an orphan vector was returned for retrieval: %+v", rows)
	}
}

// A rejected document must not leave half an item indexed: the batch is one transaction.
func TestKnowledgeEmbeddingsInsertChunksIsAllOrNothing(t *testing.T) {
	vectors, items := newVectorStores(t)
	seedItem(t, items, "i-1", "risk", "One")
	err := vectors.InsertChunks(context.Background(), []NewChunk{
		chunk("c-1", "i-1", 0, "a", "[1]", "", "", 1),
		chunk("c-2", "i-missing", 1, "b", "[1]", "", "", 1),
	})
	if err == nil {
		t.Fatal("a chunk for an absent item was accepted")
	}
	count, countErr := vectors.CountRows()
	if countErr != nil {
		t.Fatal(countErr)
	}
	if count != 0 {
		t.Fatalf("%d rows survived a rejected batch, want 0", count)
	}
	if err := vectors.InsertChunks(context.Background(), nil); err != nil {
		t.Fatalf("an empty batch must be a no-op: %v", err)
	}
}

func TestKnowledgeEmbeddingsDeleteItemAndCounters(t *testing.T) {
	vectors, items := newVectorStores(t)
	seedItem(t, items, "i-1", "risk", "One")
	seedItem(t, items, "i-2", "risk", "Two")
	if err := vectors.InsertChunks(context.Background(), []NewChunk{
		chunk("c-1", "i-1", 0, "a", "[1]", "", "", 1),
		chunk("c-2", "i-1", 1, "b", "[1]", "", "", 1),
		chunk("c-3", "i-2", 0, "c", "[1]", "", "", 1),
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := vectors.CountRows()
	if err != nil {
		t.Fatal(err)
	}
	if rows != 3 {
		t.Fatalf("CountRows = %d, want 3", rows)
	}
	indexed, err := vectors.IndexedItems()
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 2 {
		t.Fatalf("IndexedItems = %d, want 2: the index-status page counts items, not chunks", indexed)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := vectors.DeleteItem("i-1"); err != nil {
			t.Fatalf("DeleteItem pass %d: %v", pass, err)
		}
	}
	if rows, err := vectors.CountRows(); err != nil || rows != 1 {
		t.Fatalf("after deleting an item's vectors: %d rows (%v), want 1", rows, err)
	}
	if indexed, err := vectors.IndexedItems(); err != nil || indexed != 1 {
		t.Fatalf("after deleting an item's vectors: %d indexed (%v), want 1", indexed, err)
	}
}

func TestKnowledgeEmbeddingsReadsAndWritesRefuseNoConnection(t *testing.T) {
	none := NewKnowledgeEmbeddings(nil)
	if _, err := none.Searchable(context.Background(), "", ""); err == nil {
		t.Fatal("a connectionless store returned vectors")
	}
	if err := none.InsertChunks(context.Background(), []NewChunk{chunk("c", "i", 0, "a", "[1]", "", "", 1)}); err == nil {
		t.Fatal("a connectionless store wrote a batch")
	}
	if err := none.DeleteItem("i"); err == nil {
		t.Fatal("a connectionless store deleted vectors")
	}
	if _, err := none.CountRows(); err == nil {
		t.Fatal("a connectionless store counted rows")
	}
	if _, err := none.IndexedItems(); err == nil {
		t.Fatal("a connectionless store counted indexed items")
	}
}
