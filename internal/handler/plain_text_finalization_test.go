package handler

import (
	"context"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/multiagent"
	"go.uber.org/zap"
)

func TestRobotDeliversPlainTextWithoutToolEvidence(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "robot.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conv, err := database.NewConversations(db).CreateConversation("text", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}
	h := &AgentHandler{db: db, logger: zap.NewNop(), conversations: database.NewConversations(db)}
	// Same wiring as NewAgentHandler: the finalizer and the conversations store are their own
	// collaborators now, so a hand-built handler has to build them the way production does rather
	// than lean on a lazy accessor.
	h.finalizer = newRunFinalizer(db, zap.NewNop(), nil, h)
	result := &multiagent.RunResult{Response: "OK"}
	text, _, err := h.finalizeRobotAgentSuccess(context.Background(), "", conv.ID, result)
	if err != nil || text != "OK" || !result.Finalized {
		t.Fatalf("text=%q finalized=%v error=%v", text, result.Finalized, err)
	}
}
