package handler

import (
	"context"
	"cyberstrike-ai/internal/database"
	"go.uber.org/zap"
	"path/filepath"
	"testing"
)

func TestWorkflowActivityIsPersistedAndDeliveredToStream(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "progress.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conversation, err := database.NewConversations(db).CreateConversation("test-only-workflow", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}
	message, err := database.NewConversations(db).AddMessage(conversation.ID, "assistant", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The process details the callback persists go through the conversations store now, not the
	// connection wrapper, so a hand-built handler has to wire it the way NewAgentHandler does.
	handler := &AgentHandler{db: db, logger: zap.NewNop(), tasks: NewAgentTaskManager(), conversations: database.NewConversations(db)}
	streamed := map[string]int{}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	callback := handler.createProgressCallback(ctx, cancel, conversation.ID, message.ID, func(kind, message string, data interface{}) { streamed[kind]++ })
	kinds := []string{"workflow_node_start", "iteration", "tool_call", "tool_result", "workflow_node_result"}
	for _, kind := range kinds {
		callback(kind, "test-only-progress", map[string]interface{}{"workflowNodeId": "agent-1", "workflowRunId": "test-only-run", "nodeId": "agent-1", "nodeType": "agent", "iteration": 1, "toolCallId": "test-only-call", "tool": "test-only-tool", "arguments": map[string]interface{}{}, "result": "test-only-result", "success": true})
	}
	details, err := database.NewConversations(db).GetProcessDetails(message.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted := map[string]int{}
	for _, detail := range details {
		persisted[detail.EventType]++
	}
	for _, kind := range kinds {
		if streamed[kind] != 1 || persisted[kind] != 1 {
			t.Fatalf("%s delivered=%d persisted=%d", kind, streamed[kind], persisted[kind])
		}
	}
}
