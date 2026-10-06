package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberstrike-ai/internal/sqltime"
)

// Monitor owns the tool-execution ledger: the tool_executions and tool_stats tables, their schema
// and the aggregations the console reads. The row shapes (ToolExecution, ToolResult, Content,
// ToolStats) live here too - they are what the tables store, and internal/mcp keeps the names as
// aliases so the protocol code reads unchanged.
//
// The statements were copied out of internal/database verbatim: same SELECT lists (COALESCE and
// all), same ORDER BYs, same error strings. Two things changed on purpose, both recorded in §11
// 第四十一刀: the eight "scan failed, drop the row" loops now return the error (a store owns its DDL,
// so a scan failure is a fault, not a row to lose), and the logger calls are gone - this package
// holds no logger (the documented trade of every store, see batch_task.go).
type Monitor struct {
	db *sql.DB
	// access answers "may this caller reach that resource". The rule belongs to the RBAC domain; the
	// store only composes it with the execution's own owner column. A store built without one (tests,
	// disabled paths) keeps the owner match and answers false on the conversation branch.
	access func(userID, scope, resourceType, resourceID string) bool
}

// NewMonitor binds the store to a connection. access may be nil.
func NewMonitor(db *sql.DB, access func(userID, scope, resourceType, resourceID string) bool) *Monitor {
	return &Monitor{db: db, access: access}
}

// 工具执行的状态词汇。internal/mcp 保留同名常量作为别名，协议代码读法不变。
const (
	ToolExecutionStatusQueued      = "queued"
	ToolExecutionStatusRunning     = "running"
	ToolExecutionStatusCompleted   = "completed"
	ToolExecutionStatusBlocked     = "blocked"
	ToolExecutionStatusFailed      = "failed"
	ToolExecutionStatusCancelled   = "cancelled"
	ToolExecutionStatusHardTimeout = "hard_timeout"
	ToolExecutionStatusOrphaned    = "orphaned"
)

// ToolResult 表示工具执行结果
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
	// Blocked means policy stopped the call before execution. IsError remains
	// true for MCP/model handling, while monitoring uses a distinct status.
	Blocked bool `json:"blocked,omitempty"`
}

// Content 表示内容
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolExecution 工具执行记录
type ToolExecution struct {
	ID        string                 `json:"id"`
	ToolName  string                 `json:"toolName"`
	Arguments map[string]interface{} `json:"arguments"`
	Status    string                 `json:"status"` // queued, running, completed, blocked, failed, cancelled, hard_timeout, orphaned
	Result    *ToolResult            `json:"result,omitempty"`
	Error     string                 `json:"error,omitempty"`
	StartTime time.Time              `json:"startTime"`
	EndTime   *time.Time             `json:"endTime,omitempty"`
	Duration  time.Duration          `json:"duration,omitempty"`
	// PartialOutput is a bounded tail preview of output produced by a running tool.
	// It is intentionally separate from Result, which remains the final canonical tool result.
	PartialOutput          string     `json:"partialOutput,omitempty"`
	PartialOutputBytes     int64      `json:"partialOutputBytes,omitempty"`
	PartialOutputTruncated bool       `json:"partialOutputTruncated,omitempty"`
	PartialOutputUpdatedAt *time.Time `json:"partialOutputUpdatedAt,omitempty"`
	// ConversationID 仅 API 展示用（进行中的 Agent 任务），不写入 tool_executions 表。
	ConversationID string `json:"conversationId,omitempty"`
	OwnerUserID    string `json:"-"`
}

// ToolStats 工具统计信息
type ToolStats struct {
	ToolName     string     `json:"toolName"`
	TotalCalls   int        `json:"totalCalls"`
	SuccessCalls int        `json:"successCalls"`
	FailedCalls  int        `json:"failedCalls"`
	BlockedCalls int        `json:"blockedCalls"`
	LastCallTime *time.Time `json:"lastCallTime,omitempty"`
}

// SaveToolExecution 保存工具执行记录
func (m *Monitor) SaveToolExecution(exec *ToolExecution) error {
	if m == nil || m.db == nil {
		return errors.New("store: monitor requires a database")
	}
	argsJSON, err := json.Marshal(exec.Arguments)
	if err != nil {
		argsJSON = []byte("{}")
	}

	var resultJSON sql.NullString
	if exec.Result != nil {
		resultBytes, err := json.Marshal(exec.Result)
		if err != nil {
		} else {
			resultJSON = sql.NullString{String: string(resultBytes), Valid: true}
		}
	}

	var errorText sql.NullString
	if exec.Error != "" {
		errorText = sql.NullString{String: exec.Error, Valid: true}
	}

	var endTime sql.NullTime
	if exec.EndTime != nil {
		endTime = sql.NullTime{Time: *exec.EndTime, Valid: true}
	}

	var durationMs sql.NullInt64
	if exec.Duration > 0 {
		durationMs = sql.NullInt64{Int64: exec.Duration.Milliseconds(), Valid: true}
	}
	var partialUpdatedAt sql.NullTime
	if exec.PartialOutputUpdatedAt != nil {
		partialUpdatedAt = sql.NullTime{Time: *exec.PartialOutputUpdatedAt, Valid: true}
	}
	partialTruncated := 0
	if exec.PartialOutputTruncated {
		partialTruncated = 1
	}

	query := `
		INSERT OR REPLACE INTO tool_executions 
		(id, tool_name, arguments, status, result, error, start_time, end_time, duration_ms, partial_output, partial_output_bytes, partial_output_truncated, partial_output_updated_at, owner_user_id, conversation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err = m.db.Exec(query,
		exec.ID,
		exec.ToolName,
		string(argsJSON),
		exec.Status,
		resultJSON,
		errorText,
		exec.StartTime,
		endTime,
		durationMs,
		sqlNullString(exec.PartialOutput),
		exec.PartialOutputBytes,
		partialTruncated,
		partialUpdatedAt,
		strings.TrimSpace(exec.OwnerUserID),
		strings.TrimSpace(exec.ConversationID),
		time.Now(),
	)

	if err != nil {
		return err
	}

	return nil
}

// UpdateToolExecutionResult 仅更新结果字段（用于 reduction 后将监控展示与模型上下文对齐）。
func (m *Monitor) UpdateToolExecutionResult(id string, result *ToolResult) error {
	if m == nil || m.db == nil {
		return errors.New("store: monitor requires a database")
	}
	id = strings.TrimSpace(id)
	if id == "" || result == nil {
		return nil
	}
	var status string
	if err := m.db.QueryRow(`SELECT status FROM tool_executions WHERE id = ?`, id).Scan(&status); err != nil && err != sql.ErrNoRows {
		return err
	}
	if status == ToolExecutionStatusBlocked {
		copy := *result
		copy.Blocked, copy.IsError = true, true
		result = &copy
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(`UPDATE tool_executions SET result = ? WHERE id = ?`, string(resultBytes), id)
	if err != nil {
	}
	return err
}

func sqlNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// CountToolExecutions 统计工具执行记录总数
func (m *Monitor) CountToolExecutions(status, toolName string) (int, error) {
	if m == nil || m.db == nil {
		return 0, errors.New("store: monitor requires a database")
	}
	return m.CountToolExecutionsForAccess(status, toolName, Access{Scope: ScopeAll})
}

func (m *Monitor) CountToolExecutionsForAccess(status, toolName string, access Access) (int, error) {
	if m == nil || m.db == nil {
		return 0, errors.New("store: monitor requires a database")
	}
	query := `SELECT COUNT(*) FROM tool_executions`
	args := []interface{}{}
	conditions := []string{}
	if status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, status)
	}
	if toolName != "" {
		// 支持部分匹配（模糊搜索），不区分大小写
		conditions = append(conditions, "LOWER(tool_name) LIKE ?")
		args = append(args, "%"+strings.ToLower(toolName)+"%")
	}
	if len(conditions) > 0 {
		query += ` WHERE ` + conditions[0]
		for i := 1; i < len(conditions); i++ {
			query += ` AND ` + conditions[i]
		}
	}
	query, args = appendToolExecutionAccessSQL(query, args, access, len(conditions) > 0)
	var count int
	err := m.db.QueryRow(query, args...).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// LoadToolExecutions 加载所有工具执行记录（支持分页）
func (m *Monitor) LoadToolExecutions() ([]*ToolExecution, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("store: monitor requires a database")
	}
	return m.LoadToolExecutionsWithPagination(0, 1000, "", "")
}

// LoadToolExecutionsWithPagination 分页加载工具执行记录
// limit: 最大返回记录数，0 表示使用默认值 1000
// offset: 跳过的记录数，用于分页
// status: 状态筛选，空字符串表示不过滤
// toolName: 工具名称筛选，空字符串表示不过滤
func (m *Monitor) LoadToolExecutionsWithPagination(offset, limit int, status, toolName string) ([]*ToolExecution, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("store: monitor requires a database")
	}
	if limit <= 0 {
		limit = 1000 // 默认限制
	}
	if limit > 10000 {
		limit = 10000 // 最大限制，防止一次性加载过多数据
	}

	query := `
		SELECT id, tool_name, arguments, status, result, error, start_time, end_time, duration_ms, COALESCE(owner_user_id, ''), COALESCE(conversation_id, '')
		FROM tool_executions
	`
	args := []interface{}{}
	conditions := []string{}
	if status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, status)
	}
	if toolName != "" {
		// 支持部分匹配（模糊搜索），不区分大小写
		conditions = append(conditions, "LOWER(tool_name) LIKE ?")
		args = append(args, "%"+strings.ToLower(toolName)+"%")
	}
	if len(conditions) > 0 {
		query += ` WHERE ` + conditions[0]
		for i := 1; i < len(conditions); i++ {
			query += ` AND ` + conditions[i]
		}
	}
	query += ` ORDER BY start_time DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []*ToolExecution
	for rows.Next() {
		var exec ToolExecution
		var argsJSON string
		var resultJSON sql.NullString
		var errorText sql.NullString
		var endTime sql.NullTime
		var durationMs sql.NullInt64

		if err := rows.Scan(
			&exec.ID,
			&exec.ToolName,
			&argsJSON,
			&exec.Status,
			&resultJSON,
			&errorText,
			&exec.StartTime,
			&endTime,
			&durationMs,
			&exec.OwnerUserID,
			&exec.ConversationID,
		); err != nil {
			return nil, fmt.Errorf("加载执行记录失败: %w", err)
		}

		// 解析参数
		if err := json.Unmarshal([]byte(argsJSON), &exec.Arguments); err != nil {
			exec.Arguments = make(map[string]interface{})
		}

		// 解析结果
		if resultJSON.Valid && resultJSON.String != "" {
			var result ToolResult
			if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
			} else {
				exec.Result = &result
			}
		}

		// 设置错误
		if errorText.Valid {
			exec.Error = errorText.String
		}

		// 设置结束时间
		if endTime.Valid {
			exec.EndTime = &endTime.Time
		}

		// 设置持续时间
		if durationMs.Valid {
			exec.Duration = time.Duration(durationMs.Int64) * time.Millisecond
		}

		executions = append(executions, &exec)
	}

	return executions, nil
}

func toolExecutionsFilterSQL(status, toolName string) (string, []interface{}) {
	args := []interface{}{}
	conditions := []string{}
	if status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, status)
	}
	if toolName != "" {
		conditions = append(conditions, "LOWER(tool_name) LIKE ?")
		args = append(args, "%"+strings.ToLower(toolName)+"%")
	}
	if len(conditions) == 0 {
		return "", args
	}
	return ` WHERE ` + strings.Join(conditions, ` AND `), args
}

// ToolStatsSummary 工具调用汇总（全量聚合，不含逐工具明细）
type ToolStatsSummary struct {
	TotalCalls   int
	SuccessCalls int
	FailedCalls  int
	BlockedCalls int
	LastCallTime *time.Time
	ToolCount    int
}

// ToolStatsSummaryResult 汇总 + Top N 工具排行
type ToolStatsSummaryResult struct {
	Summary  ToolStatsSummary
	TopTools []*ToolStats
}

// LoadToolStatsSummary 聚合统计信息，仅返回汇总与 Top N 工具（避免全量 map 传输）。
// 监控页的失败口径只包含真实失败/异常终止；用户主动取消的 cancelled 保留在总调用中，不计入失败。
func (m *Monitor) LoadToolStatsSummary(topN int) (*ToolStatsSummaryResult, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("store: monitor requires a database")
	}
	if topN <= 0 {
		topN = 6
	}
	if topN > 100 {
		topN = 100
	}

	result := &ToolStatsSummaryResult{
		TopTools: make([]*ToolStats, 0, topN),
	}

	summaryQuery := `
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('failed', 'hard_timeout', 'orphaned') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'blocked' THEN 1 ELSE 0 END), 0),
			MAX(start_time),
			COUNT(DISTINCT tool_name)
		FROM tool_executions
	`
	var lastCallRaw sql.NullString
	err := m.db.QueryRow(summaryQuery).Scan(
		&result.Summary.TotalCalls,
		&result.Summary.SuccessCalls,
		&result.Summary.FailedCalls,
		&result.Summary.BlockedCalls,
		&lastCallRaw,
		&result.Summary.ToolCount,
	)
	if err != nil {
		return nil, err
	}
	if lastCallRaw.Valid && strings.TrimSpace(lastCallRaw.String) != "" {
		if t, parsed := sqltime.ParseOK(lastCallRaw.String); parsed {
			result.Summary.LastCallTime = &t
		}
	}

	topQuery := `
		SELECT tool_name,
			COUNT(*) AS total_calls,
			SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) AS success_calls,
			SUM(CASE WHEN status IN ('failed', 'hard_timeout', 'orphaned') THEN 1 ELSE 0 END) AS failed_calls,
			SUM(CASE WHEN status = 'blocked' THEN 1 ELSE 0 END) AS blocked_calls,
			MAX(start_time) AS last_call_time
		FROM tool_executions
		GROUP BY tool_name
		ORDER BY total_calls DESC, tool_name ASC
		LIMIT ?
	`
	rows, err := m.db.Query(topQuery, topN)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var stat ToolStats
		var lastCallTime sql.NullString
		if err := rows.Scan(
			&stat.ToolName,
			&stat.TotalCalls,
			&stat.SuccessCalls,
			&stat.FailedCalls,
			&stat.BlockedCalls,
			&lastCallTime,
		); err != nil {
			return nil, fmt.Errorf("加载 Top 工具统计失败: %w", err)
		}
		if lastCallTime.Valid {
			parsed := sqltime.Parse(lastCallTime.String)
			stat.LastCallTime = &parsed
		}
		result.TopTools = append(result.TopTools, &stat)
	}

	return result, nil
}

func (m *Monitor) LoadToolStatsSummaryForAccess(topN int, access Access) (*ToolStatsSummaryResult, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("store: monitor requires a database")
	}
	if access.Scope == ScopeAll {
		return m.LoadToolStatsSummary(topN)
	}
	if topN <= 0 {
		topN = 6
	}
	if topN > 100 {
		topN = 100
	}
	result := &ToolStatsSummaryResult{TopTools: make([]*ToolStats, 0, topN)}
	fromSQL, args := appendToolExecutionAccessSQL(` FROM tool_executions`, nil, access, false)
	var lastCall sql.NullString
	err := m.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status IN ('failed', 'hard_timeout', 'orphaned') THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'blocked' THEN 1 ELSE 0 END), 0),
		MAX(start_time), COUNT(DISTINCT tool_name)`+fromSQL, args...).Scan(
		&result.Summary.TotalCalls, &result.Summary.SuccessCalls, &result.Summary.FailedCalls, &result.Summary.BlockedCalls,
		&lastCall, &result.Summary.ToolCount,
	)
	if err != nil {
		return nil, err
	}
	if lastCall.Valid {
		parsed := sqltime.Parse(lastCall.String)
		result.Summary.LastCallTime = &parsed
	}
	rows, err := m.db.Query(`SELECT tool_name, COUNT(*),
		SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status IN ('failed', 'hard_timeout', 'orphaned') THEN 1 ELSE 0 END),
		SUM(CASE WHEN status = 'blocked' THEN 1 ELSE 0 END), MAX(start_time)`+
		fromSQL+` GROUP BY tool_name ORDER BY COUNT(*) DESC, tool_name ASC LIMIT ?`, append(args, topN)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var stat ToolStats
		var last sql.NullString
		if err := rows.Scan(&stat.ToolName, &stat.TotalCalls, &stat.SuccessCalls, &stat.FailedCalls, &stat.BlockedCalls, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			parsed := sqltime.Parse(last.String)
			stat.LastCallTime = &parsed
		}
		result.TopTools = append(result.TopTools, &stat)
	}
	return result, rows.Err()
}

func (m *Monitor) LoadToolExecutionListPageForAccess(offset, limit int, status, toolName string, access Access) ([]*ToolExecution, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("store: monitor requires a database")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	query := `
		SELECT id, tool_name, status, start_time, end_time, duration_ms, COALESCE(owner_user_id, ''), COALESCE(conversation_id, '')
		FROM tool_executions
	`
	whereSQL, args := toolExecutionsFilterSQL(status, toolName)
	query += whereSQL
	query, args = appendToolExecutionAccessSQL(query, args, access, whereSQL != "")
	query += ` ORDER BY start_time DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	executions := make([]*ToolExecution, 0, limit)
	for rows.Next() {
		var exec ToolExecution
		var endTime sql.NullTime
		var durationMs sql.NullInt64

		if err := rows.Scan(
			&exec.ID,
			&exec.ToolName,
			&exec.Status,
			&exec.StartTime,
			&endTime,
			&durationMs,
			&exec.OwnerUserID,
			&exec.ConversationID,
		); err != nil {
			return nil, fmt.Errorf("加载执行记录列表失败: %w", err)
		}
		if endTime.Valid {
			exec.EndTime = &endTime.Time
		}
		if durationMs.Valid {
			exec.Duration = time.Duration(durationMs.Int64) * time.Millisecond
		}
		executions = append(executions, &exec)
	}

	return executions, nil
}

func appendToolExecutionAccessSQL(query string, args []interface{}, access Access, hasWhere bool) (string, []interface{}) {
	if access.SeeAll() {
		return query, args
	}
	userID := strings.TrimSpace(access.UserID)
	joiner := " WHERE "
	if hasWhere {
		joiner = " AND "
	}
	if userID == "" {
		return query + joiner + "1=0", args
	}
	// 四个会话分支只有一份拼写（store/access.go 的 ConversationVisibilityClause）：
	// 这里只是把它放进 "本次执行的 owner 或它所在会话可见" 这个 OR 里。
	clause, clauseArgs := ConversationVisibilityClause("tool_executions.conversation_id", access)
	query += joiner + `(owner_user_id = ? OR (` + clause + `))`
	args = append(args, userID)
	args = append(args, clauseArgs...)
	return query, args
}

// GetToolExecution 根据ID获取单条工具执行记录
func (m *Monitor) GetToolExecution(id string) (*ToolExecution, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("store: monitor requires a database")
	}
	query := `
		SELECT id, tool_name, arguments, status, result, error, start_time, end_time, duration_ms,
		       COALESCE(partial_output, ''), COALESCE(partial_output_bytes, 0), COALESCE(partial_output_truncated, 0), partial_output_updated_at,
		       COALESCE(owner_user_id, ''), COALESCE(conversation_id, '')
		FROM tool_executions
		WHERE id = ?
	`

	row := m.db.QueryRow(query, id)

	var exec ToolExecution
	var argsJSON string
	var resultJSON sql.NullString
	var errorText sql.NullString
	var endTime sql.NullTime
	var durationMs sql.NullInt64
	var partialTruncated int
	var partialUpdatedAt sql.NullTime

	err := row.Scan(
		&exec.ID,
		&exec.ToolName,
		&argsJSON,
		&exec.Status,
		&resultJSON,
		&errorText,
		&exec.StartTime,
		&endTime,
		&durationMs,
		&exec.PartialOutput,
		&exec.PartialOutputBytes,
		&partialTruncated,
		&partialUpdatedAt,
		&exec.OwnerUserID,
		&exec.ConversationID,
	)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(argsJSON), &exec.Arguments); err != nil {
		exec.Arguments = make(map[string]interface{})
	}

	if resultJSON.Valid && resultJSON.String != "" {
		var result ToolResult
		if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
		} else {
			exec.Result = &result
		}
	}

	if errorText.Valid {
		exec.Error = errorText.String
	}

	if endTime.Valid {
		exec.EndTime = &endTime.Time
	}

	if durationMs.Valid {
		exec.Duration = time.Duration(durationMs.Int64) * time.Millisecond
	}
	exec.PartialOutputTruncated = partialTruncated != 0
	if partialUpdatedAt.Valid {
		exec.PartialOutputUpdatedAt = &partialUpdatedAt.Time
	}

	return &exec, nil
}

// UserCanAccessToolExecution enforces ownership for monitor detail and mutation
// endpoints. Legacy records without an owner or conversation fail closed for
// non-global users.
func (m *Monitor) UserCanAccessToolExecution(userID, scope, executionID string) bool {
	if m == nil || m.db == nil {
		return false
	}
	if m == nil || m.db == nil {
		return false
	}
	userID = strings.TrimSpace(userID)
	executionID = strings.TrimSpace(executionID)
	if userID == "" || executionID == "" {
		return false
	}
	if scope == ScopeAll {
		return true
	}
	var ownerUserID, conversationID sql.NullString
	if err := m.db.QueryRow(`SELECT owner_user_id, conversation_id FROM tool_executions WHERE id = ?`, executionID).Scan(&ownerUserID, &conversationID); err != nil {
		return false
	}
	if strings.TrimSpace(ownerUserID.String) == userID {
		return true
	}
	conversation := strings.TrimSpace(conversationID.String)
	if conversation == "" || m.access == nil {
		return false
	}
	return m.access(userID, scope, "conversation", conversation)
}

// CancelOrphanedRunningToolExecutions 将仍为 running 的记录批量标记为 orphaned（如进程重启后无对应执行协程）。
func (m *Monitor) CancelOrphanedRunningToolExecutions(endTime time.Time, errMsg string) (int64, error) {
	if m == nil || m.db == nil {
		return 0, errors.New("store: monitor requires a database")
	}
	errMsg = strings.TrimSpace(errMsg)
	if errMsg == "" {
		errMsg = "执行已中断（服务重启或会话结束）"
	}
	query := `
		UPDATE tool_executions
		SET status = 'orphaned',
		    error = ?,
		    end_time = ?,
		    duration_ms = MAX(0, CAST((julianday(?) - julianday(start_time)) * 86400000 AS INTEGER))
		WHERE status = 'running'
	`
	res, err := m.db.Exec(query, errMsg, endTime, endTime)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FinalizeStaleRunningToolExecutions 将「非活跃且超过 minAge」的 running 记录标记为 orphaned。
// activeIDs 为当前进程内仍登记 cancel 的 executionId；不在集合内且已超时的视为孤儿记录。
func (m *Monitor) FinalizeStaleRunningToolExecutions(endTime time.Time, minAge time.Duration, activeIDs map[string]struct{}, errMsg string) (int64, error) {
	if m == nil || m.db == nil {
		return 0, errors.New("store: monitor requires a database")
	}
	errMsg = strings.TrimSpace(errMsg)
	if errMsg == "" {
		errMsg = "执行已中断（会话已结束）"
	}
	if minAge < 0 {
		minAge = 0
	}
	cutoff := endTime.Add(-minAge)
	rows, err := m.db.Query(`
		SELECT id, start_time FROM tool_executions
		WHERE status = 'running' AND start_time <= ?
	`, cutoff)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type staleRow struct {
		id        string
		startTime time.Time
	}
	var stale []staleRow
	for rows.Next() {
		var row staleRow
		if err := rows.Scan(&row.id, &row.startTime); err != nil {
			return 0, fmt.Errorf("读取 stale running 执行记录失败: %w", err)
		}
		if activeIDs != nil {
			if _, active := activeIDs[row.id]; active {
				continue
			}
		}
		stale = append(stale, row)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(stale) == 0 {
		return 0, nil
	}

	var affected int64
	for _, row := range stale {
		durationMs := endTime.Sub(row.startTime).Milliseconds()
		if durationMs < 0 {
			durationMs = 0
		}
		res, err := m.db.Exec(`
			UPDATE tool_executions
			SET status = 'orphaned', error = ?, end_time = ?, duration_ms = ?
			WHERE id = ? AND status = 'running'
		`, errMsg, endTime, durationMs, row.id)
		if err != nil {
			continue
		}
		n, _ := res.RowsAffected()
		affected += n
	}
	return affected, nil
}

// DeleteToolExecution 删除工具执行记录
func (m *Monitor) DeleteToolExecution(id string) error {
	if m == nil || m.db == nil {
		return errors.New("store: monitor requires a database")
	}
	query := `DELETE FROM tool_executions WHERE id = ?`
	_, err := m.db.Exec(query, id)
	if err != nil {
		return err
	}
	return nil
}

// DeleteToolExecutions 批量删除工具执行记录
func (m *Monitor) DeleteToolExecutions(ids []string) error {
	if m == nil || m.db == nil {
		return errors.New("store: monitor requires a database")
	}
	if len(ids) == 0 {
		return nil
	}

	// 构建 IN 查询的占位符
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `DELETE FROM tool_executions WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	_, err := m.db.Exec(query, args...)
	if err != nil {
		return err
	}
	return nil
}

// GetToolExecutionsByIds 根据ID列表获取工具执行记录（用于批量删除前获取统计信息）
func (m *Monitor) GetToolExecutionsByIds(ids []string) ([]*ToolExecution, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("store: monitor requires a database")
	}
	if len(ids) == 0 {
		return []*ToolExecution{}, nil
	}

	// 构建 IN 查询的占位符
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `
		SELECT id, tool_name, arguments, status, result, error, start_time, end_time, duration_ms, COALESCE(owner_user_id, ''), COALESCE(conversation_id, '')
		FROM tool_executions
		WHERE id IN (` + strings.Join(placeholders, ",") + `)
	`

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []*ToolExecution
	for rows.Next() {
		var exec ToolExecution
		var argsJSON string
		var resultJSON sql.NullString
		var errorText sql.NullString
		var endTime sql.NullTime
		var durationMs sql.NullInt64

		if err := rows.Scan(
			&exec.ID,
			&exec.ToolName,
			&argsJSON,
			&exec.Status,
			&resultJSON,
			&errorText,
			&exec.StartTime,
			&endTime,
			&durationMs,
			&exec.OwnerUserID,
			&exec.ConversationID,
		); err != nil {
			return nil, fmt.Errorf("加载执行记录失败: %w", err)
		}

		// 解析参数
		if err := json.Unmarshal([]byte(argsJSON), &exec.Arguments); err != nil {
			exec.Arguments = make(map[string]interface{})
		}

		// 解析结果
		if resultJSON.Valid && resultJSON.String != "" {
			var result ToolResult
			if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
			} else {
				exec.Result = &result
			}
		}

		// 设置错误
		if errorText.Valid {
			exec.Error = errorText.String
		}

		// 设置结束时间
		if endTime.Valid {
			exec.EndTime = &endTime.Time
		}

		// 设置持续时间
		if durationMs.Valid {
			exec.Duration = time.Duration(durationMs.Int64) * time.Millisecond
		}

		executions = append(executions, &exec)
	}

	return executions, nil
}

type toolExecutionStatDelta struct {
	totalCalls   int
	successCalls int
	failedCalls  int
}

// PurgeToolExecutionsBefore deletes executions older than cutoff and adjusts tool_stats.
func (m *Monitor) PurgeToolExecutionsBefore(cutoff time.Time) (int64, error) {
	if m == nil || m.db == nil {
		return 0, errors.New("store: monitor requires a database")
	}
	query := `
		SELECT tool_name, status, COUNT(*) AS cnt
		FROM tool_executions
		WHERE ` + sqltime.Compare("start_time", "<") + `
		GROUP BY tool_name, status
	`
	rows, err := m.db.Query(query, sqltime.UTC(cutoff))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	deltas := make(map[string]*toolExecutionStatDelta)
	for rows.Next() {
		var toolName, status string
		var count int
		if err := rows.Scan(&toolName, &status, &count); err != nil {
			return 0, fmt.Errorf("读取待清理执行记录统计失败: %w", err)
		}
		toolName = strings.TrimSpace(toolName)
		if toolName == "" || count <= 0 {
			continue
		}
		delta := deltas[toolName]
		if delta == nil {
			delta = &toolExecutionStatDelta{}
			deltas[toolName] = delta
		}
		delta.totalCalls += count
		switch status {
		case "failed", "hard_timeout", "orphaned":
			delta.failedCalls += count
		case "completed":
			delta.successCalls += count
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	res, err := m.db.Exec(`DELETE FROM tool_executions WHERE `+sqltime.Compare("start_time", "<"), sqltime.UTC(cutoff))
	if err != nil {
		return 0, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	for toolName, delta := range deltas {
		if err := m.DecreaseToolStats(toolName, delta.totalCalls, delta.successCalls, delta.failedCalls); err != nil {
		}
	}

	return deleted, nil
}

// LoadToolStats 加载所有工具统计信息
func (m *Monitor) LoadToolStats() (map[string]*ToolStats, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("store: monitor requires a database")
	}
	query := `
		SELECT stats.tool_name, total_calls, success_calls, failed_calls, last_call_time,
			COALESCE(blocked.calls, 0)
		FROM tool_stats stats
		LEFT JOIN (SELECT tool_name, COUNT(*) AS calls FROM tool_executions WHERE status = 'blocked' GROUP BY tool_name) blocked
		ON blocked.tool_name = stats.tool_name
	`

	rows, err := m.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make(map[string]*ToolStats)
	for rows.Next() {
		var stat ToolStats
		var lastCallTime sql.NullTime

		if err := rows.Scan(
			&stat.ToolName,
			&stat.TotalCalls,
			&stat.SuccessCalls,
			&stat.FailedCalls,
			&lastCallTime,
			&stat.BlockedCalls,
		); err != nil {
			return nil, fmt.Errorf("加载统计信息失败: %w", err)
		}

		if lastCallTime.Valid {
			stat.LastCallTime = &lastCallTime.Time
		}

		stats[stat.ToolName] = &stat
	}

	return stats, nil
}

// UpdateToolStats 更新工具统计信息（累加模式）
func (m *Monitor) UpdateToolStats(toolName string, totalCalls, successCalls, failedCalls int, lastCallTime *time.Time) error {
	if m == nil || m.db == nil {
		return errors.New("store: monitor requires a database")
	}
	var lastCallTimeSQL sql.NullTime
	if lastCallTime != nil {
		lastCallTimeSQL = sql.NullTime{Time: *lastCallTime, Valid: true}
	}

	query := `
		INSERT INTO tool_stats (tool_name, total_calls, success_calls, failed_calls, last_call_time, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(tool_name) DO UPDATE SET
			total_calls = total_calls + ?,
			success_calls = success_calls + ?,
			failed_calls = failed_calls + ?,
			last_call_time = COALESCE(?, last_call_time),
			updated_at = ?
	`

	_, err := m.db.Exec(query,
		toolName, totalCalls, successCalls, failedCalls, lastCallTimeSQL, time.Now(),
		totalCalls, successCalls, failedCalls, lastCallTimeSQL, time.Now(),
	)

	if err != nil {
		return err
	}

	return nil
}

// CallsTimelineBucket 调用趋势时间桶
type CallsTimelineBucket struct {
	BucketTime time.Time
	Total      int
	Failed     int
	Blocked    int
}

// truncateCallsTimelineBucket 将时间截断到趋势图桶边界（本地时区，与 handler 侧 truncateToBucket 一致）
func truncateCallsTimelineBucket(t time.Time, dailyBuckets bool) time.Time {
	t = t.In(time.Local)
	if dailyBuckets {
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	}
	return t.Truncate(time.Hour)
}

// LoadCallsTimeline 按时间范围加载调用趋势（since 起至今，含边界）
func (m *Monitor) LoadCallsTimeline(since time.Time, dailyBuckets bool) ([]CallsTimelineBucket, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("store: monitor requires a database")
	}
	var query string
	if dailyBuckets {
		query = `
			SELECT date(start_time, 'localtime') AS bucket,
				COUNT(*) AS total,
				SUM(CASE WHEN status IN ('failed', 'hard_timeout', 'orphaned') THEN 1 ELSE 0 END) AS failed,
				SUM(CASE WHEN status = 'blocked' THEN 1 ELSE 0 END) AS blocked
			FROM tool_executions
			WHERE start_time >= ?
			GROUP BY bucket
			ORDER BY bucket
		`
	} else {
		query = `
			SELECT strftime('%Y-%m-%d %H:00:00', start_time, 'localtime') AS bucket,
				COUNT(*) AS total,
				SUM(CASE WHEN status IN ('failed', 'hard_timeout', 'orphaned') THEN 1 ELSE 0 END) AS failed,
				SUM(CASE WHEN status = 'blocked' THEN 1 ELSE 0 END) AS blocked
			FROM tool_executions
			WHERE start_time >= ?
			GROUP BY bucket
			ORDER BY bucket
		`
	}

	rows, err := m.db.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := make([]CallsTimelineBucket, 0)
	for rows.Next() {
		var bucketStr string
		var total, failed, blocked int
		if err := rows.Scan(&bucketStr, &total, &failed, &blocked); err != nil {
			return nil, fmt.Errorf("加载调用趋势失败: %w", err)
		}
		bucketTime, err := parseCallsTimelineBucket(bucketStr, dailyBuckets)
		if err != nil {
			continue
		}
		buckets = append(buckets, CallsTimelineBucket{
			BucketTime: bucketTime,
			Total:      total,
			Failed:     failed,
			Blocked:    blocked,
		})
	}
	return buckets, nil
}

func parseCallsTimelineBucket(bucketStr string, dailyBuckets bool) (time.Time, error) {
	if dailyBuckets {
		return time.ParseInLocation("2006-01-02", bucketStr, time.Local)
	}
	return time.ParseInLocation("2006-01-02 15:04:05", bucketStr, time.Local)
}

// DecreaseToolStats 减少工具统计信息（用于删除执行记录时）
// 如果统计信息变为0，则删除该统计记录
func (m *Monitor) DecreaseToolStats(toolName string, totalCalls, successCalls, failedCalls int) error {
	if m == nil || m.db == nil {
		return errors.New("store: monitor requires a database")
	}
	// 先更新统计信息
	query := `
		UPDATE tool_stats SET
			total_calls = CASE WHEN total_calls - ? < 0 THEN 0 ELSE total_calls - ? END,
			success_calls = CASE WHEN success_calls - ? < 0 THEN 0 ELSE success_calls - ? END,
			failed_calls = CASE WHEN failed_calls - ? < 0 THEN 0 ELSE failed_calls - ? END,
			updated_at = ?
		WHERE tool_name = ?
	`

	_, err := m.db.Exec(query, totalCalls, totalCalls, successCalls, successCalls, failedCalls, failedCalls, time.Now(), toolName)
	if err != nil {
		return err
	}

	// 检查更新后的 total_calls 是否为 0，如果是则删除该统计记录
	checkQuery := `SELECT total_calls FROM tool_stats WHERE tool_name = ?`
	var newTotalCalls int
	err = m.db.QueryRow(checkQuery, toolName).Scan(&newTotalCalls)
	if err != nil {
		// 如果查询失败（记录不存在），直接返回
		return nil
	}

	// 如果 total_calls 为 0，删除该统计记录
	if newTotalCalls == 0 {
		deleteQuery := `DELETE FROM tool_stats WHERE tool_name = ?`
		_, err = m.db.Exec(deleteQuery, toolName)
		if err != nil {
			// 不返回错误，因为主要操作（更新统计）已成功
		} else {
		}
	}

	return nil
}
