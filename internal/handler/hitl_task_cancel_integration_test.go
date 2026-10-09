package handler

import (
	"context"
	"cyberstrike-ai/internal/database"
	"errors"
	"go.uber.org/zap"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskCancellationEndsPendingHumanApprovalAndTask(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "approval.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	approvals := NewHITLManager(db, zap.NewNop())
	if err := approvals.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	conversation, err := database.NewConversations(db).CreateConversation("test approval cancellation", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}
	message, err := database.NewConversations(db).AddMessage(conversation.ID, "assistant", "test pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	approvals.ActivateConversation(conversation.ID, &HITLRequest{Enabled: true, Mode: "approval", Reviewer: "human"})
	tasks := NewAgentTaskManager()
	ctx, cancel := context.WithCancelCause(context.Background())
	task, err := tasks.StartTask(conversation.ID, "test", cancel)
	if err != nil {
		t.Fatal(err)
	}
	// The approval policy and the conversations store are their own collaborators now, so a
	// hand-built handler has to wire them the way NewAgentHandler does: waitHITLApproval reads the
	// audit-engine info through the policy and persists the interrupt's process details through
	// the conversations store.
	h := attachHitlPolicy(t, &AgentHandler{db: db, logger: zap.NewNop(), tasks: tasks, hitlManager: approvals, conversations: database.NewConversations(db)})
	pending := make(chan string, 1)
	finished := make(chan error, 1)
	go func() {
		_, err := h.waitHITLApproval(ctx, cancel, conversation.ID, message.ID, "http-framework-test", "call-1", map[string]interface{}{"arguments": "test-only"}, func(kind, message string, data interface{}) {
			if kind == "hitl_interrupt" {
				pending <- data.(map[string]interface{})["interruptId"].(string)
			}
		})
		finished <- err
	}()
	var interruptID string
	select {
	case interruptID = <-pending:
	case <-time.After(5 * time.Second):
		t.Fatal("approval never became pending")
	}
	if _, err := tasks.CancelTask(conversation.ID, ErrTaskCancelled); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("approval error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval did not stop")
	}
	var approvalStatus string
	if err := db.QueryRow("SELECT status FROM hitl_interrupts WHERE id=?", interruptID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "cancelled" {
		t.Fatalf("approval status: %s", approvalStatus)
	}
	var doneStatus string
	send := h.taskFinishingEventSender(func(kind, message string, data interface{}) {
		if kind == "done" {
			doneStatus = data.(map[string]interface{})["status"].(string)
		}
	}, conversation.ID, task.RunID, func() string { return "completed" })
	send("done", "", map[string]interface{}{})
	if doneStatus != "cancelled" || len(tasks.GetActiveTasks()) != 0 {
		t.Fatalf("task still active or wrong result: %s", doneStatus)
	}
	if history := tasks.GetCompletedTasks(); len(history) != 1 || history[0].Status != "cancelled" {
		t.Fatal("cancelled history missing")
	}
}
