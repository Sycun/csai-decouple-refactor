package handler

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// NotificationHandler 聚合通知（Phase 2：服务端统一计算）
type NotificationHandler struct {
	db           database.NotificationStore
	agentHandler *AgentHandler
	logger       *zap.Logger
	// reads owns notification_reads_by_user. The handler builds no SQL for it.
	reads notificationReadStore
	// hitl is the approval queue this surface reports on; the interrupt table
	// belongs to that domain, so this handler only ever asks it for a summary.
	hitl pendingApprovalSource
	// findings and failedRuns are other domains' tables this digest reads. The queries
	// live with those domains so this handler assembles no SQL at all.
	findings   recentFindingSource
	failedRuns failedExecutionSource
}

// pendingApprovalSource is the notification handler's read surface for HITL.
type pendingApprovalSource interface {
	PendingApprovals(limit int, access store.Access) ([]store.PendingApproval, error)
}

// recentFindingSource is the read surface over the vulnerability domain.
type recentFindingSource interface {
	RecentFindings(sinceSec int64, limit int, access store.Access) ([]store.RecentFinding, error)
}

// failedExecutionSource is the read surface over the tool-execution domain.
type failedExecutionSource interface {
	FailedSince(sinceSec int64, limit int) ([]store.FailedExecution, error)
}

// notificationReadStore is the persistence surface this handler needs. Defining it
// here (consumer side) is what keeps the handler from depending on the 361-method
// *database.DB again: *store.NotificationReads satisfies it, and a test double can too.
type notificationReadStore interface {
	EnsureSchema() error
	ReadStates(userID string, eventIDs []string) (map[string]bool, error)
	MarkRead(userID string, eventIDs []string) (int, error)
	Prune(userID string, maxRows int) error
}

// NotificationSummaryItem 通知项
type NotificationSummaryItem struct {
	ID         string `json:"id"`
	Level      string `json:"level"` // p0/p1/p2
	Type       string `json:"type"`
	Title      string `json:"title"`
	Desc       string `json:"desc"`
	Ts         string `json:"ts"` // RFC3339
	Count      int    `json:"count,omitempty"`
	Actionable bool   `json:"actionable"`
	Read       bool   `json:"read"`
	// 以下字段用于前端深链跳转（通知即入口）
	ConversationID  string `json:"conversationId,omitempty"`
	VulnerabilityID string `json:"vulnerabilityId,omitempty"`
	ExecutionID     string `json:"executionId,omitempty"`
	InterruptID     string `json:"interruptId,omitempty"`
	SessionID       string `json:"sessionId,omitempty"` // C2 会话（如新会话上线）
}

// NotificationSummaryResponse 聚合响应
type NotificationSummaryResponse struct {
	SinceMs     int64                     `json:"sinceMs"`
	GeneratedAt string                    `json:"generatedAt"`
	P0Count     int                       `json:"p0Count"`
	UnreadCount int                       `json:"unreadCount"`
	Counts      map[string]int            `json:"counts"`
	Items       []NotificationSummaryItem `json:"items"`
}

func NewNotificationHandler(db *database.DB, agentHandler *AgentHandler, logger *zap.Logger) *NotificationHandler {
	handler := &NotificationHandler{
		db:           database.Narrow[database.NotificationStore](db),
		agentHandler: agentHandler,
		logger:       logger,
	}
	if db != nil {
		handler.reads = store.NewNotificationReads(db.DB)
		handler.hitl = store.NewHITL(db.DB)
		handler.findings = store.NewVulnerabilities(db.DB)
		handler.failedRuns = store.NewExecution(db.DB)
	}
	return handler
}

func parseSinceMs(raw string) int64 {
	v := strings.TrimSpace(raw)
	if v == "" {
		return 0
	}
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms > 0 {
		return ms
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UnixMilli()
	}
	return 0
}

func unixSecToRFC3339(sec int64) string {
	if sec <= 0 {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

func normalizedSinceSec(sinceMs int64) int64 {
	sec := sinceMs / 1000
	// SQLite 默认时间精度到秒；给 1s 回看窗口，避免“同秒内新增”被漏算。
	if sec > 0 {
		return sec - 1
	}
	return 0
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func normalizeSinceMs(raw int64) int64 {
	if raw > 0 {
		return raw
	}
	// 默认仅看最近 24 小时，避免首次打开拉全量历史噪音。
	return time.Now().Add(-24 * time.Hour).UnixMilli()
}

func levelBySeverity(sev string) string {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical", "high":
		return "p0"
	case "medium":
		return "p1"
	default:
		return "p2"
	}
}

func requestWantsEnglish(c *gin.Context) bool {
	if c == nil {
		return false
	}
	lang := strings.ToLower(strings.TrimSpace(c.Query("lang")))
	if lang == "" {
		lang = strings.ToLower(strings.TrimSpace(c.GetHeader("Accept-Language")))
	}
	return strings.HasPrefix(lang, "en")
}

func i18nText(english bool, zh string, en string) string {
	if english {
		return en
	}
	return zh
}

func notificationAccessFromContext(c *gin.Context) store.Access {
	session, ok := security.CurrentSession(c)
	if !ok {
		return store.Access{}
	}
	return store.Access{UserID: session.UserID, Scope: session.Scope}
}

// storeAccess re-expresses the legacy read scope for the domain stores, which do not
// import internal/database: the scope strings are the same values, declared in each place.
func storeAccess(access store.Access) store.Access {
	return store.Access{UserID: access.UserID, Scope: access.Scope}
}

func (h *NotificationHandler) loadPendingHITLItems(limit int, english bool, access store.Access) ([]NotificationSummaryItem, error) {
	if h.hitl == nil {
		return []NotificationSummaryItem{}, nil
	}
	pending, err := h.hitl.PendingApprovals(limit, storeAccess(access))
	if err != nil {
		return nil, err
	}
	items := make([]NotificationSummaryItem, 0, len(pending))
	for _, p := range pending {
		conversationID := p.ConversationID
		desc := i18nText(english, "会话 "+conversationID+" 的审批中断待处理", "Conversation "+conversationID+" has pending HITL approval")
		if strings.TrimSpace(p.ToolName) != "" {
			desc = i18nText(english, "工具 "+p.ToolName+" 等待审批", "Tool "+p.ToolName+" is waiting for approval")
		}
		items = append(items, NotificationSummaryItem{
			ID:             "hitl:" + p.ID,
			Level:          "p0",
			Type:           "hitl_pending",
			Title:          i18nText(english, "HITL 待审批", "HITL Pending Approval"),
			Desc:           desc,
			Ts:             unixSecToRFC3339(p.CreatedAtSec),
			Count:          1,
			Actionable:     true,
			Read:           false,
			ConversationID: conversationID,
			InterruptID:    p.ID,
		})
	}
	return items, nil
}

func (h *NotificationHandler) loadVulnerabilityItems(sinceMs int64, limit int, english bool, access store.Access) ([]NotificationSummaryItem, map[string]int, error) {
	sinceSec := normalizedSinceSec(sinceMs)
	items := make([]NotificationSummaryItem, 0, limit)
	counts := map[string]int{
		"newCriticalVulns": 0,
		"newHighVulns":     0,
		"newMediumVulns":   0,
		"newLowVulns":      0,
		"newInfoVulns":     0,
	}
	if h.findings == nil {
		return items, counts, nil
	}
	// A finding with no conversation used to disappear from this digest entirely: the
	// nullable column scanned into a string, and the loop answered the Scan error with
	// continue. The store now coalesces it, so project-level findings count too.
	findings, err := h.findings.RecentFindings(sinceSec, limit, storeAccess(access))
	if err != nil {
		return nil, nil, err
	}
	for _, f := range findings {
		id, title, severity, conversationID, createdSec := f.ID, f.Title, f.Severity, f.ConversationID, f.CreatedAtSec
		switch strings.ToLower(strings.TrimSpace(severity)) {
		case "critical":
			counts["newCriticalVulns"]++
		case "high":
			counts["newHighVulns"]++
		case "medium":
			counts["newMediumVulns"]++
		case "low":
			counts["newLowVulns"]++
		default:
			counts["newInfoVulns"]++
		}
		sevUpper := strings.ToUpper(strings.TrimSpace(severity))
		if sevUpper == "" {
			sevUpper = "INFO"
		}
		finalTitle := i18nText(english, "新漏洞（"+sevUpper+"）", "New Vulnerability ("+sevUpper+")")
		finalDesc := strings.TrimSpace(title)
		if finalDesc == "" {
			finalDesc = i18nText(english, "（无标题）", "(Untitled)")
		}
		items = append(items, NotificationSummaryItem{
			ID:              "vuln:" + id,
			Level:           levelBySeverity(severity),
			Type:            "vulnerability_created",
			Title:           finalTitle,
			Desc:            finalDesc,
			Ts:              unixSecToRFC3339(createdSec),
			Count:           1,
			Actionable:      false,
			Read:            false,
			ConversationID:  conversationID,
			VulnerabilityID: id,
		})
	}
	return items, counts, nil
}

// loadC2SessionOnlineEvents 新会话上线（c2_events：session + critical，与 Manager.IngestCheckIn 一致）
func (h *NotificationHandler) loadC2SessionOnlineEvents(sinceMs int64, limit int, english bool, access store.Access) ([]NotificationSummaryItem, int, error) {
	sinceSec := normalizedSinceSec(sinceMs)
	events, err := h.db.ListC2EventsForAccess(database.ListC2EventsFilter{
		Category: "session",
		Level:    "critical",
		Since:    ptrTime(time.Unix(sinceSec, 0)),
		Limit:    limit,
	}, access)
	if err != nil {
		return nil, 0, err
	}
	items := make([]NotificationSummaryItem, 0, limit)
	for _, e := range events {
		if e == nil {
			continue
		}
		desc := strings.TrimSpace(e.Message)
		if len(desc) > 220 {
			desc = desc[:200] + "…"
		}
		if desc == "" {
			desc = i18nText(english, "新会话已建立", "A new session was created")
		}
		items = append(items, NotificationSummaryItem{
			ID:         "c2evt:" + e.ID,
			Level:      "p0",
			Type:       "c2_session_online",
			Title:      i18nText(english, "C2 新会话上线", "C2 new session online"),
			Desc:       desc,
			Ts:         e.CreatedAt.UTC().Format(time.RFC3339),
			Count:      1,
			Actionable: false,
			Read:       false,
			SessionID:  e.SessionID,
		})
	}
	return items, len(items), nil
}

func (h *NotificationHandler) loadFailedExecutionItems(sinceMs int64, limit int, english bool) ([]NotificationSummaryItem, int, error) {
	sinceSec := normalizedSinceSec(sinceMs)
	items := make([]NotificationSummaryItem, 0, limit)
	count := 0
	if h.failedRuns == nil {
		return items, count, nil
	}
	failed, err := h.failedRuns.FailedSince(sinceSec, limit)
	if err != nil {
		return nil, 0, err
	}
	for _, f := range failed {
		id, toolName, startSec := f.ID, f.ToolName, f.StartSec
		count++
		if strings.TrimSpace(toolName) == "" {
			toolName = i18nText(english, "未知工具", "unknown")
		}
		items = append(items, NotificationSummaryItem{
			ID:          "exec_failed:" + id,
			Level:       "p0",
			Type:        "task_failed",
			Title:       i18nText(english, "任务执行失败", "Task Execution Failed"),
			Desc:        i18nText(english, "工具 "+toolName+" 执行失败", "Tool "+toolName+" execution failed"),
			Ts:          unixSecToRFC3339(startSec),
			Count:       1,
			Actionable:  false,
			Read:        false,
			ExecutionID: id,
		})
	}
	return items, count, nil
}

func (h *NotificationHandler) summarizeLongRunningTasks(threshold time.Duration, english bool, access store.Access) ([]NotificationSummaryItem, int) {
	if h.agentHandler == nil || h.agentHandler.tasks == nil {
		return nil, 0
	}
	tasks := h.agentHandler.tasks.GetActiveTasks()
	now := time.Now()
	items := make([]NotificationSummaryItem, 0, len(tasks))
	for _, t := range tasks {
		if t == nil {
			continue
		}
		if !h.notificationConversationAllowed(access, t.ConversationID) {
			continue
		}
		if now.Sub(t.StartedAt) >= threshold {
			items = append(items, NotificationSummaryItem{
				ID:             "task_long:" + t.ConversationID,
				Level:          "p1",
				Type:           "long_running_tasks",
				Title:          i18nText(english, "长时间运行任务", "Long Running Task"),
				Desc:           i18nText(english, "会话 "+t.ConversationID+" 运行超过 15 分钟", "Conversation "+t.ConversationID+" has been running over 15 minutes"),
				Ts:             t.StartedAt.UTC().Format(time.RFC3339),
				Count:          1,
				Actionable:     true,
				Read:           false,
				ConversationID: t.ConversationID,
			})
		}
	}
	return items, len(items)
}

func (h *NotificationHandler) summarizeCompletedTasksSince(sinceMs int64, limit int, english bool, access store.Access) ([]NotificationSummaryItem, int) {
	if h.agentHandler == nil || h.agentHandler.tasks == nil {
		return nil, 0
	}
	since := time.UnixMilli(sinceMs)
	completed := h.agentHandler.tasks.GetCompletedTasks()
	items := make([]NotificationSummaryItem, 0, limit)
	for _, t := range completed {
		if t == nil {
			continue
		}
		if !h.notificationConversationAllowed(access, t.ConversationID) {
			continue
		}
		if t.CompletedAt.After(since) {
			items = append(items, NotificationSummaryItem{
				ID:             "task_completed:" + t.ConversationID + ":" + strconv.FormatInt(t.CompletedAt.Unix(), 10),
				Level:          "p2",
				Type:           "task_completed",
				Title:          i18nText(english, "任务完成", "Task Completed"),
				Desc:           i18nText(english, "会话 "+t.ConversationID+" 已完成", "Conversation "+t.ConversationID+" completed"),
				Ts:             t.CompletedAt.UTC().Format(time.RFC3339),
				Count:          1,
				Actionable:     false,
				Read:           false,
				ConversationID: t.ConversationID,
			})
			if len(items) >= limit {
				break
			}
		}
	}
	return items, len(items)
}

func (h *NotificationHandler) readStatesByIDs(userID string, ids []string) (map[string]bool, error) {
	if h.reads == nil {
		return map[string]bool{}, nil
	}
	return h.reads.ReadStates(userID, ids)
}

func (h *NotificationHandler) applyReadStates(userID string, items []NotificationSummaryItem) ([]NotificationSummaryItem, error) {
	markableIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item.Actionable {
			continue
		}
		markableIDs = append(markableIDs, item.ID)
	}
	readMap, err := h.readStatesByIDs(userID, markableIDs)
	if err != nil {
		return items, err
	}
	for i := range items {
		if items[i].Actionable {
			items[i].Read = false
			continue
		}
		items[i].Read = readMap[items[i].ID]
	}
	return items, nil
}

func filterVisibleItems(items []NotificationSummaryItem) []NotificationSummaryItem {
	out := make([]NotificationSummaryItem, 0, len(items))
	for _, item := range items {
		if item.Actionable || !item.Read {
			out = append(out, item)
		}
	}
	return out
}

func countP0(items []NotificationSummaryItem) int {
	total := 0
	for _, item := range items {
		if item.Level == "p0" {
			if item.Count > 0 {
				total += item.Count
			} else {
				total++
			}
		}
	}
	return total
}

func countUnread(items []NotificationSummaryItem) int {
	total := 0
	for _, item := range items {
		if item.Actionable || !item.Read {
			if item.Count > 0 {
				total += item.Count
			} else {
				total++
			}
		}
	}
	return total
}

type markReadRequest struct {
	EventIDs []string `json:"eventIds"`
}

// MarkRead 按事件 ID 标记已读
func (h *NotificationHandler) MarkRead(c *gin.Context) {
	if h.reads == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to prepare notification read table"})
		return
	}
	if err := h.reads.EnsureSchema(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to prepare notification read table"})
		return
	}
	var req markReadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if len(req.EventIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": true, "marked": 0})
		return
	}
	session, ok := security.CurrentSession(c)
	if !ok || strings.TrimSpace(session.UserID) == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authenticated user"})
		return
	}
	marked, err := h.reads.MarkRead(session.UserID, req.EventIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark read"})
		return
	}
	// Retention is best-effort: a failed prune must not turn a successful mark into
	// a 500, which is why it is reported apart from MarkRead's own error.
	if marked > 0 {
		if err := h.reads.Prune(session.UserID, store.MaxNotificationReadsPerUser); err != nil {
			h.logger.Warn("裁剪通知已读记录失败", zap.Error(err))
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "marked": marked})
}

// GetSummary 返回通知聚合视图（用于头部铃铛）
func (h *NotificationHandler) GetSummary(c *gin.Context) {
	if h.db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database unavailable"})
		return
	}

	if err := h.reads.EnsureSchema(); err != nil {
		h.logger.Warn("初始化通知已读表失败", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initialize notification read table"})
		return
	}

	english := requestWantsEnglish(c)
	sinceMs := normalizeSinceMs(parseSinceMs(c.Query("since")))
	limit, _ := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("limit", "50")))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	access := notificationAccessFromContext(c)

	hitlItems := []NotificationSummaryItem{}
	if security.SessionHasPermission(c, "hitl:read") {
		var err error
		hitlItems, err = h.loadPendingHITLItems(limit, english, access)
		if err != nil {
			h.logger.Warn("加载 HITL 通知失败", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to summarize hitl notifications"})
			return
		}
	}

	vulnItems := []NotificationSummaryItem{}
	vulnCounts := map[string]int{
		"newCriticalVulns": 0,
		"newHighVulns":     0,
		"newMediumVulns":   0,
		"newLowVulns":      0,
		"newInfoVulns":     0,
	}
	if security.SessionHasPermission(c, "vulnerability:read") {
		var err error
		vulnItems, vulnCounts, err = h.loadVulnerabilityItems(sinceMs, limit, english, access)
		if err != nil {
			h.logger.Warn("加载漏洞通知失败", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to summarize vulnerabilities"})
			return
		}
	}

	c2OnlineItems := []NotificationSummaryItem{}
	c2OnlineCount := 0
	if security.SessionHasPermission(c, "c2:read") {
		var err error
		c2OnlineItems, c2OnlineCount, err = h.loadC2SessionOnlineEvents(sinceMs, limit, english, access)
		if err != nil {
			h.logger.Warn("加载 C2 会话上线通知失败", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to summarize c2 session events"})
			return
		}
	}

	longRunningItems := []NotificationSummaryItem{}
	completedItems := []NotificationSummaryItem{}
	longRunningCount := 0
	completedCount := 0
	if security.SessionHasPermission(c, "tasks:read") || security.SessionHasPermission(c, "chat:read") {
		longRunningItems, longRunningCount = h.summarizeLongRunningTasks(15*time.Minute, english, access)
		completedItems, completedCount = h.summarizeCompletedTasksSince(sinceMs, limit, english, access)
	}

	items := make([]NotificationSummaryItem, 0, len(hitlItems)+len(vulnItems)+len(c2OnlineItems)+len(longRunningItems)+len(completedItems))
	items = append(items, hitlItems...)
	items = append(items, vulnItems...)
	items = append(items, c2OnlineItems...)
	items = append(items, longRunningItems...)
	items = append(items, completedItems...)

	session, _ := security.CurrentSession(c)
	items, err := h.applyReadStates(session.UserID, items)
	if err != nil {
		h.logger.Warn("加载通知已读状态失败", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load notification read states"})
		return
	}
	items = filterVisibleItems(items)

	sort.Slice(items, func(i, j int) bool {
		ti, errI := time.Parse(time.RFC3339, items[i].Ts)
		tj, errJ := time.Parse(time.RFC3339, items[j].Ts)
		if errI != nil || errJ != nil {
			return i < j
		}
		return ti.After(tj)
	})

	p0Count := countP0(items)
	unreadCount := countUnread(items)
	c.JSON(http.StatusOK, NotificationSummaryResponse{
		SinceMs:     sinceMs,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		P0Count:     p0Count,
		UnreadCount: unreadCount,
		Counts: map[string]int{
			"hitlPending":      len(hitlItems),
			"newCriticalVulns": vulnCounts["newCriticalVulns"],
			"newHighVulns":     vulnCounts["newHighVulns"],
			"newMediumVulns":   vulnCounts["newMediumVulns"],
			"newLowVulns":      vulnCounts["newLowVulns"],
			"newInfoVulns":     vulnCounts["newInfoVulns"],
			"failedExecutions": 0,
			"longRunningTasks": longRunningCount,
			"completedTasks":   completedCount,
			"c2SessionOnline":  c2OnlineCount,
		},
		Items: items,
	})
}

func (h *NotificationHandler) notificationConversationAllowed(access store.Access, conversationID string) bool {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return access.Scope == database.RBACScopeAll
	}
	return h.db.UserCanAccessResource(access.UserID, access.Scope, "conversation", conversationID)
}
