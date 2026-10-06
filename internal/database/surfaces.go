package database

import (
	"cyberstrike-ai/internal/store"
)

// The two surfaces below are declared here rather than in the packages that consume them, because
// those packages import this one and the reverse would be a cycle. Each consumer keeps a
// type alias pointing back (project.Store, agentfinalizer.Store), so there is still exactly one
// list of methods to edit, and *DB's compliance is checked at compile time right here.

// ProjectRowStore is the half of the split surface that is about the project itself: creating it,
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

// Deliberately absent from both: Close. A consumer of the shared handle must not be able to shut it
// down.
var (
	_ ProjectRowStore  = (*DB)(nil)
	_ BlackboardLedger = (*store.Facts)(nil)
)

// ToolExecutionLedger 已随 tool_executions 的语句一起离开连接包装：finalizer 的读面现在声明在
// internal/agentfinalizer（消费者侧），由 store.Monitor 满足。

// WorkflowRunLedger used to live here. The five workflow tables moved to store.Workflows, and the
// ledger the engine declares is now in internal/workflow - so this file no longer carries a workflow
// surface at all, and internal/workflow no longer imports this package.

// AttackChainLedger is what the attack-chain builder and the "promote to project" path need: the
// conversation evidence the chain is reconstructed from and the project rows it writes into.
// The facts and edges promoting writes belong to store.Facts and arrive as their own argument.
// 会话证据的问句（GetConversation/GetMessages/GetAgentTrace/过程详情/工具详情存在性）已随
// store.Conversations 交走；builder 现在同时持有它，这个接口只剩项目行与项目行本身的读。
type AttackChainLedger interface {
	ProjectRowStore
	GetProject(id string) (*Project, error)
}

var _ AttackChainLedger = (*DB)(nil)
