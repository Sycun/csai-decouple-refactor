package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Session owns the durable conversation rows an interrupted run leaves behind:
// the assistant placeholder in `messages` and the terminal event recorded in
// `process_details`. The HITL domain decides *why* a run ended; this store is the
// only place allowed to rewrite the placeholder and append its evidence.
type Session struct {
	db *sql.DB
}

// NewSession binds the store to a connection.
func NewSession(db *sql.DB) *Session {
	return &Session{db: db}
}

func (s *Session) requireDB() error {
	if s == nil || s.db == nil {
		return errors.New("store: session requires a database")
	}
	return nil
}

// InterruptedPlaceholder is an assistant message still showing a "working"
// placeholder while the evidence says its run is over.
type InterruptedPlaceholder struct {
	MessageID       string
	ConversationID  string
	TerminalEvent   string
	HITLStatus      string
	HITLDecision    string
	DecisionComment string
	InterruptedAt   string
}

// interruptedPlaceholderQuery finds placeholders with explicit proof of being over:
// a terminal HITL row, a terminal process event, or a later message in the same
// conversation. The evidence requirement is what keeps this from rewriting a
// placeholder another runtime could still recover.
const interruptedPlaceholderQuery = `
SELECT msg.id, msg.conversation_id,
       COALESCE((
           SELECT pd.event_type
           FROM process_details pd
           WHERE pd.message_id = msg.id
             AND pd.event_type IN ('cancelled', 'timeout', 'error')
           ORDER BY pd.created_at DESC LIMIT 1
       ), '') AS terminal_event,
       COALESCE((
           SELECT hi.status
           FROM hitl_interrupts hi
           WHERE hi.message_id = msg.id
           ORDER BY COALESCE(hi.decided_at, hi.created_at) DESC LIMIT 1
       ), '') AS hitl_status,
       COALESCE((
           SELECT hi.decision
           FROM hitl_interrupts hi
           WHERE hi.message_id = msg.id
           ORDER BY COALESCE(hi.decided_at, hi.created_at) DESC LIMIT 1
       ), '') AS hitl_decision,
       COALESCE((
           SELECT hi.decision_comment
           FROM hitl_interrupts hi
           WHERE hi.message_id = msg.id
           ORDER BY COALESCE(hi.decided_at, hi.created_at) DESC LIMIT 1
       ), '') AS decision_comment,
       COALESCE((
           SELECT MAX(COALESCE(hi.decided_at, hi.created_at))
           FROM hitl_interrupts hi
           WHERE hi.message_id = msg.id
       ), (
           SELECT MIN(later.created_at)
           FROM messages later
           WHERE later.conversation_id = msg.conversation_id
             AND later.created_at > msg.created_at
       ), (
           SELECT MAX(pd.created_at)
           FROM process_details pd
           WHERE pd.message_id = msg.id
       ), msg.updated_at, msg.created_at) AS interrupted_at
FROM messages msg
WHERE msg.role = 'assistant'
  AND TRIM(msg.content) IN ('处理中...', 'Processing...')
  AND (
      EXISTS (
          SELECT 1 FROM hitl_interrupts hi
          WHERE hi.message_id = msg.id
            AND (hi.status IN ('cancelled', 'timeout')
                 OR (hi.status = 'decided' AND hi.decision = 'reject'))
      )
      OR EXISTS (
          SELECT 1 FROM process_details pd
          WHERE pd.message_id = msg.id
            AND pd.event_type IN ('cancelled', 'timeout', 'error')
      )
      OR EXISTS (
          SELECT 1 FROM messages later
          WHERE later.conversation_id = msg.conversation_id
            AND later.created_at > msg.created_at
      )
  )`

// InterruptedPlaceholders lists the rows a restart has to finish.
func (s *Session) InterruptedPlaceholders() ([]InterruptedPlaceholder, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(interruptedPlaceholderQuery)
	if err != nil {
		return nil, fmt.Errorf("scan interrupted assistant placeholders: %w", err)
	}
	defer rows.Close()

	items := make([]InterruptedPlaceholder, 0)
	for rows.Next() {
		var item InterruptedPlaceholder
		if err := rows.Scan(&item.MessageID, &item.ConversationID, &item.TerminalEvent,
			&item.HITLStatus, &item.HITLDecision, &item.DecisionComment, &item.InterruptedAt); err != nil {
			return nil, fmt.Errorf("scan interrupted assistant placeholder: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// InterruptedUpdate is the terminal state to write for one placeholder: the notice
// the user sees, the event that explains it, and the timestamp the interruption is
// pinned to.
type InterruptedUpdate struct {
	MessageID      string
	ConversationID string
	EventType      string
	Notice         string
	Reason         string
	InterruptedAt  string
}

// FinalizeInterruptedPlaceholders rewrites each placeholder and records its terminal
// event in one transaction, and reports how many placeholders were actually still
// untouched. A placeholder that someone else updated in the meantime is skipped by
// the content guard, so a restart cannot overwrite a finished answer.
func (s *Session) FinalizeInterruptedPlaceholders(updates []InterruptedUpdate) (int, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	if len(updates) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin interruption finalize: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	finished := 0
	for _, u := range updates {
		detail, err := json.Marshal(map[string]string{"reason": u.Reason, "status": u.EventType})
		if err != nil {
			return 0, fmt.Errorf("encode interruption detail for %s: %w", u.MessageID, err)
		}
		res, err := tx.Exec(`
UPDATE messages
SET content = ?, updated_at = ?
WHERE id = ? AND TRIM(content) IN ('处理中...', 'Processing...')`,
			u.Notice, u.InterruptedAt, u.MessageID)
		if err != nil {
			return 0, fmt.Errorf("finalize message %s: %w", u.MessageID, err)
		}
		updated, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count finalized messages: %w", err)
		}
		if updated == 0 {
			continue
		}
		finished++
		if _, err := tx.Exec(`
INSERT INTO process_details (id, message_id, conversation_id, event_type, message, data, created_at)
SELECT ?, ?, ?, ?, ?, ?, ?
WHERE NOT EXISTS (
    SELECT 1 FROM process_details
    WHERE message_id = ? AND event_type IN ('cancelled', 'timeout', 'error')
)`, uuid.NewString(), u.MessageID, u.ConversationID, u.EventType, u.Notice, string(detail),
			u.InterruptedAt, u.MessageID); err != nil {
			return 0, fmt.Errorf("record interruption event for %s: %w", u.MessageID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit interruption finalize: %w", err)
	}
	return finished, nil
}

// SetMessageContent rewrites one message's body and stamps updated_at. It is the single
// statement the HTTP layer used to repeat at fifteen sites: every path that ends a run
// abnormally (task already running, wait timeout, client failure, batch failure, finalizer
// refusal) overwrites the assistant placeholder it had already inserted.
//
// It returns the rows it touched so callers can tell "no such message" from success; the
// run paths deliberately ignore that, since the message is a nicety and the SSE error frame
// is the answer.
func (s *Session) SetMessageContent(id, content string) (int64, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	res, err := s.db.Exec("UPDATE messages SET content = ?, updated_at = ? WHERE id = ?", content, time.Now(), id)
	if err != nil {
		return 0, fmt.Errorf("update message content %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count message content update: %w", err)
	}
	return n, nil
}

// appendFragmentSQL is one CASE expression with two callers in the HTTP layer that used to
// carry their own copy of it. They differed by exactly one clause - whether an untouched
// placeholder counts as empty - and were otherwise character-for-character the same
// statement, which is the shape this refactor is meant to collapse.
const appendFragmentSQL = `UPDATE messages
 SET content = CASE
  WHEN content IS NULL OR TRIM(content) = ''%s THEN ?
  WHEN INSTR(content, ?) > 0 THEN content
  ELSE content || '\n\n' || ?
 END,
 updated_at = ?
 WHERE id = ?`

// emptyWhenPlaceholder is the clause that also treats an untouched "working" placeholder as
// empty, so a fragment replaces it instead of being appended under the spinner.
const emptyWhenPlaceholder = ` OR TRIM(content) = '处理中...'`

// AppendNotice adds a notice to the end of a message: a fresh message takes the notice as
// its content, a message that already contains it is left alone, otherwise the notice is
// appended after a blank line. An untouched placeholder is *not* treated as empty here,
// because a notice is commentary and the body that follows it still matters.
func (s *Session) AppendNotice(id, notice string) (int64, error) {
	return s.appendFragment(id, notice, "")
}

// AppendPartialOnCancel is AppendNotice for the case where the run was cancelled and the
// fragment is the only answer that will ever arrive: an untouched placeholder is replaced
// rather than annotated.
func (s *Session) AppendPartialOnCancel(id, partial string) (int64, error) {
	return s.appendFragment(id, partial, emptyWhenPlaceholder)
}

func (s *Session) appendFragment(id, fragment, emptyClause string) (int64, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(fmt.Sprintf(appendFragmentSQL, emptyClause),
		fragment, fragment, fragment, time.Now(), id)
	if err != nil {
		return 0, fmt.Errorf("append fragment to message %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count message fragment append: %w", err)
	}
	return n, nil
}

// The six statements below were the last writes to `messages` and `process_details` still written
// from the data layer - the two entries the write ledger carried as debt. They moved here so every
// table this package claims has exactly one writer, and each keeps its own error text: some of these
// strings are returned to the console, so a paraphrase would be a wire change.

// InsertMessage writes one message row. The caller generates the id and the timestamp so the value
// it returns afterwards carries the same ones, and marshals mcpExecutionIDs because that is a
// presentation concern of the caller, not of the row.
func (s *Session) InsertMessage(id, conversationID, role, content, reasoningContent, mcpIDsJSON string, now time.Time) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		"INSERT INTO messages (id, conversation_id, role, content, reasoning_content, mcp_execution_ids, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		id, conversationID, role, content, reasoningContent, mcpIDsJSON, now, now,
	); err != nil {
		return fmt.Errorf("添加消息失败: %w", err)
	}
	return nil
}

// FinalizeAssistantMessage writes the end state of an assistant placeholder: body, MCP ids and the
// aggregated reasoning text that the replay path reads back when a run left no trajectory behind.
func (s *Session) FinalizeAssistantMessage(messageID, content, mcpIDsJSON, reasoningContent string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		"UPDATE messages SET content = ?, mcp_execution_ids = ?, reasoning_content = ?, updated_at = ? WHERE id = ?",
		content, mcpIDsJSON, reasoningContent, time.Now(), messageID,
	); err != nil {
		return fmt.Errorf("更新助手消息失败: %w", err)
	}
	return nil
}

// DeleteMessagesInTurn removes one turn's rows on the caller's transaction, because the count has to
// agree with the ids the caller picked before anything was written. It answers the number of rows
// removed so the caller can still refuse a partial delete.
func (s *Session) DeleteMessagesInTurn(tx *sql.Tx, conversationID string, messageIDs []string) (int64, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	if tx == nil {
		return 0, errors.New("store: deleting a turn requires the caller's transaction")
	}
	if len(messageIDs) == 0 {
		return 0, nil
	}
	placeholders := strings.Repeat("?,", len(messageIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, 1+len(messageIDs))
	args = append(args, conversationID)
	for _, id := range messageIDs {
		args = append(args, id)
	}
	res, err := tx.Exec(
		"DELETE FROM messages WHERE conversation_id = ? AND id IN ("+placeholders+")",
		args...,
	)
	if err != nil {
		return 0, fmt.Errorf("delete messages: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// InsertProcessDetail writes one step of a run's visible trace.
func (s *Session) InsertProcessDetail(id, messageID, conversationID, eventType, message, dataJSON string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		"INSERT INTO process_details (id, message_id, conversation_id, event_type, message, data, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		id, messageID, conversationID, eventType, message, dataJSON, time.Now(),
	); err != nil {
		return fmt.Errorf("添加过程详情失败: %w", err)
	}
	return nil
}

// UpdateProcessDetailContent rewrites a streaming detail in place, which is what keeps one planning
// output from becoming one row per token. A missing row is an error, not a silent zero: the caller is
// mid-stream and has already promised this id to the page.
func (s *Session) UpdateProcessDetailContent(id, message, dataJSON string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	result, err := s.db.Exec(
		"UPDATE process_details SET message = ?, data = ? WHERE id = ?",
		message, dataJSON, strings.TrimSpace(id),
	)
	if err != nil {
		return fmt.Errorf("更新过程详情失败: %w", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected == 0 {
		return fmt.Errorf("过程详情不存在: %s", id)
	}
	return nil
}

// DeleteProcessDetail removes a planning row that turned out to be a tool-result echo.
func (s *Session) DeleteProcessDetail(id string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if _, err := s.db.Exec("DELETE FROM process_details WHERE id = ?", strings.TrimSpace(id)); err != nil {
		return fmt.Errorf("删除过程详情失败: %w", err)
	}
	return nil
}

// BackfillMessageUpdatedAt gives rows written before `updated_at` existed at least their creation
// time, so the console cannot fall back to "now" for a message that finished long ago. It runs once
// at start-up and tolerates a database that has no such column yet.
func (s *Session) BackfillMessageUpdatedAt() error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if _, err := s.db.Exec("UPDATE messages SET updated_at = created_at WHERE updated_at IS NULL OR updated_at = ''"); err != nil {
		return fmt.Errorf("回填 messages.updated_at 失败: %w", err)
	}
	return nil
}
