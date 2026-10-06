package database

import (
	"cyberstrike-ai/internal/store"
)

// The two surfaces below are declared here rather than in the packages that consume them, because
// those packages import this one and the reverse would be a cycle. Each consumer keeps a
// type alias pointing back (project.Store, agentfinalizer.Store), so there is still exactly one
// list of methods to edit, and *DB's compliance is checked at compile time right here.

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

// Deliberately absent from both: Close. A consumer of the shared handle must not be able to shut it
// down.
// BlackboardLedger 的满足者只剩 store.Facts（项目行那半已随 store.Projects 交走）。
var _ BlackboardLedger = (*store.Facts)(nil)

// ToolExecutionLedger 已随 tool_executions 的语句一起离开连接包装：finalizer 的读面现在声明在
// internal/agentfinalizer（消费者侧），由 store.Monitor 满足。

// WorkflowRunLedger used to live here. The five workflow tables moved to store.Workflows, and the
// ledger the engine declares is now in internal/workflow - so this file no longer carries a workflow
// surface at all, and internal/workflow no longer imports this package.
