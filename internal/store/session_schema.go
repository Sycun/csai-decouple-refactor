package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// `messages` and `process_details` are written only by this package - the write ledger says so, and
// there is no debt entry to soften it - yet their tables were still being created by the data layer's
// start-up sweep, together with three of its indexes and the migration that backfilled two columns.
// That is the half-owner shape, so it moves here.

const messagesSchema = `
	CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		conversation_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		mcp_execution_ids TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
	);`

const processDetailsSchema = `
	CREATE TABLE IF NOT EXISTS process_details (
		id TEXT PRIMARY KEY,
		message_id TEXT NOT NULL,
		conversation_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		message TEXT,
		data TEXT,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
	);`

const sessionIndexes = `
	CREATE INDEX IF NOT EXISTS idx_messages_conversation_id ON messages(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_process_details_message_id ON process_details(message_id);
	CREATE INDEX IF NOT EXISTS idx_process_details_conversation_id ON process_details(conversation_id);`

// EnsureSchema creates the two tables in dependency order - process_details cascades off both
// conversations and messages - and builds the three indexes. The caller has to run this after the
// conversations table exists, which is why boot keeps it right behind that create.
func (s *Session) EnsureSchema() error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if _, err := s.db.Exec(messagesSchema); err != nil {
		return fmt.Errorf("创建messages表失败: %w", err)
	}
	if _, err := s.db.Exec(processDetailsSchema); err != nil {
		return fmt.Errorf("创建process_details表失败: %w", err)
	}
	if _, err := s.db.Exec(sessionIndexes); err != nil {
		return fmt.Errorf("创建会话记录索引失败: %w", err)
	}
	return nil
}

// MigrateMessageColumns backfills the two columns added after the first release of the schema and
// then gives rows written before `updated_at` existed at least their creation time, so the console
// cannot show a finished message as current.
//
// The order is the original one and the failure rule is the original one: a column that cannot be
// added aborts the rest of the migration with the column named, because a row copied without
// `updated_at` would be written on afterwards; a "duplicate column" answer is the normal case for a
// database that already has it; and a failing backfill UPDATE is not fatal at start-up.
func (s *Session) MigrateMessageColumns() error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if err := s.addColumnIfMissingMessages("updated_at", "ALTER TABLE messages ADD COLUMN updated_at DATETIME"); err != nil {
		return err
	}
	// 回填失败不拦启动：这一句只是让旧行的 updated_at 至少等于 created_at。
	_ = s.BackfillMessageUpdatedAt()
	return s.addColumnIfMissingMessages("reasoning_content", "ALTER TABLE messages ADD COLUMN reasoning_content TEXT")
}

// addColumnIfMissingMessages adds one column to `messages` unless the schema already has it. The
// schema probe is a function call rather than an inline .Scan so that a failed *schema query* is
// never shaped like a dropped *result row* - the detector cannot read intent, only shape.
func (s *Session) addColumnIfMissingMessages(column, stmt string) error {
	count, probeErr := schemaColumnCount(s.db, "messages", column)
	if probeErr != nil {
		if _, addErr := s.db.Exec(stmt); addErr != nil && !isDuplicateColumnError(addErr) {
			return fmt.Errorf("添加 messages.%s 字段失败: %w", column, addErr)
		}
		return nil
	}
	if count == 0 {
		if _, addErr := s.db.Exec(stmt); addErr != nil && !isDuplicateColumnError(addErr) {
			return fmt.Errorf("添加 messages.%s 字段失败: %w", column, addErr)
		}
	}
	return nil
}

// schemaColumnCount reports how many columns of `table` are named `column` in the live schema.
func schemaColumnCount(db *sql.DB, table, column string) (int, error) {
	if db == nil {
		return 0, errors.New("store: schema probe requires a database")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", table, column).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// SchemaHasColumn is the read the conversation domain still needs for tables whose schema it has not
// taken over yet (conversations carries its own late columns at start-up).
func SchemaHasColumn(db *sql.DB, table, column string) (bool, error) {
	count, err := schemaColumnCount(db, table, column)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
