package database

import (
	"cyberstrike-ai/internal/sqltime"
	"database/sql"

	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Project 渗透测试项目（跨对话共享黑板）。
type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	ScopeJSON   string    `json:"scope_json,omitempty"`
	Status      string    `json:"status"` // active | archived
	Pinned      bool      `json:"pinned"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CreateProject 创建项目。
func (db *DB) CreateProject(p *Project) (*Project, error) {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	if strings.TrimSpace(p.Status) == "" {
		p.Status = "active"
	}
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	_, err := db.Exec(
		`INSERT INTO projects (id, name, description, scope_json, status, pinned, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, p.ScopeJSON, p.Status, boolToInt(p.Pinned), p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("创建项目失败: %w", err)
	}
	return p, nil
}

// GetProject 获取项目。
func (db *DB) GetProject(id string) (*Project, error) {
	var p Project
	var pinned int
	var createdAt, updatedAt string
	err := db.QueryRow(
		`SELECT id, name, COALESCE(description,''), COALESCE(scope_json,''), status, pinned, created_at, updated_at
		 FROM projects WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.Description, &p.ScopeJSON, &p.Status, &pinned, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("项目不存在")
		}
		return nil, fmt.Errorf("获取项目失败: %w", err)
	}
	p.Pinned = pinned != 0
	p.CreatedAt = parseDBTime(createdAt)
	p.UpdatedAt = parseDBTime(updatedAt)
	return &p, nil
}

// GetProjectName returns a project display name without loading the full record.
func (db *DB) GetProjectName(id string) (string, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM projects WHERE id = ?`, id).Scan(&name)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("项目不存在")
		}
		return "", fmt.Errorf("获取项目名称失败: %w", err)
	}
	return strings.TrimSpace(name), nil
}

func projectListSearchPattern(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	var b strings.Builder
	b.WriteByte('%')
	for _, r := range q {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('%')
	return b.String()
}

func appendProjectListFilters(query string, args []interface{}, status, search string) (string, []interface{}) {
	if s := strings.TrimSpace(status); s != "" {
		query += " AND status = ?"
		args = append(args, s)
	}
	if pattern := projectListSearchPattern(search); pattern != "" {
		query += ` AND (LOWER(name) LIKE LOWER(?) ESCAPE '\' OR LOWER(COALESCE(description,'')) LIKE LOWER(?) ESCAPE '\' OR LOWER(id) LIKE LOWER(?) ESCAPE '\')`
		args = append(args, pattern, pattern, pattern)
	}
	return query, args
}

func appendProjectAccessFilter(query string, args []interface{}, userID, scope string) (string, []interface{}) {
	userID = strings.TrimSpace(userID)
	if userID == "" || scope == RBACScopeAll {
		return query, args
	}
	query += ` AND (owner_user_id = ? OR EXISTS (
		SELECT 1 FROM rbac_resource_assignments ra
		WHERE ra.user_id = ? AND ra.resource_type = 'project' AND ra.resource_id = projects.id
	))`
	args = append(args, userID, userID)
	return query, args
}

func (db *DB) CountProjectsForAccess(status, search, userID, scope string) (int, error) {
	query := `SELECT COUNT(*) FROM projects WHERE 1=1`
	args := []interface{}{}
	query, args = appendProjectListFilters(query, args, status, search)
	query, args = appendProjectAccessFilter(query, args, userID, scope)
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计项目失败: %w", err)
	}
	return count, nil
}

// ListProjects 列出项目。
func (db *DB) ListProjects(status, search string, limit, offset int) ([]*Project, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, name, COALESCE(description,''), COALESCE(scope_json,''), status, pinned, created_at, updated_at
		FROM projects WHERE 1=1`
	args := []interface{}{}
	query, args = appendProjectListFilters(query, args, status, search)
	query += " ORDER BY pinned DESC, updated_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("列出项目失败: %w", err)
	}
	defer rows.Close()

	var out []*Project
	for rows.Next() {
		var p Project
		var pinned int
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.ScopeJSON, &p.Status, &pinned, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p.Pinned = pinned != 0
		p.CreatedAt = parseDBTime(createdAt)
		p.UpdatedAt = parseDBTime(updatedAt)
		out = append(out, &p)
	}
	return out, rows.Err()
}

func (db *DB) ListProjectsForAccess(status, search string, limit, offset int, userID, scope string) ([]*Project, error) {
	if scope == RBACScopeAll || strings.TrimSpace(userID) == "" {
		return db.ListProjects(status, search, limit, offset)
	}
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, name, COALESCE(description,''), COALESCE(scope_json,''), status, pinned, created_at, updated_at
		FROM projects WHERE 1=1`
	args := []interface{}{}
	query, args = appendProjectListFilters(query, args, status, search)
	query, args = appendProjectAccessFilter(query, args, userID, scope)
	query += " ORDER BY pinned DESC, updated_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("列出项目失败: %w", err)
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		var p Project
		var pinned int
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.ScopeJSON, &p.Status, &pinned, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p.Pinned = pinned != 0
		p.CreatedAt = parseDBTime(createdAt)
		p.UpdatedAt = parseDBTime(updatedAt)
		out = append(out, &p)
	}
	return out, rows.Err()
}

// UpdateProject 更新项目。
func (db *DB) UpdateProject(p *Project) error {
	p.UpdatedAt = time.Now()
	_, err := db.Exec(
		`UPDATE projects SET name = ?, description = ?, scope_json = ?, status = ?, pinned = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Description, p.ScopeJSON, p.Status, boolToInt(p.Pinned), p.UpdatedAt, p.ID,
	)
	if err != nil {
		return fmt.Errorf("更新项目失败: %w", err)
	}
	return nil
}

// DeleteProject 删除项目（级联删除事实；对话 project_id 置空由 FK 处理；其他资源 project_id 置空）。
func (db *DB) DeleteProject(id string) error {
	if err := NewFindings(db).UnlinkProject(id); err != nil {
		return fmt.Errorf("解除漏洞项目关联失败: %w", err)
	}
	if err := NewAssets(db).UnlinkProject(id); err != nil {
		return fmt.Errorf("解除资产项目关联失败: %w", err)
	}
	if err := NewWebshell(db).UnlinkProject(id); err != nil {
		return fmt.Errorf("解除 WebShell 项目关联失败: %w", err)
	}
	if _, err := db.Exec(`UPDATE c2_listeners SET project_id = NULL WHERE project_id = ?`, id); err != nil {
		return fmt.Errorf("解除 C2 监听器项目关联失败: %w", err)
	}
	_, err := db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除项目失败: %w", err)
	}
	db.removeProjectScopedDirs(id)
	return nil
}

// GetConversationProjectID 返回对话绑定的项目 ID。
func (db *DB) GetConversationProjectID(conversationID string) (string, error) {
	var pid sql.NullString
	err := db.QueryRow(`SELECT project_id FROM conversations WHERE id = ?`, conversationID).Scan(&pid)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("对话不存在")
		}
		return "", err
	}
	if pid.Valid {
		return strings.TrimSpace(pid.String), nil
	}
	return "", nil
}

// SetConversationProjectID 设置对话所属项目（空字符串表示解除绑定）。
func (db *DB) SetConversationProjectID(conversationID, projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		if _, err := db.GetProject(projectID); err != nil {
			return err
		}
	}
	var val interface{}
	if projectID == "" {
		val = nil
	} else {
		val = projectID
	}
	_, err := db.Exec(`UPDATE conversations SET project_id = ?, updated_at = ? WHERE id = ?`, val, time.Now(), conversationID)
	if err != nil {
		return fmt.Errorf("设置对话项目失败: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfEmpty(s string) interface{} {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// parseDBTime reads a DATETIME column. Which text forms are accepted is owned by internal/sqltime,
// the one spelling every layer now reads stored instants through; this is the data layer's entry.
func parseDBTime(s string) time.Time {
	return sqltime.Parse(s)
}
