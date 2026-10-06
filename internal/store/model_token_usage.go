package store

import (
	"cyberstrike-ai/internal/sqltime"
	"database/sql"

	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ModelTokenUsage owns model_token_usage: one row per model-usage summary emitted by an agent run,
// the structured counterpart to the eino_usage_summary process detail.
//
// The row is written from the timeline write path (a process detail arrives, its payload carries the
// counters), and read by the usage dashboard. Both of those reach two neighbouring tables on purpose:
// project_id is copied from the conversation the run belongs to, and the backfill walks process_details
// to make rows written before this table existed queryable. The JOIN against conversations is also what
// hides usage whose conversation has since been deleted.
type ModelTokenUsage struct {
	db *sql.DB
}

// NewModelTokenUsage binds the store to a connection.
func NewModelTokenUsage(db *sql.DB) *ModelTokenUsage {
	return &ModelTokenUsage{db: db}
}

func (m *ModelTokenUsage) requireDB() error {
	if m == nil || m.db == nil {
		return errors.New("store: model token usage requires a database")
	}
	return nil
}

// UsageEventType is the process-detail event whose payload carries a usage summary.
const UsageEventType = "eino_usage_summary"

// ProjectUnbound filters to conversations that have no project. The sentinel is the value the
// console sends, not a name to look up.
const ProjectUnbound = "__none__"

// TokenUsage is one row. ProjectID is the copy taken from the conversation at write time, which is why a
// row survives being read after its project is unlinked: the column is then NULL and reads as "".
type TokenUsage struct {
	ID               string    `json:"id"`
	ProcessDetailID  string    `json:"processDetailId"`
	MessageID        string    `json:"messageId"`
	ConversationID   string    `json:"conversationId"`
	ProjectID        string    `json:"projectId,omitempty"`
	Source           string    `json:"source"`
	Orchestration    string    `json:"orchestration"`
	Reason           string    `json:"reason"`
	Model            string    `json:"model,omitempty"`
	ModelCalls       int64     `json:"modelCalls"`
	PromptTokens     int64     `json:"promptTokens"`
	CompletionTokens int64     `json:"completionTokens"`
	TotalTokens      int64     `json:"totalTokens"`
	CachedTokens     int64     `json:"cachedTokens"`
	ReasoningTokens  int64     `json:"reasoningTokens"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// TokenUsageSummary is the aggregate shape used by dashboard and APIs.
type TokenUsageSummary struct {
	Events           int64 `json:"events"`
	ModelCalls       int64 `json:"modelCalls"`
	PromptTokens     int64 `json:"promptTokens"`
	CompletionTokens int64 `json:"completionTokens"`
	TotalTokens      int64 `json:"totalTokens"`
	CachedTokens     int64 `json:"cachedTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens"`
}

// TokenUsageBreakdown is one grouped aggregate row. Key is what the group was matched on, Label is what the
// console prints; they differ only for the fallback ("unknown" / "Unknown").
type TokenUsageBreakdown struct {
	Key              string `json:"key"`
	Label            string `json:"label,omitempty"`
	Events           int64  `json:"events"`
	ModelCalls       int64  `json:"modelCalls"`
	PromptTokens     int64  `json:"promptTokens"`
	CompletionTokens int64  `json:"completionTokens"`
	TotalTokens      int64  `json:"totalTokens"`
	CachedTokens     int64  `json:"cachedTokens"`
	ReasoningTokens  int64  `json:"reasoningTokens"`
}

// TokenUsageStats is the compact API response for usage dashboards.
type TokenUsageStats struct {
	Summary         TokenUsageSummary     `json:"summary"`
	Today           TokenUsageSummary     `json:"today"`
	ByDay           []TokenUsageBreakdown `json:"byDay"`
	ByModel         []TokenUsageBreakdown `json:"byModel"`
	ByOrchestration []TokenUsageBreakdown `json:"byOrchestration"`
	Recent          []TokenUsage          `json:"recent"`
}

// TokenUsageFilter scopes usage queries. Access is the caller's reach, not a filter they chose: an access with
// no user id constrains the query to nothing rather than to everything.
type TokenUsageFilter struct {
	ConversationID string
	ProjectID      string
	Since          time.Time
	Until          time.Time
	Days           int
	Access         Access
	Limit          int
}

const modelTokenUsageSchema = `
	CREATE TABLE IF NOT EXISTS model_token_usage (
		id TEXT PRIMARY KEY,
		process_detail_id TEXT NOT NULL UNIQUE,
		message_id TEXT NOT NULL,
		conversation_id TEXT NOT NULL,
		project_id TEXT,
		source TEXT NOT NULL DEFAULT '',
		orchestration TEXT NOT NULL DEFAULT '',
		reason TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		model_calls INTEGER NOT NULL DEFAULT 0,
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		reasoning_tokens INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (process_detail_id) REFERENCES process_details(id) ON DELETE CASCADE,
		FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
		FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE SET NULL
	);
	CREATE INDEX IF NOT EXISTS idx_model_token_usage_created_at ON model_token_usage(created_at);
	CREATE INDEX IF NOT EXISTS idx_model_token_usage_conversation ON model_token_usage(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_model_token_usage_project ON model_token_usage(project_id);
	CREATE INDEX IF NOT EXISTS idx_model_token_usage_model ON model_token_usage(model);
`

// EnsureSchema creates the table and its four indexes. Idempotent, and it must run before the
// timeline path can record usage.
func (m *ModelTokenUsage) EnsureSchema() error {
	if err := m.requireDB(); err != nil {
		return err
	}
	if _, err := m.db.Exec(modelTokenUsageSchema); err != nil {
		return fmt.Errorf("创建model_token_usage表失败: %w", err)
	}
	return nil
}

// usageFromProcessDetail turns one timeline payload into a row. It returns false for anything that is
// not a usage summary: no counters, no conversation/message/process-detail identity it can be tied to.
func usageFromProcessDetail(messageID, conversationID, processDetailID string, data any) (TokenUsage, bool) {
	m := mapFromJSONish(data)
	if len(m) == 0 {
		return TokenUsage{}, false
	}
	usage := TokenUsage{
		ID:               uuid.New().String(),
		ProcessDetailID:  strings.TrimSpace(processDetailID),
		MessageID:        strings.TrimSpace(messageID),
		ConversationID:   strings.TrimSpace(conversationID),
		Source:           textField(m, "source"),
		Orchestration:    textField(m, "orchestration"),
		Reason:           textField(m, "reason"),
		Model:            textField(m, "model"),
		ModelCalls:       asInt64(m["modelCalls"]),
		PromptTokens:     asInt64(m["promptTokens"]),
		CompletionTokens: asInt64(m["completionTokens"]),
		TotalTokens:      asInt64(m["totalTokens"]),
		CachedTokens:     asInt64(m["cachedTokens"]),
		ReasoningTokens:  asInt64(m["reasoningTokens"]),
	}
	if usage.TotalTokens == 0 && (usage.PromptTokens > 0 || usage.CompletionTokens > 0) {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if usage.ProcessDetailID == "" || usage.MessageID == "" || usage.ConversationID == "" {
		return TokenUsage{}, false
	}
	if usage.ModelCalls == 0 && usage.TotalTokens == 0 && usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.CachedTokens == 0 && usage.ReasoningTokens == 0 {
		return TokenUsage{}, false
	}
	return usage, true
}

// textField reads one string field of a decoded payload. A key the emitter left out is an empty
// string: fmt.Sprint(nil) gives the four letters "<nil>", which is what earlier builds stored, and a
// column holding it looks like a model name to every grouping that tries to fall back to "unknown".
func textField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// mapFromJSONish accepts the three shapes a process-detail payload arrives in: already-decoded,
// a JSON string, or raw bytes. Anything else is round-tripped through JSON, which is how the
// eino usage structs get in.
func mapFromJSONish(data any) map[string]any {
	switch v := data.(type) {
	case nil:
		return nil
	case map[string]any:
		return v
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(v), &m); err == nil {
			return m
		}
	case []byte:
		var m map[string]any
		if err := json.Unmarshal(v, &m); err == nil {
			return m
		}
	default:
		raw, err := json.Marshal(v)
		if err == nil {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err == nil {
				return m
			}
		}
	}
	return nil
}

// asInt64 reads a counter out of decoded JSON, where the same number can be a float64, a
// json.Number or a string depending on who wrote the payload.
func asInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int8:
		return int64(n)
	case int16:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint:
		return int64(n)
	case uint8:
		return int64(n)
	case uint16:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		if n > math.MaxInt64 {
			return math.MaxInt64
		}
		return int64(n)
	case float32:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i
	default:
		i, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(v)), 10, 64)
		return i
	}
}

// RecordFromProcessDetail is the write path's hook: it ignores every event type that is not a usage
// summary and every payload that carries no counters, so the timeline writer can call it for
// everything it stores.
func (m *ModelTokenUsage) RecordFromProcessDetail(messageID, conversationID, processDetailID, eventType string, data any) error {
	if eventType != UsageEventType {
		return nil
	}
	usage, ok := usageFromProcessDetail(messageID, conversationID, processDetailID, data)
	if !ok {
		return nil
	}
	return m.Upsert(usage)
}

// Upsert persists one usage row, idempotent on process_detail_id: the same timeline event replayed
// updates the counters instead of adding a second row, which is what makes a re-indexed run's
// dashboard add up once.
func (m *ModelTokenUsage) Upsert(usage TokenUsage) error {
	if err := m.requireDB(); err != nil {
		return err
	}
	now := time.Now()
	createdAt := usage.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	if usage.ID == "" {
		usage.ID = uuid.New().String()
	}
	// project_id is copied from the conversation, and a conversation with no project stores NULL:
	// the dashboard's project column then reads as empty rather than as "none of them".
	var projectID sql.NullString
	err := m.db.QueryRow(`SELECT project_id FROM conversations WHERE id = ?`, usage.ConversationID).Scan(&projectID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("查询对话项目失败: %w", err)
	}
	projectValue := any(nil)
	if projectID.Valid && strings.TrimSpace(projectID.String) != "" {
		projectValue = strings.TrimSpace(projectID.String)
	}
	_, err = m.db.Exec(`
INSERT INTO model_token_usage (
	id, process_detail_id, message_id, conversation_id, project_id,
	source, orchestration, reason, model, model_calls,
	prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens,
	created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(process_detail_id) DO UPDATE SET
	message_id = excluded.message_id,
	conversation_id = excluded.conversation_id,
	project_id = excluded.project_id,
	source = excluded.source,
	orchestration = excluded.orchestration,
	reason = excluded.reason,
	model = excluded.model,
	model_calls = excluded.model_calls,
	prompt_tokens = excluded.prompt_tokens,
	completion_tokens = excluded.completion_tokens,
	total_tokens = excluded.total_tokens,
	cached_tokens = excluded.cached_tokens,
	reasoning_tokens = excluded.reasoning_tokens,
	created_at = excluded.created_at,
	updated_at = excluded.updated_at`,
		usage.ID, usage.ProcessDetailID, usage.MessageID, usage.ConversationID, projectValue,
		usage.Source, usage.Orchestration, usage.Reason, usage.Model, usage.ModelCalls,
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.CachedTokens, usage.ReasoningTokens,
		createdAt, now,
	)
	if err != nil {
		return fmt.Errorf("写入模型Token用量失败: %w", err)
	}
	return nil
}

// BackfillFromProcessDetails makes usage events written before this table existed queryable. It reads
// the timeline table on purpose - the row it writes belongs to this table, and process_details is only
// the source. Re-running it changes nothing: a detail already carried over is skipped, and one whose
// payload was rewritten is upserted under the same id.
func (m *ModelTokenUsage) BackfillFromProcessDetails() error {
	if err := m.requireDB(); err != nil {
		return nil
	}
	rows, err := m.db.Query(`
SELECT pd.id, pd.message_id, pd.conversation_id, pd.data, pd.created_at
FROM process_details pd
LEFT JOIN model_token_usage mtu ON mtu.process_detail_id = pd.id
WHERE pd.event_type = ?
	AND (mtu.id IS NULL OR mtu.created_at != pd.created_at)`, UsageEventType)
	if err != nil {
		return fmt.Errorf("查询历史模型Token用量失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var processDetailID, messageID, conversationID string
		var data sql.NullString
		var createdAt string
		if err := rows.Scan(&processDetailID, &messageID, &conversationID, &data, &createdAt); err != nil {
			return fmt.Errorf("扫描历史模型Token用量失败: %w", err)
		}
		if !data.Valid {
			continue
		}
		usage, ok := usageFromProcessDetail(messageID, conversationID, processDetailID, data.String)
		if !ok {
			continue
		}
		usage.CreatedAt = parseUsageTime(createdAt)
		if err := m.Upsert(usage); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历历史模型Token用量失败: %w", err)
	}
	return nil
}

const usageFrom = ` FROM model_token_usage mtu JOIN conversations c ON c.id = mtu.conversation_id`

func summarySelect(alias string) string {
	p := ""
	if alias != "" {
		p = alias + "."
	}
	return fmt.Sprintf(`COUNT(%sid),
COALESCE(SUM(%smodel_calls), 0),
COALESCE(SUM(%sprompt_tokens), 0),
COALESCE(SUM(%scompletion_tokens), 0),
COALESCE(SUM(%stotal_tokens), 0),
COALESCE(SUM(%scached_tokens), 0),
COALESCE(SUM(%sreasoning_tokens), 0)`, p, p, p, p, p, p, p)
}

// where builds the shared predicate for every read on this table. The conversation JOIN above is what
// the access clause and the project filter both resolve through, and it is also what drops usage whose
// conversation is gone.
func (f TokenUsageFilter) where() (string, []any) {
	where := " WHERE 1=1"
	var args []any
	if cid := strings.TrimSpace(f.ConversationID); cid != "" {
		where += " AND mtu.conversation_id = ?"
		args = append(args, cid)
	}
	if pid := strings.TrimSpace(f.ProjectID); pid != "" {
		if pid == ProjectUnbound {
			where += ` AND (mtu.project_id IS NULL OR TRIM(COALESCE(mtu.project_id, '')) = '')`
		} else {
			where += " AND mtu.project_id = ?"
			args = append(args, pid)
		}
	}
	if !f.Since.IsZero() {
		where += " AND mtu.created_at >= ?"
		args = append(args, f.Since)
	}
	if !f.Until.IsZero() {
		where += " AND mtu.created_at <= ?"
		args = append(args, f.Until)
	}
	// The column is this table's own conversation id, not the joined row's: the clause correlates its
	// subqueries on the value passed here, and handing it `c.id` would let its own alias answer to it.
	// The JOIN stays because it is what hides usage whose conversation is gone - including for a
	// caller whose scope is unrestricted, where the clause adds nothing.
	return ConstrainConversation(where, args, "mtu.conversation_id", f.Access)
}

func (m *ModelTokenUsage) querySummary(query string, args ...any) (TokenUsageSummary, error) {
	var s TokenUsageSummary
	err := m.db.QueryRow(query, args...).Scan(
		&s.Events, &s.ModelCalls, &s.PromptTokens, &s.CompletionTokens,
		&s.TotalTokens, &s.CachedTokens, &s.ReasoningTokens,
	)
	if err != nil {
		return s, fmt.Errorf("查询模型Token用量汇总失败: %w", err)
	}
	return s, nil
}

func (m *ModelTokenUsage) queryBreakdown(query string, args ...any) ([]TokenUsageBreakdown, error) {
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询模型Token用量分组失败: %w", err)
	}
	defer rows.Close()
	out := []TokenUsageBreakdown{}
	for rows.Next() {
		var row TokenUsageBreakdown
		if err := rows.Scan(
			&row.Key, &row.Label, &row.Events, &row.ModelCalls, &row.PromptTokens,
			&row.CompletionTokens, &row.TotalTokens, &row.CachedTokens, &row.ReasoningTokens,
		); err != nil {
			return nil, fmt.Errorf("扫描模型Token用量分组失败: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历模型Token用量分组失败: %w", err)
	}
	return out, nil
}

// Stats answers the usage dashboard in one call: totals, today's totals, three groupings and the
// recent rows. Days and Limit fall back to 7 and 10 the way the API has always done it.
func (m *ModelTokenUsage) Stats(filter TokenUsageFilter) (*TokenUsageStats, error) {
	if err := m.requireDB(); err != nil {
		return nil, err
	}
	if filter.Days <= 0 {
		filter.Days = 7
	}
	if filter.Limit <= 0 {
		filter.Limit = 10
	}
	where, args := filter.where()
	summary, err := m.querySummary("SELECT "+summarySelect("mtu")+usageFrom+where, args...)
	if err != nil {
		return nil, err
	}
	todayFilter := filter
	now := time.Now()
	todayFilter.Since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	todayWhere, todayArgs := todayFilter.where()
	today, err := m.querySummary("SELECT "+summarySelect("mtu")+usageFrom+todayWhere, todayArgs...)
	if err != nil {
		return nil, err
	}
	byDay, err := m.queryBreakdown(
		"SELECT date(mtu.created_at) AS k, date(mtu.created_at) AS label, "+summarySelect("mtu")+usageFrom+where+" GROUP BY date(mtu.created_at) ORDER BY k DESC LIMIT ?",
		append(args, filter.Days)...,
	)
	if err != nil {
		return nil, err
	}
	byModel, err := m.queryBreakdown(
		"SELECT COALESCE(NULLIF(TRIM(mtu.model), ''), 'unknown') AS k, COALESCE(NULLIF(TRIM(mtu.model), ''), 'Unknown') AS label, "+summarySelect("mtu")+usageFrom+where+" GROUP BY k ORDER BY SUM(mtu.total_tokens) DESC LIMIT ?",
		append(args, filter.Limit)...,
	)
	if err != nil {
		return nil, err
	}
	byOrch, err := m.queryBreakdown(
		"SELECT COALESCE(NULLIF(TRIM(mtu.orchestration), ''), 'unknown') AS k, COALESCE(NULLIF(TRIM(mtu.orchestration), ''), 'Unknown') AS label, "+summarySelect("mtu")+usageFrom+where+" GROUP BY k ORDER BY SUM(mtu.total_tokens) DESC LIMIT ?",
		append(args, filter.Limit)...,
	)
	if err != nil {
		return nil, err
	}
	recent, err := m.List(filter)
	if err != nil {
		return nil, err
	}
	return &TokenUsageStats{
		Summary:         summary,
		Today:           today,
		ByDay:           byDay,
		ByModel:         byModel,
		ByOrchestration: byOrch,
		Recent:          recent,
	}, nil
}

// List returns the recent rows, newest first. Limit is clamped to 500 because this is a dashboard
// feed, not an export.
func (m *ModelTokenUsage) List(filter TokenUsageFilter) ([]TokenUsage, error) {
	if err := m.requireDB(); err != nil {
		return nil, err
	}
	if filter.Limit <= 0 {
		filter.Limit = 20
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}
	where, args := filter.where()
	args = append(args, filter.Limit)
	rows, err := m.db.Query(`
SELECT mtu.id, mtu.process_detail_id, mtu.message_id, mtu.conversation_id,
	COALESCE(mtu.project_id, ''), mtu.source, mtu.orchestration, mtu.reason, mtu.model,
	mtu.model_calls, mtu.prompt_tokens, mtu.completion_tokens, mtu.total_tokens,
	mtu.cached_tokens, mtu.reasoning_tokens, mtu.created_at, mtu.updated_at
FROM model_token_usage mtu
JOIN conversations c ON c.id = mtu.conversation_id`+where+`
ORDER BY mtu.created_at DESC, mtu.rowid DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("查询模型Token用量明细失败: %w", err)
	}
	defer rows.Close()
	out := []TokenUsage{}
	for rows.Next() {
		var u TokenUsage
		var createdAt, updatedAt string
		if err := rows.Scan(
			&u.ID, &u.ProcessDetailID, &u.MessageID, &u.ConversationID, &u.ProjectID,
			&u.Source, &u.Orchestration, &u.Reason, &u.Model, &u.ModelCalls,
			&u.PromptTokens, &u.CompletionTokens, &u.TotalTokens, &u.CachedTokens,
			&u.ReasoningTokens, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("扫描模型Token用量明细失败: %w", err)
		}
		u.CreatedAt = parseUsageTime(createdAt)
		u.UpdatedAt = parseUsageTime(updatedAt)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历模型Token用量明细失败: %w", err)
	}
	return out, nil
}

// parseUsageTime reads the two instant columns of this table. A value in none of the accepted forms
// reads as the zero time, which the dashboard shows as "today" rather than dropping the row - the same
// reading the API has always returned, and the reason the accepted forms live in internal/sqltime
// rather than beside the query that happens to need them.
func parseUsageTime(s string) time.Time {
	return sqltime.Parse(s)
}
