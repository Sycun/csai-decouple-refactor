package database

import (
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/store"
)

// The two surfaces below are declared here rather than in the packages that consume them, because
// those packages import this one and the reverse would be a cycle. Each consumer keeps a
// type alias pointing back (project.Store, agentfinalizer.Store), so there is still exactly one
// list of methods to edit, and *DB's compliance is checked at compile time right here.

// ProjectFactStore is what the project blackboard, stats and fact-graph builders need: the project
// row plus the fact and fact-edge ledger.
//
// Deliberately absent: Close. A consumer of the shared handle must not be able to shut it down.
type ProjectFactStore interface {
	ProjectRowStore
	BlackboardLedger
}

// ProjectRowStore is the half of the old surface that is about the project itself: creating it,
// reading it, and the counters the project page shows. *DB answers it - the rows are its own.
type ProjectRowStore interface {
	CreateProject(p *Project) (*Project, error)
	GetProject(id string) (*Project, error)
	GetProjectStatsCounts(projectID string) (*ProjectStats, error)
}

// BlackboardLedger is the other half: the fact rows and the edges between them. store.Facts answers it
// directly, because the SQL for these tables is no longer written here at all. The names are the ones
// the data layer always used, so the consumers that switch to this half change a type, not a call.
type BlackboardLedger interface {
	ListProjectFactsForSparseCheck(projectID string) ([]store.ProjectFactSparseRow, error)
	ListProjectFactsForIndex(projectID string, includeDeprecated bool) ([]*store.ProjectFact, error)
	ListProjectFacts(projectID string, filter store.ProjectFactListFilter, limit, offset int) ([]*store.ProjectFact, error)
	GetProjectFactByKey(projectID, factKey string) (*store.ProjectFact, error)
	UpsertProjectFact(f *store.ProjectFact) (*store.ProjectFact, error)
	ListProjectFactEdgesByProject(projectID string) ([]*store.ProjectFactEdge, error)
	ListIncomingProjectFactEdges(projectID, targetFactKey string) ([]*store.ProjectFactEdge, error)
	ReplaceOutgoingProjectFactEdges(projectID, sourceFactKey, sourceConversationID string, inputs []store.ProjectFactEdgeInput) error
	ReplaceIncomingProjectFactEdges(projectID, targetFactKey string, inputs []store.ProjectFactEdgeFromInput) error
	AddProjectFactEdge(projectID string, in store.ProjectFactEdgeInput, sourceFactKey, sourceConversationID string) (*store.ProjectFactEdge, error)
}

var (
	_ ProjectFactStore = (*DB)(nil)
	_ ProjectRowStore  = (*DB)(nil)
	_ BlackboardLedger = (*store.Facts)(nil)
)

// ToolExecutionLedger is what the run finalizer needs: read a tool execution's recorded state, and
// write one back when a cancelled run leaves an execution dangling.
type ToolExecutionLedger interface {
	GetToolExecution(id string) (*mcp.ToolExecution, error)
	SaveToolExecution(exec *mcp.ToolExecution) error
}

var _ ToolExecutionLedger = (*DB)(nil)

// WorkflowRunLedger used to live here. The five workflow tables moved to store.Workflows, and the
// ledger the engine declares is now in internal/workflow - so this file no longer carries a workflow
// surface at all, and internal/workflow no longer imports this package.

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
	GetProjectFactByKey(projectID, factKey string) (*store.ProjectFact, error)
}

var _ AttackChainLedger = (*DB)(nil)
