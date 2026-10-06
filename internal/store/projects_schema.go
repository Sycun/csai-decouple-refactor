package store

import (
	"errors"
	"fmt"
)

// projectsSchema is the boot file's CREATE, copied verbatim. project_facts / project_fact_edges,
// assets, batch_task_queues and c2_listeners all carry a foreign key onto it, so this ensure runs
// before those stores'.
const projectsSchema = `
	CREATE TABLE IF NOT EXISTS projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		scope_json TEXT,
		status TEXT NOT NULL DEFAULT 'active',
		pinned INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);`

// EnsureSchema creates the projects table. The late columns and the indexes are the two phases below.
func (s *Projects) EnsureSchema() error {
	if s == nil || s.db == nil {
		return errors.New("store: projects requires a database")
	}
	if _, err := s.db.Exec(projectsSchema); err != nil {
		return fmt.Errorf("创建projects表失败: %w", err)
	}
	return nil
}

// MigrateLateColumns adds the RBAC owner column the first release shipped without. conversations'
// project_id column is the same migration's other half, but it alters that table and lives with its
// owner (see Conversations.MigrateLateColumns).
func (s *Projects) MigrateLateColumns() error {
	if s == nil || s.db == nil {
		return errors.New("store: projects requires a database")
	}
	return addColumnIfMissing(s.db, "projects", "owner_user_id", "ALTER TABLE projects ADD COLUMN owner_user_id TEXT")
}

// EnsureIndexes builds the two projects indexes.
func (s *Projects) EnsureIndexes() error {
	if s == nil || s.db == nil {
		return errors.New("store: projects requires a database")
	}
	const indexes = `
	CREATE INDEX IF NOT EXISTS idx_projects_status ON projects(status);
	CREATE INDEX IF NOT EXISTS idx_projects_updated_at ON projects(updated_at);
	`
	if _, err := s.db.Exec(indexes); err != nil {
		return fmt.Errorf("创建projects索引失败: %w", err)
	}
	return nil
}
