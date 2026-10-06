package store

import (
	"errors"
	"fmt"
	"strings"
)

// EnsureSchema creates the two monitor tables. The statements were copied out of the boot file
// verbatim; the order does not matter for these two (tool_stats has no foreign key).
func (m *Monitor) EnsureSchema() error {
	if m == nil || m.db == nil {
		return errors.New("store: monitor requires a database")
	}
	for _, table := range []struct {
		name string
		ddl  string
	}{
		{"tool_executions", toolExecutionsSchema},
		{"tool_stats", toolStatsSchema},
	} {
		if _, err := m.db.Exec(table.ddl); err != nil {
			return fmt.Errorf("创建%s表失败: %w", table.name, err)
		}
	}
	return nil
}

// MigrateLateColumns adds the columns the first release shipped without: the four partial-output
// preview columns and the two ownership columns (owner_user_id, conversation_id) RBAC's migration
// used to backfill for this table. The column check mirrors the data layer's addColumnIfMissing,
// including which errors it refuses to read as a duplicate.
func (m *Monitor) MigrateLateColumns() error {
	if m == nil || m.db == nil {
		return errors.New("store: monitor requires a database")
	}
	for _, col := range []struct {
		name string
		stmt string
	}{
		{"partial_output", "ALTER TABLE tool_executions ADD COLUMN partial_output TEXT"},
		{"partial_output_bytes", "ALTER TABLE tool_executions ADD COLUMN partial_output_bytes INTEGER NOT NULL DEFAULT 0"},
		{"partial_output_truncated", "ALTER TABLE tool_executions ADD COLUMN partial_output_truncated INTEGER NOT NULL DEFAULT 0"},
		{"partial_output_updated_at", "ALTER TABLE tool_executions ADD COLUMN partial_output_updated_at DATETIME"},
		{"owner_user_id", "ALTER TABLE tool_executions ADD COLUMN owner_user_id TEXT"},
		{"conversation_id", "ALTER TABLE tool_executions ADD COLUMN conversation_id TEXT"},
	} {
		var count int
		if err := m.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", "tool_executions", col.name).Scan(&count); err != nil || count == 0 {
			if _, addErr := m.db.Exec(col.stmt); addErr != nil {
				msg := strings.ToLower(addErr.Error())
				if !strings.Contains(msg, "duplicate column") && !strings.Contains(msg, "already exists") {
					return fmt.Errorf("添加tool_executions.%s字段失败: %w", col.name, addErr)
				}
			}
		}
	}
	return nil
}

// EnsureIndexes creates the five tool_executions indexes. Two of them (owner, conversation) the RBAC
// migration used to build from outside; they moved here with the rest of the table's DDL.
func (m *Monitor) EnsureIndexes() error {
	if m == nil || m.db == nil {
		return errors.New("store: monitor requires a database")
	}
	if _, err := m.db.Exec(toolExecutionsIndexes); err != nil {
		return fmt.Errorf("创建tool_executions索引失败: %w", err)
	}
	return nil
}

const toolExecutionsSchema = `
	CREATE TABLE IF NOT EXISTS tool_executions (
		id TEXT PRIMARY KEY,
		tool_name TEXT NOT NULL,
		arguments TEXT NOT NULL,
		status TEXT NOT NULL,
		result TEXT,
		error TEXT,
		start_time DATETIME NOT NULL,
		end_time DATETIME,
		duration_ms INTEGER,
		partial_output TEXT,
		partial_output_bytes INTEGER NOT NULL DEFAULT 0,
		partial_output_truncated INTEGER NOT NULL DEFAULT 0,
		partial_output_updated_at DATETIME,
		owner_user_id TEXT,
		conversation_id TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`

const toolStatsSchema = `
	CREATE TABLE IF NOT EXISTS tool_stats (
		tool_name TEXT PRIMARY KEY,
		total_calls INTEGER NOT NULL DEFAULT 0,
		success_calls INTEGER NOT NULL DEFAULT 0,
		failed_calls INTEGER NOT NULL DEFAULT 0,
		last_call_time DATETIME,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`

const toolExecutionsIndexes = `
	CREATE INDEX IF NOT EXISTS idx_tool_executions_owner ON tool_executions(owner_user_id);
	CREATE INDEX IF NOT EXISTS idx_tool_executions_conversation ON tool_executions(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_tool_executions_tool_name ON tool_executions(tool_name);
	CREATE INDEX IF NOT EXISTS idx_tool_executions_start_time ON tool_executions(start_time);
	CREATE INDEX IF NOT EXISTS idx_tool_executions_status ON tool_executions(status);
`
