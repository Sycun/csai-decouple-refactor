package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Webshell owns the operator's saved webshell connections and the per-connection workspace state.
// Two tables, one owner: the state row is deleted by the connection's foreign key, so splitting them
// would put the cascade out of reach of the code that depends on it.
//
// The statements came over from internal/database. Four log lines went with the logger that stayed
// there - each of those paths now returns a wrapped error and the caller that owns the request logs
// it, which is the same trade the attack-chain and workflow moves made deliberately rather than
// inventing a logger for this package. One difference is intentional and not cosmetic: the listing
// used to answer a failed row scan by logging a warning and dropping that connection from the list,
// which is the defect class this package is not allowed to carry (it owns its DDL, so a scan failure
// can only be a fault). It returns the error now.
type Webshell struct {
	db *sql.DB
}

func NewWebshell(db *sql.DB) *Webshell { return &Webshell{db: db} }

func (s *Webshell) requireDB() error {
	if s == nil || s.db == nil {
		return errors.New("store: webshell requires a database")
	}
	return nil
}

// WebShellConnection WebShell 连接配置
type WebShellConnection struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id,omitempty"`
	URL       string    `json:"url"`
	Password  string    `json:"password"`
	Type      string    `json:"type"`
	Method    string    `json:"method"`
	CmdParam  string    `json:"cmdParam"`
	Remark    string    `json:"remark"`
	Encoding  string    `json:"encoding"` // 目标响应编码：auto / utf-8 / gbk / gb18030，空值视为 auto
	OS        string    `json:"os"`       // 目标操作系统：auto / linux / windows，空值/未知视为 auto
	CreatedAt time.Time `json:"createdAt"`
}

const webshellSchema = `
CREATE TABLE IF NOT EXISTS webshell_connections (
		id TEXT PRIMARY KEY,
		project_id TEXT,
		url TEXT NOT NULL,
		password TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT 'php',
		method TEXT NOT NULL DEFAULT 'post',
		cmd_param TEXT NOT NULL DEFAULT '',
		remark TEXT NOT NULL DEFAULT '',
		encoding TEXT NOT NULL DEFAULT '',
		os TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS webshell_connection_states (
		connection_id TEXT PRIMARY KEY,
		state_json TEXT NOT NULL DEFAULT '{}',
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (connection_id) REFERENCES webshell_connections(id) ON DELETE CASCADE
	);`

const webshellIndexes = `
CREATE INDEX IF NOT EXISTS idx_webshell_connections_created_at ON webshell_connections(created_at);
	CREATE INDEX IF NOT EXISTS idx_webshell_connections_project_id ON webshell_connections(project_id);
	CREATE INDEX IF NOT EXISTS idx_webshell_connection_states_updated_at ON webshell_connection_states(updated_at);`

func (s *Webshell) EnsureSchema() error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if _, err := s.db.Exec(webshellSchema); err != nil {
		return fmt.Errorf("创建 WebShell 连接表失败: %w", err)
	}
	if _, err := s.db.Exec(webshellIndexes); err != nil {
		return fmt.Errorf("创建 WebShell 连接索引失败: %w", err)
	}
	return nil
}

// webshellConnectionColumns is the read shape of both queries. The nullable columns are COALESCEd
// because rows written before encoding/os/project_id existed must keep loading: a plain scan into
// string would fail them and the connection would vanish from the list.
const webshellConnectionColumns = `id, COALESCE(project_id, '') AS project_id, url, password, type, method, cmd_param, remark,
			COALESCE(encoding, '') AS encoding, COALESCE(os, '') AS os, created_at`

// GetState returns the persisted workspace state of a connection as JSON. Absent, or stored as the
// empty string, both answer "{}" - the front end reads the object without checking.
func (s *Webshell) GetState(connectionID string) (string, error) {
	if err := s.requireDB(); err != nil {
		return "", err
	}
	var stateJSON string
	err := s.db.QueryRow(`SELECT state_json FROM webshell_connection_states WHERE connection_id = ?`, connectionID).Scan(&stateJSON)
	if err == sql.ErrNoRows {
		return "{}", nil
	}
	if err != nil {
		return "", fmt.Errorf("查询 WebShell 连接状态失败: %w", err)
	}
	if stateJSON == "" {
		stateJSON = "{}"
	}
	return stateJSON, nil
}

// UpsertState saves the persisted workspace state of a connection.
func (s *Webshell) UpsertState(connectionID, stateJSON string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if stateJSON == "" {
		stateJSON = "{}"
	}
	query := `
		INSERT INTO webshell_connection_states (connection_id, state_json, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(connection_id) DO UPDATE SET
			state_json = excluded.state_json,
			updated_at = excluded.updated_at
	`
	if _, err := s.db.Exec(query, connectionID, stateJSON, time.Now()); err != nil {
		return fmt.Errorf("保存 WebShell 连接状态失败: %w", err)
	}
	return nil
}

// List returns the connections the caller can see, newest first.
//
// The visibility rule is owner-or-assigned, and an empty user id together with a non-`all` scope
// yields `1=0`: no session means no rows, never superuser.
func (s *Webshell) List(access Access, projectID string) ([]WebShellConnection, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	query := `
		SELECT ` + webshellConnectionColumns + `
		FROM webshell_connections
		WHERE 1=1
	`
	args := []interface{}{}
	projectID = strings.TrimSpace(projectID)
	if projectID == ProjectUnbound {
		query += ` AND COALESCE(project_id, '') = ''`
	} else if projectID != "" {
		query += ` AND COALESCE(project_id, '') = ?`
		args = append(args, projectID)
	}
	userID := strings.TrimSpace(access.UserID)
	if userID != "" && !access.SeeAll() {
		query += ` AND (
			owner_user_id = ?
			OR EXISTS (
				SELECT 1 FROM rbac_resource_assignments ra
				WHERE ra.user_id = ? AND ra.resource_type = 'webshell' AND ra.resource_id = webshell_connections.id
			)
		)`
		args = append(args, userID, userID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询 WebShell 连接列表失败: %w", err)
	}
	defer rows.Close()

	var list []WebShellConnection
	for rows.Next() {
		var c WebShellConnection
		err := rows.Scan(&c.ID, &c.ProjectID, &c.URL, &c.Password, &c.Type, &c.Method, &c.CmdParam, &c.Remark, &c.Encoding, &c.OS, &c.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("扫描 WebShell 连接行失败: %w", err)
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// Get returns one connection, or (nil, nil) when the id is unknown: the authorization helpers check
// for a missing connection before they decide between 403 and 404.
func (s *Webshell) Get(id string) (*WebShellConnection, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	query := `
		SELECT ` + webshellConnectionColumns + `
		FROM webshell_connections WHERE id = ?
	`
	var c WebShellConnection
	err := s.db.QueryRow(query, id).Scan(&c.ID, &c.ProjectID, &c.URL, &c.Password, &c.Type, &c.Method, &c.CmdParam, &c.Remark, &c.Encoding, &c.OS, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询 WebShell 连接失败: %w", err)
	}
	return &c, nil
}

// Create stores a new connection.
func (s *Webshell) Create(c *WebShellConnection) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	query := `
		INSERT INTO webshell_connections (id, project_id, url, password, type, method, cmd_param, remark, encoding, os, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	if _, err := s.db.Exec(query, c.ID, strings.TrimSpace(c.ProjectID), c.URL, c.Password, c.Type, c.Method, c.CmdParam, c.Remark, c.Encoding, c.OS, c.CreatedAt); err != nil {
		return fmt.Errorf("创建 WebShell 连接失败: %w", err)
	}
	return nil
}

// Update rewrites a connection. Zero rows affected is sql.ErrNoRows, which is how the handler
// answers "unknown connection" rather than "storage failed".
func (s *Webshell) Update(c *WebShellConnection) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	query := `
		UPDATE webshell_connections
		SET project_id = ?, url = ?, password = ?, type = ?, method = ?, cmd_param = ?, remark = ?, encoding = ?, os = ?
		WHERE id = ?
	`
	result, err := s.db.Exec(query, strings.TrimSpace(c.ProjectID), c.URL, c.Password, c.Type, c.Method, c.CmdParam, c.Remark, c.Encoding, c.OS, c.ID)
	if err != nil {
		return fmt.Errorf("更新 WebShell 连接失败: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Delete removes a connection; its state row goes with it through the foreign key.
func (s *Webshell) Delete(id string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	result, err := s.db.Exec(`DELETE FROM webshell_connections WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除 WebShell 连接失败: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// MigrateConnectionsTable backfills the three columns added after the table first shipped. It is
// idempotent: each column is probed through pragma_table_info, and an ALTER that reports a duplicate
// column is the same success as finding the column already present.
func (s *Webshell) MigrateConnectionsTable() error {
	if err := s.requireDB(); err != nil {
		return err
	}
	columns := []struct {
		name string
		stmt string
	}{
		{name: "project_id", stmt: "ALTER TABLE webshell_connections ADD COLUMN project_id TEXT"},
		{name: "encoding", stmt: "ALTER TABLE webshell_connections ADD COLUMN encoding TEXT NOT NULL DEFAULT ''"},
		{name: "os", stmt: "ALTER TABLE webshell_connections ADD COLUMN os TEXT NOT NULL DEFAULT ''"},
		{name: "owner_user_id", stmt: "ALTER TABLE webshell_connections ADD COLUMN owner_user_id TEXT"},
	}

	for _, col := range columns {
		var count int
		// A failed probe is not a missing column: the ALTER is attempted either way and a
		// duplicate-column answer is treated as the column already being there.
		probeErr := s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('webshell_connections') WHERE name=?", col.name).Scan(&count)
		if probeErr == nil && count > 0 {
			continue
		}
		if _, addErr := s.db.Exec(col.stmt); addErr == nil {
			continue
		} else if probeErr != nil {
			msg := strings.ToLower(addErr.Error())
			if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
				continue
			}
			return fmt.Errorf("添加 webshell_connections 字段 %s 失败: %w", col.name, addErr)
		} else {
			return fmt.Errorf("添加 webshell_connections 字段 %s 失败: %w", col.name, addErr)
		}
	}
	return nil
}

// UnlinkProject clears the project stamp of every connection in a project that is being deleted.
// It is a method on this store rather than an UPDATE in the project domain for the same reason the
// findings unlink is: the table has one writer, and the caller hands over a value instead of SQL.
func (s *Webshell) UnlinkProject(projectID string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE webshell_connections SET project_id = NULL WHERE project_id = ?`,
		strings.TrimSpace(projectID)); err != nil {
		return fmt.Errorf("解除 WebShell 连接项目关联失败: %w", err)
	}
	return nil
}
