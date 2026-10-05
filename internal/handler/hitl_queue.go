package handler

import (
	"net/http"
	"strings"

	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"

	"github.com/gin-gonic/gin"
)

// conversationAccessSource is the one permission question the HITL read surface asks: may this
// principal see this conversation? It is a parameter, not *database.DB, so moving this cluster out
// of AgentHandler did not smuggle a raw handle into a smaller type.
type conversationAccessSource interface {
	UserCanAccessResource(userID, scope, resourceType, resourceID string) bool
}

// HITLQueue owns the human-in-the-loop interrupt surface that is *not* the run loop: listing
// the pending queue and the decided log, rendering them for the API, the per-conversation
// permission checks that decide what a caller may see, and the endpoints that answer a pending
// interrupt (decision, dismissal) or delete through those checks.
//
// It came out of AgentHandler because those methods shared only four things - the HITL domain
// store, a conversation-access check, the retention setting and the audit service - while the rest of
// AgentHandler is the agent run loop. The state that stays in AgentHandler (tasks, sessions, SSE,
// the agent client) is genuinely the conversation-execution concern; this was not.
//
// The audit service is a plain field rather than a SetAudit method: internal/app injects it by
// assignment from AgentHandler.SetAudit, which keeps the "how many injection setters the wiring must
// remember" number going down instead of up.
//
// `manager` is a constructor argument rather than something wired after the fact, because
// NewAgentHandler builds the manager first and the queue needs it to answer an interrupt. It is
// only ever asked two questions - release this interrupt, drop it from the in-memory set - so the
// queue reaches HITLManager through its methods, never through `mu`/`pending`/`decideCh`.
type HITLQueue struct {
	access    conversationAccessSource
	hitlStore *store.HITL
	config    *config.Config
	audit     *audit.Service
	manager   *HITLManager
}

func newHITLQueue(access conversationAccessSource, hitlStore *store.HITL, cfg *config.Config, manager *HITLManager) *HITLQueue {
	return &HITLQueue{access: access, hitlStore: hitlStore, config: cfg, manager: manager}
}

// hitlStoreOrErr is where the HTTP layer reaches the interrupt table now: through the domain store
// instead of a raw statement.
func (q *HITLQueue) hitlStoreOrErr() (*store.HITL, error) {
	if q == nil || q.hitlStore == nil {
		return nil, errHitlStoreUnavailable
	}
	return q.hitlStore, nil
}

func (q *HITLQueue) listHitlInterrupts(set store.InterruptSet, f store.InterruptFilter) ([]store.Interrupt, int, error) {
	s, err := q.hitlStoreOrErr()
	if err != nil {
		return nil, 0, err
	}
	return s.List(set, f)
}

func (q *HITLQueue) hitlRetentionDays() int {
	if q.config != nil {
		return q.config.Hitl.RetentionDaysEffective()
	}
	return config.HitlConfig{}.RetentionDaysEffective()
}

// ListHITLLogs GET /api/hitl/logs - the decided log, page-clamped, with the retention window the
// caller's UI shows next to it.
func (q *HITLQueue) ListHITLLogs(c *gin.Context) {
	page, pageSize, offset := hitlListPaging(c)
	f := hitlFilterFromRequest(c)
	f.Access = hitlAccessFromRequest(c)
	f.Limit, f.Offset = pageSize, offset

	items, total, err := q.listHitlInterrupts(store.InterruptsLog, f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": hitlInterruptMaps(items), "page": page, "pageSize": pageSize, "total": total, "retentionDays": q.hitlRetentionDays()})
}

// DeleteHITLLogs DELETE /api/hitl/logs - batch delete or clear-by-filter, never pending rows.
func (q *HITLQueue) DeleteHITLLogs(c *gin.Context) {
	var request struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效: " + err.Error()})
		return
	}

	s, err := q.hitlStoreOrErr()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var deleted int64
	if request.All {
		f := hitlFilterFromRequest(c)
		f.Access = hitlAccessFromRequest(c)
		deleted, err = s.DeleteLogs(store.InterruptsLog, f)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if q.audit != nil {
			q.audit.RecordOK(c, "hitl", "logs_clear", "清空人机协同审计日志", "hitl_interrupt", "", map[string]interface{}{
				"deleted": deleted,
			})
		}
	} else {
		if len(request.IDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "审计日志 ID 列表不能为空"})
			return
		}
		ids, filterErr := q.filterAllowedHitlInterruptIDs(c, request.IDs)
		if filterErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": filterErr.Error()})
			return
		}
		deleted, err = s.DeleteLogsByIDs(ids)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if q.audit != nil {
			q.audit.RecordOK(c, "hitl", "logs_delete_batch", "批量删除人机协同审计日志", "hitl_interrupt", "", map[string]interface{}{
				"count":   len(request.IDs),
				"deleted": deleted,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "删除成功", "deleted": deleted})
}

// GetHITLLog GET /api/hitl/logs/:id. A row the caller cannot see answers 403 after the store said
// it exists, which is the same shape the rest of the queue uses: existence is not secret, content is.
func (q *HITLQueue) GetHITLLog(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}
	s, err := q.hitlStoreOrErr()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	it, found, err := s.Get(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if !q.hitlConversationAllowed(c, it.ConversationID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该资源"})
		return
	}
	c.JSON(http.StatusOK, hitlInterruptToMap(it))
}

// filterAllowedHitlInterruptIDs drops ids the caller may not touch. Requested ids
// that are not in the table at all drop out too - they cannot be deleted, and
// saying so would leak which ids exist.
func (q *HITLQueue) filterAllowedHitlInterruptIDs(c *gin.Context, ids []string) ([]string, error) {
	clean := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
	}
	if len(clean) == 0 {
		return clean, nil
	}
	s, err := q.hitlStoreOrErr()
	if err != nil {
		return nil, err
	}
	owners, err := s.ConversationOwners(clean)
	if err != nil {
		return nil, err
	}
	allowed := make([]string, 0, len(clean))
	for _, id := range clean {
		conversationID, ok := owners[id]
		if !ok {
			continue
		}
		if q.hitlConversationAllowed(c, conversationID) {
			allowed = append(allowed, id)
		}
	}
	return allowed, nil
}

func (q *HITLQueue) hitlInterruptAllowed(c *gin.Context, interruptID string) bool {
	s, err := q.hitlStoreOrErr()
	if err != nil {
		return false
	}
	conversationID, found, err := s.ConversationOwner(interruptID)
	if err != nil || !found {
		return false
	}
	return q.hitlConversationAllowed(c, conversationID)
}

func (q *HITLQueue) hitlConversationAllowed(c *gin.Context, conversationID string) bool {
	session, ok := security.CurrentSession(c)
	if !ok {
		return false
	}
	return q.access.UserCanAccessResource(session.UserID, session.Scope, "conversation", conversationID)
}

// The pending interrupt's answer surface. These three endpoints used to sit on AgentHandler and
// only forwarded to this queue's own store and access checks - DecideHITLInterrupt even reached into
// HITLManager's mutex and pending map to drain a dismissed interrupt, which the manager now does
// through DropPending. Wiring them here means one object owns "what is waiting on a human".
func (q *HITLQueue) ListHITLPending(c *gin.Context) {
	page, pageSize, offset := hitlListPaging(c)
	f := hitlFilterFromRequest(c)
	f.Access = hitlAccessFromRequest(c)
	f.Limit, f.Offset = pageSize, offset
	items, total, err := q.listHitlInterrupts(store.InterruptsAwaitingHuman, f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": hitlInterruptMaps(items), "page": page, "pageSize": pageSize, "total": total})
}

type hitlDecisionReq struct {
	InterruptID     string                 `json:"interruptId" binding:"required"`
	Decision        string                 `json:"decision" binding:"required"`
	Comment         string                 `json:"comment,omitempty"`
	EditedArguments map[string]interface{} `json:"editedArguments,omitempty"`
}

func (q *HITLQueue) DecideHITLInterrupt(c *gin.Context) {
	var req hitlDecisionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if q.manager == nil {
		c.JSON(500, gin.H{"error": "hitl manager unavailable"})
		return
	}
	if !q.hitlInterruptAllowed(c, req.InterruptID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该资源"})
		return
	}
	if err := q.manager.ResolveInterrupt(req.InterruptID, req.Decision, req.Comment, req.EditedArguments); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if q.audit != nil {
		q.audit.RecordOK(c, "hitl", "decision", "HITL 审批决策", "hitl_interrupt", req.InterruptID, map[string]interface{}{
			"decision": req.Decision,
		})
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (q *HITLQueue) DismissHITLInterrupt(c *gin.Context) {
	var req struct {
		InterruptID string `json:"interruptId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if q.manager == nil {
		c.JSON(500, gin.H{"error": "hitl manager unavailable"})
		return
	}
	if !q.hitlInterruptAllowed(c, req.InterruptID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该资源"})
		return
	}
	s, storeErr := q.hitlStoreOrErr()
	if storeErr != nil {
		c.JSON(500, gin.H{"error": storeErr.Error()})
		return
	}
	n, err := s.Dismiss(req.InterruptID, "dismissed by user")
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if n == 0 {
		c.JSON(404, gin.H{"error": "interrupt not found or already resolved"})
		return
	}
	q.manager.DropPending(req.InterruptID, "dismissed by user")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
