package database

import (
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/store"
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
	ToolExecutionLedger
	AddMessage(conversationID, role, content string, mcpExecutionIDs []string) (*Message, error)
	AddProcessDetail(messageID, conversationID, eventType, message string, data interface{}) error
	AddProcessDetailWithID(messageID, conversationID, eventType, message string, data interface{}) (string, error)
	AssignResourceToUser(userID, resourceType, resourceID string) error
	CreateConversation(title string, meta ConversationCreateMeta) (*Conversation, error)
	CreateConversationWithWebshell(webshellConnectionID, title string, meta ConversationCreateMeta) (*Conversation, error)
	DeleteProcessDetail(id string) error
	GetAgentTrace(conversationID string) (traceInputJSON, assistantOutput string, err error)
	GetAssistantCognitionTexts(assistantMessageID string) (AssistantCognitionTexts, error)
	GetConversation(id string) (*Conversation, error)
	GetConversationProjectID(conversationID string) (string, error)
	GetConversationTitle(id string) (string, error)
	GetMessages(conversationID string) ([]Message, error)
	GetResourceOwner(resourceType, resourceID string) string
	GetToolExecution(id string) (*mcp.ToolExecution, error)
	GetTurnUserMessage(conversationID, anchorMessageID string) (string, error)
	ResolveRBACAccess(userID string) (*RBACAccess, error)
	SaveAgentTrace(conversationID, traceInputJSON, assistantOutput string) error
	SetConversationAgentMode(id, agentMode string) error
	SetConversationRoleName(id, roleName string) error
	SetResourceOwner(resourceType, resourceID, userID string) error
	UpdateAssistantMessageFinalize(messageID, content string, mcpExecutionIDs []string, reasoningContent string) error
	UpdateProcessDetailContent(id, message string, data interface{}) error
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
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
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ AssetContextStore = (*DB)(nil)

// AttackChainStore is the persistence surface required by AttackChainHandler.
type AttackChainStore interface {
	AttackChainLedger
	GetConversation(id string) (*Conversation, error)
	// The rest reaches this surface through attackchain.NewBuilder, which declares its own Store
	// (a fourth case of the generation blind spot: interfaces built from direct h.db.X calls miss
	// anything the handler hands to a helper as an argument). Keep it aligned with attackchain.Store.
	ConversationHasToolProcessDetails(conversationID string) (bool, error)
	GetAgentTrace(conversationID string) (traceInputJSON, assistantOutput string, err error)
	GetMessages(conversationID string) ([]Message, error)
	GetProcessDetailsByConversation(conversationID string) (map[string][]ProcessDetail, error)
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
type ResourceExistence interface {
	ConversationExists(id string) (bool, error)
	GetToolExecution(id string) (*mcp.ToolExecution, error)
}

var _ ResourceExistence = (*DB)(nil)

// BatchTaskStore 已删除：批量任务两张表的 22 个方法全部由 store.BatchTasks 亲自答，
// 消费者（BatchTaskManager）现在直接持有那个 store。

// ChatUploadsStore is the persistence surface required by ChatUploadsHandler.
type ChatUploadsStore interface {
	ConversationArtifactsBaseDir() string
	EinoReductionBaseDir() string
	EinoWorkspaceBaseDir() string
	GetConversationProjectID(conversationID string) (string, error)
	GetConversationTitle(id string) (string, error)
	GetProjectName(id string) (string, error)
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ ChatUploadsStore = (*DB)(nil)

// ConfigStore is the persistence surface required by ConfigHandler.
type ConfigStore interface {
	GetRBACUserByID(id string) (*RBACUser, error)
}

var _ ConfigStore = (*DB)(nil)

// ConversationStore is the persistence surface required by ConversationHandler.
type ConversationStore interface {
	AssignResourceToUser(userID, resourceType, resourceID string) error
	CountConversationsForAccess(search, projectID, userID, scope string) (int, error)
	CreateConversation(title string, meta ConversationCreateMeta) (*Conversation, error)
	DeleteConversation(id string) error
	DeleteConversationTurn(conversationID, anchorMessageID string) (deletedIDs []string, err error)
	GetConversation(id string) (*Conversation, error)
	GetConversationLite(id string) (*Conversation, error)
	GetProcessDetailByID(id string) (*ProcessDetail, error)
	GetProcessDetailOffset(messageID, detailID string) (int, error)
	GetProcessDetails(messageID string) ([]ProcessDetail, error)
	GetProcessDetailsPage(messageID string, limit, offset int) ([]ProcessDetail, int, error)
	GetProcessDetailsSummary(messageID string) (*ProcessDetailsSummary, error)
	ListConversationPlanTasksSince(conversationID string, since time.Time) ([]ConversationPlanTask, error)
	ListConversationsForAccess(limit, offset int, search, sortBy, projectID, userID, scope string) ([]*Conversation, error)
	SetConversationProjectID(conversationID, projectID string) error
	SetResourceOwner(resourceType, resourceID, userID string) error
	UpdateConversationPinned(id string, pinned bool) error
	UpdateConversationTitle(id, title string) error
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ ConversationStore = (*DB)(nil)

// MonitorStore is the persistence surface required by MonitorHandler.
type MonitorStore interface {
	CountToolExecutions(status, toolName string) (int, error)
	CountToolExecutionsForAccess(status, toolName string, access store.Access) (int, error)
	DecreaseToolStats(toolName string, totalCalls, successCalls, failedCalls int) error
	DeleteToolExecution(id string) error
	DeleteToolExecutions(ids []string) error
	GetToolExecution(id string) (*mcp.ToolExecution, error)
	GetToolExecutionsByIds(ids []string) ([]*mcp.ToolExecution, error)
	LoadCallsTimeline(since time.Time, dailyBuckets bool) ([]CallsTimelineBucket, error)
	LoadToolExecutionListPageForAccess(offset, limit int, status, toolName string, access store.Access) ([]*mcp.ToolExecution, error)
	LoadToolExecutionsWithPagination(offset, limit int, status, toolName string) ([]*mcp.ToolExecution, error)
	LoadToolStats() (map[string]*mcp.ToolStats, error)
	LoadToolStatsSummary(topN int) (*ToolStatsSummaryResult, error)
	LoadToolStatsSummaryForAccess(topN int, access store.Access) (*ToolStatsSummaryResult, error)
	UserCanAccessToolExecution(userID, scope, executionID string) bool
	// Reached through handler.toolExecutionVisible, which takes the handler's storage as a
	// one-method interface: an execution with no owner match is visible iff its conversation is.
	// Same generation blind spot as AuditStore's existence lookups - direct h.db.X calls only.
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ MonitorStore = (*DB)(nil)

// NotificationStore is the persistence surface required by NotificationHandler.
type NotificationStore interface {
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
	Begin() (*sql.Tx, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

var _ NotificationStore = (*DB)(nil)

// OpenAPIStore is the persistence surface required by OpenAPIHandler.
type OpenAPIStore interface {
	GetConversation(id string) (*Conversation, error)
	GetMessages(conversationID string) ([]Message, error)
}

var _ OpenAPIStore = (*DB)(nil)

// ProjectStore is the persistence surface required by ProjectHandler.
type ProjectStore interface {
	ProjectRowStore
	AttackChainLedger
	AssignResourceToUser(userID, resourceType, resourceID string) error
	CountConversationsByProjectID(projectID string) (int, error)
	CountProjectsForAccess(status, search, userID, scope string) (int, error)
	CreateProject(p *Project) (*Project, error)
	DeleteProject(id string) error
	GetProject(id string) (*Project, error)
	GetProjectDashboardSummaryForAccess(factLimit int, userID, scope string) (*ProjectDashboardSummary, error)
	ListConversationsByProjectID(projectID string, limit, offset int) ([]*Conversation, error)
	ListProjectsForAccess(status, search string, limit, offset int, userID, scope string) ([]*Project, error)
	SetResourceOwner(resourceType, resourceID, userID string) error
	UpdateProject(p *Project) error
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ ProjectStore = (*DB)(nil)

// RBACStore is the persistence surface required by RBACHandler.
type RBACStore interface {
	AssignResourcesToUser(userID, resourceType string, resourceIDs []string) (int64, error)
	AssignResourcesToUserAuto(userID string, resourceIDs []string) (int64, map[string]string, error)
	CountAssignableRBACResources(resourceType, search string) (int, error)
	CreateRBACUser(username, displayName, passwordHash string, enabled bool, roleIDs []string) (*RBACUser, error)
	DeleteRBACResourceAssignmentWithDetails(id string) (*RBACResourceAssignment, error)
	DeleteRBACRole(id string) error
	DeleteRBACUser(userID string) error
	GetRBACRoleByID(id string) (*RBACRole, error)
	GetRBACUserByID(id string) (*RBACUser, error)
	ListAssignableRBACResourcesPage(resourceType, search string, limit, offset int) ([]RBACResourceOption, error)
	ListRBACResourceAssignments(userID string) ([]RBACResourceAssignment, error)
	ListRBACRolePermissionKeys(roleID string) ([]string, error)
	ListRBACRoles() ([]RBACRole, error)
	ListRBACUserRoleIDs(userID string) ([]string, error)
	ListRBACUsers() ([]RBACUser, error)
	UpdateRBACUser(userID, displayName string, enabled *bool, roleIDs *[]string) error
	UpdateRBACUserPassword(userID, passwordHash string) error
	UpsertRBACRole(id, name, description, scope string, permissionKeys []string) (*RBACRole, error)
}

var _ RBACStore = (*DB)(nil)

// RobotStore is the persistence surface required by RobotHandler. The alert subscription and the
// durable outbox are not in it any more - they are store.VulnerabilityAlerts, and the robot handler
// holds that store directly (its worker only needs the queue, its commands only need the subscription).
type RobotStore interface {
	GetRBACUserByID(id string) (*RBACUser, error)
	CreateConversation(title string, meta ConversationCreateMeta) (*Conversation, error)
	CreateProject(p *Project) (*Project, error)
	DeleteConversation(id string) error
	GetConversation(id string) (*Conversation, error)
	GetConversationProjectID(conversationID string) (string, error)
	GetProject(id string) (*Project, error)
	ListConversationsForAccess(limit, offset int, search, sortBy, projectID, userID, scope string) ([]*Conversation, error)
	ListProjectsForAccess(status, search string, limit, offset int, userID, scope string) ([]*Project, error)
	ResolveRBACAccess(userID string) (*RBACAccess, error)
	SetConversationProjectID(conversationID, projectID string) error
	SetResourceOwner(resourceType, resourceID, userID string) error
	UpdateConversationTitle(id, title string) error
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ RobotStore = (*DB)(nil)

// VulnerabilityStore is what VulnerabilityHandler still needs from the connection wrapper once the
// finding rows themselves moved to store.Vulnerabilities: the three RBAC lookups a finding's owner and
// candidates are decided with. The alert subscription the same page edits is store.VulnerabilityAlerts,
// so it is not here either. The name says which handler this surface serves, not which table it owns.
type VulnerabilityStore interface {
	AssignResourceToUser(userID, resourceType, resourceID string) error
	SetResourceOwner(resourceType, resourceID, userID string) error
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ VulnerabilityStore = (*DB)(nil)

// WebShellStore is the persistence surface required by WebShellHandler.
type WebShellStore interface {
	AssignResourceToUser(userID, resourceType, resourceID string) error
	GetConversationByWebshellConnectionID(connectionID string) (*Conversation, error)
	ListConversationsByWebshellConnectionID(connectionID string) ([]WebShellConversationItem, error)
	SetResourceOwner(resourceType, resourceID, userID string) error
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ WebShellStore = (*DB)(nil)

// WorkflowStore is what the workflow handler still needs from the connection wrapper once the five
// workflow tables moved to store.Workflows: the project fact surface a run writes into, the process
// detail a run appends, and the one RBAC question the page asks. The workflow rows themselves are not
// here - h.runs answers those.
type WorkflowStore interface {
	ProjectRowStore
	AddProcessDetail(conversationID, messageID, id, eventType string, data interface{}) error
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ WorkflowStore = (*DB)(nil)
