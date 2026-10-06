package store

import (
	"cyberstrike-ai/internal/sqltime"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Facts owns the project blackboard's persistence: the fact rows, the edges that link them into a
// DAG, the two tables' schema, and the unlink the findings domain owes them when a finding is deleted.
//
// One type for both tables because they are one domain - the cascades a fact write triggers
// (deprecate marks its edges, delete removes them, rename moves both ends) cross from one table to the
// other inside the same call. Splitting them would turn an internal call into an injected collaborator
// for no ownership gain.
//
// The statements here were copied out of internal/database verbatim: same SELECT lists (including the
// COALESCEs that keep pre-migration rows readable), same ORDER BY, same error strings, and the same
// non-transactional delete-then-insert shape of the two Replace* methods. Where a behaviour looked
// wrong - GetEdge answering "边不存在" for a database error as well as for a missing row, AddEdge
// returning the in-memory edge when its follow-up read fails, Restore leaving an edge it marked
// deprecated behind - it is preserved, because this slice is about ownership, not about fixing.
type Facts struct {
	db *sql.DB
}

func NewFacts(db *sql.DB) *Facts { return &Facts{db: db} }

func (s *Facts) requireDB() error {
	if s == nil || s.db == nil {
		return errors.New("store: facts requires a database")
	}
	return nil
}

// EnsureSchema creates both tables and their six indexes. It runs after the projects table, since
// both carry a foreign key onto it.
func (s *Facts) EnsureSchema() error {
	if err := s.requireDB(); err != nil {
		return err
	}
	if _, err := s.db.Exec(factsSchema); err != nil {
		return fmt.Errorf("创建黑板表失败: %w", err)
	}
	if _, err := s.db.Exec(factsIndexes); err != nil {
		return fmt.Errorf("创建黑板索引失败: %w", err)
	}
	return nil
}

const factsSchema = `
CREATE TABLE IF NOT EXISTS project_facts (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		fact_key TEXT NOT NULL,
		category TEXT NOT NULL DEFAULT 'note',
		summary TEXT NOT NULL DEFAULT '',
		body TEXT,
		confidence TEXT NOT NULL DEFAULT 'tentative',
		source_conversation_id TEXT,
		source_message_id TEXT,
		pinned INTEGER NOT NULL DEFAULT 0,
		related_vulnerability_id TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
		UNIQUE(project_id, fact_key)
	);

	CREATE TABLE IF NOT EXISTS project_fact_edges (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		source_fact_key TEXT NOT NULL,
		target_fact_key TEXT NOT NULL,
		edge_type TEXT NOT NULL,
		confidence TEXT NOT NULL DEFAULT 'tentative',
		source_conversation_id TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
		UNIQUE(project_id, source_fact_key, target_fact_key, edge_type)
	);`

const factsIndexes = `
CREATE INDEX IF NOT EXISTS idx_project_facts_project_id ON project_facts(project_id);
	CREATE INDEX IF NOT EXISTS idx_project_facts_confidence ON project_facts(confidence);
	CREATE INDEX IF NOT EXISTS idx_project_facts_related_vuln ON project_facts(related_vulnerability_id);
	CREATE INDEX IF NOT EXISTS idx_project_fact_edges_project ON project_fact_edges(project_id);
	CREATE INDEX IF NOT EXISTS idx_project_fact_edges_source ON project_fact_edges(project_id, source_fact_key);
	CREATE INDEX IF NOT EXISTS idx_project_fact_edges_target ON project_fact_edges(project_id, target_fact_key);`

// factColumns is the read shape every fact query uses. body is COALESCEd because the column is
// nullable and the console concatenates it; the two source ids and the finding link are COALESCEd too,
// and the link is what the caller compares against "" to decide whether to emit the JSON key at all.
const factColumns = `id, project_id, fact_key, category, summary, COALESCE(body,''), confidence,
			COALESCE(source_conversation_id,''), COALESCE(source_message_id,''), pinned,
			COALESCE(related_vulnerability_id,''), created_at, updated_at`

// ListForIndex 列出用于黑板索引注入的事实（不含 deprecated，除非 includeDeprecated）。
func (s *Facts) ListForIndex(projectID string, includeDeprecated bool) ([]*ProjectFact, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	query := `SELECT ` + factColumns + `
		FROM project_facts WHERE project_id = ?`
	args := []interface{}{projectID}
	if !includeDeprecated {
		query += " AND confidence != 'deprecated'"
	}
	query += " ORDER BY pinned DESC, updated_at DESC"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

// List 分页列出项目事实。
func (s *Facts) List(projectID string, filter ProjectFactListFilter, limit, offset int) ([]*ProjectFact, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT ` + factColumns + `
		FROM project_facts WHERE project_id = ?`
	args := []interface{}{projectID}
	if c := strings.TrimSpace(filter.Category); c != "" {
		query += " AND category = ?"
		args = append(args, c)
	}
	if c := strings.TrimSpace(filter.Confidence); c != "" {
		query += " AND confidence = ?"
		args = append(args, c)
	}
	if filter.ExcludeDeprecated {
		query += " AND confidence != 'deprecated'"
	}
	if rid := strings.TrimSpace(filter.RelatedVulnerabilityID); rid != "" {
		query += " AND related_vulnerability_id = ?"
		args = append(args, rid)
	}
	if q := strings.TrimSpace(filter.Search); q != "" {
		pat := "%" + q + "%"
		query += " AND (fact_key LIKE ? OR summary LIKE ? OR body LIKE ?)"
		args = append(args, pat, pat, pat)
	}
	query += " ORDER BY pinned DESC, updated_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

// ListForSparseCheck 返回用于待补全检测的事实字段（非 deprecated）。
func (s *Facts) ListForSparseCheck(projectID string) ([]ProjectFactSparseRow, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT category, fact_key, COALESCE(body,'') FROM project_facts WHERE project_id = ? AND confidence != 'deprecated'`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectFactSparseRow
	for rows.Next() {
		var row ProjectFactSparseRow
		if err := rows.Scan(&row.Category, &row.FactKey, &row.Body); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetByKey 按 key 获取事实。
func (s *Facts) GetByKey(projectID, factKey string) (*ProjectFact, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	row := s.db.QueryRow(
		`SELECT `+factColumns+`
			 FROM project_facts WHERE project_id = ? AND fact_key = ?`,
		projectID, factKey,
	)
	return scanFactRow(row)
}

// Get 按 ID 获取事实。
func (s *Facts) Get(id string) (*ProjectFact, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	row := s.db.QueryRow(
		`SELECT `+factColumns+`
			 FROM project_facts WHERE id = ?`, id,
	)
	return scanFactRow(row)
}

// mergeFactBody 更新时若 incoming body 为空则保留已有内容，避免仅改 summary 时丢失攻击链。
func mergeFactBody(incoming, existing string) string {
	if strings.TrimSpace(incoming) == "" {
		return existing
	}
	return incoming
}

// Upsert 创建或更新事实（按 project_id + fact_key）。
func (s *Facts) Upsert(f *ProjectFact) (*ProjectFact, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	if err := ValidateFactKey(f.FactKey); err != nil {
		return nil, err
	}
	if strings.TrimSpace(f.Category) == "" {
		f.Category = "note"
	}
	if strings.TrimSpace(f.Confidence) == "" {
		f.Confidence = "tentative"
	}
	now := time.Now()

	existing, err := s.GetByKey(f.ProjectID, f.FactKey)
	if err == nil && existing != nil {
		f.ID = existing.ID
		f.CreatedAt = existing.CreatedAt
		f.UpdatedAt = now
		f.Body = mergeFactBody(f.Body, existing.Body)
		if strings.TrimSpace(f.Category) == "" {
			f.Category = existing.Category
		}
		if strings.TrimSpace(f.Confidence) == "" {
			f.Confidence = existing.Confidence
		}
		_, err = s.db.Exec(
			`UPDATE project_facts SET category = ?, summary = ?, body = ?, confidence = ?,
				source_conversation_id = COALESCE(?, source_conversation_id),
				source_message_id = COALESCE(?, source_message_id),
				pinned = ?, related_vulnerability_id = ?, updated_at = ?
				 WHERE id = ?`,
			f.Category, f.Summary, f.Body, f.Confidence,
			nullIfEmpty(f.SourceConversationID), nullIfEmpty(f.SourceMessageID), boolToInt(f.Pinned),
			nullIfEmpty(f.RelatedVulnerabilityID), f.UpdatedAt, f.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("更新事实失败: %w", err)
		}
		return f, nil
	}

	if f.ID == "" {
		f.ID = uuid.New().String()
	}
	f.CreatedAt = now
	f.UpdatedAt = now
	_, err = s.db.Exec(
		`INSERT INTO project_facts (
			id, project_id, fact_key, category, summary, body, confidence,
			source_conversation_id, source_message_id, pinned, related_vulnerability_id,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.ProjectID, f.FactKey, f.Category, f.Summary, f.Body, f.Confidence,
		nullIfEmpty(f.SourceConversationID), nullIfEmpty(f.SourceMessageID), boolToInt(f.Pinned),
		nullIfEmpty(f.RelatedVulnerabilityID),
		f.CreatedAt, f.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("创建事实失败: %w", err)
	}
	return f, nil
}

// Deprecate 将事实标记为 deprecated（关联边同步 deprecated）。
func (s *Facts) Deprecate(projectID, factKey string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	res, err := s.db.Exec(
		`UPDATE project_facts SET confidence = 'deprecated', updated_at = ? WHERE project_id = ? AND fact_key = ?`,
		time.Now(), projectID, factKey,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("事实不存在")
	}
	return s.DeprecateEdgesForKey(projectID, factKey)
}

// Restore 将已废弃事实恢复为 tentative 或 confirmed（重新参与黑板索引）。
func (s *Facts) Restore(projectID, factKey, confidence string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	confidence = strings.TrimSpace(strings.ToLower(confidence))
	if confidence == "" {
		confidence = "tentative"
	}
	if confidence != "confirmed" && confidence != "tentative" {
		return fmt.Errorf("confidence 须为 confirmed 或 tentative")
	}

	existing, err := s.GetByKey(projectID, factKey)
	if err != nil {
		return fmt.Errorf("事实不存在")
	}
	if strings.ToLower(strings.TrimSpace(existing.Confidence)) != "deprecated" {
		return fmt.Errorf("事实未处于废弃状态")
	}

	_, err = s.db.Exec(
		`UPDATE project_facts SET confidence = ?, updated_at = ? WHERE project_id = ? AND fact_key = ?`,
		confidence, time.Now(), projectID, factKey,
	)
	return err
}

// Delete 删除事实（级联删除相关边）。
func (s *Facts) Delete(id string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	f, err := s.Get(id)
	if err != nil {
		return err
	}
	if err := s.DeleteEdgesForKey(f.ProjectID, f.FactKey); err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM project_facts WHERE id = ?`, id)
	return err
}

// UnlinkFindingReferences clears the fact rows that point at the given findings. It runs on the
// caller's transaction, so a finding deleted without clearing its references cannot be observed: the
// console renders such a leftover as a dangling short id.
//
// The ids are chunked because SQLite caps how many host parameters one statement may carry, and a
// batch delete that matches thousands of findings has to keep working the way it did when the clear
// was a subquery.
func (s *Facts) UnlinkFindingReferences(tx *sql.Tx, findingIDs []string) error {
	if tx == nil {
		return errors.New("store: facts unlink requires the caller's transaction")
	}
	const chunk = 500
	for start := 0; start < len(findingIDs); start += chunk {
		end := start + chunk
		if end > len(findingIDs) {
			end = len(findingIDs)
		}
		batch := findingIDs[start:end]
		args := make([]interface{}, 0, len(batch)+1)
		placeholders := make([]string, 0, len(batch))
		for _, id := range batch {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		query := "UPDATE project_facts SET related_vulnerability_id = NULL WHERE related_vulnerability_id IN (" + strings.Join(placeholders, ",") + ")"
		if _, err := tx.Exec(query, args...); err != nil {
			return err
		}
	}
	return nil
}

func scanFacts(rows *sql.Rows) ([]*ProjectFact, error) {
	var out []*ProjectFact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func scanFactRow(row *sql.Row) (*ProjectFact, error) {
	var f ProjectFact
	var pinned int
	var createdAt, updatedAt string
	err := row.Scan(
		&f.ID, &f.ProjectID, &f.FactKey, &f.Category, &f.Summary, &f.Body, &f.Confidence,
		&f.SourceConversationID, &f.SourceMessageID, &pinned,
		&f.RelatedVulnerabilityID, &createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("事实不存在")
		}
		return nil, err
	}
	f.Pinned = pinned != 0
	f.CreatedAt = sqltime.Parse(createdAt)
	f.UpdatedAt = sqltime.Parse(updatedAt)
	return &f, nil
}

func scanFact(rows *sql.Rows) (*ProjectFact, error) {
	var f ProjectFact
	var pinned int
	var createdAt, updatedAt string
	err := rows.Scan(
		&f.ID, &f.ProjectID, &f.FactKey, &f.Category, &f.Summary, &f.Body, &f.Confidence,
		&f.SourceConversationID, &f.SourceMessageID, &pinned,
		&f.RelatedVulnerabilityID, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	f.Pinned = pinned != 0
	f.CreatedAt = sqltime.Parse(createdAt)
	f.UpdatedAt = sqltime.Parse(updatedAt)
	return &f, nil
}
