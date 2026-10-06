package handler

import (
	"net/http"
	"time"

	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"

	"cyberstrike-ai/internal/store"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// AuditHandler serves platform audit log APIs.
type AuditHandler struct {
	logs       *store.AuditLogs           // audit_logs: 这个页面唯一的读来源
	db         database.ResourceExistence // 只剩"被审计的资源还在不在"里还没轮到搬迁的两条（会话与工具执行）
	findings   audit.FindingLookup        // 漏洞那条存在性检查在 store.Vulnerabilities 里，不在这几条里
	webshells  audit.WebshellLookup       // WebShell 那条存在性检查在 store.Webshell 里，同理
	batches    *store.BatchTasks          // 批量队列那条同理：读的是 store.BatchTasks，不是连接包装
	c2         *store.C2                  // C2 的 listener/session/task 三条同理：读的是 store.C2
	executions *store.Monitor             // 工具执行那条同理：读的是 store.Monitor
	audit      *audit.Service
	logger     *zap.Logger
}

// NewAuditHandler creates an audit log handler.
func NewAuditHandler(db *database.DB, auditSvc *audit.Service, logger *zap.Logger) *AuditHandler {
	return &AuditHandler{
		// Narrow, not a plain assignment: a nil *database.DB has to stay a nil interface, or every
		// guard below takes the wrong branch.
		logs:       newAuditLogsStore(db),
		db:         database.Narrow[database.ResourceExistence](db),
		findings:   newFindingLookup(db),
		webshells:  database.NewWebshell(db),
		batches:    database.NewBatchTasks(db),
		c2:         database.NewC2(db),
		executions: database.NewMonitor(db),
		audit:      auditSvc,
		logger:     logger,
	}
}

// Meta GET /api/audit/meta
func (h *AuditHandler) Meta(c *gin.Context) {
	enabled := false
	retentionDays := 0
	if h.audit != nil {
		enabled = h.audit.Enabled()
		retentionDays = h.audit.RetentionDays()
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":           enabled,
		"retention_days":    retentionDays,
		"default_page_size": 20,
		"max_page_size":     100,
		"max_export":        5000,
	})
}

// Summary GET /api/audit/summary
func (h *AuditHandler) Summary(c *gin.Context) {
	if h.logs == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database unavailable"})
		return
	}
	base := auditFilterForAccess(c, auditFilterFromQuery(c))
	total, err := h.logs.Count(base)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	failFilter := base
	failFilter.Result = "failure"
	failures, err := h.logs.Count(failFilter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	since := time.Now().AddDate(0, 0, -7)
	recentFilter := base
	recentFilter.Since = &since
	recent7d, err := h.logs.Count(recentFilter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total":     total,
		"failures":  failures,
		"recent_7d": recent7d,
		"has_filters": c.Query("category") != "" || c.Query("action") != "" || c.Query("result") != "" ||
			c.Query("q") != "" || c.Query("since") != "" || c.Query("until") != "",
	})
}

// ListLogs GET /api/audit/logs
func (h *AuditHandler) ListLogs(c *gin.Context) {
	if h.logs == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database unavailable"})
		return
	}
	filter := auditFilterForAccess(c, auditFilterFromQuery(c))
	page, pageSize := auditPaginationFromQuery(c)
	filter.Limit = pageSize
	filter.Offset = (page - 1) * pageSize

	logs, err := h.logs.List(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	total, err := h.logs.Count(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"logs":      logs,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetLog GET /api/audit/logs/:id
func (h *AuditHandler) GetLog(c *gin.Context) {
	if h.logs == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database unavailable"})
		return
	}
	row, err := h.logs.GetByID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "审计记录不存在"})
		return
	}
	if session, ok := security.CurrentSession(c); !ok || (session.Scope != database.RBACScopeAll && row.Actor != session.Username) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该资源"})
		return
	}
	audit.ApplyResourceAvailability(h.db, h.c2, h.executions, h.findings, h.webshells, h.batches, row)
	c.JSON(http.StatusOK, gin.H{"log": row})
}

// ExportLogs GET /api/audit/logs/export — JSON or CSV (?format=csv), max 5000 rows.
func (h *AuditHandler) ExportLogs(c *gin.Context) {
	if h.logs == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database unavailable"})
		return
	}
	filter := auditFilterForAccess(c, auditFilterFromQuery(c))
	filter.Limit = 5000
	filter.Offset = 0

	logs, err := h.logs.List(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if c.Query("format") == "csv" {
		writeAuditLogsCSV(c, logs)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="audit-logs.json"`)
	c.JSON(http.StatusOK, gin.H{
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"logs":        logs,
	})
}

func auditFilterForAccess(c *gin.Context, filter store.AuditListFilter) store.AuditListFilter {
	if session, ok := security.CurrentSession(c); ok && session.Scope != database.RBACScopeAll {
		filter.Actor = session.Username
	}
	return filter
}

// newAuditLogsStore is the only way this package comes by audit_logs. A handler built without a
// database keeps a nil store, whose methods answer an error instead of panicking.
// newFindingLookup mirrors newAuditLogsStore's nil rule: a handler built without a connection must
// leave the findings existence answer at "unknown", and only an untyped nil in the interface field
// reads that way - a typed (*store.Vulnerabilities)(nil) would pass the guard and then claim the
// finding is gone.
func newFindingLookup(db *database.DB) audit.FindingLookup {
	if db == nil {
		return nil
	}
	return database.NewFindings(db)
}

func newAuditLogsStore(db *database.DB) *store.AuditLogs {
	if db == nil {
		return nil
	}
	return store.NewAuditLogs(db.DB)
}
