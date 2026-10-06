package database

import (
	"context"
	"cyberstrike-ai/internal/mcp"
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
	ProjectFactStore
	ToolExecutionLedger
	WorkflowRunLedger
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
	GetWebshellConnection(id string) (*WebShellConnection, error)
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

// AssetStore is the persistence surface required by AssetHandler.
type AssetStore interface {
	BatchTaskBelongsToQueue(taskID, queueID string) bool
	DeleteAsset(id string, access RBACListAccess) error
	DeleteAssets(ids []string, access RBACListAccess) (int, error)
	GetAsset(id string, access RBACListAccess) (*Asset, error)
	GetAssetStats(access RBACListAccess, requestedDays ...int) (map[string]interface{}, error)
	GetProject(id string) (*Project, error)
	ListAssets(limit, offset int, filter AssetListFilter, access RBACListAccess) ([]*Asset, int, error)
	ListAssetsForOperation(limit int, filter AssetListFilter, access RBACListAccess) ([]*Asset, int, error)
	MarkAssetScanned(id, conversationID, queueID, taskID string, access RBACListAccess) error
	MergeAssets(primary *Asset, duplicateIDs []string, writeAccess, deleteAccess RBACListAccess) (int, error)
	UpdateAsset(id string, a *Asset, access RBACListAccess) error
	UpdateAssetsBulk(ids []string, patch AssetBulkPatch, access RBACListAccess) (int, error)
	UpdateAssetsProject(ids []string, projectID string, access RBACListAccess) (int, error)
	UpsertAssets(assets []*Asset, ownerUserID string, allowGlobal ...bool) (AssetImportResult, error)
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ AssetStore = (*DB)(nil)

// AttackChainStore is the persistence surface required by AttackChainHandler.
type AttackChainStore interface {
	AttackChainLedger
	DeleteAttackChain(conversationID string) error
	GetConversation(id string) (*Conversation, error)
	// The rest reaches this surface through attackchain.NewBuilder, which declares its own Store
	// (a fourth case of the generation blind spot: interfaces built from direct h.db.X calls miss
	// anything the handler hands to a helper as an argument). Keep it aligned with attackchain.Store.
	ConversationHasToolProcessDetails(conversationID string) (bool, error)
	GetAgentTrace(conversationID string) (traceInputJSON, assistantOutput string, err error)
	GetMessages(conversationID string) ([]Message, error)
	GetProcessDetailsByConversation(conversationID string) (map[string][]ProcessDetail, error)
	LoadAttackChainEdges(conversationID string) ([]AttackChainEdge, error)
	LoadAttackChainNodes(conversationID string) ([]AttackChainNode, error)
	SaveAttackChainEdge(conversationID, edgeID, sourceNodeID, targetNodeID, edgeType string, weight int) error
	SaveAttackChainNode(conversationID, nodeID, nodeType, nodeName, toolExecutionID, metadata string, riskScore int) error
}

var _ AttackChainStore = (*DB)(nil)

// ResourceExistence is the read surface audit.ResourceExistenceSource needs: given the resource an
// audit row points at, does that row still exist? Eight lookups over seven other tables, declared
// here because internal/audit is the consumer and handing it the connection wrapper would put every
// table back within reach.
//
// audit_logs' own queries are deliberately absent: they live in store.AuditLogs now, and an
// interface that mixes a table's reads with other tables' existence checks is how a surface stops
// describing one thing.
type ResourceExistence interface {
	ConversationExists(id string) (bool, error)
	GetBatchQueue(queueID string) (*BatchTaskQueueRow, error)
	GetC2Listener(id string) (*C2Listener, error)
	GetC2Session(id string) (*C2Session, error)
	GetC2Task(id string) (*C2Task, error)
	GetToolExecution(id string) (*mcp.ToolExecution, error)
	GetVulnerability(id string) (*Vulnerability, error)
	GetWebshellConnection(id string) (*WebShellConnection, error)
}

var _ ResourceExistence = (*DB)(nil)

// BatchTaskStore is the persistence surface required by BatchTaskManager.
type BatchTaskStore interface {
	AddBatchTask(queueID, taskID, message string) error
	CancelPendingBatchTasks(queueID string, completedAt time.Time) error
	CountBatchQueuesForAccess(status, keyword, userID, scope string) (int, error)
	CreateBatchQueue(queueID string, title string, role string, agentMode string, scheduleMode string, cronExpr string, nextRunAt *time.Time, projectID string, concurrency int, tasks []map[string]interface{}, hitlPolicies ...string) error
	DeleteBatchQueue(queueID string) error
	DeleteBatchTask(queueID, taskID string) error
	GetAllBatchQueues() ([]*BatchTaskQueueRow, error)
	GetBatchQueue(queueID string) (*BatchTaskQueueRow, error)
	GetBatchTasks(queueID string) ([]*BatchTaskRow, error)
	ListBatchQueuesForAccess(limit, offset int, status, keyword, userID, scope string) ([]*BatchTaskQueueRow, error)
	PrepareBatchSingleTaskRun(queueID, taskID string, taskIndex int, resetTask, resumeQueue bool) error
	RecordBatchQueueScheduledTriggerStart(queueID string, at time.Time) error
	ResetBatchQueueForRerun(queueID string) error
	SetBatchQueueLastRunError(queueID, msg string) error
	SetBatchQueueLastScheduleError(queueID, msg string) error
	UpdateBatchQueueCurrentIndex(queueID string, currentIndex int) error
	UpdateBatchQueueMetadata(queueID, title, role, agentMode string, concurrency int, hitlPolicies ...string) error
	UpdateBatchQueueSchedule(queueID, scheduleMode, cronExpr string, nextRunAt *time.Time) error
	UpdateBatchQueueScheduleEnabled(queueID string, enabled bool) error
	UpdateBatchQueueStatus(queueID, status string) error
	UpdateBatchTaskMessage(queueID, taskID, message string) error
	UpdateBatchTaskStatus(queueID, taskID, status string, conversationID, result, errorMsg string) error
}

var _ BatchTaskStore = (*DB)(nil)

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
	// FindNearestToolExecutionArguments reaches this surface through the history renderer, not
	// through a direct h.db call - an interface generated by counting only direct field calls
	// would silently omit it, and the omission shows up as "cannot use h.db" the moment the
	// handler's field becomes this interface.
	FindNearestToolExecutionArguments(conversationID, toolName string, at time.Time, window time.Duration) (string, map[string]interface{}, error)
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
	CountToolExecutionsForAccess(status, toolName string, access RBACListAccess) (int, error)
	DecreaseToolStats(toolName string, totalCalls, successCalls, failedCalls int) error
	DeleteToolExecution(id string) error
	DeleteToolExecutions(ids []string) error
	GetToolExecution(id string) (*mcp.ToolExecution, error)
	GetToolExecutionsByIds(ids []string) ([]*mcp.ToolExecution, error)
	LoadCallsTimeline(since time.Time, dailyBuckets bool) ([]CallsTimelineBucket, error)
	LoadToolExecutionListPageForAccess(offset, limit int, status, toolName string, access RBACListAccess) ([]*mcp.ToolExecution, error)
	LoadToolExecutionsWithPagination(offset, limit int, status, toolName string) ([]*mcp.ToolExecution, error)
	LoadToolStats() (map[string]*mcp.ToolStats, error)
	LoadToolStatsSummary(topN int) (*ToolStatsSummaryResult, error)
	LoadToolStatsSummaryForAccess(topN int, access RBACListAccess) (*ToolStatsSummaryResult, error)
	UserCanAccessToolExecution(userID, scope, executionID string) bool
	// Reached through handler.toolExecutionVisible, which takes the handler's storage as a
	// one-method interface: an execution with no owner match is visible iff its conversation is.
	// Same generation blind spot as AuditStore's existence lookups - direct h.db.X calls only.
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ MonitorStore = (*DB)(nil)

// NotificationStore is the persistence surface required by NotificationHandler.
type NotificationStore interface {
	ListC2EventsForAccess(filter ListC2EventsFilter, access RBACListAccess) ([]*C2Event, error)
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
	Begin() (*sql.Tx, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

var _ NotificationStore = (*DB)(nil)

// OpenAPIStore is the persistence surface required by OpenAPIHandler.
type OpenAPIStore interface {
	GetConversation(id string) (*Conversation, error)
	GetMessages(conversationID string) ([]Message, error)
	ListVulnerabilities(limit, offset int, filter VulnerabilityListFilter) ([]*Vulnerability, error)
}

var _ OpenAPIStore = (*DB)(nil)

// ProjectStore is the persistence surface required by ProjectHandler.
type ProjectStore interface {
	ProjectFactStore
	AttackChainLedger
	AddProjectFactEdge(projectID string, in ProjectFactEdgeInput, sourceFactKey, sourceConversationID string) (*ProjectFactEdge, error)
	AssignResourceToUser(userID, resourceType, resourceID string) error
	CountConversationsByProjectID(projectID string) (int, error)
	CountProjectsForAccess(status, search, userID, scope string) (int, error)
	CreateProject(p *Project) (*Project, error)
	DeleteProject(id string) error
	DeleteProjectFact(id string) error
	DeleteProjectFactEdge(edgeID string) error
	DeprecateProjectFact(projectID, factKey string) error
	GetProject(id string) (*Project, error)
	GetProjectDashboardSummaryForAccess(factLimit int, userID, scope string) (*ProjectDashboardSummary, error)
	GetProjectFact(id string) (*ProjectFact, error)
	GetProjectFactByKey(projectID, factKey string) (*ProjectFact, error)
	GetProjectFactEdge(edgeID string) (*ProjectFactEdge, error)
	ListConversationsByProjectID(projectID string, limit, offset int) ([]*Conversation, error)
	ListIncomingProjectFactEdges(projectID, targetFactKey string) ([]*ProjectFactEdge, error)
	ListOutgoingProjectFactEdges(projectID, sourceFactKey string) ([]*ProjectFactEdge, error)
	ListProjectFactEdgesByProject(projectID string) ([]*ProjectFactEdge, error)
	ListProjectFacts(projectID string, filter ProjectFactListFilter, limit, offset int) ([]*ProjectFact, error)
	ListProjectsForAccess(status, search string, limit, offset int, userID, scope string) ([]*Project, error)
	RenameProjectFactKeyEdges(projectID, oldKey, newKey string) error
	RestoreProjectFact(projectID, factKey, confidence string) error
	SetResourceOwner(resourceType, resourceID, userID string) error
	UpdateProject(p *Project) error
	UpsertProjectFact(f *ProjectFact) (*ProjectFact, error)
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

// RobotStore is the persistence surface required by RobotHandler.
type RobotStore interface {
	GetRBACUserByID(id string) (*RBACUser, error)
	CreateConversation(title string, meta ConversationCreateMeta) (*Conversation, error)
	CreateProject(p *Project) (*Project, error)
	DeleteConversation(id string) error
	EnqueueVulnerabilityAlertDeliveries(vulnerabilityID string, recipients []VulnerabilityAlertRecipient) error
	GetConversation(id string) (*Conversation, error)
	GetConversationProjectID(conversationID string) (string, error)
	GetProject(id string) (*Project, error)
	GetVulnerabilityAlertSubscription(userID string) (*VulnerabilityAlertSubscription, error)
	ListConversationsForAccess(limit, offset int, search, sortBy, projectID, userID, scope string) ([]*Conversation, error)
	ListDueVulnerabilityAlertDeliveries(limit int) ([]VulnerabilityAlertDelivery, error)
	ListProjectsForAccess(status, search string, limit, offset int, userID, scope string) ([]*Project, error)
	ListVulnerabilityAlertRecipients(vuln *Vulnerability) ([]VulnerabilityAlertRecipient, error)
	MarkVulnerabilityAlertDeliveryFailed(id int64, attempts int, sendErr error) error
	MarkVulnerabilityAlertDeliverySent(id int64) error
	ResolveRBACAccess(userID string) (*RBACAccess, error)
	SetConversationProjectID(conversationID, projectID string) error
	SetResourceOwner(resourceType, resourceID, userID string) error
	UpdateConversationTitle(id, title string) error
	UpsertVulnerabilityAlertSubscription(userID string, enabled bool, minSeverity string) (*VulnerabilityAlertSubscription, error)
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ RobotStore = (*DB)(nil)

// VulnerabilityStore is the persistence surface required by VulnerabilityHandler.
type VulnerabilityStore interface {
	AssignResourceToUser(userID, resourceType, resourceID string) error
	CountVulnerabilitiesForAccess(filter VulnerabilityListFilter, access RBACListAccess) (int, error)
	CreateVulnerability(vuln *Vulnerability) (*Vulnerability, error)
	DeleteVulnerabilitiesByFilterForAccess(filter VulnerabilityListFilter, access RBACListAccess) (int64, error)
	DeleteVulnerability(id string) error
	GetVulnerability(id string) (*Vulnerability, error)
	GetVulnerabilityAlertSubscription(userID string) (*VulnerabilityAlertSubscription, error)
	GetVulnerabilityFilterOptionsForAccess(access RBACListAccess) (map[string][]string, error)
	GetVulnerabilityStatsForAccess(filter VulnerabilityListFilter, access RBACListAccess) (map[string]interface{}, error)
	ListVulnerabilitiesForAccess(limit, offset int, filter VulnerabilityListFilter, access RBACListAccess) ([]*Vulnerability, error)
	SetResourceOwner(resourceType, resourceID, userID string) error
	UpdateVulnerability(id string, vuln *Vulnerability) error
	UpsertVulnerabilityAlertSubscription(userID string, enabled bool, minSeverity string) (*VulnerabilityAlertSubscription, error)
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ VulnerabilityStore = (*DB)(nil)

// WebShellStore is the persistence surface required by WebShellHandler.
type WebShellStore interface {
	AssignResourceToUser(userID, resourceType, resourceID string) error
	CreateWebshellConnection(c *WebShellConnection) error
	DeleteWebshellConnection(id string) error
	GetConversationByWebshellConnectionID(connectionID string) (*Conversation, error)
	GetWebshellConnection(id string) (*WebShellConnection, error)
	GetWebshellConnectionState(connectionID string) (string, error)
	ListConversationsByWebshellConnectionID(connectionID string) ([]WebShellConversationItem, error)
	ListWebshellConnectionsForAccess(userID, scope, projectID string) ([]WebShellConnection, error)
	SetResourceOwner(resourceType, resourceID, userID string) error
	UpdateWebshellConnection(c *WebShellConnection) error
	UpsertWebshellConnectionState(connectionID, stateJSON string) error
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ WebShellStore = (*DB)(nil)

// WorkflowStore is the persistence surface required by WorkflowHandler.
type WorkflowStore interface {
	ProjectFactStore
	WorkflowRunLedger
	ApplyWorkflowPackageImport(ctx context.Context, req WorkflowPackageApplyRequest) (*WorkflowPackageImport, bool, error)
	CreateWorkflowPackageInspection(v *WorkflowPackageInspection) error
	DeleteWorkflowDefinition(id string) error
	GetWorkflowDefinition(id string) (*WorkflowDefinition, error)
	GetWorkflowPackageImport(id, actor string) (*WorkflowPackageImport, error)
	GetWorkflowPackageInspection(id, actor string) (*WorkflowPackageInspection, error)
	GetWorkflowRun(runID string) (*WorkflowRun, error)
	ListWorkflowDefinitions(includeDisabled bool) ([]*WorkflowDefinition, error)
	ListWorkflowNodeRuns(runID string) ([]*WorkflowNodeRun, error)
	ListWorkflowRunsAwaitingHITLFiltered(conversationID string, limit int) ([]*WorkflowRun, error)
	RecordWorkflowRunHITLDecision(runID string, approved bool, comment string) error
	UpsertWorkflowDefinition(wf *WorkflowDefinition) error
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

var _ WorkflowStore = (*DB)(nil)
