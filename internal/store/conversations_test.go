package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// The conversation ledger - the conversations table, the read side of messages / process_details,
// the agent trace columns and the create hook - moved out of the connection wrapper. These cases
// run against a real database: the conversations DDL still boots from the data layer (its cut is
// the boot skeleton's), so the test builds the same table plus the sessions' schema it reads.

func testConversationsStore(t *testing.T) (*Conversations, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "conversations.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE conversations (
		id TEXT PRIMARY KEY, title TEXT NOT NULL, project_id TEXT, role_name TEXT, agent_mode TEXT,
		pinned INTEGER DEFAULT 0, last_react_input TEXT, last_react_output TEXT,
		webshell_connection_id TEXT, owner_user_id TEXT,
		created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`); err != nil {
		t.Fatalf("seed conversations: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT, status TEXT DEFAULT 'active', updated_at DATETIME, owner_user_id TEXT)`); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if err := NewSession(db).EnsureSchema(); err != nil {
		t.Fatalf("session schema: %v", err)
	}
	if err := NewSession(db).MigrateMessageColumns(); err != nil {
		t.Fatalf("message columns: %v", err)
	}
	if err := NewRBAC(db).EnsureSchema(); err != nil {
		t.Fatalf("rbac schema: %v", err)
	}
	return NewConversations(db), db
}

func TestConversationsCreateReadUpdateDelete(t *testing.T) {
	c, db := testConversationsStore(t)
	if _, err := db.Exec(`INSERT INTO projects (id, name) VALUES ('p1', 'P1')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	conv, err := c.CreateConversation("first", ConversationCreateMeta{ProjectID: "p1", RoleName: "  红队  ", AgentMode: "deep"})
	if err != nil || conv == nil {
		t.Fatalf("create: %v", err)
	}
	if conv.RoleName != "红队" || conv.AgentMode != "deep" || conv.ProjectID != "p1" {
		t.Fatalf("meta normalisation lost: %#v", conv)
	}
	if _, err := c.CreateConversation("ghost", ConversationCreateMeta{ProjectID: "missing"}); err == nil {
		t.Fatal("a missing project must be refused")
	}
	got, err := c.GetConversation(conv.ID)
	if err != nil || got == nil || got.Title != "first" || got.AgentMode != "deep" {
		t.Fatalf("get: %#v / %v", got, err)
	}
	if err := c.UpdateConversationTitle(conv.ID, "renamed"); err != nil {
		t.Fatalf("title: %v", err)
	}
	if err := c.UpdateConversationPinned(conv.ID, true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := c.SetConversationRoleName(conv.ID, ""); err != nil {
		t.Fatalf("role: %v", err)
	}
	if err := c.SetConversationAgentMode(conv.ID, "bogus"); err != nil {
		t.Fatalf("mode: %v", err)
	}
	got, _ = c.GetConversation(conv.ID)
	if got.Title != "renamed" || !got.Pinned || got.RoleName != "默认" || got.AgentMode != "eino_single" {
		t.Fatalf("updates lost: %#v", got)
	}
	exists, err := c.ConversationExists(conv.ID)
	if err != nil || !exists {
		t.Fatalf("exists = %v/%v", exists, err)
	}
	if err := c.SaveAgentTrace(conv.ID, `{"in":1}`, "out"); err != nil {
		t.Fatalf("trace: %v", err)
	}
	in, out, err := c.GetAgentTrace(conv.ID)
	if err != nil || in != `{"in":1}` || out != "out" {
		t.Fatalf("trace roundtrip: %q %q %v", in, out, err)
	}
	pid, err := c.GetConversationProjectID(conv.ID)
	if err != nil || pid != "p1" {
		t.Fatalf("project id = %q/%v", pid, err)
	}
	if err := c.SetConversationProjectID(conv.ID, ""); err != nil {
		t.Fatalf("unbind: %v", err)
	}
	if pid, _ = c.GetConversationProjectID(conv.ID); pid != "" {
		t.Fatalf("project id after unbind = %q", pid)
	}
	if err := c.SetConversationProjectID(conv.ID, "missing"); err == nil {
		t.Fatal("binding to a missing project must be refused")
	}
	deleted := ""
	c.SetSourceTagBackfill(func(id string) { deleted = id })
	if err := c.DeleteConversation(conv.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted != conv.ID {
		t.Fatalf("findings backfill ran with %q, want %q", deleted, conv.ID)
	}
	if exists, _ = c.ConversationExists(conv.ID); exists {
		t.Fatal("the conversation survived the delete")
	}
}

func TestConversationsMessagesAndTurns(t *testing.T) {
	c, _ := testConversationsStore(t)
	conv, err := c.CreateConversation("t", ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	user, err := c.AddMessage(conv.ID, "user", "hello", []string{"e1"})
	if err != nil || user == nil {
		t.Fatalf("add user: %v", err)
	}
	assistant, err := c.AddMessage(conv.ID, "assistant", "处理中...", nil)
	if err != nil {
		t.Fatalf("add assistant: %v", err)
	}
	if err := c.UpdateAssistantMessageFinalize(assistant.ID, "done", []string{"e2"}, "think"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	msgs, err := c.GetMessages(conv.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("messages = %d/%v", len(msgs), err)
	}
	if msgs[1].Content != "done" || msgs[1].ReasoningContent != "think" || len(msgs[1].MCPExecutionIDs) != 1 || msgs[1].MCPExecutionIDs[0] != "e2" {
		t.Fatalf("finalized message = %#v", msgs[1])
	}
	lite, err := c.GetMessagesLite(conv.ID)
	if err != nil || len(lite) != 2 || lite[1].ReasoningContent != "" {
		t.Fatalf("lite = %#v/%v", lite, err)
	}
	// Second turn, then delete the first one by its anchor message.
	second, err := c.AddMessage(conv.ID, "user", "again", nil)
	if err != nil {
		t.Fatalf("add second: %v", err)
	}
	deleted, err := c.DeleteConversationTurn(conv.ID, user.ID)
	if err != nil || len(deleted) != 2 {
		t.Fatalf("turn delete = %v/%v", deleted, err)
	}
	msgs, _ = c.GetMessages(conv.ID)
	if len(msgs) != 1 || msgs[0].ID != second.ID {
		t.Fatalf("messages after turn delete = %#v", msgs)
	}
	trace, out, _ := c.GetAgentTrace(conv.ID)
	if trace != "" || out != "" {
		t.Fatalf("turn delete must clear the trace, got %q/%q", trace, out)
	}
	if got, err := c.GetTurnUserMessage(conv.ID, second.ID); err != nil || got != "again" {
		t.Fatalf("turn user message = %q/%v", got, err)
	}
}

func TestConversationsProcessDetailsAndSummary(t *testing.T) {
	c, db := testConversationsStore(t)
	conv, _ := c.CreateConversation("t", ConversationCreateMeta{})
	msg, err := c.AddMessage(conv.ID, "assistant", "处理中...", nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := c.AddProcessDetailWithID(msg.ID, conv.ID, "planning", "snapshot", map[string]interface{}{"partial": true}); err != nil {
		t.Fatalf("planning: %v", err)
	}
	callID, err := c.AddProcessDetailWithID(msg.ID, conv.ID, "tool_call", "call", map[string]interface{}{"toolName": "nmap", "toolCallId": "c1"})
	if err != nil {
		t.Fatalf("tool_call: %v", err)
	}
	if _, err := c.AddProcessDetailWithID(msg.ID, conv.ID, "tool_result", "result", map[string]interface{}{"toolName": "nmap", "toolCallId": "c1", "success": true}); err != nil {
		t.Fatalf("tool_result: %v", err)
	}
	details, err := c.GetProcessDetails(msg.ID)
	if err != nil || len(details) != 3 {
		t.Fatalf("details = %d/%v", len(details), err)
	}
	if got, err := c.GetProcessDetailByID(callID); err != nil || got == nil || got.EventType != "tool_call" {
		t.Fatalf("by id: %#v/%v", got, err)
	}
	summary, err := c.GetProcessDetailsSummary(msg.ID)
	if err != nil || summary == nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Status != "running" || summary.ToolCount != 1 || len(summary.ToolExecutions) != 1 || summary.ToolExecutions[0].Status != "completed" {
		t.Fatalf("summary = %#v", summary)
	}
	if _, total, err := c.GetProcessDetailsPage(msg.ID, 2, 0); err != nil || total != 3 {
		t.Fatalf("page = %d/%v", total, err)
	}
	// 同秒落库的 created_at 在 DATETIME 列上比较不区分先后（既有语义），先把三行错开再钉
	// offset 的本意：锚点前面有几行。
	if _, err := db.Exec(`UPDATE process_details SET created_at = CASE rowid WHEN 1 THEN '2026-01-01T00:00:01Z' WHEN 2 THEN '2026-01-01T00:00:02Z' ELSE '2026-01-01T00:00:03Z' END WHERE message_id = ?`, msg.ID); err != nil {
		t.Fatalf("spread timestamps: %v", err)
	}
	if offset, err := c.GetProcessDetailOffset(msg.ID, callID); err != nil || offset != 1 {
		t.Fatalf("offset = %d/%v, want 1 (anchor is the second row)", offset, err)
	}
	byConv, err := c.GetProcessDetailsByConversation(conv.ID)
	if err != nil || len(byConv[msg.ID]) != 3 {
		t.Fatalf("by conversation = %d/%v", len(byConv[msg.ID]), err)
	}
}

func TestConversationsAccessFiltersRespectAssignments(t *testing.T) {
	c, db := testConversationsStore(t)
	r := NewRBAC(db)
	alice, err := r.CreateRBACUser("alice", "Alice", "h", true, nil)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	visible, _ := c.CreateConversation("visible", ConversationCreateMeta{})
	hidden, _ := c.CreateConversation("hidden", ConversationCreateMeta{})
	if err := r.AssignResourceToUser(alice.ID, "conversation", visible.ID); err != nil {
		t.Fatalf("assign: %v", err)
	}
	var assignmentRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rbac_resource_assignments WHERE user_id = ? AND resource_type = 'conversation' AND resource_id = ?`, alice.ID, visible.ID).Scan(&assignmentRows); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	t.Logf("assignments for visible = %d; alice=%q visible=%q", assignmentRows, alice.ID, visible.ID)
	var manual int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversations c WHERE c.owner_user_id = ? OR EXISTS (SELECT 1 FROM rbac_resource_assignments ra WHERE ra.user_id = ? AND ra.resource_type = 'conversation' AND ra.resource_id = c.id)`, alice.ID, alice.ID).Scan(&manual); err != nil {
		t.Logf("manual query err = %v", err)
	}
	t.Logf("manual predicate matches = %d", manual)
	where, fargs := appendConversationAccessFilter("", nil, alice.ID, ScopeAssigned, "")
	where = " WHERE" + strings.TrimPrefix(where, " AND")
	var clauseCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM conversations"+where, fargs...).Scan(&clauseCount); err != nil {
		t.Logf("clause err = %v", err)
	}
	t.Logf("clause=%q args=%d clauseCount=%d", where, len(fargs), clauseCount)
	list, err := c.ListConversationsForAccess(10, 0, "", "", "", alice.ID, ScopeAssigned)
	if err != nil || len(list) != 1 || list[0].ID != visible.ID {
		t.Fatalf("list = %#v/%v", list, err)
	}
	count, err := c.CountConversationsForAccess("", "", alice.ID, ScopeAssigned)
	if err != nil || count != 1 {
		t.Fatalf("count = %d/%v", count, err)
	}
	if _, err := c.GetConversation(hidden.ID); err != nil {
		t.Fatalf("the row itself is still readable: %v", err)
	}
	all, err := c.ListConversationsForAccess(10, 0, "", "", "", "", ScopeAll)
	if err != nil || len(all) != 2 {
		t.Fatalf("scope all = %d/%v", len(all), err)
	}
}
