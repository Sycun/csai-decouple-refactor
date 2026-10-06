package store

import (
	"cyberstrike-ai/internal/sqltime"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The edge half of the blackboard: the DAG over fact keys, and the three cascades a fact write
// triggers on it. All of it is the same store as the facts themselves, so those cascades are internal
// calls rather than an injected collaborator.

const edgeColumns = `id, project_id, source_fact_key, target_fact_key, edge_type, confidence,
	        COALESCE(source_conversation_id,''), created_at, updated_at`

// ListEdges 列出项目全部边。
func (s *Facts) ListEdges(projectID string) ([]*ProjectFactEdge, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT `+edgeColumns+`
		   FROM project_fact_edges
		  WHERE project_id = ?
		  ORDER BY created_at ASC, rowid ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// ListOutgoing 列出某事实的全部出边。
func (s *Facts) ListOutgoing(projectID, sourceFactKey string) ([]*ProjectFactEdge, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT `+edgeColumns+`
		   FROM project_fact_edges
		  WHERE project_id = ? AND source_fact_key = ?
		  ORDER BY created_at ASC, rowid ASC`,
		projectID, sourceFactKey,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// ListIncoming 列出某事实的全部入边。
func (s *Facts) ListIncoming(projectID, targetFactKey string) ([]*ProjectFactEdge, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT `+edgeColumns+`
		   FROM project_fact_edges
		  WHERE project_id = ? AND target_fact_key = ?
		  ORDER BY created_at ASC, rowid ASC`,
		projectID, targetFactKey,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// ReplaceOutgoing 替换某事实的全部出边（links 省略时不调用）。
//
// The delete and the inserts are deliberately not wrapped in a transaction: that is how the statements
// were written for as long as the console has been calling them, and a slice about ownership does not
// get to decide that a partial failure should now leave a different state behind.
func (s *Facts) ReplaceOutgoing(projectID, sourceFactKey, sourceConversationID string, inputs []ProjectFactEdgeInput) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	sourceFactKey = strings.TrimSpace(sourceFactKey)
	if sourceFactKey == "" {
		return fmt.Errorf("source_fact_key 不能为空")
	}
	if _, err := s.db.Exec(
		`DELETE FROM project_fact_edges WHERE project_id = ? AND source_fact_key = ?`,
		projectID, sourceFactKey,
	); err != nil {
		return fmt.Errorf("清除旧边失败: %w", err)
	}
	for _, in := range inputs {
		target := strings.TrimSpace(in.To)
		if target == "" {
			continue
		}
		if err := ValidateFactKey(target); err != nil {
			return fmt.Errorf("target fact_key 无效 (%s): %w", target, err)
		}
		if target == sourceFactKey {
			return fmt.Errorf("边不能指向自身: %s", sourceFactKey)
		}
		if err := ValidateProjectFactEdgeType(in.Type); err != nil {
			return err
		}
		edge := &ProjectFactEdge{
			ID:                   uuid.New().String(),
			ProjectID:            projectID,
			SourceFactKey:        sourceFactKey,
			TargetFactKey:        target,
			EdgeType:             strings.ToLower(strings.TrimSpace(in.Type)),
			Confidence:           normalizeEdgeConfidence(in.Confidence),
			SourceConversationID: sourceConversationID,
			CreatedAt:            time.Now(),
			UpdatedAt:            time.Now(),
		}
		if err := s.insertEdge(edge); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceIncoming 替换某事实的全部入边（From 为来源 fact_key）。
func (s *Facts) ReplaceIncoming(projectID, targetFactKey string, inputs []ProjectFactEdgeFromInput) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	targetFactKey = strings.TrimSpace(targetFactKey)
	if targetFactKey == "" {
		return fmt.Errorf("target_fact_key 不能为空")
	}
	if _, err := s.db.Exec(
		`DELETE FROM project_fact_edges WHERE project_id = ? AND target_fact_key = ?`,
		projectID, targetFactKey,
	); err != nil {
		return fmt.Errorf("清除旧入边失败: %w", err)
	}
	for _, in := range inputs {
		source := strings.TrimSpace(in.From)
		if source == "" {
			continue
		}
		if err := ValidateFactKey(source); err != nil {
			return fmt.Errorf("source fact_key 无效 (%s): %w", source, err)
		}
		if source == targetFactKey {
			return fmt.Errorf("边不能指向自身: %s", targetFactKey)
		}
		if err := ValidateProjectFactEdgeType(in.Type); err != nil {
			return err
		}
		sourceConversationID := ""
		if srcFact, err := s.GetByKey(projectID, source); err == nil && srcFact != nil {
			sourceConversationID = srcFact.SourceConversationID
		}
		edge := &ProjectFactEdge{
			ID:                   uuid.New().String(),
			ProjectID:            projectID,
			SourceFactKey:        source,
			TargetFactKey:        targetFactKey,
			EdgeType:             strings.ToLower(strings.TrimSpace(in.Type)),
			Confidence:           normalizeEdgeConfidence(in.Confidence),
			SourceConversationID: sourceConversationID,
			CreatedAt:            time.Now(),
			UpdatedAt:            time.Now(),
		}
		if err := s.insertEdge(edge); err != nil {
			return err
		}
	}
	return nil
}

// GetEdge 按 ID 获取边。Any read failure answers "边不存在", including a real database error - the
// handler turns that into a 404, and this preserved quirk is why.
func (s *Facts) GetEdge(edgeID string) (*ProjectFactEdge, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	var e ProjectFactEdge
	var createdAt, updatedAt string
	err := s.db.QueryRow(
		`SELECT `+edgeColumns+`
		   FROM project_fact_edges WHERE id = ?`, edgeID,
	).Scan(&e.ID, &e.ProjectID, &e.SourceFactKey, &e.TargetFactKey, &e.EdgeType, &e.Confidence,
		&e.SourceConversationID, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("边不存在")
	}
	e.CreatedAt = sqltime.Parse(createdAt)
	e.UpdatedAt = sqltime.Parse(updatedAt)
	return &e, nil
}

// AddEdge 新增单条边（已存在则更新 confidence）。
func (s *Facts) AddEdge(projectID string, in ProjectFactEdgeInput, sourceFactKey, sourceConversationID string) (*ProjectFactEdge, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	sourceFactKey = strings.TrimSpace(sourceFactKey)
	target := strings.TrimSpace(in.To)
	if sourceFactKey == "" || target == "" {
		return nil, fmt.Errorf("source 与 target 必填")
	}
	if sourceFactKey == target {
		return nil, fmt.Errorf("边不能指向自身")
	}
	if err := ValidateProjectFactEdgeType(in.Type); err != nil {
		return nil, err
	}
	if err := ValidateFactKey(target); err != nil {
		return nil, err
	}
	now := time.Now()
	e := &ProjectFactEdge{
		ID:                   uuid.New().String(),
		ProjectID:            projectID,
		SourceFactKey:        sourceFactKey,
		TargetFactKey:        target,
		EdgeType:             strings.ToLower(strings.TrimSpace(in.Type)),
		Confidence:           normalizeEdgeConfidence(in.Confidence),
		SourceConversationID: sourceConversationID,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	_, err := s.db.Exec(
		`INSERT INTO project_fact_edges (
			id, project_id, source_fact_key, target_fact_key, edge_type, confidence,
			source_conversation_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, source_fact_key, target_fact_key, edge_type)
		DO UPDATE SET confidence = excluded.confidence, updated_at = excluded.updated_at`,
		e.ID, e.ProjectID, e.SourceFactKey, e.TargetFactKey, e.EdgeType, e.Confidence,
		nullIfEmpty(e.SourceConversationID), e.CreatedAt, e.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("添加边失败: %w", err)
	}
	// 返回最新
	rows, err := s.db.Query(
		`SELECT `+edgeColumns+`
		   FROM project_fact_edges
		  WHERE project_id = ? AND source_fact_key = ? AND target_fact_key = ? AND edge_type = ?`,
		projectID, sourceFactKey, target, e.EdgeType,
	)
	if err != nil {
		return e, nil
	}
	defer rows.Close()
	list, err := scanEdges(rows)
	if err != nil || len(list) == 0 {
		return e, nil
	}
	return list[0], nil
}

// DeleteEdge 删除单条边。
func (s *Facts) DeleteEdge(edgeID string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	res, err := s.db.Exec(`DELETE FROM project_fact_edges WHERE id = ?`, edgeID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("边不存在")
	}
	return nil
}

func (s *Facts) insertEdge(e *ProjectFactEdge) error {
	_, err := s.db.Exec(
		`INSERT INTO project_fact_edges (
			id, project_id, source_fact_key, target_fact_key, edge_type, confidence,
			source_conversation_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.ProjectID, e.SourceFactKey, e.TargetFactKey, e.EdgeType, e.Confidence,
		nullIfEmpty(e.SourceConversationID), e.CreatedAt, e.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("写入边失败: %w", err)
	}
	return nil
}

// RenameKeyEdges 事实 key 变更时同步边上的引用。
func (s *Facts) RenameKeyEdges(projectID, oldKey, newKey string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	oldKey = strings.TrimSpace(oldKey)
	newKey = strings.TrimSpace(newKey)
	if oldKey == "" || newKey == "" || oldKey == newKey {
		return nil
	}
	now := time.Now()
	if _, err := s.db.Exec(
		`UPDATE project_fact_edges SET source_fact_key = ?, updated_at = ?
		  WHERE project_id = ? AND source_fact_key = ?`,
		newKey, now, projectID, oldKey,
	); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`UPDATE project_fact_edges SET target_fact_key = ?, updated_at = ?
		  WHERE project_id = ? AND target_fact_key = ?`,
		newKey, now, projectID, oldKey,
	)
	return err
}

// DeleteEdgesForKey 删除与某 fact_key 相关的全部边。
func (s *Facts) DeleteEdgesForKey(projectID, factKey string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`DELETE FROM project_fact_edges
		  WHERE project_id = ? AND (source_fact_key = ? OR target_fact_key = ?)`,
		projectID, factKey, factKey,
	)
	return err
}

// DeprecateEdgesForKey 将关联边标记为 deprecated。
func (s *Facts) DeprecateEdgesForKey(projectID, factKey string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	now := time.Now()
	_, err := s.db.Exec(
		`UPDATE project_fact_edges SET confidence = 'deprecated', updated_at = ?
		  WHERE project_id = ? AND (source_fact_key = ? OR target_fact_key = ?)
		    AND confidence != 'deprecated'`,
		now, projectID, factKey, factKey,
	)
	return err
}

func normalizeEdgeConfidence(confidence string) string {
	confidence = strings.TrimSpace(strings.ToLower(confidence))
	switch confidence {
	case "confirmed", "deprecated":
		return confidence
	default:
		return "tentative"
	}
}

func scanEdges(rows *sql.Rows) ([]*ProjectFactEdge, error) {
	var out []*ProjectFactEdge
	for rows.Next() {
		var e ProjectFactEdge
		var createdAt, updatedAt string
		if err := rows.Scan(
			&e.ID, &e.ProjectID, &e.SourceFactKey, &e.TargetFactKey, &e.EdgeType, &e.Confidence,
			&e.SourceConversationID, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		e.CreatedAt = sqltime.Parse(createdAt)
		e.UpdatedAt = sqltime.Parse(updatedAt)
		out = append(out, &e)
	}
	return out, rows.Err()
}
