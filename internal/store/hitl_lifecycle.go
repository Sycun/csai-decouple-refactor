package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// EnsureSchema creates the two HITL tables, adds the columns that databases created
// before the audit-reviewer feature does not have, and cancels the pending rows left
// behind by the previous process.
//
// Those rows can never be answered: their in-memory channels are gone with the old
// process, so leaving them 'pending' would show an approval queue nobody can act on.
// The ALTER statements ignore "duplicate column" because they run on every start.
func (s *HITL) EnsureSchema() (int64, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS hitl_interrupts (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    message_id TEXT,
    mode TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    tool_call_id TEXT,
    payload TEXT,
    status TEXT NOT NULL,
    reviewer TEXT NOT NULL DEFAULT 'human',
    decision TEXT,
    decision_comment TEXT,
    created_at DATETIME NOT NULL,
    decided_at DATETIME
);`); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS hitl_conversation_configs (
    conversation_id TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL DEFAULT 0,
    mode TEXT NOT NULL DEFAULT 'off',
    sensitive_tools TEXT NOT NULL DEFAULT '[]',
    timeout_seconds INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME NOT NULL
);`); err != nil {
		return 0, err
	}

	_, _ = s.db.Exec(`ALTER TABLE hitl_interrupts ADD COLUMN decided_by TEXT NOT NULL DEFAULT 'human'`)
	_, _ = s.db.Exec(`ALTER TABLE hitl_interrupts ADD COLUMN reviewer TEXT NOT NULL DEFAULT 'human'`)
	if _, err := s.db.Exec(`UPDATE hitl_interrupts SET reviewer='audit_agent'
		WHERE COALESCE(decided_by, '') IN ('audit_agent', 'agent', 'ai')`); err != nil {
		return 0, fmt.Errorf("backfill hitl reviewers: %w", err)
	}
	_, _ = s.db.Exec(`ALTER TABLE hitl_conversation_configs ADD COLUMN reviewer TEXT NOT NULL DEFAULT 'human'`)

	res, err := s.db.Exec(`UPDATE hitl_interrupts SET status='cancelled', decision='reject',
		decision_comment='process restarted', decided_at=CURRENT_TIMESTAMP, decided_by='system'
		WHERE status='pending'`)
	if err != nil {
		return 0, fmt.Errorf("cancel orphaned hitl interrupts: %w", err)
	}
	cancelled, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count orphaned hitl interrupts: %w", err)
	}
	return cancelled, nil
}

// NewInterrupt is the row a pending approval starts as.
type NewInterrupt struct {
	ID             string
	ConversationID string
	MessageID      string
	Mode           string
	ToolName       string
	ToolCallID     string
	Payload        string
	Reviewer       string
	CreatedAt      time.Time
}

// CreateInterrupt records an interrupt that is waiting for a decision.
func (s *HITL) CreateInterrupt(in NewInterrupt) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if strings.TrimSpace(in.ID) == "" {
		return errors.New("store: hitl interrupt requires an id")
	}
	reviewer := strings.TrimSpace(in.Reviewer)
	if reviewer == "" {
		// An unnamed reviewer is a human review; storing "" would make the row
		// invisible to both the human queue and the agent-reviewed filters.
		reviewer = "human"
	}
	_, err := s.db.Exec(`INSERT INTO hitl_interrupts
		(id, conversation_id, message_id, mode, tool_name, tool_call_id, payload, status, reviewer, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		in.ID, in.ConversationID, in.MessageID, in.Mode, in.ToolName, in.ToolCallID, in.Payload,
		reviewer, in.CreatedAt)
	if err != nil {
		return fmt.Errorf("create hitl interrupt: %w", err)
	}
	return nil
}

// Resolution is the terminal state of an interrupt once somebody or something has
// answered it. DecidedBy distinguishes the human who clicked from the runtime that
// gave up, which is what the audit log groups by.
type Resolution struct {
	Status    string
	Decision  string
	Comment   string
	DecidedBy string
	At        time.Time
}

// Resolve writes a terminal state for one interrupt.
func (s *HITL) Resolve(id string, r Resolution) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE hitl_interrupts SET status=?, decision=?, decision_comment=?, decided_at=?, decided_by=? WHERE id=?`,
		r.Status, r.Decision, r.Comment, r.At, r.DecidedBy, strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("resolve hitl interrupt: %w", err)
	}
	return nil
}

// ResolvePending writes a terminal state only while the row is still pending and
// reports whether it changed anything. A caller that gives up on an approval (a
// timeout, a cancelled context) must not overwrite the decision a human made in the
// meantime - the guard is in the statement, so the race cannot be lost.
func (s *HITL) ResolvePending(id string, r Resolution) (bool, error) {
	if err := s.requireDB(); err != nil {
		return false, err
	}
	res, err := s.db.Exec(`UPDATE hitl_interrupts SET status=?, decision=?, decision_comment=?, decided_at=?, decided_by=?
		WHERE id=? AND status='pending'`,
		r.Status, r.Decision, r.Comment, r.At, r.DecidedBy, strings.TrimSpace(id))
	if err != nil {
		return false, fmt.Errorf("resolve pending hitl interrupt: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count resolved hitl interrupts: %w", err)
	}
	return changed > 0, nil
}

// InterruptDecision is what a caller polling for an answer needs: whether the row
// still waits and, if it does not, how it was decided.
type InterruptDecision struct {
	Status   string
	Decision string
	Found    bool
}

// Decision reads the current state of one interrupt.
func (s *HITL) Decision(id string) (InterruptDecision, error) {
	if err := s.requireDB(); err != nil {
		return InterruptDecision{}, err
	}
	var out InterruptDecision
	var decision sql.NullString
	err := s.db.QueryRow(`SELECT status, decision FROM hitl_interrupts WHERE id = ?`, strings.TrimSpace(id)).
		Scan(&out.Status, &decision)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("read hitl decision: %w", err)
	}
	out.Decision = decision.String
	out.Found = true
	return out, nil
}

// LatestPendingMode returns the collaboration mode of the conversation's newest
// pending interrupt. A sidebar that reads "off" from the config table while an
// approval is still open would hide the dialog the user has to answer.
func (s *HITL) LatestPendingMode(conversationID string) (string, bool, error) {
	if err := s.requireDB(); err != nil {
		return "", false, err
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", false, nil
	}
	var mode string
	err := s.db.QueryRow(`SELECT mode FROM hitl_interrupts WHERE conversation_id = ? AND status = 'pending' ORDER BY created_at DESC LIMIT 1`,
		conversationID).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read pending hitl mode: %w", err)
	}
	return mode, true, nil
}

// ConversationConfig is the per-conversation approval configuration row.
type ConversationConfig struct {
	Enabled        bool
	Mode           string
	Reviewer       string
	SensitiveTools []string
	TimeoutSeconds int
}

// SaveConversationConfig upserts the row. A negative timeout is stored as 0 so that
// "wait forever" has exactly one representation in the database.
func (s *HITL) SaveConversationConfig(conversationID string, c ConversationConfig) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("store: hitl conversation config requires a conversation")
	}
	tools, err := json.Marshal(c.SensitiveTools)
	if err != nil {
		return fmt.Errorf("encode sensitive tools: %w", err)
	}
	timeout := c.TimeoutSeconds
	if timeout < 0 {
		timeout = 0
	}
	reviewer := strings.TrimSpace(c.Reviewer)
	if reviewer == "" {
		reviewer = "human"
	}
	_, err = s.db.Exec(`INSERT INTO hitl_conversation_configs
		(conversation_id, enabled, mode, reviewer, sensitive_tools, timeout_seconds, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(conversation_id) DO UPDATE SET
		enabled=excluded.enabled, mode=excluded.mode, reviewer=excluded.reviewer, sensitive_tools=excluded.sensitive_tools, timeout_seconds=excluded.timeout_seconds, updated_at=excluded.updated_at`,
		conversationID, boolToInt(c.Enabled), c.Mode, reviewer, string(tools), timeout, time.Now())
	if err != nil {
		return fmt.Errorf("save hitl conversation config: %w", err)
	}
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ConversationConfig reads the row. The bool is false when the conversation never
// had one, which callers should render as "off" rather than an error.
func (s *HITL) ConversationConfig(conversationID string) (ConversationConfig, bool, error) {
	if err := s.requireDB(); err != nil {
		return ConversationConfig{}, false, err
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ConversationConfig{}, false, nil
	}
	var (
		enabledInt int
		c          ConversationConfig
		toolsJSON  string
	)
	err := s.db.QueryRow(`SELECT enabled, mode, COALESCE(reviewer,'human'), sensitive_tools, timeout_seconds FROM hitl_conversation_configs WHERE conversation_id = ?`,
		conversationID).Scan(&enabledInt, &c.Mode, &c.Reviewer, &toolsJSON, &c.TimeoutSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return ConversationConfig{}, false, nil
	}
	if err != nil {
		return ConversationConfig{}, false, fmt.Errorf("read hitl conversation config: %w", err)
	}
	c.Enabled = enabledInt == 1
	if c.TimeoutSeconds < 0 {
		c.TimeoutSeconds = 0
	}
	c.SensitiveTools = make([]string, 0)
	_ = json.Unmarshal([]byte(toolsJSON), &c.SensitiveTools)
	return c, true, nil
}

// HasConversationConfig reports whether the conversation has a stored row at all.
func (s *HITL) HasConversationConfig(conversationID string) (bool, error) {
	if err := s.requireDB(); err != nil {
		return false, err
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM hitl_conversation_configs WHERE conversation_id = ? LIMIT 1`, conversationID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check hitl conversation config: %w", err)
	}
	return true, nil
}

// CopyConversationConfigInto carries a conversation's approval policy onto a branch without copying its
// live interrupts: a pending approval belongs to the run that raised it, and answering it from the
// branch would resolve a channel that no longer exists. The table itself is created by the start-up
// schema repair, but a database from before that has none, and copying a policy is not the place to
// fail on it.
func (s *HITL) CopyConversationConfigInto(tx *sql.Tx, destinationConversationID, sourceConversationID string, now time.Time) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	var present int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='hitl_conversation_configs'`).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO hitl_conversation_configs (conversation_id, enabled, mode, reviewer, sensitive_tools, timeout_seconds, updated_at)
            SELECT ?, enabled, mode, reviewer, sensitive_tools, timeout_seconds, ? FROM hitl_conversation_configs WHERE conversation_id=?`,
		destinationConversationID, now, sourceConversationID)
	return err
}
