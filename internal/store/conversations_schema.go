package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// conversationsSchema is the boot file's CREATE, copied verbatim. It is the first table created at
// start-up: messages, process_details, workflow_runs, vulnerabilities and the C2 tables all carry a
// foreign key onto it, so this ensure runs before all of them.
const conversationsSchema = `
	CREATE TABLE IF NOT EXISTS conversations (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		role_name TEXT NOT NULL DEFAULT '默认',
		agent_mode TEXT NOT NULL DEFAULT 'eino_single',
		last_react_input TEXT,
		last_react_output TEXT
	);`

// EnsureSchema creates the conversations table. The late columns and the indexes are the two phases
// below; a caller that skips them still gets a table the first release could read.
func (c *Conversations) EnsureSchema() error {
	if err := c.requireDB(); err != nil {
		return err
	}
	if _, err := c.db.Exec(conversationsSchema); err != nil {
		return fmt.Errorf("创建conversations表失败: %w", err)
	}
	return nil
}

// MigrateLateColumns adds every column the conversations table grew after the first release, in the
// order the boot file added them: the agent trace pair, pinned, the WebShell binding, the role / mode
// stamps, then the project binding and the RBAC owner. The index on project_id is EnsureIndexes' job,
// which must run after this.
func (c *Conversations) MigrateLateColumns() error {
	if err := c.requireDB(); err != nil {
		return err
	}
	for _, col := range []struct {
		name string
		stmt string
	}{
		{"last_react_input", "ALTER TABLE conversations ADD COLUMN last_react_input TEXT"},
		{"last_react_output", "ALTER TABLE conversations ADD COLUMN last_react_output TEXT"},
		{"pinned", "ALTER TABLE conversations ADD COLUMN pinned INTEGER DEFAULT 0"},
		{"webshell_connection_id", "ALTER TABLE conversations ADD COLUMN webshell_connection_id TEXT"},
		{"role_name", "ALTER TABLE conversations ADD COLUMN role_name TEXT NOT NULL DEFAULT '默认'"},
		{"agent_mode", "ALTER TABLE conversations ADD COLUMN agent_mode TEXT NOT NULL DEFAULT 'eino_single'"},
		{"project_id", "ALTER TABLE conversations ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE SET NULL"},
		{"owner_user_id", "ALTER TABLE conversations ADD COLUMN owner_user_id TEXT"},
	} {
		if err := addColumnIfMissing(c.db, "conversations", col.name, col.stmt); err != nil {
			return err
		}
	}
	return nil
}

// EnsureIndexes builds the three conversations indexes. idx_conversations_project_id sits on the
// column MigrateLateColumns creates, so this runs after it.
func (c *Conversations) EnsureIndexes() error {
	if err := c.requireDB(); err != nil {
		return err
	}
	const indexes = `
	CREATE INDEX IF NOT EXISTS idx_conversations_updated_at ON conversations(updated_at);
	CREATE INDEX IF NOT EXISTS idx_conversations_pinned ON conversations(pinned);
	CREATE INDEX IF NOT EXISTS idx_conversations_project_id ON conversations(project_id);
	`
	if _, err := c.db.Exec(indexes); err != nil {
		return fmt.Errorf("创建conversations索引失败: %w", err)
	}
	return nil
}

func (c *Conversations) requireDB() error {
	if c == nil || c.db == nil {
		return errors.New("store: conversations requires a database")
	}
	return nil
}

// addColumnIfMissing adds one column unless pragma_table_info already lists it. This was the data
// layer's helper, copied verbatim including which errors it refuses to read as a duplicate ("already
// exists" is as legitimate as "duplicate column" when the probe raced another connection).
func addColumnIfMissing(db *sql.DB, table, name, stmt string) error {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", table, name).Scan(&count)
	if err != nil || count == 0 {
		if _, addErr := db.Exec(stmt); addErr != nil {
			msg := strings.ToLower(addErr.Error())
			if !strings.Contains(msg, "duplicate column") && !strings.Contains(msg, "already exists") {
				return fmt.Errorf("添加%s.%s字段失败: %w", table, name, addErr)
			}
		}
	}
	return nil
}
