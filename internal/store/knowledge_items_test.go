package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func newItemsStore(t *testing.T) (*KnowledgeItems, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "items.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	items := NewKnowledgeItems(db)
	if err := items.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	return items, db
}

func TestKnowledgeItemsEnsureSchemaIsIdempotent(t *testing.T) {
	items, db := newItemsStore(t)
	if err := items.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	var index string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_knowledge_items_category'`).Scan(&index); err != nil {
		t.Fatalf("the category index was not created: %v", err)
	}
}

func TestKnowledgeItemsInsertThenGetRoundTrips(t *testing.T) {
	items, _ := newItemsStore(t)
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	if err := items.Insert("i-1", "risk", "Title", "/kb/risk/one.md", "the body", at); err != nil {
		t.Fatal(err)
	}
	got, found, err := items.GetByID("i-1")
	if err != nil || !found {
		t.Fatalf("GetByID: found=%v err=%v", found, err)
	}
	if got.ID != "i-1" || got.Category != "risk" || got.Title != "Title" || got.FilePath != "/kb/risk/one.md" || got.Content != "the body" {
		t.Fatalf("row came back changed: %+v", got)
	}
	// Both timestamps are the same instant on insert: the directory scan reads updated_at as "what
	// this build last saw", and a created_at that differs would make a fresh row look already changed.
	if got.CreatedAt != got.UpdatedAt {
		t.Fatalf("created_at %q != updated_at %q on a fresh row", got.CreatedAt, got.UpdatedAt)
	}
	if _, found, err := items.GetByID("nope"); err != nil || found {
		t.Fatalf("a missing id must not be found: found=%v err=%v", found, err)
	}
}

// content is the one nullable column on this table. A row written without it must read back as an
// empty string and not an error - scanning NULL into a plain string is how rows vanish from a list.
func TestKnowledgeItemsContentNullStaysReadable(t *testing.T) {
	items, db := newItemsStore(t)
	if _, err := db.Exec(`INSERT INTO knowledge_base_items (id, category, title, file_path, created_at, updated_at)
		VALUES ('i-null', 'risk', 'No Content', '/kb/null.md', '2026-10-06 12:00:00', '2026-10-06 12:00:00')`); err != nil {
		t.Fatal(err)
	}
	got, found, err := items.GetByID("i-null")
	if err != nil || !found {
		t.Fatalf("GetByID on a NULL-content row: found=%v err=%v", found, err)
	}
	if got.Content != "" {
		t.Fatalf("NULL content read as %q", got.Content)
	}
	list, err := items.List(ItemFilter{IncludeContent: true})
	if err != nil || len(list) != 1 {
		t.Fatalf("List over a NULL-content row: %d items (%v)", len(list), err)
	}
	if list[0].Content != "" {
		t.Fatalf("List read NULL content as %q", list[0].Content)
	}
}

func TestKnowledgeItemsExistingAtSeparatesMissingFromStored(t *testing.T) {
	items, _ := newItemsStore(t)
	if _, _, _, found, err := items.ExistingAt("/kb/absent.md"); err != nil || found {
		t.Fatalf("ExistingAt on an absent path: found=%v err=%v", found, err)
	}
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	if err := items.Insert("i-1", "risk", "Title", "/kb/one.md", "body", at); err != nil {
		t.Fatal(err)
	}
	id, content, updatedAt, found, err := items.ExistingAt("/kb/one.md")
	if err != nil || !found {
		t.Fatalf("ExistingAt: found=%v err=%v", found, err)
	}
	if id != "i-1" || content != "body" {
		t.Fatalf("ExistingAt returned %+v / %q", id, content)
	}
	if !updatedAt.Equal(at) {
		t.Fatalf("updated_at read as %v, want %v", updatedAt, at)
	}
}

// The two update paths are different operations: editing text in place versus a rename. Collapsing
// them would let a content edit silently move a row's file pointer.
func TestKnowledgeItemsUpdateContentKeepsPathAndUpdateMovesIt(t *testing.T) {
	items, _ := newItemsStore(t)
	first := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	later := first.Add(time.Hour)
	if err := items.Insert("i-1", "risk", "Title", "/kb/one.md", "body", first); err != nil {
		t.Fatal(err)
	}
	if err := items.UpdateContent("i-1", "risk", "Renamed Title", "new body", later); err != nil {
		t.Fatal(err)
	}
	got, _, _ := items.GetByID("i-1")
	if got.FilePath != "/kb/one.md" {
		t.Fatalf("UpdateContent moved the file path to %q", got.FilePath)
	}
	if got.Title != "Renamed Title" || got.Content != "new body" {
		t.Fatalf("UpdateContent did not apply: %+v", got)
	}
	if err := items.Update("i-1", "other", "Renamed Title", "/kb/two.md", "new body", later); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = items.GetByID("i-1"); got.FilePath != "/kb/two.md" || got.Category != "other" {
		t.Fatalf("Update did not move the row: %+v", got)
	}
}

func TestKnowledgeItemsCategoriesAndCounts(t *testing.T) {
	items, _ := newItemsStore(t)
	at := time.Now()
	seed := []struct{ id, category, title string }{
		{"i-1", "risk", "B"},
		{"i-2", "risk", "A"},
		{"i-3", "asset", "C"},
	}
	for _, s := range seed {
		if err := items.Insert(s.id, s.category, s.title, "/kb/"+s.id+".md", "body", at); err != nil {
			t.Fatal(err)
		}
	}
	cats, err := items.Categories()
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 2 || cats[0] != "asset" || cats[1] != "risk" {
		t.Fatalf("Categories = %v, want [asset risk]", cats)
	}
	grouped, err := items.CategoriesWithCounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(grouped) != 2 || grouped[0].Category != "asset" || grouped[0].Count != 1 || grouped[1].Category != "risk" || grouped[1].Count != 2 {
		t.Fatalf("CategoriesWithCounts = %+v", grouped)
	}
	if n, err := items.Count(""); err != nil || n != 3 {
		t.Fatalf("Count(all) = %d (%v), want 3", n, err)
	}
	if n, err := items.Count("risk"); err != nil || n != 2 {
		t.Fatalf("Count(risk) = %d (%v), want 2", n, err)
	}
	if n, err := items.Count("missing"); err != nil || n != 0 {
		t.Fatalf("Count(missing) = %d (%v), want 0", n, err)
	}
}

func TestKnowledgeItemsListPagesAndOrders(t *testing.T) {
	items, _ := newItemsStore(t)
	at := time.Now()
	seed := []struct{ id, category, title string }{
		{"i-1", "risk", "Beta"},
		{"i-2", "risk", "Alpha"},
		{"i-3", "asset", "Only"},
	}
	for _, s := range seed {
		if err := items.Insert(s.id, s.category, s.title, "/kb/"+s.id+".md", "body of "+s.id, at); err != nil {
			t.Fatal(err)
		}
	}
	all, err := items.List(ItemFilter{IncludeContent: true})
	if err != nil {
		t.Fatal(err)
	}
	// category then title: the knowledge page's order is part of what the console shows.
	if len(all) != 3 || all[0].ID != "i-3" || all[1].ID != "i-2" || all[2].ID != "i-1" {
		t.Fatalf("List order = %v", all)
	}
	if all[0].Content != "body of i-3" {
		t.Fatalf("IncludeContent did not carry the text: %+v", all[0])
	}
	page, err := items.List(ItemFilter{Category: "risk", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != "i-1" {
		t.Fatalf("second page of risk = %+v", page)
	}
	if page[0].Content != "" {
		t.Fatalf("the default List must not carry content, got %q", page[0].Content)
	}
	// Zero limit means "no limit", which is how every caller has always used it.
	if none, err := items.List(ItemFilter{Limit: 0, Offset: 5}); err != nil || len(none) != 3 {
		t.Fatalf("Limit 0 with an offset: %d rows (%v), want all 3", len(none), err)
	}
}

func TestKnowledgeItemsSearchMatchesFourColumns(t *testing.T) {
	items, _ := newItemsStore(t)
	at := time.Now()
	if err := items.Insert("i-1", "risk", "SQL Injection", "/kb/risk/sqli.md", "how to test sqli targets", at); err != nil {
		t.Fatal(err)
	}
	if err := items.Insert("i-2", "asset", "Hosts", "/kb/asset/hosts.md", "a list of hosts", at); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		keyword, category string
		want              int
		why               string
	}{
		{"injection", "", 1, "title match"},
		{"RISK", "", 1, "category match, ASCII case-insensitive"},
		{"sqli.md", "", 1, "file path match"},
		{"targets", "", 1, "content match"},
		{"hosts", "risk", 0, "a match in another category is narrowed away"},
		{"hosts", "asset", 1, "and found when the category fits"},
		{"nothing-matches-this", "", 0, "a keyword nobody has"},
	} {
		got, err := items.Search(tc.keyword, tc.category)
		if err != nil {
			t.Fatalf("%s: %v", tc.why, err)
		}
		if len(got) != tc.want {
			t.Fatalf("%s: %q matched %d rows, want %d (%+v)", tc.why, tc.keyword, len(got), tc.want, got)
		}
	}
	if _, err := items.Search("", ""); err == nil {
		t.Fatal("an empty keyword returned rows instead of refusing")
	}
}

func TestKnowledgeItemsFilePathAndDelete(t *testing.T) {
	items, db := newItemsStore(t)
	at := time.Now()
	if err := items.Insert("i-1", "risk", "Title", "/kb/one.md", "body", at); err != nil {
		t.Fatal(err)
	}
	vectors := NewKnowledgeEmbeddings(db)
	if err := vectors.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	if err := vectors.InsertChunks(context.Background(), []NewChunk{chunk("c-1", "i-1", 0, "a", "[1]", "", "", 1)}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := items.FilePath("missing"); err != nil || found {
		t.Fatalf("FilePath on a missing id: found=%v err=%v", found, err)
	}
	path, found, err := items.FilePath("i-1")
	if err != nil || !found || path != "/kb/one.md" {
		t.Fatalf("FilePath = %q (found=%v, err=%v)", path, found, err)
	}
	// Deleting the row is what removes its vectors, through the foreign key the embeddings table is
	// created with. A base opened without foreign keys enforced would keep them, and the retrieval
	// join then hides them - which is why retrieval reads through the join at all.
	if err := items.Delete("i-1"); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge_embeddings`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d vector rows survived their item's delete, want 0", left)
	}
	if err := items.Delete("i-1"); err != nil {
		t.Fatalf("deleting an absent row must be clean: %v", err)
	}
}

func TestKnowledgeItemsRebuildOrderingAndMissingVectors(t *testing.T) {
	items, db := newItemsStore(t)
	vectors := NewKnowledgeEmbeddings(db)
	if err := vectors.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fresh := old.Add(time.Hour)
	// Inserted newest-first: the rebuild walks oldest updated_at first, so the order must come from
	// the query, not from insertion order.
	if err := items.Insert("i-3", "risk", "Newest", "/kb/3.md", "b", fresh); err != nil {
		t.Fatal(err)
	}
	if err := items.Insert("i-2", "risk", "Middle", "/kb/2.md", "b", old.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := items.Insert("i-1", "risk", "Oldest", "/kb/1.md", "b", old); err != nil {
		t.Fatal(err)
	}
	if err := vectors.InsertChunks(context.Background(), []NewChunk{chunk("c-1", "i-1", 0, "a", "[1]", "", "", 1)}); err != nil {
		t.Fatal(err)
	}
	ids, err := items.AllIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "i-1" || ids[1] != "i-2" || ids[2] != "i-3" {
		t.Fatalf("AllIDs order = %v", ids)
	}
	missing, err := items.WithoutVectors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 2 || missing[0] != "i-2" || missing[1] != "i-3" {
		t.Fatalf("WithoutVectors = %v, want the two never-indexed rows oldest first", missing)
	}
}

func TestKnowledgeItemsEveryMethodRefusesNoConnection(t *testing.T) {
	none := NewKnowledgeItems(nil)
	calls := map[string]func() error{
		"EnsureSchema":  func() error { return none.EnsureSchema() },
		"Insert":        func() error { return none.Insert("i", "c", "t", "/p", "b", time.Now()) },
		"UpdateContent": func() error { return none.UpdateContent("i", "c", "t", "b", time.Now()) },
		"Update":        func() error { return none.Update("i", "c", "t", "/p", "b", time.Now()) },
		"Delete":        func() error { return none.Delete("i") },
		"Categories":    func() error { _, err := none.Categories(); return err },
		"CategoriesWithCounts": func() error {
			_, err := none.CategoriesWithCounts()
			return err
		},
		"Count":      func() error { _, err := none.Count(""); return err },
		"List":       func() error { _, err := none.List(ItemFilter{}); return err },
		"Search":     func() error { _, err := none.Search("k", ""); return err },
		"GetByID":    func() error { _, _, err := none.GetByID("i"); return err },
		"ExistingAt": func() error { _, _, _, _, err := none.ExistingAt("/p"); return err },
		"FilePath":   func() error { _, _, err := none.FilePath("i"); return err },
		"AllIDs":     func() error { _, err := none.AllIDs(context.Background()); return err },
		"WithoutVectors": func() error {
			_, err := none.WithoutVectors(context.Background())
			return err
		},
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("%s succeeded against a connectionless store", name)
		}
	}
}
