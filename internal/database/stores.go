package database

import (
	"database/sql"
	"time"
)

// Narrow persistence surfaces implemented by *DB.

// One 361-method object embedding *sql.DB was handed to every package in the process,
// so any consumer could reach any table and any raw statement. Each interface below is
// exactly what one consumer calls today. *DB satisfies all of them, so nothing about
// runtime behaviour changes: the compiler verifies the narrowing, and pulling a domain
// into its own store later is a move rather than a rewrite, because the store only has to
// keep satisfying the same interface.

// The raw Exec/Query/Begin members below are debt, not design: they mark the handlers
// that still build SQL in the HTTP layer. Each one is a to-do for a domain store, and the
// handler_raw_sql_ratchet test keeps the count from growing.

// AgentStore is the persistence surface required by AgentHandler.
type AgentStore interface {
	ProjectRowStore
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

var _ AgentStore = (*DB)(nil)

// AssetContextStore is what an asset request still needs from *other* domains: the project name that
// comes with an asset row, and the RBAC existence check behind a visibility question. The asset rows
// themselves left this interface - store.Assets answers for those, so no method on the connection
// wrapper can change the assets table any more.
type AssetContextStore interface {
	GetProject(id string) (*Project, error)
}

var _ AssetContextStore = (*DB)(nil)

// AttackChainStore is the persistence surface required by AttackChainHandler.
type AttackChainStore interface {
	AttackChainLedger
	// The rest reaches this surface through attackchain.NewBuilder, which declares its own Store
	// (a fourth case of the generation blind spot: interfaces built from direct h.db.X calls miss
	// anything the handler hands to a helper as an argument). Keep it aligned with attackchain.Store.
}

var _ AttackChainStore = (*DB)(nil)

// ResourceExistence is the read surface audit's existence check needs: given the resource an audit
// row points at, does that row still exist? Seven lookups over six other tables, declared here
// because internal/audit is the consumer and handing it the connection wrapper would put every table
// back within reach.
//
// audit_logs' own queries are deliberately absent: they live in store.AuditLogs now, and an interface
// that mixes a table's reads with other tables' existence checks is how a surface stops describing
// one thing. The findings lookup is absent for the same reason - it is store.Vulnerabilities.Get.

// BatchTaskStore 已删除：批量任务两张表的 22 个方法全部由 store.BatchTasks 亲自答，
// 消费者（BatchTaskManager）现在直接持有那个 store。

// ChatUploadsStore is the persistence surface required by ChatUploadsHandler.
type ChatUploadsStore interface {
	GetProjectName(id string) (string, error)
}

var _ ChatUploadsStore = (*DB)(nil)

// ConfigStore is the persistence surface required by ConfigHandler.

// ConversationStore is the persistence surface required by ConversationHandler.
type ConversationStore interface {
	ListConversationPlanTasksSince(conversationID string, since time.Time) ([]ConversationPlanTask, error)
}

var _ ConversationStore = (*DB)(nil)

// MonitorContextStore is what MonitorHandler still needs from the connection wrapper: the RBAC
// conversation visibility that toolExecutionVisible reaches for. The fourteen tool-execution members
// it used to carry are store.Monitor's now (see §11 第四十一刀).

// NotificationStore is the persistence surface required by NotificationHandler.
type NotificationStore interface {
	Begin() (*sql.Tx, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

var _ NotificationStore = (*DB)(nil)

// OpenAPIStore is the persistence surface required by OpenAPIHandler.
type OpenAPIStore interface {
}

var _ OpenAPIStore = (*DB)(nil)

// ProjectStore is the persistence surface required by ProjectHandler.
type ProjectStore interface {
	ProjectRowStore
	AttackChainLedger
	CountConversationsByProjectID(projectID string) (int, error)
	CountProjectsForAccess(status, search, userID, scope string) (int, error)
	CreateProject(p *Project) (*Project, error)
	DeleteProject(id string) error
	GetProject(id string) (*Project, error)
	GetProjectDashboardSummaryForAccess(factLimit int, userID, scope string) (*ProjectDashboardSummary, error)
	ListConversationsByProjectID(projectID string, limit, offset int) ([]*Conversation, error)
	ListProjectsForAccess(status, search string, limit, offset int, userID, scope string) ([]*Project, error)
	UpdateProject(p *Project) error
}

var _ ProjectStore = (*DB)(nil)

// RBACStore is the persistence surface required by RBACHandler.

// RobotStore is the persistence surface required by RobotHandler. The alert subscription and the
// durable outbox are not in it any more - they are store.VulnerabilityAlerts, and the robot handler
// holds that store directly (its worker only needs the queue, its commands only need the subscription).
type RobotStore interface {
	CreateProject(p *Project) (*Project, error)
	GetProject(id string) (*Project, error)
	ListProjectsForAccess(status, search string, limit, offset int, userID, scope string) ([]*Project, error)
}

var _ RobotStore = (*DB)(nil)

// VulnerabilityStore is what VulnerabilityHandler still needs from the connection wrapper once the
// finding rows themselves moved to store.Vulnerabilities: the three RBAC lookups a finding's owner and
// candidates are decided with. The alert subscription the same page edits is store.VulnerabilityAlerts,
// so it is not here either. The name says which handler this surface serves, not which table it owns.

// WebShellStore is the persistence surface required by WebShellHandler.
type WebShellStore interface {
}

var _ WebShellStore = (*DB)(nil)

// WorkflowStore is what the workflow handler still needs from the connection wrapper once the five
// workflow tables moved to store.Workflows: the project fact surface a run writes into, the process
// detail a run appends, and the one RBAC question the page asks. The workflow rows themselves are not
// here - h.runs answers those.
type WorkflowStore interface {
	ProjectRowStore
}

var _ WorkflowStore = (*DB)(nil)
