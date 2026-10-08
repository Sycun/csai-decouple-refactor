package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberstrike-ai/internal/agentmode"
	"cyberstrike-ai/internal/sqltime"
)

// RobotSessions owns robot_user_sessions: which conversation one chat thread is talking to, plus the
// role and agent mode that thread asked for. It is the reason a robot conversation survives a restart.
//
// The session key is `platform + tenant + external user`, built by the handler. The store treats it as
// opaque: it neither parses nor validates its shape, so a change to how a thread is identified stays one
// decision in one place.
type RobotSessions struct {
	db *sql.DB
}

// NewRobotSessions binds the store to a connection.
func NewRobotSessions(db *sql.DB) *RobotSessions {
	return &RobotSessions{db: db}
}

func (r *RobotSessions) requireDB() error {
	if r == nil || r.db == nil {
		return errors.New("store: robot sessions requires a database")
	}
	return nil
}

// DefaultRoleName and DefaultAgentMode are what a thread gets when it never asked for anything. Both
// exist in the column defaults as well, so a row written by an older build reads back the same way.
const (
	DefaultRoleName  = "默认"
	DefaultAgentMode = agentmode.DefaultID
)

// The agent_mode column arrived later than the table: a base created by an older release has four
// columns, and the read below has to keep working against it.
const robotSessionsSchema = `
	CREATE TABLE IF NOT EXISTS robot_user_sessions (
		session_key TEXT PRIMARY KEY,
		conversation_id TEXT NOT NULL,
		role_name TEXT NOT NULL DEFAULT '默认',
		agent_mode TEXT NOT NULL DEFAULT 'eino_single',
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_robot_user_sessions_updated_at ON robot_user_sessions(updated_at);
`

// EnsureSchema creates the table and its index, then backfills agent_mode on a table that predates it.
func (r *RobotSessions) EnsureSchema() error {
	if err := r.requireDB(); err != nil {
		return err
	}
	if _, err := r.db.Exec(robotSessionsSchema); err != nil {
		return fmt.Errorf("创建robot_user_sessions表失败: %w", err)
	}
	var count int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('robot_user_sessions') WHERE name='agent_mode'").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := r.db.Exec("ALTER TABLE robot_user_sessions ADD COLUMN agent_mode TEXT NOT NULL DEFAULT 'eino_single'"); err != nil {
			return fmt.Errorf("迁移robot_user_sessions表失败: %w", err)
		}
	}
	return nil
}

// SessionBinding is one row, with the two defaults already applied so no reader has to know which
// column was empty.
type SessionBinding struct {
	SessionKey     string
	ConversationID string
	RoleName       string
	AgentMode      string
	UpdatedAt      time.Time
}

// Get returns the binding for a session key, or nil for one that was never written - and nil for an
// empty key, which is what a caller whose platform identity is missing gets rather than an error.
func (r *RobotSessions) Get(sessionKey string) (*SessionBinding, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil, nil
	}
	var b SessionBinding
	var updatedAt string
	err := r.db.QueryRow(
		"SELECT session_key, conversation_id, role_name, agent_mode, updated_at FROM robot_user_sessions WHERE session_key = ?",
		sessionKey,
	).Scan(&b.SessionKey, &b.ConversationID, &b.RoleName, &b.AgentMode, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("查询机器人会话绑定失败: %w", err)
	}
	b.UpdatedAt = sqltime.Parse(updatedAt)
	if strings.TrimSpace(b.RoleName) == "" {
		b.RoleName = DefaultRoleName
	}
	if strings.TrimSpace(b.AgentMode) == "" {
		b.AgentMode = DefaultAgentMode
	}
	return &b, nil
}

// Upsert points a thread at a conversation with a role and mode. An empty key or conversation is not an
// error and writes nothing: the callers reach this on every turn, and a turn without a conversation has
// nothing to remember. Empty role and mode fall back to the defaults on the way in, so a row always
// carries what a later read will show.
func (r *RobotSessions) Upsert(sessionKey, conversationID, roleName, agentMode string) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	sessionKey = strings.TrimSpace(sessionKey)
	conversationID = strings.TrimSpace(conversationID)
	roleName = strings.TrimSpace(roleName)
	agentMode = strings.TrimSpace(agentMode)
	if sessionKey == "" || conversationID == "" {
		return nil
	}
	if roleName == "" {
		roleName = DefaultRoleName
	}
	if agentMode == "" {
		agentMode = DefaultAgentMode
	}
	_, err := r.db.Exec(`
		INSERT INTO robot_user_sessions (session_key, conversation_id, role_name, agent_mode, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_key) DO UPDATE SET
			conversation_id = excluded.conversation_id,
			role_name = excluded.role_name,
			agent_mode = excluded.agent_mode,
			updated_at = excluded.updated_at
	`, sessionKey, conversationID, roleName, agentMode, time.Now())
	if err != nil {
		return fmt.Errorf("写入机器人会话绑定失败: %w", err)
	}
	return nil
}

// Delete forgets a thread's binding. An empty key is a no-op, matching Upsert's silence, and a binding
// that was never there is not an error either - the caller is tearing state down.
func (r *RobotSessions) Delete(sessionKey string) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil
	}
	if _, err := r.db.Exec("DELETE FROM robot_user_sessions WHERE session_key = ?", sessionKey); err != nil {
		return fmt.Errorf("删除机器人会话绑定失败: %w", err)
	}
	return nil
}
