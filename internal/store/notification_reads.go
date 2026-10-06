// Package store holds per-domain persistence. It is the target shape for the
// data-layer item of the refactor plan: `*database.DB` carries 361 methods and
// embeds `*sql.DB`, so passing it around let any consumer reach any table and any
// raw statement - 32 ad-hoc queries ended up in the HTTP layer that way.
//
// A store owns one domain's SQL and is constructed from the connection it needs,
// so it can be tested on its own against a real database.
package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// MaxNotificationReadsPerUser bounds retained read markers per user; older rows
// are pruned after a successful mark, which is what keeps the table bounded.
const MaxNotificationReadsPerUser = 150

// NotificationReads owns the notification_reads_by_user table.
type NotificationReads struct {
	db *sql.DB
}

// NewNotificationReads binds the store to a connection.
func NewNotificationReads(db *sql.DB) *NotificationReads {
	return &NotificationReads{db: db}
}

// EnsureSchema creates the table and the index used for recency pruning.
func (s *NotificationReads) EnsureSchema() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store: notification reads requires a database")
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS notification_reads_by_user (
			user_id TEXT NOT NULL,
			event_id TEXT NOT NULL,
			read_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY(user_id, event_id)
		);
	`); err != nil {
		return err
	}
	_, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_notification_reads_user_read_at ON notification_reads_by_user(user_id, read_at DESC);`)
	return err
}

// informationOnlyEventPrefixes are the notification ids that "read" is meaningful
// for. Actionable events (pending approval, running tasks) are deliberately absent:
// marking them read would hide work that still needs a decision.
var informationOnlyEventPrefixes = []string{
	"vuln:",
	"exec_failed:",
	"task_completed:",
	"c2evt:",
}

// MarkableEventID reports whether an event id may be marked read. Notification
// ids are derived from the surfaces that raise them, so accepting arbitrary
// strings would let a caller write rows that nothing ever reads.
func MarkableEventID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	for _, prefix := range informationOnlyEventPrefixes {
		if strings.HasPrefix(value, prefix) {
			return value, true
		}
	}
	return "", false
}

// ReadStates returns which of the given event ids the user already read.
func (s *NotificationReads) ReadStates(userID string, eventIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(eventIDs))
	userID = strings.TrimSpace(userID)
	if s == nil || s.db == nil || userID == "" || len(eventIDs) == 0 {
		return result, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(eventIDs)), ",")
	query := "SELECT event_id FROM notification_reads_by_user WHERE user_id = ? AND event_id IN (" + placeholders + ")"
	args := make([]any, 0, len(eventIDs)+1)
	args = append(args, userID)
	for _, id := range eventIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		// event_id is part of the table's unique key and cannot be NULL; a scan failure is a fault.
		// Answering it by skipping would report that row as unread, which is the one thing this
		// read must not get wrong.
		if err := rows.Scan(&id); err != nil {
			return result, err
		}
		result[id] = true
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	return result, nil
}

// MarkRead upserts the markable event ids in one transaction and returns how many
// were written. Re-marking an event refreshes its timestamp rather than erroring,
// which is what makes a client retry safe. Retention is the caller's concern: a
// failed Prune must not turn a successful mark into an error, so Prune is called
// separately and logged on failure.
func (s *NotificationReads) MarkRead(userID string, eventIDs []string) (int, error) {
	userID = strings.TrimSpace(userID)
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("store: notification reads requires a database")
	}
	if userID == "" {
		return 0, fmt.Errorf("store: notification reads require a user")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin read marks: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT INTO notification_reads_by_user(user_id, event_id, read_at)
		VALUES(?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(user_id, event_id) DO UPDATE SET read_at = CURRENT_TIMESTAMP
	`)
	if err != nil {
		return 0, fmt.Errorf("prepare read mark: %w", err)
	}
	defer stmt.Close()

	marked := 0
	for _, raw := range eventIDs {
		id, ok := MarkableEventID(raw)
		if !ok {
			continue
		}
		if _, err := stmt.Exec(userID, id); err != nil {
			return 0, fmt.Errorf("mark read %s: %w", id, err)
		}
		marked++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit read marks: %w", err)
	}
	return marked, nil
}

// Prune keeps only the most recent maxRows read markers for a user.
func (s *NotificationReads) Prune(userID string, maxRows int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store: notification reads requires a database")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" || maxRows <= 0 {
		return nil
	}
	_, err := s.db.Exec(`
		DELETE FROM notification_reads_by_user
		WHERE user_id = ?
		  AND rowid NOT IN (
			SELECT rowid FROM notification_reads_by_user
			WHERE user_id = ?
			ORDER BY read_at DESC, rowid DESC
			LIMIT ?
		)
	`, userID, userID, maxRows)
	if err != nil {
		return fmt.Errorf("prune read marks: %w", err)
	}
	return nil
}
