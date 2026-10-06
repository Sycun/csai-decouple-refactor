package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// knowledge_retrieval_logs is written by the RAG path and cleared when a conversation goes away.
// What the store owns is the rows; how a row is displayed (the several historical time formats, the
// item-ID JSON) stays with its reader, so these cases pin the pass-through as well as the queries.

func newRetrievalStore(t *testing.T) (*KnowledgeRetrieval, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "retrieval.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewKnowledgeRetrieval(db), path
}

func TestKnowledgeRetrievalSchemaCreatesItsIndexes(t *testing.T) {
	store, _ := newRetrievalStore(t)
	if err := store.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	for _, object := range []string{
		"knowledge_retrieval_logs",
		"idx_knowledge_retrieval_logs_conversation",
		"idx_knowledge_retrieval_logs_message",
		"idx_knowledge_retrieval_logs_created_at",
	} {
		var name string
		if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, object).Scan(&name); err != nil {
			t.Fatalf("%s was not created: %v", object, err)
		}
	}
	var keys int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('knowledge_retrieval_logs')`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if keys != 2 {
		t.Fatalf("the main-database form must keep both foreign keys, got %d", keys)
	}
}

func TestKnowledgeRetrievalStandaloneSchemaHasNoForeignKeys(t *testing.T) {
	store, _ := newRetrievalStore(t)
	if err := store.EnsureStandaloneSchema(); err != nil {
		t.Fatal(err)
	}
	var keys int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('knowledge_retrieval_logs')`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if keys != 0 {
		t.Fatalf("the knowledge-database form declares no foreign keys, got %d", keys)
	}
}

func TestKnowledgeRetrievalListPrecedenceAndOrder(t *testing.T) {
	store, _ := newRetrievalStore(t)
	if err := store.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	entries := []struct {
		id, conv, msg string
		offset        time.Duration
	}{
		{"old", "c1", "m1", -2 * time.Hour},
		{"new", "c1", "m1", -time.Hour},
		{"other-conv", "c2", "m2", 0},
	}
	for _, e := range entries {
		if err := store.Record(RetrievalEntry{
			ID: e.id, ConversationID: e.conv, MessageID: e.msg,
			Query: "q", RiskType: "sql", ItemsJSON: `["k1"]`,
		}, base.Add(e.offset)); err != nil {
			t.Fatal(err)
		}
	}

	all, err := store.ListNewest("", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("unfiltered list = %d rows, want 3", len(all))
	}
	if all[0].CreatedAt < all[len(all)-1].CreatedAt {
		t.Fatal("the list is not newest first")
	}

	byConversation, err := store.ListNewest("", "c1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(byConversation) != 2 {
		t.Fatalf("conversation filter returned %d rows, want 2", len(byConversation))
	}

	byMessage, err := store.ListNewest("m2", "c1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(byMessage) != 1 || byMessage[0].ID != "other-conv" {
		t.Fatalf("a message filter must win over the conversation filter, got %+v", byMessage)
	}

	limited, err := store.ListNewest("", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Fatalf("limit ignored: %d rows", len(limited))
	}
}

func TestKnowledgeRetrievalPassesStoredTextThrough(t *testing.T) {
	store, _ := newRetrievalStore(t)
	if err := store.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 4, 2, 8, 30, 0, 0, time.UTC)
	if err := store.Record(RetrievalEntry{
		ID: "row", ConversationID: "c1", Query: "q", RiskType: "", ItemsJSON: `["a","b"]`,
	}, at); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListNewest("", "c1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d", len(got))
	}
	if got[0].ItemsJSON != `["a","b"]` {
		t.Fatalf("items JSON was rewritten: %q", got[0].ItemsJSON)
	}
	if got[0].CreatedAt == "" {
		t.Fatal("created_at came back empty; the reader parses it from text")
	}
	if got[0].RiskType != "" {
		t.Fatalf("an absent risk type should read back as empty, got %q", got[0].RiskType)
	}
}

func TestKnowledgeRetrievalDeletes(t *testing.T) {
	store, _ := newRetrievalStore(t)
	if err := store.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, id := range []string{"keep", "drop"} {
		if err := store.Record(RetrievalEntry{ID: id, ConversationID: "c1", Query: "q"}, now); err != nil {
			t.Fatal(err)
		}
	}
	found, err := store.DeleteByID("drop")
	if err != nil || !found {
		t.Fatalf("delete existing = %v, %v", found, err)
	}
	found, err = store.DeleteByID("drop")
	if err != nil || found {
		t.Fatalf("deleting a gone row must report false, got %v, %v", found, err)
	}
	if err := store.DeleteForConversation("c1"); err != nil {
		t.Fatal(err)
	}
	left, err := store.ListNewest("", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("conversation cleanup left %d rows", len(left))
	}
}

func TestKnowledgeRetrievalWithoutDatabaseIsRefused(t *testing.T) {
	none := NewKnowledgeRetrieval(nil)
	if err := none.EnsureSchema(); err == nil {
		t.Fatal("a connectionless store created the table")
	}
	if err := none.EnsureStandaloneSchema(); err == nil {
		t.Fatal("a connectionless store created the standalone table")
	}
	if err := none.Record(RetrievalEntry{ID: "x"}, time.Now()); err == nil {
		t.Fatal("a connectionless store wrote a row")
	}
	if _, err := none.ListNewest("", "", 10); err == nil {
		t.Fatal("a connectionless store returned rows")
	}
	if _, err := none.DeleteByID("x"); err == nil {
		t.Fatal("a connectionless store deleted a row")
	}
	if err := none.DeleteForConversation("c"); err == nil {
		t.Fatal("a connectionless store cleared a conversation")
	}
}
