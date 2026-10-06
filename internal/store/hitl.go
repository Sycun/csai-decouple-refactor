package store

import (
	"cyberstrike-ai/internal/sqltime"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// interruptColumns is the full row shape the HITL surfaces render. COALESCEs keep
// rows written before the reviewer/decided_by columns existed readable as human
// work instead of vanishing from the lists.
const interruptColumns = `id, conversation_id, message_id, mode, tool_name, tool_call_id, payload, status, ` +
	`COALESCE(reviewer,'human'), decision, decision_comment, COALESCE(decided_by,'human'), created_at, decided_at`

// Interrupt is one row of hitl_interrupts: a tool call that paused for a decision,
// plus the decision once someone made one.
type Interrupt struct {
	ID             string
	ConversationID string
	MessageID      string
	Mode           string
	ToolName       string
	ToolCallID     string
	Payload        string
	Status         string
	Reviewer       string
	Decision       string
	Comment        string
	DecidedBy      string
	CreatedAt      time.Time
	DecidedAt      *time.Time
}

// InterruptSet picks the rows a surface cares about.
type InterruptSet int

const (
	// InterruptsLog is the audit view: rows that already reached a terminal state.
	InterruptsLog InterruptSet = iota
	// InterruptsAwaitingHuman is the approval queue. Agent-reviewed pendings are
	// excluded on purpose: they need no one, so listing them would open dialogs,
	// start timeouts and inflate pending counts for nobody.
	InterruptsAwaitingHuman
)

// InterruptFilter are the filters the UI exposes; zero values and "all" mean
// "no filter". Search matches any of id, conversation, tool, payload or comment.
type InterruptFilter struct {
	ConversationID string
	ToolName       string
	Decision       string
	DecidedBy      string
	Status         string
	Search         string
	Access         Access
	Limit          int
	Offset         int
}

// HITL owns hitl_interrupts.
type HITL struct {
	db *sql.DB
}

// NewHITL binds the store to a connection.
func NewHITL(db *sql.DB) *HITL {
	return &HITL{db: db}
}

func (s *HITL) requireDB() error {
	if s == nil || s.db == nil {
		return errors.New("store: hitl requires a database")
	}
	return nil
}

// Where returns the SQL fragment and args selecting `set` under `f`.
func (s *HITL) Where(set InterruptSet, f InterruptFilter) (string, []any) {
	q := " WHERE 1=1"
	args := []any{}
	switch set {
	case InterruptsLog:
		q += " AND status != 'pending'"
	case InterruptsAwaitingHuman:
		q += " AND status = 'pending' AND COALESCE(reviewer,'human') = 'human'"
	}
	if v := strings.TrimSpace(f.ConversationID); v != "" {
		q += " AND conversation_id = ?"
		args = append(args, v)
	}
	if v := strings.TrimSpace(f.ToolName); v != "" {
		q += " AND tool_name LIKE ?"
		args = append(args, "%"+v+"%")
	}
	if v := strings.TrimSpace(f.Decision); v != "" && v != "all" {
		q += " AND decision = ?"
		args = append(args, v)
	}
	if v := strings.TrimSpace(f.DecidedBy); v != "" && v != "all" {
		q += " AND COALESCE(decided_by,'human') = ?"
		args = append(args, v)
	}
	if v := strings.TrimSpace(f.Status); v != "" && v != "all" {
		q += " AND status = ?"
		args = append(args, v)
	}
	if v := strings.TrimSpace(f.Search); v != "" {
		like := "%" + v + "%"
		q += " AND (id LIKE ? OR conversation_id LIKE ? OR tool_name LIKE ? OR payload LIKE ? OR COALESCE(decision_comment,'') LIKE ?)"
		args = append(args, like, like, like, like, like)
	}
	return ConstrainConversation(q, args, "conversation_id", f.Access)
}

func (s *HITL) order(set InterruptSet) string {
	if set == InterruptsLog {
		// A decision is the interesting moment of a log row; its creation time is
		// only the fallback for rows that never got one.
		return " ORDER BY COALESCE(decided_at, created_at) DESC"
	}
	return " ORDER BY created_at DESC"
}

// List returns one page of interrupts plus the total number of matching rows, so a
// caller can paginate without issuing a second filtered count.
func (s *HITL) List(set InterruptSet, f InterruptFilter) ([]Interrupt, int, error) {
	if err := s.requireDB(); err != nil {
		return nil, 0, err
	}
	where, args := s.Where(set, f)
	base := "SELECT " + interruptColumns + " FROM hitl_interrupts" + where

	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM ("+base+") AS hitl_cnt", args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 {
		f.Limit = total
		f.Offset = 0
	}
	query := base + s.order(set) + " LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanInterrupts(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func scanInterrupts(rows *sql.Rows) ([]Interrupt, error) {
	items := make([]Interrupt, 0)
	for rows.Next() {
		it, err := scanInterrupt(rows)
		if err != nil {
			// Every column this reads is one the table can hold, so a failure here is
			// not a row shape to tolerate. Reporting it beats the old behaviour of
			// dropping the row: an audit listing that quietly omits entries is worse
			// than one that says it could not read them.
			return nil, fmt.Errorf("scan hitl interrupt: %w", err)
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// nullableInterruptColumns are the columns the table allows to be NULL. They must
// be scanned as NullString: a plain string scan fails on NULL, and the listing used
// to swallow that error by skipping the row - an audit entry disappearing from the
// audit log because one optional column was never filled in.
func scanInterrupt(row scanner) (Interrupt, error) {
	var it Interrupt
	var messageID, toolCallID, payload, decision, comment sql.NullString
	var decidedAt sql.NullTime
	err := row.Scan(&it.ID, &it.ConversationID, &messageID, &it.Mode, &it.ToolName, &toolCallID,
		&payload, &it.Status, &it.Reviewer, &decision, &comment, &it.DecidedBy, &it.CreatedAt, &decidedAt)
	if err != nil {
		return it, err
	}
	it.MessageID = messageID.String
	it.ToolCallID = toolCallID.String
	it.Payload = payload.String
	it.Decision = decision.String
	it.Comment = comment.String
	if decidedAt.Valid {
		at := decidedAt.Time
		it.DecidedAt = &at
	}
	return it, nil
}

type scanner interface {
	Scan(dest ...any) error
}

// Get returns one interrupt. The bool is false when the id is unknown.
func (s *HITL) Get(id string) (Interrupt, bool, error) {
	if err := s.requireDB(); err != nil {
		return Interrupt{}, false, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Interrupt{}, false, nil
	}
	it, err := scanInterrupt(s.db.QueryRow("SELECT "+interruptColumns+" FROM hitl_interrupts WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Interrupt{}, false, nil
	}
	if err != nil {
		return Interrupt{}, false, err
	}
	return it, true, nil
}

// ConversationOwner returns the conversation an interrupt belongs to, which is
// what a per-resource authorization check needs before it can decide.
func (s *HITL) ConversationOwner(id string) (string, bool, error) {
	if err := s.requireDB(); err != nil {
		return "", false, err
	}
	var conversationID string
	err := s.db.QueryRow(`SELECT conversation_id FROM hitl_interrupts WHERE id = ?`, strings.TrimSpace(id)).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return conversationID, true, nil
}

// conversationOwnerChunk keeps a batch lookup below the host variable limit that
// SQLite applies to a single statement.
const conversationOwnerChunk = 500

// ConversationOwners maps interrupt ids to their conversation. Ids that are not in
// the table are absent from the result, which is how a caller tells "not yours"
// from "no longer here".
func (s *HITL) ConversationOwners(ids []string) (map[string]string, error) {
	owners := map[string]string{}
	if err := s.requireDB(); err != nil {
		return owners, err
	}
	for start := 0; start < len(ids); start += conversationOwnerChunk {
		end := start + conversationOwnerChunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		args := make([]any, 0, len(batch))
		for _, id := range batch {
			args = append(args, id)
		}
		rows, err := s.db.Query(`SELECT id, conversation_id FROM hitl_interrupts WHERE id IN (`+placeholders(len(batch))+`)`, args...)
		if err != nil {
			return owners, err
		}
		for rows.Next() {
			var id, conversationID string
			if err := rows.Scan(&id, &conversationID); err != nil {
				continue
			}
			owners[id] = conversationID
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return owners, err
		}
		rows.Close()
	}
	return owners, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// MutatePayload rewrites one interrupt's payload from its current value in a single
// transaction, so two concurrent result writers cannot interleave a read of an old
// payload with a write of the merged one and drop a result on the floor.
//
// merge reports the new payload, or false to leave the row untouched.
func (s *HITL) MutatePayload(id string, merge func(current string) (string, bool, error)) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("store: hitl payload requires an interrupt id")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin payload mutation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current sql.NullString
	err = tx.QueryRow(`SELECT payload FROM hitl_interrupts WHERE id = ?`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read interrupt payload: %w", err)
	}
	next, ok, err := merge(current.String)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if _, err := tx.Exec(`UPDATE hitl_interrupts SET payload = ? WHERE id = ?`, next, id); err != nil {
		return fmt.Errorf("write interrupt payload: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit payload mutation: %w", err)
	}
	return nil
}

// RecordAgentDecision stores the audit agent's verdict for an interrupt.
func (s *HITL) RecordAgentDecision(id, decision, comment string, decidedAt time.Time) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE hitl_interrupts SET status='decided', decision=?, decision_comment=?, decided_at=?, decided_by='audit_agent' WHERE id=?`,
		strings.TrimSpace(decision), comment, decidedAt, strings.TrimSpace(id))
	return err
}

// Dismiss cancels a pending interrupt as a human decision and returns how many rows
// changed. The status guard makes a double click, or a decision that landed in the
// meantime, report 0 instead of overwriting the recorded outcome.
func (s *HITL) Dismiss(id, comment string) (int64, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`UPDATE hitl_interrupts SET status='cancelled', decision='reject',
		decision_comment=?, decided_at=CURRENT_TIMESTAMP, decided_by='human'
		WHERE id=? AND status='pending'`, comment, strings.TrimSpace(id))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteLogs removes the terminal rows matching f - the "clear this filtered view"
// path. The set is part of the contract rather than a filter the caller may pass:
// deleting with an empty filter would otherwise be a way to wipe the audit log, and
// InterruptsLog already excludes anything still awaiting a decision.
func (s *HITL) DeleteLogs(set InterruptSet, f InterruptFilter) (int64, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	if set != InterruptsLog {
		return 0, errors.New("store: only decided hitl logs may be deleted as a group")
	}
	where, args := s.Where(set, f)
	res, err := s.db.Exec("DELETE FROM hitl_interrupts"+where, args...)
	if err != nil {
		return 0, fmt.Errorf("delete hitl logs: %w", err)
	}
	return res.RowsAffected()
}

// DeleteLogsByIDs deletes the named terminal rows and returns how many went away.
// Pending rows are skipped by the statement itself, so a caller cannot delete an
// interrupt that someone is still deciding on by guessing its id.
func (s *HITL) DeleteLogsByIDs(ids []string) (int64, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(clean))
	for _, id := range clean {
		args = append(args, id)
	}
	res, err := s.db.Exec("DELETE FROM hitl_interrupts WHERE status != 'pending' AND id IN ("+placeholders(len(clean))+")", args...)
	if err != nil {
		return 0, fmt.Errorf("delete hitl logs by id: %w", err)
	}
	return res.RowsAffected()
}

// PurgeDecidedBefore deletes terminal rows whose decision - or creation, for rows
// that never got one - predates cutoff. This is what retention_days enforces.
func (s *HITL) PurgeDecidedBefore(cutoff time.Time) (int64, error) {
	if err := s.requireDB(); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(
		`DELETE FROM hitl_interrupts WHERE status != 'pending' AND datetime(COALESCE(decided_at, created_at)) < datetime(?)`,
		cutoff.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("purge expired hitl logs: %w", err)
	}
	return res.RowsAffected()
}

// PendingApproval is the shape a notification surface needs to announce that
// something is waiting, without reading the interrupt table itself.
type PendingApproval struct {
	ID             string
	ConversationID string
	ToolName       string
	CreatedAtSec   int64
}

// PendingApprovals returns up to `limit` interrupts still waiting on a human,
// newest first. Timestamps come back as epoch seconds because the column is stored
// as text and that is how SQLite rounds it without a parse step per row.
func (s *HITL) PendingApprovals(limit int, access Access) ([]PendingApproval, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []PendingApproval{}, nil
	}
	query, args := ConstrainConversation(`
		SELECT
			id,
			conversation_id,
			tool_name,
			`+sqltime.SecondsOrNull("created_at")+`
		FROM hitl_interrupts
		WHERE status = 'pending'
	`, []any{}, "conversation_id", access)
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PendingApproval, 0, limit)
	for rows.Next() {
		var item PendingApproval
		var createdSec sql.NullInt64
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.ToolName, &createdSec); err != nil {
			continue
		}
		item.CreatedAtSec = createdSec.Int64
		items = append(items, item)
	}
	return items, nil
}
