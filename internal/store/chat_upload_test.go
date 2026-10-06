package store

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// chat_upload_artifacts is the authorization record for an uploaded file: whoever is about to serve
// or move the bytes asks this table who owns the path. Those semantics - the prefix cascade, the
// subtree rename, the "empty input writes nothing" rule - are what these cases pin, because they used
// to be tested from the data-layer package that no longer holds the SQL.

func newChatUploadStore(t *testing.T) *ChatUploads {
	t.Helper()
	db := openDB(t)
	uploads := NewChatUploads(db)
	if err := uploads.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	if err := uploads.EnsureSchema(); err != nil {
		t.Fatalf("schema is not idempotent: %v", err)
	}
	// The schema is three statements in one Exec: the table plus both indexes. An implementation that
	// stops after the first statement still looks like it worked, so the indexes are checked by name.
	for _, object := range []string{"chat_upload_artifacts", "idx_chat_upload_artifacts_conversation", "idx_chat_upload_artifacts_owner"} {
		var found string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, object).Scan(&found)
		if err != nil {
			t.Fatalf("%s was not created: %v", object, err)
		}
	}
	return uploads
}

func TestChatUploadRecordAndOwner(t *testing.T) {
	uploads := newChatUploadStore(t)
	const path = "2026-10-06/conv-1/a.txt"
	if err := uploads.Record(path, "conv-1", "u1"); err != nil {
		t.Fatal(err)
	}
	conv, owner, ok := uploads.OwnerOf(path)
	if !ok || conv != "conv-1" || owner != "u1" {
		t.Fatalf("owner lookup = conv=%q owner=%q ok=%v", conv, owner, ok)
	}
	// Re-recording the same path is an update, not a second row.
	if err := uploads.Record(path, "conv-1", "u2"); err != nil {
		t.Fatal(err)
	}
	if _, owner, _ := uploads.OwnerOf(path); owner != "u2" {
		t.Fatalf("the upsert kept the old owner: %q", owner)
	}
}

// An upload endpoint calls this best-effort; an incomplete identity must not fail the request.
func TestChatUploadRecordIgnoresIncompleteIdentity(t *testing.T) {
	uploads := newChatUploadStore(t)
	for _, args := range [][3]string{
		{"", "conv-1", "u1"},
		{"path", "", "u1"},
		{"path", "conv-1", " "},
	} {
		if err := uploads.Record(args[0], args[1], args[2]); err != nil {
			t.Fatalf("Record(%q): %v", args, err)
		}
	}
	if _, _, ok := uploads.OwnerOf("path"); ok {
		t.Fatal("an incomplete identity wrote a row anyway")
	}
}

func TestChatUploadForgetTakesTheSubtree(t *testing.T) {
	uploads := newChatUploadStore(t)
	for _, path := range []string{"dir/a.txt", "dir/sub/b.txt", "other/c.txt"} {
		if err := uploads.Record(path, "conv-1", "u1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := uploads.Forget("dir"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"dir/a.txt", "dir/sub/b.txt"} {
		if _, _, ok := uploads.OwnerOf(path); ok {
			t.Fatalf("%s survived a directory forget", path)
		}
	}
	if _, _, ok := uploads.OwnerOf("other/c.txt"); !ok {
		t.Fatal("Forget cascaded outside the directory")
	}
}

// A rename has to move the whole subtree with the same prefix rule, or the files on disk and the rows
// that authorize them stop describing the same paths.
func TestChatUploadRenameMovesTheSubtree(t *testing.T) {
	uploads := newChatUploadStore(t)
	for _, path := range []string{"old/a.txt", "old/sub/b.txt"} {
		if err := uploads.Record(path, "conv-1", "u1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := uploads.Rename("old", "new"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"new/a.txt", "new/sub/b.txt"} {
		conv, owner, ok := uploads.OwnerOf(path)
		if !ok || conv != "conv-1" || owner != "u1" {
			t.Fatalf("%s did not move with its parent: conv=%q owner=%q ok=%v", path, conv, owner, ok)
		}
	}
	if _, _, ok := uploads.OwnerOf("old/a.txt"); ok {
		t.Fatal("the old path is still recorded")
	}
}

func TestChatUploadWithoutDatabaseIsRefused(t *testing.T) {
	uploads := NewChatUploads(nil)
	if err := uploads.EnsureSchema(); err == nil {
		t.Fatal("a connectionless store created a table")
	}
	if _, _, ok := uploads.OwnerOf("x"); ok {
		t.Fatal("a connectionless store found an owner")
	}
	if err := uploads.Record("x", "conv", "u1"); err == nil {
		t.Fatal("a connectionless store wrote a row")
	}
	var missing *ChatUploads
	if err := missing.Forget("x"); err == nil {
		t.Fatal("a nil store answered a forget call")
	}
}
