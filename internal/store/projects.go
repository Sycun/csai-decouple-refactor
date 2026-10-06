package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberstrike-ai/internal/sqltime"
	"cyberstrike-ai/internal/storage"

	"github.com/google/uuid"
)

// Projects owns the project row: create / read / rename / list / delete, the counters the project
// page shows and the last-activity stamp. Deleting a project unlinks the other domains' rows by
// calling their stores' own UnlinkProject - the connection wrapper used to do that through its
// bridges; the SQL for each table stays where its store put it.
//
// The statements were copied out of internal/database verbatim. The directory cleanup and the
// memory-usage counters are injected/wired at the bridge, so this store never reaches for the
// connection wrapper or a logger.
type Projects struct {
	db   *sql.DB
	dirs storage.ConversationDirs
}

// NewProjects binds the store to a connection.
func NewProjects(db *sql.DB) *Projects { return &Projects{db: db} }

// SetDirs wires the project-scoped directory cleanup DeleteProject performs.
func (s *Projects) SetDirs(dirs storage.ConversationDirs) { s.dirs = dirs }

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
func (s *Projects) CreateProject(p *Project) (*Project, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store: projects requires a database")
	}
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

	_, err := s.db.Exec(
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
func (s *Projects) GetProject(id string) (*Project, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store: projects requires a database")
	}
	var p Project
	var pinned int
	var createdAt, updatedAt string
	err := s.db.QueryRow(
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
	p.CreatedAt = sqltime.Parse(createdAt)
	p.UpdatedAt = sqltime.Parse(updatedAt)
	return &p, nil
}

// GetProjectName returns a project display name without loading the full record.
func (s *Projects) GetProjectName(id string) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("store: projects requires a database")
	}
	var name string
	err := s.db.QueryRow(`SELECT name FROM projects WHERE id = ?`, id).Scan(&name)
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
	if userID == "" || scope == ScopeAll {
		return query, args
	}
	query += ` AND (owner_user_id = ? OR EXISTS (
		SELECT 1 FROM rbac_resource_assignments ra
		WHERE ra.user_id = ? AND ra.resource_type = 'project' AND ra.resource_id = projects.id
	))`
	args = append(args, userID, userID)
	return query, args
}

func (s *Projects) CountProjectsForAccess(status, search, userID, scope string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("store: projects requires a database")
	}
	query := `SELECT COUNT(*) FROM projects WHERE 1=1`
	args := []interface{}{}
	query, args = appendProjectListFilters(query, args, status, search)
	query, args = appendProjectAccessFilter(query, args, userID, scope)
	var count int
	if err := s.db.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计项目失败: %w", err)
	}
	return count, nil
}

// ListProjects 列出项目。
func (s *Projects) ListProjects(status, search string, limit, offset int) ([]*Project, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store: projects requires a database")
	}
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, name, COALESCE(description,''), COALESCE(scope_json,''), status, pinned, created_at, updated_at
		FROM projects WHERE 1=1`
	args := []interface{}{}
	query, args = appendProjectListFilters(query, args, status, search)
	query += " ORDER BY pinned DESC, updated_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
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
		p.CreatedAt = sqltime.Parse(createdAt)
		p.UpdatedAt = sqltime.Parse(updatedAt)
		out = append(out, &p)
	}
	return out, rows.Err()
}

func (s *Projects) ListProjectsForAccess(status, search string, limit, offset int, userID, scope string) ([]*Project, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store: projects requires a database")
	}
	if scope == ScopeAll || strings.TrimSpace(userID) == "" {
		return s.ListProjects(status, search, limit, offset)
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

	rows, err := s.db.Query(query, args...)
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
		p.CreatedAt = sqltime.Parse(createdAt)
		p.UpdatedAt = sqltime.Parse(updatedAt)
		out = append(out, &p)
	}
	return out, rows.Err()
}

// UpdateProject 更新项目。
func (s *Projects) UpdateProject(p *Project) error {
	if s == nil || s.db == nil {
		return errors.New("store: projects requires a database")
	}
	p.UpdatedAt = time.Now()
	_, err := s.db.Exec(
		`UPDATE projects SET name = ?, description = ?, scope_json = ?, status = ?, pinned = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Description, p.ScopeJSON, p.Status, boolToInt(p.Pinned), p.UpdatedAt, p.ID,
	)
	if err != nil {
		return fmt.Errorf("更新项目失败: %w", err)
	}
	return nil
}

// DeleteProject 删除项目（级联删除事实；对话 project_id 置空由 FK 处理；其他资源 project_id 置空）。
func (s *Projects) DeleteProject(id string) error {
	if s == nil || s.db == nil {
		return errors.New("store: projects requires a database")
	}
	if err := NewVulnerabilities(s.db, nil).UnlinkProject(id); err != nil {
		return fmt.Errorf("解除漏洞项目关联失败: %w", err)
	}
	if err := NewAssets(s.db).UnlinkProject(id); err != nil {
		return fmt.Errorf("解除资产项目关联失败: %w", err)
	}
	if err := NewWebshell(s.db).UnlinkProject(id); err != nil {
		return fmt.Errorf("解除 WebShell 项目关联失败: %w", err)
	}
	if err := NewC2(s.db).UnlinkProject(id); err != nil {
		return fmt.Errorf("解除 C2 监听器项目关联失败: %w", err)
	}
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除项目失败: %w", err)
	}
	s.dirs.RemoveProject(id)
	return nil
}

// ProjectLastActivity 返回项目最近活动时间；ok=false 表示项目已不存在。
func (s *Projects) ProjectLastActivity(id string) (time.Time, bool, error) {
	if s == nil || s.db == nil {
		return time.Time{}, false, errors.New("store: projects requires a database")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return time.Time{}, false, nil
	}
	var createdAt, updatedAt string
	err := s.db.QueryRow(
		"SELECT created_at, updated_at FROM projects WHERE id = ? LIMIT 1", id,
	).Scan(&createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	created, updated := sqltime.Parse(createdAt), sqltime.Parse(updatedAt)
	if created.After(updated) {
		return created, true, nil
	}
	return updated, true, nil
}
