package database

import "cyberstrike-ai/internal/mcp"

// The two surfaces below are declared here rather than in the packages that consume them, because
// those packages import this one and the reverse would be a cycle. Each consumer keeps a
// type alias pointing back (project.Store, agentfinalizer.Store), so there is still exactly one
// list of methods to edit, and *DB's compliance is checked at compile time right here.

// ProjectFactStore is what the project blackboard, stats and fact-graph builders need: the project
// row plus the fact and fact-edge ledger.
//
// Deliberately absent: Close. A consumer of the shared handle must not be able to shut it down.
type ProjectFactStore interface {
	CreateProject(p *Project) (*Project, error)
	GetProject(id string) (*Project, error)
	GetProjectStatsCounts(projectID string) (*ProjectStats, error)
	ListProjectFactsForSparseCheck(projectID string) ([]ProjectFactSparseRow, error)
	ListProjectFactsForIndex(projectID string, includeDeprecated bool) ([]*ProjectFact, error)
	ListProjectFacts(projectID string, filter ProjectFactListFilter, limit, offset int) ([]*ProjectFact, error)
	GetProjectFactByKey(projectID, factKey string) (*ProjectFact, error)
	UpsertProjectFact(f *ProjectFact) (*ProjectFact, error)
	ListProjectFactEdgesByProject(projectID string) ([]*ProjectFactEdge, error)
	ListIncomingProjectFactEdges(projectID, targetFactKey string) ([]*ProjectFactEdge, error)
	ReplaceOutgoingProjectFactEdges(projectID, sourceFactKey, sourceConversationID string, inputs []ProjectFactEdgeInput) error
	ReplaceIncomingProjectFactEdges(projectID, targetFactKey string, inputs []ProjectFactEdgeFromInput) error
	AddProjectFactEdge(projectID string, in ProjectFactEdgeInput, sourceFactKey, sourceConversationID string) (*ProjectFactEdge, error)
}

var _ ProjectFactStore = (*DB)(nil)

// ToolExecutionLedger is what the run finalizer needs: read a tool execution's recorded state, and
// write one back when a cancelled run leaves an execution dangling.
type ToolExecutionLedger interface {
	GetToolExecution(id string) (*mcp.ToolExecution, error)
	SaveToolExecution(exec *mcp.ToolExecution) error
}

var _ ToolExecutionLedger = (*DB)(nil)

// WorkflowRunLedger is the workflow engine's own run state: which run is executing, which node is
// awaiting approval, and how each finished. Anything broader than this is another domain's table.
type WorkflowRunLedger interface {
	CreateWorkflowRun(run *WorkflowRun) error
	GetWorkflowRun(runID string) (*WorkflowRun, error)
	SetWorkflowRunStatus(runID, status string) error
	SetWorkflowRunAwaitingHITL(runID, nodeID, pendingJSON string) error
	FinishWorkflowRun(runID, status, outputJSON, errText string) error
	CreateWorkflowNodeRun(n *WorkflowNodeRun) error
	FinishWorkflowNodeRun(nodeRunID, status, outputJSON, errText string) error
	GetWorkflowDefinition(id string) (*WorkflowDefinition, error)
}

var _ WorkflowRunLedger = (*DB)(nil)

// AttackChainLedger is what the attack-chain builder and the "promote to project" path need: the
// chain's node/edge rows, the conversation evidence they are reconstructed from, and - because
// promoting writes facts and edges into the project - the whole project fact surface as well.
type AttackChainLedger interface {
	ProjectFactStore
	ConversationHasToolProcessDetails(conversationID string) (bool, error)
	GetAgentTrace(conversationID string) (traceInputJSON, assistantOutput string, err error)
	GetConversation(id string) (*Conversation, error)
	GetMessages(conversationID string) ([]Message, error)
	GetProcessDetailsByConversation(conversationID string) (map[string][]ProcessDetail, error)
	GetProject(id string) (*Project, error)
	GetProjectFactByKey(projectID, factKey string) (*ProjectFact, error)
}

var _ AttackChainLedger = (*DB)(nil)
