package workflow

import (
	"context"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/config"

	"go.uber.org/zap"
)

type workflowRuntimeCtxKey struct{}

// workflowRuntime carries per-run execution context into Eino Workflow local state.
type workflowRuntime struct {
	args  RunArgs
	runID string
	idx   *graphIndex
	state *WorkflowLocalState
}

func withWorkflowRuntime(ctx context.Context, rt *workflowRuntime) context.Context {
	return context.WithValue(ctx, workflowRuntimeCtxKey{}, rt)
}

func workflowRuntimeFrom(ctx context.Context) *workflowRuntime {
	rt, _ := ctx.Value(workflowRuntimeCtxKey{}).(*workflowRuntime)
	return rt
}

func newWorkflowRuntime(args RunArgs, runID string, idx *graphIndex, inputs map[string]interface{}) *workflowRuntime {
	return &workflowRuntime{
		args:  args,
		runID: runID,
		idx:   idx,
		state: newWorkflowLocalState(inputs, runID),
	}
}

// RunArgs is the execution context for a role-bound workflow run.
type RunArgs struct {
	DB                 Store
	Logger             *zap.Logger
	Role               config.RoleConfig
	AppCfg             *config.Config
	Agent              *agent.Agent
	ConversationID     string
	ProjectID          string
	UserMessage        string
	History            []agent.ChatMessage
	RoleTools          []string
	AgentsMarkdownDir  string
	SystemPromptExtra  string
	AssistantMessageID string
	Progress           agent.ProgressCallback
	// CheckAgentMode is the same fail-closed verdict chat, robot and batch ask before running:
	// "is this conversation mode executable right now". The wiring lives in the handler package
	// because the catalog does; a nil checker means a bare assembly, where only the built-in
	// single-agent mode may pass - an agent node naming an uninstalled mode fails its node
	// rather than becoming the back door that runs multi-agent after the pack was unplugged.
	CheckAgentMode func(id string) error
}

type RunResult struct {
	Response     string
	RunID        string
	Status       string
	AwaitingHITL bool
}
