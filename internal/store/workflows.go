package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Workflows owns the five tables the workflow engine records in: the definition the graph was
// compiled from, the run, the per-node runs, and the two package-exchange tables (an inspection is
// what a proposed import was checked against; an import is what was applied, idempotently).
//
// The run state is why this store is its own thing: while these rows sat on the connection wrapper,
// every Eino graph node could reach every table in the application through the handle it was handed.
// Owning them here is what lets internal/workflow declare its own ledger instead of importing the
// data layer.
type Workflows struct {
	db *sql.DB
}

func NewWorkflows(db *sql.DB) *Workflows {
	return &Workflows{db: db}
}

// requireDB guards every method rather than relying on the caller: a handler built without a
// connection holds a connectionless store, and the panic that would otherwise follow is what the
// wiring test in internal/handler caught while these guards were still missing.
func (w *Workflows) requireDB() error {
	if w == nil || w.db == nil {
		return errors.New("store: workflows require a database")
	}
	return nil
}

// workflowSchema builds the five tables and their nine indexes in foreign-key order: workflow_runs
// before workflow_node_runs (the latter references it), workflow_package_inspections before
// workflow_package_imports. It has to be applied after conversations, because
// workflow_runs.conversation_id references it - the boot ordering is asserted by
// TestSchemaEnsuresAreWiredAtBoot.
const workflowSchema = `
	CREATE TABLE IF NOT EXISTS workflow_definitions (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		version INTEGER NOT NULL DEFAULT 1,
		graph_json TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);
	CREATE TABLE IF NOT EXISTS workflow_runs (
		id TEXT PRIMARY KEY,
		workflow_id TEXT NOT NULL,
		workflow_version INTEGER NOT NULL DEFAULT 1,
		conversation_id TEXT,
		project_id TEXT,
		role_id TEXT,
		status TEXT NOT NULL,
		input_json TEXT,
		output_json TEXT,
		error TEXT,
		pending_hitl_node_id TEXT,
		pending_hitl_json TEXT,
		started_at DATETIME NOT NULL,
		finished_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE SET NULL
	);
	CREATE TABLE IF NOT EXISTS workflow_node_runs (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		status TEXT NOT NULL,
		input_json TEXT,
		output_json TEXT,
		error TEXT,
		started_at DATETIME NOT NULL,
		finished_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (run_id) REFERENCES workflow_runs(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS workflow_package_inspections (
		id TEXT PRIMARY KEY, package_hash TEXT NOT NULL, manifest_json TEXT NOT NULL,
		workflow_payload_json TEXT NOT NULL, inspection_json TEXT NOT NULL,
		source_workflow_id TEXT NOT NULL, source_revision INTEGER NOT NULL,
		source_content_hash TEXT NOT NULL, source_graph_hash TEXT NOT NULL,
		local_conflict_state TEXT NOT NULL CHECK (local_conflict_state IN ('none','identical','id_conflict')),
		local_workflow_id TEXT, local_content_hash TEXT, local_graph_hash TEXT,
		created_by TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'ready' CHECK (status IN ('ready','consumed','expired')),
		created_at DATETIME NOT NULL, expires_at DATETIME NOT NULL, consumed_at DATETIME
	);
	CREATE TABLE IF NOT EXISTS workflow_package_imports (
		id TEXT PRIMARY KEY, inspection_id TEXT NOT NULL, request_hash TEXT NOT NULL,
		idempotency_key TEXT NOT NULL, actor_user_id TEXT NOT NULL,
		action TEXT NOT NULL CHECK (action IN ('create','keep_existing','overwrite','rename')),
		source_workflow_id TEXT NOT NULL, target_workflow_id TEXT NOT NULL, resulting_workflow_id TEXT,
		result TEXT NOT NULL CHECK (result IN ('created','overwritten','renamed','kept_existing','skipped_identical','failed')),
		error_code TEXT, error_message TEXT, created_at DATETIME NOT NULL, applied_at DATETIME,
		FOREIGN KEY (inspection_id) REFERENCES workflow_package_inspections(id)
	);
	CREATE INDEX IF NOT EXISTS idx_workflow_definitions_updated_at ON workflow_definitions(updated_at);
	CREATE INDEX IF NOT EXISTS idx_workflow_definitions_enabled ON workflow_definitions(enabled);
	CREATE INDEX IF NOT EXISTS idx_workflow_runs_workflow ON workflow_runs(workflow_id);
	CREATE INDEX IF NOT EXISTS idx_workflow_runs_conversation ON workflow_runs(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_workflow_runs_status ON workflow_runs(status);
	CREATE INDEX IF NOT EXISTS idx_workflow_node_runs_run ON workflow_node_runs(run_id);
	CREATE INDEX IF NOT EXISTS idx_workflow_package_inspections_creator_expiry ON workflow_package_inspections(created_by, expires_at);
	CREATE UNIQUE INDEX IF NOT EXISTS uq_workflow_package_imports_actor_key ON workflow_package_imports(actor_user_id, idempotency_key);
	CREATE UNIQUE INDEX IF NOT EXISTS uq_workflow_package_imports_inspection_success ON workflow_package_imports(inspection_id) WHERE result IN ('created','overwritten','renamed','kept_existing','skipped_identical');`

func (w *Workflows) EnsureSchema() error {
	if err := w.requireDB(); err != nil {
		return err
	}
	if _, err := w.db.Exec(workflowSchema); err != nil {
		return fmt.Errorf("create workflow tables: %w", err)
	}
	return nil
}

// WorkflowDefinition is a persisted user-defined graph/workflow template.
// graph_json intentionally remains opaque so users can define their own fields.
type WorkflowDefinition struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Version     int       `json:"version"`
	GraphJSON   string    `json:"graph_json"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type WorkflowRun struct {
	ID                string     `json:"id"`
	WorkflowID        string     `json:"workflow_id"`
	WorkflowVersion   int        `json:"workflow_version"`
	ConversationID    string     `json:"conversation_id,omitempty"`
	ProjectID         string     `json:"project_id,omitempty"`
	RoleID            string     `json:"role_id,omitempty"`
	Status            string     `json:"status"`
	InputJSON         string     `json:"input_json,omitempty"`
	OutputJSON        string     `json:"output_json,omitempty"`
	Error             string     `json:"error,omitempty"`
	PendingHITLNodeID string     `json:"pending_hitl_node_id,omitempty"`
	PendingHITLJSON   string     `json:"pending_hitl_json,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
}

type WorkflowNodeRun struct {
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	NodeID     string     `json:"node_id"`
	Status     string     `json:"status"`
	InputJSON  string     `json:"input_json,omitempty"`
	OutputJSON string     `json:"output_json,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func scanWorkflowNodeRun(scanner interface {
	Scan(dest ...interface{}) error
}) (*WorkflowNodeRun, error) {
	var row WorkflowNodeRun
	var inputJSON, outputJSON, errText sql.NullString
	var finishedAt sql.NullTime
	if err := scanner.Scan(&row.ID, &row.RunID, &row.NodeID, &row.Status, &inputJSON, &outputJSON, &errText, &row.StartedAt, &finishedAt); err != nil {
		return nil, err
	}
	row.InputJSON = inputJSON.String
	row.OutputJSON = outputJSON.String
	row.Error = errText.String
	if finishedAt.Valid {
		t := finishedAt.Time
		row.FinishedAt = &t
	}
	return &row, nil
}

func scanWorkflowDefinition(scanner interface {
	Scan(dest ...interface{}) error
}) (*WorkflowDefinition, error) {
	var row WorkflowDefinition
	var desc sql.NullString
	var enabled int
	if err := scanner.Scan(&row.ID, &row.Name, &desc, &row.Version, &row.GraphJSON, &enabled, &row.CreatedAt, &row.UpdatedAt); err != nil {
		return nil, err
	}
	row.Description = desc.String
	row.Enabled = enabled != 0
	return &row, nil
}

const workflowDefinitionColumns = `id, name, description, version, graph_json, enabled, created_at, updated_at`

func (w *Workflows) ListWorkflowDefinitions(includeDisabled bool) ([]*WorkflowDefinition, error) {
	if err := w.requireDB(); err != nil {
		return nil, err
	}
	query := "SELECT " + workflowDefinitionColumns + " FROM workflow_definitions"
	if !includeDisabled {
		query += " WHERE enabled = 1"
	}
	query += " ORDER BY updated_at DESC"
	rows, err := w.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("查询工作流列表失败: %w", err)
	}
	defer rows.Close()

	var out []*WorkflowDefinition
	for rows.Next() {
		wf, err := scanWorkflowDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描工作流失败: %w", err)
		}
		out = append(out, wf)
	}
	return out, rows.Err()
}

func (w *Workflows) GetWorkflowDefinition(id string) (*WorkflowDefinition, error) {
	if err := w.requireDB(); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	wf, err := scanWorkflowDefinition(w.db.QueryRow("SELECT "+workflowDefinitionColumns+" FROM workflow_definitions WHERE id = ?", id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询工作流失败: %w", err)
	}
	return wf, nil
}

func (w *Workflows) UpsertWorkflowDefinition(wf *WorkflowDefinition) error {
	if err := w.requireDB(); err != nil {
		return err
	}
	if wf == nil {
		return fmt.Errorf("工作流为空")
	}
	wf.ID = strings.TrimSpace(wf.ID)
	wf.Name = strings.TrimSpace(wf.Name)
	if wf.ID == "" || wf.Name == "" {
		return fmt.Errorf("工作流 id 和 name 不能为空")
	}
	if strings.TrimSpace(wf.GraphJSON) == "" {
		wf.GraphJSON = `{"nodes":[],"edges":[],"config":{}}`
	}
	if wf.Version <= 0 {
		wf.Version = 1
	}
	now := time.Now()
	existing, err := w.GetWorkflowDefinition(wf.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		_, err = w.db.Exec(
			`INSERT INTO workflow_definitions (id, name, description, version, graph_json, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			wf.ID, wf.Name, wf.Description, wf.Version, wf.GraphJSON, boolToInt(wf.Enabled), now, now,
		)
	} else {
		nextVersion := existing.Version + 1
		if wf.Version > existing.Version {
			nextVersion = wf.Version
		}
		_, err = w.db.Exec(
			`UPDATE workflow_definitions
			 SET name = ?, description = ?, version = ?, graph_json = ?, enabled = ?, updated_at = ?
			 WHERE id = ?`,
			wf.Name, wf.Description, nextVersion, wf.GraphJSON, boolToInt(wf.Enabled), now, wf.ID,
		)
	}
	if err != nil {
		return fmt.Errorf("保存工作流失败: %w", err)
	}
	return nil
}

func (w *Workflows) DeleteWorkflowDefinition(id string) error {
	if err := w.requireDB(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("工作流 id 不能为空")
	}
	if _, err := w.db.Exec("DELETE FROM workflow_definitions WHERE id = ?", id); err != nil {
		return fmt.Errorf("删除工作流失败: %w", err)
	}
	return nil
}

func (w *Workflows) CreateWorkflowRun(run *WorkflowRun) error {
	if err := w.requireDB(); err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("工作流运行为空")
	}
	if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.WorkflowID) == "" {
		return fmt.Errorf("工作流运行 id 和 workflow_id 不能为空")
	}
	if run.WorkflowVersion <= 0 {
		run.WorkflowVersion = 1
	}
	if strings.TrimSpace(run.Status) == "" {
		run.Status = "running"
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	_, err := w.db.Exec(
		`INSERT INTO workflow_runs (id, workflow_id, workflow_version, conversation_id, project_id, role_id, status, input_json, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.WorkflowID, run.WorkflowVersion, nullString(run.ConversationID), nullString(run.ProjectID), nullString(run.RoleID), run.Status, run.InputJSON, run.StartedAt,
	)
	if err != nil {
		return fmt.Errorf("创建工作流运行失败: %w", err)
	}
	return nil
}

func (w *Workflows) FinishWorkflowRun(runID, status, outputJSON, errText string) error {
	if err := w.requireDB(); err != nil {
		return err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("工作流运行 id 不能为空")
	}
	if strings.TrimSpace(status) == "" {
		status = "completed"
	}
	now := time.Now()
	_, err := w.db.Exec(
		`UPDATE workflow_runs SET status = ?, output_json = ?, error = ?, finished_at = ? WHERE id = ?`,
		status, outputJSON, errText, now, runID,
	)
	if err != nil {
		return fmt.Errorf("更新工作流运行失败: %w", err)
	}
	return nil
}

func (w *Workflows) CreateWorkflowNodeRun(n *WorkflowNodeRun) error {
	if err := w.requireDB(); err != nil {
		return err
	}
	if n == nil {
		return fmt.Errorf("工作流节点运行为空")
	}
	if strings.TrimSpace(n.ID) == "" || strings.TrimSpace(n.RunID) == "" || strings.TrimSpace(n.NodeID) == "" {
		return fmt.Errorf("节点运行 id、run_id 和 node_id 不能为空")
	}
	if strings.TrimSpace(n.Status) == "" {
		n.Status = "running"
	}
	if n.StartedAt.IsZero() {
		n.StartedAt = time.Now()
	}
	_, err := w.db.Exec(
		`INSERT INTO workflow_node_runs (id, run_id, node_id, status, input_json, started_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		n.ID, n.RunID, n.NodeID, n.Status, n.InputJSON, n.StartedAt,
	)
	if err != nil {
		return fmt.Errorf("创建工作流节点运行失败: %w", err)
	}
	return nil
}

func (w *Workflows) FinishWorkflowNodeRun(nodeRunID, status, outputJSON, errText string) error {
	if err := w.requireDB(); err != nil {
		return err
	}
	nodeRunID = strings.TrimSpace(nodeRunID)
	if nodeRunID == "" {
		return fmt.Errorf("节点运行 id 不能为空")
	}
	if strings.TrimSpace(status) == "" {
		status = "completed"
	}
	now := time.Now()
	_, err := w.db.Exec(
		`UPDATE workflow_node_runs SET status = ?, output_json = ?, error = ?, finished_at = ? WHERE id = ?`,
		status, outputJSON, errText, now, nodeRunID,
	)
	if err != nil {
		return fmt.Errorf("更新工作流节点运行失败: %w", err)
	}
	return nil
}

func (w *Workflows) ListWorkflowNodeRuns(runID string) ([]*WorkflowNodeRun, error) {
	if err := w.requireDB(); err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("工作流运行 id 不能为空")
	}
	rows, err := w.db.Query(
		`SELECT id, run_id, node_id, status, input_json, output_json, error, started_at, finished_at
		 FROM workflow_node_runs WHERE run_id = ? ORDER BY started_at ASC`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("查询工作流节点运行失败: %w", err)
	}
	defer rows.Close()
	var out []*WorkflowNodeRun
	for rows.Next() {
		row, err := scanWorkflowNodeRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanWorkflowRun(scanner interface {
	Scan(dest ...interface{}) error
}) (*WorkflowRun, error) {
	var row WorkflowRun
	var convID, projectID, roleID, inputJSON, outputJSON, errText, pendingNode, pendingJSON sql.NullString
	var finishedAt sql.NullTime
	if err := scanner.Scan(
		&row.ID, &row.WorkflowID, &row.WorkflowVersion,
		&convID, &projectID, &roleID, &row.Status,
		&inputJSON, &outputJSON, &errText,
		&pendingNode, &pendingJSON,
		&row.StartedAt, &finishedAt,
	); err != nil {
		return nil, err
	}
	row.ConversationID = convID.String
	row.ProjectID = projectID.String
	row.RoleID = roleID.String
	row.InputJSON = inputJSON.String
	row.OutputJSON = outputJSON.String
	row.Error = errText.String
	row.PendingHITLNodeID = pendingNode.String
	row.PendingHITLJSON = pendingJSON.String
	if finishedAt.Valid {
		t := finishedAt.Time
		row.FinishedAt = &t
	}
	return &row, nil
}

const workflowRunColumns = `id, workflow_id, workflow_version, conversation_id, project_id, role_id, status, input_json, output_json, error, pending_hitl_node_id, pending_hitl_json, started_at, finished_at`

func (w *Workflows) GetWorkflowRun(runID string) (*WorkflowRun, error) {
	if err := w.requireDB(); err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	row, err := scanWorkflowRun(w.db.QueryRow("SELECT "+workflowRunColumns+" FROM workflow_runs WHERE id = ?", runID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询工作流运行失败: %w", err)
	}
	return row, nil
}

func (w *Workflows) SetWorkflowRunStatus(runID, status string) error {
	if err := w.requireDB(); err != nil {
		return err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("工作流运行 id 不能为空")
	}
	_, err := w.db.Exec(`UPDATE workflow_runs SET status = ? WHERE id = ?`, strings.TrimSpace(status), runID)
	if err != nil {
		return fmt.Errorf("更新工作流运行状态失败: %w", err)
	}
	return nil
}

func (w *Workflows) SetWorkflowRunAwaitingHITL(runID, nodeID, pendingJSON string) error {
	if err := w.requireDB(); err != nil {
		return err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("工作流运行 id 不能为空")
	}
	_, err := w.db.Exec(
		`UPDATE workflow_runs SET status = 'awaiting_hitl', pending_hitl_node_id = ?, pending_hitl_json = ?, finished_at = NULL WHERE id = ?`,
		strings.TrimSpace(nodeID), pendingJSON, runID,
	)
	if err != nil {
		return fmt.Errorf("更新工作流 HITL 等待状态失败: %w", err)
	}
	return nil
}

// RecordWorkflowRunHITLDecision stores a human decision on a paused workflow run.
func (w *Workflows) RecordWorkflowRunHITLDecision(runID string, approved bool, comment string) error {
	if err := w.requireDB(); err != nil {
		return err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("工作流运行 id 不能为空")
	}
	run, err := w.GetWorkflowRun(runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("工作流运行不存在")
	}
	pending := map[string]interface{}{}
	if strings.TrimSpace(run.PendingHITLJSON) != "" {
		_ = json.Unmarshal([]byte(run.PendingHITLJSON), &pending)
	}
	if approved {
		pending["decision"] = "approved"
	} else {
		pending["decision"] = "rejected"
	}
	pending["comment"] = strings.TrimSpace(comment)
	raw, _ := json.Marshal(pending)
	_, err = w.db.Exec(
		`UPDATE workflow_runs SET pending_hitl_json = ? WHERE id = ? AND status = 'awaiting_hitl'`,
		string(raw), runID,
	)
	if err != nil {
		return fmt.Errorf("记录工作流审批决定失败: %w", err)
	}
	return nil
}

// ListWorkflowRunsAwaitingHITLFiltered returns awaiting_hitl runs, optionally scoped to a conversation.
func (w *Workflows) ListWorkflowRunsAwaitingHITLFiltered(conversationID string, limit int) ([]*WorkflowRun, error) {
	if err := w.requireDB(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	conversationID = strings.TrimSpace(conversationID)
	var rows *sql.Rows
	var err error
	if conversationID != "" {
		rows, err = w.db.Query(
			`SELECT `+workflowRunColumns+` FROM workflow_runs WHERE status = 'awaiting_hitl' AND conversation_id = ? ORDER BY started_at DESC LIMIT ?`,
			conversationID, limit,
		)
	} else {
		rows, err = w.db.Query(
			`SELECT `+workflowRunColumns+` FROM workflow_runs WHERE status = 'awaiting_hitl' ORDER BY started_at DESC LIMIT ?`,
			limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("查询等待审批的工作流运行失败: %w", err)
	}
	defer rows.Close()
	var out []*WorkflowRun
	for rows.Next() {
		row, err := scanWorkflowRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// MigrateRunsTable backfills the two columns the HITL handshake added after the table existed.
// It is deliberately a separate step from EnsureSchema: the boot path treats a failed backfill as a
// warning and carries on, while a table it cannot create stops the start-up.
func (w *Workflows) MigrateRunsTable() error {
	if err := w.requireDB(); err != nil {
		return err
	}
	cols := []struct{ name, ddl string }{
		{"pending_hitl_node_id", "ALTER TABLE workflow_runs ADD COLUMN pending_hitl_node_id TEXT"},
		{"pending_hitl_json", "ALTER TABLE workflow_runs ADD COLUMN pending_hitl_json TEXT"},
	}
	for _, col := range cols {
		var count int
		err := w.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('workflow_runs') WHERE name=?", col.name).Scan(&count)
		if err != nil || count > 0 {
			continue
		}
		if _, err := w.db.Exec(col.ddl); err != nil {
			errMsg := strings.ToLower(err.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				return err
			}
		}
	}
	return nil
}

func nullString(v string) interface{} {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return v
}
