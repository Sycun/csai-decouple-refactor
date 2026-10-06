package database

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/store"
	"go.uber.org/zap"
)

// Seven statements used to be written by the conversation domain even though `messages` and
// `process_details` belong to store.Session - they were the last two entries in the write ledger's
// debt list. They moved into that store, so the columns each one writes are pinned here against the
// real schema (the tables are still created by the conversation domain, which is why these cases live
// in this package rather than in a store test with a hand-made DDL).

func openSessionWritesDB(t *testing.T, name string) *DB {
	t.Helper()
	db, err := NewDB(filepath.Join(t.TempDir(), name), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newConversation(t *testing.T, db *DB, title string) *Conversation {
	t.Helper()
	conv, err := db.CreateConversation(title, ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return conv
}

func readMessageRow(t *testing.T, db *DB, id string) (role, content, reasoning, mcpIDs string, created, updated time.Time) {
	t.Helper()
	err := db.QueryRow(`SELECT role, content, COALESCE(reasoning_content,''), COALESCE(mcp_execution_ids,''), created_at, updated_at
		FROM messages WHERE id = ?`, id).Scan(&role, &content, &reasoning, &mcpIDs, &created, &updated)
	if err != nil {
		t.Fatalf("read message %s: %v", id, err)
	}
	return
}

func TestAddMessageWritesEveryColumnTheRowContractPromises(t *testing.T) {
	db := openSessionWritesDB(t, "session-add-message.db")
	conv := newConversation(t, db, "add")

	message, err := db.AddMessage(conv.ID, "user", "查一下 /login", []string{"exec-1", "exec-2"})
	if err != nil {
		t.Fatal(err)
	}
	if message.CreatedAt.IsZero() || message.UpdatedAt.IsZero() {
		t.Fatalf("the returned message carries no timestamps: %+v", message)
	}
	role, content, reasoning, mcpIDs, created, updated := readMessageRow(t, db, message.ID)
	if role != "user" || content != "查一下 /login" {
		t.Fatalf("stored row=(%q,%q)", role, content)
	}
	if reasoning != "" {
		t.Fatalf("a user message must not carry reasoning text: %q", reasoning)
	}
	if !strings.Contains(mcpIDs, "exec-1") || !strings.Contains(mcpIDs, "exec-2") {
		t.Fatalf("mcp ids not marshalled into the row: %q", mcpIDs)
	}
	// The same instant is written to both columns; the finalize step is what moves updated_at later.
	if !created.Equal(updated) {
		t.Fatalf("created=%v updated=%v want equal", created, updated)
	}

	// An empty id list is stored as an empty string, not "null": readers treat "" as "no ids".
	bare, err := db.AddMessage(conv.ID, "assistant", "在跑", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, mcpIDs, _, _ := readMessageRow(t, db, bare.ID); mcpIDs != "" {
		t.Fatalf("no ids stored as %q, want the empty string", mcpIDs)
	}
}

func TestUpdateAssistantMessageFinalizeKeepsCreatedAndReasoning(t *testing.T) {
	db := openSessionWritesDB(t, "session-finalize.db")
	conv := newConversation(t, db, "finalize")
	message, err := db.AddMessage(conv.ID, "assistant", "处理中...", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _, createdBefore, updatedBefore := readMessageRow(t, db, message.ID)

	time.Sleep(1200 * time.Millisecond)
	if err := db.UpdateAssistantMessageFinalize(message.ID, "结论正文", []string{"exec-9"}, "  思考链  "); err != nil {
		t.Fatal(err)
	}
	_, content, reasoning, mcpIDs, createdAfter, updatedAfter := readMessageRow(t, db, message.ID)
	if content != "结论正文" {
		t.Fatalf("content=%q", content)
	}
	if reasoning != "思考链" {
		t.Fatalf("reasoning=%q, want trimmed", reasoning)
	}
	if !strings.Contains(mcpIDs, "exec-9") {
		t.Fatalf("mcp ids=%q", mcpIDs)
	}
	if !createdAfter.Equal(createdBefore) {
		t.Fatalf("created_at moved: %v -> %v", createdBefore, createdAfter)
	}
	if !updatedAfter.After(updatedBefore) {
		t.Fatalf("updated_at did not move: %v -> %v", updatedBefore, updatedAfter)
	}

	// Trimming happens before the write, so a whitespace-only chain clears the column rather than
	// storing spaces - the replay path checks for empty.
	if err := db.UpdateAssistantMessageFinalize(message.ID, "再答一次", nil, "   "); err != nil {
		t.Fatal(err)
	}
	if _, _, reasoning, mcpIDs, _, _ := readMessageRow(t, db, message.ID); reasoning != "" || mcpIDs != "" {
		t.Fatalf("blank chain / nil ids left values: reasoning=%q mcp=%q", reasoning, mcpIDs)
	}
}

func TestProcessDetailWritesAndUpdateTheSameRowInPlace(t *testing.T) {
	db := openSessionWritesDB(t, "session-process-detail.db")
	conv := newConversation(t, db, "detail")
	message, err := db.AddMessage(conv.ID, "assistant", "规划", nil)
	if err != nil {
		t.Fatal(err)
	}

	// 名字里的 WithID 指的是"返回可用 id"，id 本身由这一对语句生成：调用方要把这条记录原地续写，
	// 拿到的就是后续 Update / Delete 的键。
	id, err := db.AddProcessDetailWithID(message.ID, conv.ID, "planning", "第一步", map[string]any{"tokens": 3})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(id) == "" {
		t.Fatal("no id came back, so the streaming caller has nothing to update in place")
	}
	var data string
	if err := db.QueryRow(`SELECT data FROM process_details WHERE id = ?`, id).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "tokens") {
		t.Fatalf("payload not stored: %q", data)
	}

	if err := db.UpdateProcessDetailContent(id, "第一步（续）", map[string]any{"tokens": 7}); err != nil {
		t.Fatal(err)
	}
	var rows int
	var messageText, payload string
	if err := db.QueryRow(`SELECT COUNT(*), MAX(message), MAX(data) FROM process_details WHERE id = ?`, id).Scan(&rows, &messageText, &payload); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("the streaming update created %d rows, want the same one rewritten", rows)
	}
	if messageText != "第一步（续）" || !strings.Contains(payload, "7") {
		t.Fatalf("in-place update left message=%q data=%q", messageText, payload)
	}

	// A missing row is an error: the caller is mid-stream and already showed this id to the page.
	if err := db.UpdateProcessDetailContent("never-existed", "x", nil); err == nil || !strings.Contains(err.Error(), "过程详情不存在") {
		t.Fatalf("update of a missing row answered %v, want the 过程详情不存在 refusal", err)
	}
	if err := db.DeleteProcessDetail(id); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProcessDetail(id); err != nil {
		t.Fatalf("deleting an already-gone detail answered %v, want the original silent no-op", err)
	}
}

func TestDeleteConversationTurnRemovesOnlyThatTurnAndCountsIt(t *testing.T) {
	db := openSessionWritesDB(t, "session-delete-turn.db")
	keep := newConversation(t, db, "keep")
	target := newConversation(t, db, "target")

	anchor, err := db.AddMessage(target.ID, "user", "问题", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.AddMessage(target.ID, "assistant", "回答", nil); err != nil {
		t.Fatal(err)
	}
	other, err := db.AddMessage(keep.ID, "user", "别碰我", nil)
	if err != nil {
		t.Fatal(err)
	}

	deletedIDs, err := db.DeleteConversationTurn(target.ID, anchor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deletedIDs) == 0 {
		t.Fatal("nothing was reported as deleted")
	}
	var leftTarget, leftOther int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id = ?`, target.ID).Scan(&leftTarget); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE id = ?`, other.ID).Scan(&leftOther); err != nil {
		t.Fatal(err)
	}
	if leftTarget != 0 {
		t.Fatalf("%d rows of the deleted turn are still there", leftTarget)
	}
	if leftOther != 1 {
		t.Fatal("the delete reached another conversation's rows")
	}

	// The statement is handed the caller's transaction, so refusing to run outside one is part of the
	// contract - a nil tx would otherwise delete before the caller can decide to roll back.
	if _, err := store.NewSession(nil).DeleteMessagesInTurn(nil, target.ID, []string{"x"}); err == nil {
		t.Fatal("a connectionless session deleted something")
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := store.NewSession(db.DB).DeleteMessagesInTurn(nil, target.ID, []string{"x"}); err == nil {
		t.Fatal("DeleteMessagesInTurn accepted a nil transaction")
	}
	// No ids is a no-op that answers zero, not an error and not a syntax failure on the empty IN ().
	if n, err := store.NewSession(db.DB).DeleteMessagesInTurn(tx, target.ID, nil); err != nil || n != 0 {
		t.Fatalf("empty id list: n=%d err=%v", n, err)
	}
}

func TestStartupBackfillGivesOldRowsTheirCreatedTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-backfill.db")
	db, err := NewDB(path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	conv := newConversation(t, db, "backfill")
	message, err := db.AddMessage(conv.ID, "assistant", "正文", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE messages SET updated_at = '' WHERE id = ?`, message.ID); err != nil {
		t.Fatal(err)
	}
	// Put the row into the shape the migration exists for: updated_at present but empty.
	var rawUpdated string
	if err := db.QueryRow(`SELECT COALESCE(updated_at,'') FROM messages WHERE id = ?`, message.ID).Scan(&rawUpdated); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(rawUpdated) != "" {
		t.Fatalf("the fixture is not in the legacy shape, updated_at=%q", rawUpdated)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// A second boot of the same file is what the migration step runs against.
	reopened, err := NewDB(path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var updated time.Time
	if err := reopened.QueryRow(`SELECT updated_at FROM messages WHERE id = ?`, message.ID).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.IsZero() {
		t.Fatal("the start-up backfill left updated_at empty; the console would show a finished message as current")
	}
	var created time.Time
	if err := reopened.QueryRow(`SELECT created_at FROM messages WHERE id = ?`, message.ID).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if !updated.Equal(created) {
		t.Fatalf("backfill left updated_at=%v want created_at=%v", updated, created)
	}
}
