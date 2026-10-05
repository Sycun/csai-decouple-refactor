package handler

import (
	"context"
	"cyberstrike-ai/internal/capability"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/multiagent"
	"cyberstrike-ai/internal/store"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type hitlRuntimeConfig struct {
	Enabled        bool
	Mode           string
	Reviewer       string
	SensitiveTools map[string]struct{}
	Timeout        time.Duration
}

type hitlDecision struct {
	Decision        string
	Comment         string
	EditedArguments map[string]interface{}
}

type pendingInterrupt struct {
	ConversationID string
	InterruptID    string
	Mode           string
	ToolName       string
	ToolCallID     string
	decideCh       chan hitlDecision
}

type HITLManager struct {
	// interrupts and sessions are the domain stores that own the SQL this manager
	// used to write inline. The manager keeps in-memory state (who is waiting, what
	// has been approved) and delegates every durable read and write.
	interrupts *store.HITL
	sessions   *store.Session
	logger     *zap.Logger

	mu      sync.RWMutex
	runtime map[string]hitlRuntimeConfig
	pending map[string]*pendingInterrupt
	// approvedExec 审批通过、待回写 tool_result 的队列（按会话 FIFO）
	approvedExec map[string][]hitlApprovedExecTrack
	// globalWhitelist 是 config.yaml 的免审批工具集（小写）。交集判定的运维者半边，
	// 只能由运维者写入（配置落盘或管理员 API），请求体无法拓宽。
	globalWhitelist map[string]bool
}

// SetGlobalWhitelist replaces the operator-owned half of the exemption
// intersection. A session request body can never reach this.
func (m *HITLManager) SetGlobalWhitelist(tools []string) {
	if m == nil {
		return
	}
	next := builtInHitlExempt()
	for _, t := range tools {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			next[t] = true
		}
	}
	m.mu.Lock()
	m.globalWhitelist = next
	m.mu.Unlock()
}

// builtInHitlExempt returns the agent-internal meta tools that are always
// exempt from approval. They are readonly control-plane helpers, not callable
// capabilities, and the capability registry gives them no approval floor.
func builtInHitlExempt() map[string]bool {
	out := map[string]bool{}
	for _, t := range multiagent.MergeHitlExemptMetaTools(nil) {
		out[strings.ToLower(strings.TrimSpace(t))] = true
	}
	return out
}

func NewHITLManager(db *database.DB, logger *zap.Logger) *HITLManager {
	m := &HITLManager{
		globalWhitelist: builtInHitlExempt(),
		logger:          logger,
		runtime:         make(map[string]hitlRuntimeConfig),
		pending:         make(map[string]*pendingInterrupt),
	}
	if db != nil {
		m.interrupts = store.NewHITL(db.DB)
		m.sessions = store.NewSession(db.DB)
	}
	return m
}

func (m *HITLManager) EnsureSchema() error {
	if m.interrupts == nil {
		return errors.New("hitl store unavailable")
	}
	cancelled, err := m.interrupts.EnsureSchema()
	if err != nil {
		return err
	}
	if cancelled > 0 {
		m.logger.Info("cancelled orphaned HITL interrupts from previous process", zap.Int64("count", cancelled))
	}
	if err := m.reconcileRestartInterruptedMessages(); err != nil {
		m.logger.Warn("failed to finalize assistant messages interrupted by process restart", zap.Error(err))
	}
	return nil
}

// reconcileRestartInterruptedMessages completes durable terminal state for
// historical assistant placeholders that have explicit evidence of being over:
// a terminal HITL/process event, or a later message in the same conversation.
// The evidence requirement avoids rewriting a placeholder that could still be
// recoverable by another runtime.
//
// The store finds and writes the rows; the wording below is this package's
// contribution, because what the user is told about an interruption is not
// something the persistence layer should decide.
func (m *HITLManager) reconcileRestartInterruptedMessages() error {
	if m.sessions == nil {
		return errors.New("hitl session store unavailable")
	}
	candidates, err := m.sessions.InterruptedPlaceholders()
	if err != nil {
		return err
	}
	updates := make([]store.InterruptedUpdate, 0, len(candidates))
	for _, item := range candidates {
		eventType := strings.ToLower(strings.TrimSpace(item.TerminalEvent))
		decision := strings.ToLower(strings.TrimSpace(item.HITLDecision))
		comment := strings.ToLower(strings.TrimSpace(item.DecisionComment))
		if eventType == "" {
			if strings.EqualFold(strings.TrimSpace(item.HITLStatus), "timeout") || strings.Contains(comment, "timeout") {
				eventType = "timeout"
			} else {
				eventType = "cancelled"
			}
		}

		notice := "任务因服务重启已中断。"
		reason := "process_restarted"
		switch eventType {
		case "timeout":
			notice = "任务等待审批超时，已自动拒绝。"
			reason = "hitl_timeout"
		case "error":
			notice = "任务执行失败，已停止。"
			reason = "execution_error"
		case "cancelled":
			if decision == "reject" && comment != "process restarted" {
				notice = "任务审批已拒绝，执行已停止。"
				reason = "hitl_rejected"
			} else if comment == "process restarted" {
				notice = "任务因服务重启已中断，审批已取消。"
			}
		default:
			eventType = "cancelled"
		}
		updates = append(updates, store.InterruptedUpdate{
			MessageID:      item.MessageID,
			ConversationID: item.ConversationID,
			EventType:      eventType,
			Notice:         notice,
			Reason:         reason,
			InterruptedAt:  item.InterruptedAt,
		})
	}
	_, err = m.sessions.FinalizeInterruptedPlaceholders(updates)
	return err
}

func normalizeHitlMode(mode string) string {
	v := strings.ToLower(strings.TrimSpace(mode))
	if v == "" {
		return "approval"
	}
	switch v {
	case "off":
		return "off"
	case "feedback", "followup":
		return "approval"
	case "approval", "review_edit":
		return v
	default:
		return "approval"
	}
}

func normalizeHitlDefaultMode(mode string) string {
	v := strings.ToLower(strings.TrimSpace(mode))
	switch v {
	case "feedback", "followup":
		return "approval"
	case "approval", "review_edit":
		return v
	default:
		return "off"
	}
}

func (m *HITLManager) ActivateConversation(conversationID string, req *HITLRequest) {
	if req == nil || !req.Enabled {
		m.DeactivateConversation(conversationID)
		return
	}
	tools := make(map[string]struct{})
	for _, t := range req.SensitiveTools {
		n := strings.ToLower(strings.TrimSpace(t))
		if n != "" {
			tools[n] = struct{}{}
		}
	}
	// timeout <= 0 means wait forever (no timeout).
	timeout := time.Duration(0)
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	m.mu.Lock()
	m.runtime[conversationID] = hitlRuntimeConfig{
		Enabled:        true,
		Mode:           normalizeHitlMode(req.Mode),
		Reviewer:       normalizeHitlReviewer(req.Reviewer),
		SensitiveTools: tools,
		Timeout:        timeout,
	}
	m.mu.Unlock()
}

func (m *HITLManager) DeactivateConversation(conversationID string) {
	m.mu.Lock()
	delete(m.runtime, conversationID)
	m.mu.Unlock()
}

func (m *HITLManager) shouldInterrupt(conversationID, toolName string) (hitlRuntimeConfig, bool) {
	m.mu.RLock()
	cfg, ok := m.runtime[conversationID]
	m.mu.RUnlock()

	tool := strings.ToLower(strings.TrimSpace(toolName))

	// 能力级审批下限：destructive 类能力无论会话是否开启 HITL、无论任何白名单，
	// 都必须逐次人工授权。白名单只能缩小审批范围，永远不能拓宽运维者批准的范围。
	if capabilityRequiresApproval(tool) {
		if !ok {
			return hitlRuntimeConfig{Enabled: true}, true
		}
		return cfg, true
	}

	if !ok || !cfg.Enabled {
		return hitlRuntimeConfig{}, false
	}

	// 免审批判定改为“交集”：工具必须同时出现在会话白名单与 config 全局白名单中
	// 才可豁免。原先的并集语义允许一次请求体提交就把工具永久免审，是提权通道。
	if len(cfg.SensitiveTools) == 0 {
		return cfg, true
	}
	_, inSessionWhitelist := cfg.SensitiveTools[tool]
	if !inSessionWhitelist {
		return cfg, true
	}
	if !m.globallyWhitelisted(tool) {
		return cfg, true
	}
	return cfg, false
}

// globallyWhitelisted reports whether config.yaml lists the tool as exempt. The
// global list is the operator-owned half of the exemption intersection.
func (m *HITLManager) globallyWhitelisted(tool string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.globalWhitelist == nil {
		return false
	}
	return m.globalWhitelist[strings.ToLower(strings.TrimSpace(tool))]
}

// capabilityRequiresApproval 询问能力注册表：该工具是否带有不可豁免的审批下限。
//
// 未登记的工具不在这里强制审批：agent 内部元工具（write_file/read_file/glob 等）
// 本就不是可调用能力，而真正的风险工具会在执行点被注册表 fail-closed 拒绝。
// 审批下限只针对已声明为 destructive 的能力。
func capabilityRequiresApproval(toolName string) bool {
	spec, err := capability.Global().Lookup(toolName)
	if err != nil {
		return false
	}
	return spec.RequiresHumanDecision()
}

// NeedsToolApproval 与 Agent 工具层 shouldInterrupt 语义一致：仅当该会话已开启人机协同且工具不在免审批白名单时为 true。
func (m *HITLManager) NeedsToolApproval(conversationID, toolName string) bool {
	if m == nil {
		return false
	}
	_, need := m.shouldInterrupt(conversationID, toolName)
	return need
}

func (m *HITLManager) CreatePendingInterrupt(conversationID, assistantMessageID, mode, toolName, toolCallID, payload, reviewer string) (*pendingInterrupt, error) {
	now := time.Now()
	id := "hitl_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	reviewer = normalizeHitlReviewer(reviewer)
	if m.interrupts == nil {
		return nil, errors.New("hitl store unavailable")
	}
	if err := m.interrupts.CreateInterrupt(store.NewInterrupt{
		ID:             id,
		ConversationID: conversationID,
		MessageID:      assistantMessageID,
		Mode:           mode,
		ToolName:       toolName,
		ToolCallID:     toolCallID,
		Payload:        payload,
		Reviewer:       reviewer,
		CreatedAt:      now,
	}); err != nil {
		return nil, err
	}
	// 刷新页面后侧栏依赖 DB 配置；若仅内存 Activate 未落库，会导致「有待审批却显示关闭」
	_ = m.ensureConversationHITLModePersisted(conversationID, mode)
	p := &pendingInterrupt{
		ConversationID: conversationID,
		InterruptID:    id,
		Mode:           normalizeHitlMode(mode),
		ToolName:       toolName,
		ToolCallID:     toolCallID,
		decideCh:       make(chan hitlDecision, 1),
	}
	// Agent 审查不会等待人工决策，也不应进入人工审批的内存待办队列。
	if reviewer != "audit_agent" {
		m.mu.Lock()
		m.pending[id] = p
		m.mu.Unlock()
	}
	return p, nil
}

// ensureConversationHITLModePersisted 在产生待审批时把 mode 写入 hitl_conversation_configs，避免刷新后 GET 配置仍为关闭。
func (m *HITLManager) ensureConversationHITLModePersisted(conversationID, interruptMode string) error {
	if strings.TrimSpace(conversationID) == "" {
		return nil
	}
	nm := normalizeHitlMode(interruptMode)
	if nm == "off" {
		return nil
	}
	cfg, err := m.LoadConversationConfig(conversationID)
	if err != nil {
		return err
	}
	if cfg.Enabled && normalizeHitlMode(cfg.Mode) == nm {
		return nil
	}
	cfg.Enabled = true
	cfg.Mode = nm
	if cfg.TimeoutSeconds < 0 {
		cfg.TimeoutSeconds = 0
	}
	return m.SaveConversationConfig(conversationID, cfg)
}

// PendingHITLInterruptMode 返回该会话最新一条 pending 中断的协同模式（用于 GET 配置时与库内「关闭」状态对齐）。
func (m *HITLManager) PendingHITLInterruptMode(conversationID string) (string, bool) {
	if strings.TrimSpace(conversationID) == "" || m.interrupts == nil {
		return "", false
	}
	mode, found, err := m.interrupts.LatestPendingMode(conversationID)
	if err != nil || !found {
		return "", false
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return "", false
	}
	return mode, true
}

func hitlStoredConfigEffective(cfg *HITLRequest) bool {
	if cfg == nil {
		return false
	}
	if cfg.Enabled {
		return true
	}
	return normalizeHitlMode(cfg.Mode) != "off"
}

func (m *HITLManager) ResolveInterrupt(interruptID, decision, comment string, editedArguments map[string]interface{}) error {
	decision = strings.ToLower(strings.TrimSpace(decision))
	if decision != "approve" && decision != "reject" {
		return errors.New("decision must be approve/reject")
	}
	m.mu.RLock()
	p, ok := m.pending[interruptID]
	m.mu.RUnlock()
	if !ok {
		return errors.New("interrupt not found or already resolved")
	}
	d := hitlDecision{
		Decision:        decision,
		Comment:         strings.TrimSpace(comment),
		EditedArguments: editedArguments,
	}
	select {
	case p.decideCh <- d:
		return nil
	default:
		return errors.New("interrupt already resolved or decision channel busy")
	}
}

// DropPending removes an interrupt from the in-memory pending set and wakes whatever is
// waiting on it with a rejection. The durable half is the store's own Dismiss; this is the
// process half of the same operation. It exists as a method rather than a few lines at the
// call site because `mu`/`pending`/`decideCh` are the manager's private state - an HTTP
// handler reaching in there has to know the locking and the channel's buffer semantics, and
// nothing outside this type should have to.
func (m *HITLManager) DropPending(interruptID, comment string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[interruptID]
	if !ok {
		return
	}
	delete(m.pending, interruptID)
	select {
	case p.decideCh <- hitlDecision{Decision: "reject", Comment: comment}:
	default:
	}
}

func (m *HITLManager) SaveConversationConfig(conversationID string, req *HITLRequest) error {
	if strings.TrimSpace(conversationID) == "" {
		return errors.New("conversationId is required")
	}
	if req == nil {
		req = &HITLRequest{Enabled: false, Mode: "off", TimeoutSeconds: 0}
	}
	mode := normalizeHitlMode(req.Mode)
	if !req.Enabled {
		mode = "off"
	}
	if m.interrupts == nil {
		return errors.New("hitl store unavailable")
	}
	return m.interrupts.SaveConversationConfig(conversationID, store.ConversationConfig{
		Enabled:        req.Enabled,
		Mode:           mode,
		Reviewer:       normalizeHitlReviewer(req.Reviewer),
		SensitiveTools: req.SensitiveTools,
		TimeoutSeconds: req.TimeoutSeconds,
	})
}

func (m *HITLManager) LoadConversationConfig(conversationID string) (*HITLRequest, error) {
	absent := &HITLRequest{Enabled: false, Mode: "off", Reviewer: "human", SensitiveTools: []string{}, TimeoutSeconds: 0}
	if m.interrupts == nil {
		return absent, nil
	}
	stored, found, err := m.interrupts.ConversationConfig(conversationID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !found) {
		return absent, nil
	}
	if err != nil {
		return nil, err
	}
	return &HITLRequest{
		Enabled:        stored.Enabled,
		Mode:           stored.Mode,
		Reviewer:       normalizeHitlReviewer(stored.Reviewer),
		SensitiveTools: stored.SensitiveTools,
		TimeoutSeconds: stored.TimeoutSeconds,
	}, nil
}

func (m *HITLManager) HasConversationConfig(conversationID string) (bool, error) {
	if strings.TrimSpace(conversationID) == "" {
		return false, nil
	}
	if m.interrupts == nil {
		return false, nil
	}
	return m.interrupts.HasConversationConfig(conversationID)
}

func (m *HITLManager) waitDecision(ctx context.Context, p *pendingInterrupt, timeout time.Duration) (hitlDecision, error) {
	defer func() {
		m.mu.Lock()
		delete(m.pending, p.InterruptID)
		m.mu.Unlock()
	}()
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}
	select {
	case d := <-p.decideCh:
		// 只有 review_edit 模式允许改参；其他模式一律忽略 edited arguments
		if p.Mode != "review_edit" && len(d.EditedArguments) > 0 {
			d.EditedArguments = nil
		}
		_ = m.interrupts.Resolve(p.InterruptID, store.Resolution{Status: "decided", Decision: d.Decision, Comment: d.Comment, DecidedBy: "human", At: time.Now()})
		return d, nil
	case <-timeoutCh:
		comment := "HITL timeout auto-reject for safety"
		_ = m.interrupts.Resolve(p.InterruptID, store.Resolution{Status: "timeout", Decision: "reject", Comment: comment, DecidedBy: "system", At: time.Now()})
		return hitlDecision{Decision: "reject", Comment: comment}, nil
	case <-ctx.Done():
		_ = m.interrupts.Resolve(p.InterruptID, store.Resolution{Status: "cancelled", Decision: "reject", Comment: "task cancelled", DecidedBy: "system", At: time.Now()})
		return hitlDecision{Decision: "reject", Comment: "task cancelled"}, ctx.Err()
	}
}

func (h *AgentHandler) activateHITLForConversation(conversationID string, req *HITLRequest) {
	if h.hitlManager == nil {
		return
	}
	h.hitlManager.SetGlobalWhitelist(h.HitlPolicy().hitlConfigGlobalToolWhitelist())
	if req == nil {
		cfg, err := h.HitlPolicy().loadHITLConversationConfig(conversationID)
		if err == nil {
			req = cfg
		}
	}
	if req != nil && strings.TrimSpace(req.Reviewer) == "" {
		req.Reviewer = h.HitlPolicy().hitlEffectiveDefaultReviewer()
	}
	h.hitlManager.ActivateConversation(conversationID, h.HitlPolicy().hitlRequestWithMergedConfigWhitelist(req))
}

func (h *AgentHandler) waitHITLApproval(runCtx context.Context, cancelRun context.CancelCauseFunc, conversationID, assistantMessageID, toolName, toolCallID string, payload map[string]interface{}, sendEventFunc func(eventType, message string, data interface{})) (*hitlDecision, error) {
	cfg, need := h.hitlManager.shouldInterrupt(conversationID, toolName)
	if !need {
		return nil, nil
	}
	h.enrichHitlApprovalPayload(conversationID, assistantMessageID, payload)
	approvalStartedAt := time.Now().UTC()
	timeoutSeconds := int(cfg.Timeout / time.Second)
	var approvalExpiresAt *time.Time
	if timeoutSeconds > 0 {
		expiresAt := approvalStartedAt.Add(cfg.Timeout)
		approvalExpiresAt = &expiresAt
	}
	auditBackend, auditModel := h.HitlPolicy().hitlAuditEngineInfo()
	payload["hitlApproval"] = map[string]interface{}{
		"createdAt":      approvalStartedAt,
		"timeoutSeconds": timeoutSeconds,
		"expiresAt":      approvalExpiresAt,
		"auditBackend":   auditBackend,
		"auditModel":     auditModel,
	}
	payloadRaw, _ := json.Marshal(payload)
	p, err := h.hitlManager.CreatePendingInterrupt(conversationID, assistantMessageID, cfg.Mode, toolName, toolCallID, string(payloadRaw), cfg.Reviewer)
	if err != nil {
		h.logger.Warn("创建 HITL 中断失败", zap.Error(err))
		return nil, err
	}
	emitHITL := func(eventType, message string, eventData map[string]interface{}) {
		clientData := enrichProgressEventData(eventData, conversationID, assistantMessageID)
		if sendEventFunc != nil {
			sendEventFunc(eventType, message, clientData)
		}
		if strings.TrimSpace(assistantMessageID) != "" && h.db != nil {
			if err := h.db.AddProcessDetail(assistantMessageID, conversationID, eventType, message, clientData); err != nil {
				h.logger.Warn("保存 HITL 过程详情失败", zap.Error(err), zap.String("eventType", eventType))
			}
		}
	}

	if cfg.Reviewer == "audit_agent" {
		emitHITL("hitl_audit_agent_started", "审计 Agent 正在审查此请求", map[string]interface{}{
			"conversationId": conversationID,
			"interruptId":    p.InterruptID,
			"toolName":       toolName,
			"toolCallId":     toolCallID,
			"mode":           cfg.Mode,
			"reviewer":       "audit_agent",
			"status":         "audit_running",
			"payload":        payload,
		})
		ad := h.auditAgentReview(runCtx, cfg.Mode, toolName, payload)
		if s, storeErr := h.hitlQueue.hitlStoreOrErr(); storeErr == nil {
			if decErr := s.RecordAgentDecision(p.InterruptID, ad.Decision, ad.Comment, time.Now()); decErr != nil {
				h.logger.Warn("保存审计 Agent HITL 决策失败", zap.Error(decErr), zap.String("interruptId", p.InterruptID))
			}
		}
		emitHITL("hitl_audit_agent", "审计 Agent 已裁决", map[string]interface{}{
			"conversationId": conversationID,
			"interruptId":    p.InterruptID,
			"toolName":       toolName,
			"toolCallId":     toolCallID,
			"mode":           cfg.Mode,
			"status":         "decided",
			"decision":       ad.Decision,
			"comment":        ad.Comment,
			"editedArgs":     ad.EditedArguments,
			"decidedBy":      "audit_agent",
			"reviewer":       "audit_agent",
		})
		if ad.Decision == "reject" {
			emitHITL("hitl_rejected", "审计 Agent 拒绝本次工具调用", map[string]interface{}{
				"conversationId": conversationID,
				"interruptId":    p.InterruptID,
				"toolName":       toolName,
				"toolCallId":     toolCallID,
				"mode":           cfg.Mode,
				"decision":       "reject",
				"comment":        ad.Comment,
				"decidedBy":      "audit_agent",
				"reviewer":       "audit_agent",
			})
			return &ad, nil
		}
		emitHITL("hitl_resumed", "审计 Agent 已通过，继续执行", map[string]interface{}{
			"conversationId": conversationID,
			"interruptId":    p.InterruptID,
			"toolName":       toolName,
			"toolCallId":     toolCallID,
			"mode":           cfg.Mode,
			"decision":       "approve",
			"comment":        ad.Comment,
			"editedArgs":     ad.EditedArguments,
			"decidedBy":      "audit_agent",
			"reviewer":       "audit_agent",
		})
		h.hitlManager.TrackApprovedHitlExecution(p.InterruptID, conversationID, toolName, toolCallID)
		return &ad, nil
	}

	emitHITL("hitl_interrupt", "命中人机协同审批", map[string]interface{}{
		"conversationId": conversationID,
		"interruptId":    p.InterruptID,
		"mode":           cfg.Mode,
		"toolName":       toolName,
		"toolCallId":     toolCallID,
		"reviewer":       "human",
		"status":         "pending",
		"createdAt":      approvalStartedAt,
		"timeoutSeconds": timeoutSeconds,
		"expiresAt":      approvalExpiresAt,
		"payload":        payload,
	})
	d, waitErr := h.hitlManager.waitDecision(runCtx, p, cfg.Timeout)
	if waitErr != nil {
		if cancelRun != nil && (errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded)) {
			cause := context.Cause(runCtx)
			switch {
			case errors.Is(cause, ErrTaskCancelled):
				cancelRun(ErrTaskCancelled)
			case cause != nil:
				cancelRun(cause)
			case errors.Is(waitErr, context.DeadlineExceeded):
				cancelRun(context.DeadlineExceeded)
			default:
				cancelRun(ErrTaskCancelled)
			}
		}
		return nil, waitErr
	}
	if d.Decision == "reject" {
		rejectMsg := "人工拒绝本次工具调用，模型将基于反馈继续迭代"
		timedOut := strings.Contains(strings.ToLower(strings.TrimSpace(d.Comment)), "timeout")
		if timedOut {
			rejectMsg = "审批超时，安全起见已自动拒绝，模型将基于反馈继续迭代"
		}
		status := "decided"
		decidedBy := "human"
		if timedOut {
			status = "timeout"
			decidedBy = "system"
		}
		emitHITL("hitl_rejected", rejectMsg, map[string]interface{}{
			"conversationId": conversationID,
			"interruptId":    p.InterruptID,
			"toolName":       toolName,
			"toolCallId":     toolCallID,
			"mode":           cfg.Mode,
			"status":         status,
			"decision":       "reject",
			"comment":        d.Comment,
			"decidedBy":      decidedBy,
			"reviewer":       "human",
		})
		return &d, nil
	}
	emitHITL("hitl_resumed", "人工确认通过，继续执行", map[string]interface{}{
		"conversationId": conversationID,
		"interruptId":    p.InterruptID,
		"toolName":       toolName,
		"toolCallId":     toolCallID,
		"mode":           cfg.Mode,
		"decision":       "approve",
		"comment":        d.Comment,
		"editedArgs":     d.EditedArguments,
		"reviewer":       "human",
	})
	h.hitlManager.TrackApprovedHitlExecution(p.InterruptID, conversationID, toolName, toolCallID)
	return &d, nil
}

func (h *AgentHandler) handleHITLToolCall(runCtx context.Context, cancelRun context.CancelCauseFunc, conversationID, assistantMessageID string, data map[string]interface{}, sendEventFunc func(eventType, message string, data interface{})) {
	if h.hitlManager == nil {
		return
	}
	toolName, _ := data["toolName"].(string)
	toolCallID, _ := data["toolCallId"].(string)
	d, err := h.waitHITLApproval(runCtx, cancelRun, conversationID, assistantMessageID, toolName, toolCallID, data, sendEventFunc)
	if err != nil || d == nil {
		return
	}
	if len(d.EditedArguments) > 0 {
		if argsObj, ok := data["argumentsObj"].(map[string]interface{}); ok {
			for k := range argsObj {
				delete(argsObj, k)
			}
			for k, v := range d.EditedArguments {
				argsObj[k] = v
			}
			if b, mErr := json.Marshal(argsObj); mErr == nil {
				data["arguments"] = string(b)
			}
		}
	}
}

func (h *AgentHandler) interceptHITLForEinoTool(runCtx context.Context, cancelRun context.CancelCauseFunc, conversationID, assistantMessageID string, sendEventFunc func(eventType, message string, data interface{}), toolName, arguments string) (string, error) {
	payload := map[string]interface{}{
		"toolName":   toolName,
		"arguments":  arguments,
		"source":     "eino_middleware",
		"toolCallId": "",
	}
	var argsObj map[string]interface{}
	if strings.TrimSpace(arguments) != "" {
		_ = json.Unmarshal([]byte(arguments), &argsObj)
		if argsObj != nil {
			payload["argumentsObj"] = argsObj
		}
	}
	d, err := h.waitHITLApproval(runCtx, cancelRun, conversationID, assistantMessageID, toolName, "", payload, sendEventFunc)
	if err != nil || d == nil {
		return arguments, err
	}
	if d.Decision == "reject" {
		return arguments, multiagent.NewHumanRejectError(d.Comment)
	}
	if len(d.EditedArguments) > 0 {
		edited, mErr := json.Marshal(d.EditedArguments)
		if mErr == nil {
			return string(edited), nil
		}
	}
	return arguments, nil
}

type hitlConfigReq struct {
	ConversationID string `json:"conversationId" binding:"required"`
	HITLRequest
}

type mergeHitlGlobalWhitelistReq struct {
	SensitiveTools []string `json:"sensitiveTools"`
}

type setHitlGlobalWhitelistReq struct {
	ToolWhitelist []string `json:"toolWhitelist"`
}

type setHitlDefaultReviewerReq struct {
	Reviewer string `json:"reviewer"`
}

type setHitlDefaultConfigReq struct {
	Mode           string `json:"mode"`
	Reviewer       string `json:"reviewer"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}
