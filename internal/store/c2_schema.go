package store

import (
	"errors"
	"fmt"
	"strings"
)

// EnsureSchema creates the six C2 tables. The statements were copied out of the boot file verbatim;
// the order follows the foreign keys: c2_sessions points at c2_listeners, and c2_tasks / c2_files
// point at c2_sessions.
func (c *C2) EnsureSchema() error {
	if c == nil || c.db == nil {
		return errors.New("store: c2 requires a database")
	}
	for _, table := range []struct {
		name string
		ddl  string
	}{
		{"c2_listeners", c2ListenersSchema},
		{"c2_sessions", c2SessionsSchema},
		{"c2_tasks", c2TasksSchema},
		{"c2_files", c2FilesSchema},
		{"c2_events", c2EventsSchema},
		{"c2_profiles", c2ProfilesSchema},
	} {
		if _, err := c.db.Exec(table.ddl); err != nil {
			return fmt.Errorf("创建%s表失败: %w", table.name, err)
		}
	}
	return nil
}

// MigrateListenerColumns adds the project binding the first release shipped without. The index on
// that column is EnsureIndexes' job, which must run after this; the column check is the same
// probe-and-tolerance the package's addColumnIfMissing runs (conversations_schema.go), including
// which errors it refuses to read as a duplicate.
func (c *C2) MigrateListenerColumns() error {
	if c == nil || c.db == nil {
		return errors.New("store: c2 requires a database")
	}
	var count int
	if err := c.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", "c2_listeners", "project_id").Scan(&count); err != nil || count == 0 {
		if _, addErr := c.db.Exec("ALTER TABLE c2_listeners ADD COLUMN project_id TEXT"); addErr != nil {
			msg := strings.ToLower(addErr.Error())
			if !strings.Contains(msg, "duplicate column") && !strings.Contains(msg, "already exists") {
				return fmt.Errorf("添加c2_listeners.project_id字段失败: %w", addErr)
			}
		}
	}
	// owner_user_id：RBAC 迁移搬来时它建的列，同一套 probe + duplicate 容错。
	if err := c.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", "c2_listeners", "owner_user_id").Scan(&count); err != nil || count == 0 {
		if _, addErr := c.db.Exec("ALTER TABLE c2_listeners ADD COLUMN owner_user_id TEXT"); addErr != nil {
			msg := strings.ToLower(addErr.Error())
			if !strings.Contains(msg, "duplicate column") && !strings.Contains(msg, "already exists") {
				return fmt.Errorf("添加c2_listeners.owner_user_id字段失败: %w", addErr)
			}
		}
	}
	return nil
}

// EnsureIndexes creates the fourteen C2 indexes. One of them sits on project_id, which only
// MigrateListenerColumns creates, so this runs after it.
func (c *C2) EnsureIndexes() error {
	if c == nil || c.db == nil {
		return errors.New("store: c2 requires a database")
	}
	if _, err := c.db.Exec(c2Indexes); err != nil {
		return fmt.Errorf("创建C2索引失败: %w", err)
	}
	return nil
}

const c2ListenersSchema = `
	CREATE TABLE IF NOT EXISTS c2_listeners (
		id TEXT PRIMARY KEY,
		project_id TEXT,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		bind_host TEXT NOT NULL DEFAULT '127.0.0.1',
		bind_port INTEGER NOT NULL,
		profile_id TEXT,
		encryption_key TEXT NOT NULL DEFAULT '',
		implant_token TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'stopped',
		config_json TEXT NOT NULL DEFAULT '{}',
		remark TEXT NOT NULL DEFAULT '',
		owner_user_id TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		started_at DATETIME,
		last_error TEXT
	);`

const c2SessionsSchema = `
	CREATE TABLE IF NOT EXISTS c2_sessions (
		id TEXT PRIMARY KEY,
		listener_id TEXT NOT NULL,
		implant_uuid TEXT NOT NULL UNIQUE,
		hostname TEXT,
		username TEXT,
		os TEXT,
		arch TEXT,
		pid INTEGER DEFAULT 0,
		process_name TEXT,
		is_admin INTEGER DEFAULT 0,
		internal_ip TEXT,
		external_ip TEXT,
		user_agent TEXT,
		sleep_seconds INTEGER NOT NULL DEFAULT 5,
		jitter_percent INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'active',
		first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_check_in DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		metadata_json TEXT DEFAULT '{}',
		note TEXT NOT NULL DEFAULT '',
		FOREIGN KEY (listener_id) REFERENCES c2_listeners(id) ON DELETE CASCADE
	);`

const c2TasksSchema = `
	CREATE TABLE IF NOT EXISTS c2_tasks (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		task_type TEXT NOT NULL,
		payload_json TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'queued',
		result_text TEXT,
		result_blob_path TEXT,
		error TEXT,
		source TEXT NOT NULL DEFAULT 'manual',
		conversation_id TEXT,
		approval_status TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		sent_at DATETIME,
		started_at DATETIME,
		completed_at DATETIME,
		duration_ms INTEGER DEFAULT 0,
		FOREIGN KEY (session_id) REFERENCES c2_sessions(id) ON DELETE CASCADE
	);`

const c2FilesSchema = `
	CREATE TABLE IF NOT EXISTS c2_files (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		task_id TEXT,
		direction TEXT NOT NULL,
		remote_path TEXT NOT NULL,
		local_path TEXT NOT NULL,
		size_bytes INTEGER DEFAULT 0,
		sha256 TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (session_id) REFERENCES c2_sessions(id) ON DELETE CASCADE
	);`

const c2EventsSchema = `
	CREATE TABLE IF NOT EXISTS c2_events (
		id TEXT PRIMARY KEY,
		level TEXT NOT NULL DEFAULT 'info',
		category TEXT NOT NULL,
		session_id TEXT,
		task_id TEXT,
		message TEXT NOT NULL,
		data_json TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`

const c2ProfilesSchema = `
	CREATE TABLE IF NOT EXISTS c2_profiles (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		user_agent TEXT,
		uris_json TEXT NOT NULL DEFAULT '[]',
		request_headers_json TEXT,
		response_headers_json TEXT,
		body_template TEXT,
		jitter_min_ms INTEGER DEFAULT 0,
		jitter_max_ms INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`

const c2Indexes = `
	CREATE INDEX IF NOT EXISTS idx_c2_listeners_created_at ON c2_listeners(created_at);
	CREATE INDEX IF NOT EXISTS idx_c2_listeners_project_id ON c2_listeners(project_id);
	CREATE INDEX IF NOT EXISTS idx_c2_listeners_status ON c2_listeners(status);
	CREATE INDEX IF NOT EXISTS idx_c2_sessions_listener ON c2_sessions(listener_id);
	CREATE INDEX IF NOT EXISTS idx_c2_sessions_status ON c2_sessions(status);
	CREATE INDEX IF NOT EXISTS idx_c2_sessions_last_check_in ON c2_sessions(last_check_in);
	CREATE INDEX IF NOT EXISTS idx_c2_tasks_session ON c2_tasks(session_id);
	CREATE INDEX IF NOT EXISTS idx_c2_tasks_status ON c2_tasks(status);
	CREATE INDEX IF NOT EXISTS idx_c2_tasks_created_at ON c2_tasks(created_at);
	CREATE INDEX IF NOT EXISTS idx_c2_tasks_conversation ON c2_tasks(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_c2_files_session ON c2_files(session_id);
	CREATE INDEX IF NOT EXISTS idx_c2_events_created_at ON c2_events(created_at);
	CREATE INDEX IF NOT EXISTS idx_c2_events_category ON c2_events(category);
	CREATE INDEX IF NOT EXISTS idx_c2_events_session ON c2_events(session_id);
`
