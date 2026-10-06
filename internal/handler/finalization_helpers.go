package handler

import (
	"fmt"
	"strings"

	"cyberstrike-ai/internal/agentfinalizer"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/multiagent"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// runFinalizer owns what happens to a finished agent run: decide the terminal status, persist it,
// cancel tool executions left dangling by a cancelled run, and the auto-continue step that gives a
// run one more turn when it stopped on an unfinished tool call.
//
// It came out of AgentHandler because its whole dependency set is four narrow things - the storage
// surface, a logger, one cancel call on the agent, and one message-content write - while the
// methods that stayed in AgentHandler are the run loop itself (tasks, sessions, SSE, HITL resume).
// content is the AgentHandler that owns the messages store, taken through the one method this
// needs rather than as the whole handler.
type runFinalizer struct {
	db            database.AgentStore
	executions    *store.Monitor
	conversations *store.Conversations
	logger        *zap.Logger
	agent         cancellableToolExecution
	content       messageContentWriter
}

type cancellableToolExecution interface {
	CancelMCPToolExecutionWithNote(executionID, note string) bool
}

type messageContentWriter interface {
	setMessageContent(messageID, content string) error
}

// newRunFinalizer takes the concrete handle and narrows it here, at the boundary, like every other
// collaborator in this package: a caller that happens to hold a nil *database.DB then cannot turn
// the field into a non-nil interface wrapping nil.
func newRunFinalizer(db *database.DB, logger *zap.Logger, agent cancellableToolExecution, content messageContentWriter) *runFinalizer {
	return &runFinalizer{
		db:            database.Narrow[database.AgentStore](db),
		executions:    database.NewMonitor(db),
		conversations: database.NewConversations(db),
		logger:        logger,
		agent:         agent,
		content:       content,
	}
}

// decisionStore keeps the boundary rule newRunFinalizer documents: a nil *store.Monitor must stay a
// nil interface, or agentfinalizer's `db == nil` branch takes the wrong path.
func (f *runFinalizer) decisionStore() agentfinalizer.Store {
	if f.executions == nil {
		return nil
	}
	return f.executions
}

func (f *runFinalizer) finalizeAgentRunForDelivery(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	result *multiagent.RunResult,
	mcpExecutionIDs []string,
	reasoningContent string,
) agentfinalizer.Decision {
	return f.finalizeAgentRunForDeliveryWithPolicy(conversationID, assistantMessageID, agentMode, result, mcpExecutionIDs, reasoningContent, false)
}

func (f *runFinalizer) finalizeAgentRunForDeliveryWithPolicy(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	result *multiagent.RunResult,
	mcpExecutionIDs []string,
	reasoningContent string,
	requireExecutionEvidence bool,
) agentfinalizer.Decision {
	decision := agentfinalizer.FromRunResult(f.decisionStore(), result, agentfinalizer.Input{
		ConversationID:           conversationID,
		AssistantMessageID:       assistantMessageID,
		AgentMode:                agentMode,
		MCPExecutionIDs:          mcpExecutionIDs,
		RequireExecutionEvidence: requireExecutionEvidence,
	})
	f.persistFinalizationDecision(conversationID, assistantMessageID, agentMode, mcpExecutionIDs, reasoningContent, decision)
	return decision
}

func (f *runFinalizer) decideAgentRunForDeliveryWithPolicy(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	result *multiagent.RunResult,
	mcpExecutionIDs []string,
	requireExecutionEvidence bool,
) agentfinalizer.Decision {
	return agentfinalizer.FromRunResult(f.decisionStore(), result, agentfinalizer.Input{
		ConversationID:           conversationID,
		AssistantMessageID:       assistantMessageID,
		AgentMode:                agentMode,
		MCPExecutionIDs:          mcpExecutionIDs,
		RequireExecutionEvidence: requireExecutionEvidence,
	})
}

func (f *runFinalizer) decideAgentRunForDelivery(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	result *multiagent.RunResult,
	mcpExecutionIDs []string,
) agentfinalizer.Decision {
	return agentfinalizer.FromRunResult(f.decisionStore(), result, agentfinalizer.Input{
		ConversationID:           conversationID,
		AssistantMessageID:       assistantMessageID,
		AgentMode:                agentMode,
		MCPExecutionIDs:          mcpExecutionIDs,
		RequireExecutionEvidence: false,
	})
}

func (f *runFinalizer) persistFinalizationDecision(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	mcpExecutionIDs []string,
	reasoningContent string,
	decision agentfinalizer.Decision,
) {
	if assistantMessageID == "" || f.db == nil {
		return
	}
	_ = f.conversations.AddProcessDetail(assistantMessageID, conversationID, "finalization_check", finalizationCheckMessage(decision), decision)
	if decision.Finalizable {
		if err := f.conversations.UpdateAssistantMessageFinalize(assistantMessageID, decision.FinalText, mcpExecutionIDs, reasoningContent); err != nil && f.logger != nil {
			f.logger.Warn("更新最终助手消息失败", zap.Error(err), zap.String("conversationId", conversationID), zap.String("agentMode", agentMode))
		}
		return
	}
	_ = f.content.setMessageContent(assistantMessageID, finalizationBlockedMessage(decision))
}

func (f *runFinalizer) finalizeCandidateForDelivery(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	response string,
	mcpExecutionIDs []string,
	awaitingHITL bool,
	reasoningContent string,
) agentfinalizer.Decision {
	return f.finalizeCandidateForDeliveryWithPolicy(conversationID, assistantMessageID, agentMode, response, mcpExecutionIDs, awaitingHITL, reasoningContent, false)
}

func (f *runFinalizer) finalizeCandidateForDeliveryWithPolicy(
	conversationID string,
	assistantMessageID string,
	agentMode string,
	response string,
	mcpExecutionIDs []string,
	awaitingHITL bool,
	reasoningContent string,
	requireExecutionEvidence bool,
) agentfinalizer.Decision {
	decision := agentfinalizer.Decide(f.decisionStore(), agentfinalizer.Input{
		Response:                 response,
		ConversationID:           conversationID,
		AssistantMessageID:       assistantMessageID,
		AgentMode:                agentMode,
		MCPExecutionIDs:          mcpExecutionIDs,
		AwaitingHITL:             awaitingHITL,
		RequireExecutionEvidence: requireExecutionEvidence,
	})
	if assistantMessageID == "" || f.db == nil {
		return decision
	}
	_ = f.conversations.AddProcessDetail(assistantMessageID, conversationID, "finalization_check", finalizationCheckMessage(decision), decision)
	if decision.Finalizable {
		if err := f.conversations.UpdateAssistantMessageFinalize(assistantMessageID, decision.FinalText, mcpExecutionIDs, reasoningContent); err != nil && f.logger != nil {
			f.logger.Warn("更新最终助手消息失败", zap.Error(err), zap.String("conversationId", conversationID), zap.String("agentMode", agentMode))
		}
		return decision
	}
	_ = f.content.setMessageContent(assistantMessageID, finalizationBlockedMessage(decision))
	return decision
}

func finalizationCheckMessage(d agentfinalizer.Decision) string {
	if d.Finalizable {
		return "最终回复检查通过。"
	}
	return finalizationBlockedMessage(d)
}

func finalizationBlockedMessage(d agentfinalizer.Decision) string {
	parts := []string{"任务尚未达到最终回复条件，暂不生成成功结论。"}
	if d.CompletionReason != "" {
		parts = append(parts, "原因: "+d.CompletionReason)
	}
	if len(d.PendingExecutionIDs) > 0 {
		parts = append(parts, fmt.Sprintf("仍有 %d 个工具执行未结束: %s", len(d.PendingExecutionIDs), strings.Join(d.PendingExecutionIDs, ", ")))
	}
	if len(d.MissingChecks) > 0 {
		parts = append(parts, "缺失检查: "+strings.Join(d.MissingChecks, "; "))
	}
	return strings.Join(parts, "\n")
}

func finalizationResponsePayload(d agentfinalizer.Decision, extra map[string]interface{}) map[string]interface{} {
	return agentfinalizer.ResponsePayload(d, extra)
}

func requestRequiresExecutionEvidence(req *ChatRequest) bool {
	return req != nil && req.Finalization.RequireExecutionEvidence != nil && *req.Finalization.RequireExecutionEvidence
}
