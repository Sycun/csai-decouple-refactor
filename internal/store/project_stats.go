package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/sqltime"
)

// ProjectStats 项目聚合统计。
type ProjectStats struct {
	FactCount         int `json:"fact_count"`
	VulnCount         int `json:"vuln_count"`
	ConversationCount int `json:"conversation_count"`
	SparseFactCount   int `json:"sparse_fact_count"`
}

// GetProjectStatsCounts 统计项目下事实、漏洞、对话数量（不含 sparse，由 project 包补全）。
func (s *Projects) GetProjectStatsCounts(projectID string) (*ProjectStats, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store: projects requires a database")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project_id 不能为空")
	}
	if _, err := s.GetProject(projectID); err != nil {
		return nil, err
	}
	stats := &ProjectStats{}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM project_facts WHERE project_id = ? AND confidence != 'deprecated'`,
		projectID,
	).Scan(&stats.FactCount); err != nil {
		return nil, fmt.Errorf("统计事实失败: %w", err)
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM vulnerabilities WHERE project_id = ?`,
		projectID,
	).Scan(&stats.VulnCount); err != nil {
		return nil, fmt.Errorf("统计漏洞失败: %w", err)
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM conversations WHERE project_id = ?`,
		projectID,
	).Scan(&stats.ConversationCount); err != nil {
		return nil, fmt.Errorf("统计对话失败: %w", err)
	}
	return stats, nil
}

// ListConversationsByProjectID 列出绑定到项目的对话。
func (c *Conversations) ListConversationsByProjectID(projectID string, limit, offset int) ([]*Conversation, error) {
	if c == nil || c.db == nil {
		return nil, errors.New("store: conversations requires a database")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := c.db.Query(
		`SELECT id, title, COALESCE(pinned, 0), created_at, updated_at, project_id, role_name
		 FROM conversations WHERE project_id = ? ORDER BY updated_at DESC LIMIT ? OFFSET ?`,
		projectID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("查询项目对话失败: %w", err)
	}
	defer rows.Close()

	var conversations []*Conversation
	for rows.Next() {
		var conv Conversation
		var createdAt, updatedAt string
		var pinned int
		var pid sql.NullString
		var roleName sql.NullString
		if err := rows.Scan(&conv.ID, &conv.Title, &pinned, &createdAt, &updatedAt, &pid, &roleName); err != nil {
			return nil, err
		}
		if pid.Valid {
			conv.ProjectID = strings.TrimSpace(pid.String)
		}
		if roleName.Valid {
			conv.RoleName = NormalizeConversationRoleName(roleName.String)
		}
		conv.CreatedAt = sqltime.Parse(createdAt)
		conv.UpdatedAt = sqltime.Parse(updatedAt)
		conv.Pinned = pinned != 0
		conversations = append(conversations, &conv)
	}
	return conversations, rows.Err()
}

// CountConversationsByProjectID 统计项目绑定对话数。
func (c *Conversations) CountConversationsByProjectID(projectID string) (int, error) {
	if c == nil || c.db == nil {
		return 0, errors.New("store: conversations requires a database")
	}
	var n int
	err := c.db.QueryRow(`SELECT COUNT(*) FROM conversations WHERE project_id = ?`, projectID).Scan(&n)
	return n, err
}
