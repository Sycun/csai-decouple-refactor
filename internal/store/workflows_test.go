package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// The definitions and the run ledger came over from the connection wrapper. These cases run against a
// real database, and one of them exists because a test caught a panic: the moved methods had no
// connection guard at all, so a handler built without a database dereferenced a nil *sql.DB instead
// of answering with an error. See TestWorkflowsRefuseAConnectionlessHandle.

const workflowJoinedSchema = `
CREATE TABLE conversations (id TEXT PRIMARY KEY, title TEXT);
`

func newWorkflowStore(t *testing.T) (*Workflows, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(workflowJoinedSchema); err != nil {
		t.Fatalf("create the joined table: %v", err)
	}
	w := NewWorkflows(db)
	if err := w.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return w, db
}

func TestWorkflowsEnsureSchemaBuildsEveryTableAndIndex(t *testing.T) {
	w, db := newWorkflowStore(t)
	if err := w.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	for _, object := range []struct{ kind, name string }{
		{"table", "workflow_definitions"},
		{"table", "workflow_runs"},
		{"table", "workflow_node_runs"},
		{"table", "workflow_package_inspections"},
		{"table", "workflow_package_imports"},
		{"index", "idx_workflow_definitions_updated_at"},
		{"index", "idx_workflow_definitions_enabled"},
		{"index", "idx_workflow_runs_workflow"},
		{"index", "idx_workflow_runs_conversation"},
		{"index", "idx_workflow_runs_status"},
		{"index", "idx_workflow_node_runs_run"},
		{"index", "idx_workflow_package_inspections_creator_expiry"},
		{"index", "uq_workflow_package_imports_actor_key"},
		{"index", "uq_workflow_package_imports_inspection_success"},
	} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = ? AND name = ?`, object.kind, object.name).
			Scan(&name); err != nil {
			t.Fatalf("%s %s was not created by EnsureSchema: %v", object.kind, object.name, err)
		}
	}
}

// MigrateRunsTable is the step a database written before the HITL handshake needs: the two columns
// arrive after the fact, and the reads select them unconditionally.
func TestWorkflowsMigrateRunsTableAddsTheLateColumns(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "old-workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The pre-HITL shape: same table, neither pending column.
	if _, err := db.Exec(`CREATE TABLE workflow_runs (
		id TEXT PRIMARY KEY, workflow_id TEXT NOT NULL, workflow_version INTEGER NOT NULL DEFAULT 1,
		conversation_id TEXT, project_id TEXT, role_id TEXT, status TEXT NOT NULL,
		input_json TEXT, output_json TEXT, error TEXT, started_at DATETIME NOT NULL,
		finished_at DATETIME, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	w := NewWorkflows(db)
	if err := w.MigrateRunsTable(); err != nil {
		t.Fatalf("MigrateRunsTable: %v", err)
	}
	for _, column := range []string{"pending_hitl_node_id", "pending_hitl_json"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('workflow_runs') WHERE name = ?`, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s was not added", column)
		}
	}
	// Calling it again is a no-op rather than a duplicate-column error.
	if err := w.MigrateRunsTable(); err != nil {
		t.Fatalf("second MigrateRunsTable: %v", err)
	}
}

func TestWorkflowsDefinitionRoundTripAndListing(t *testing.T) {
	w, _ := newWorkflowStore(t)
	if err := w.UpsertWorkflowDefinition(&WorkflowDefinition{
		ID: "wf-1", Name: "Recon", Description: "scan then report", GraphJSON: `{"nodes":[]}`, Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertWorkflowDefinition: %v", err)
	}
	got, err := w.GetWorkflowDefinition("wf-1")
	if err != nil {
		t.Fatalf("GetWorkflowDefinition: %v", err)
	}
	if got.Version != 1 || !got.Enabled || got.Name != "Recon" {
		t.Fatalf("definition = %+v, want version 1 enabled", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("EnsureSchema's timestamps were not filled: %+v", got)
	}

	// Re-saving the same id bumps the version rather than adding a row - the console shows the number
	// and the package exchange compares it.
	if err := w.UpsertWorkflowDefinition(&WorkflowDefinition{ID: "wf-1", Name: "Recon v2", GraphJSON: `{"nodes":[]}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = w.GetWorkflowDefinition("wf-1")
	if got.Version != 2 || got.Name != "Recon v2" {
		t.Fatalf("after the second save = %+v, want version 2", got)
	}
	all, err := w.ListWorkflowDefinitions(false)
	if err != nil {
		t.Fatalf("ListWorkflowDefinitions: %v", err)
	}
	if len(all) != 1 || all[0].ID != "wf-1" {
		t.Fatalf("definitions = %+v", all)
	}

	// A disabled definition is hidden from the default listing and shown when asked for.
	second := &WorkflowDefinition{ID: "wf-2", Name: "Retired", GraphJSON: `{}`, Enabled: false}
	if err := w.UpsertWorkflowDefinition(second); err != nil {
		t.Fatal(err)
	}
	enabled, err := w.ListWorkflowDefinitions(false)
	if err != nil || len(enabled) != 1 {
		t.Fatalf("enabled listing = %d / %v, want 1", len(enabled), err)
	}
	including, err := w.ListWorkflowDefinitions(true)
	if err != nil || len(including) != 2 {
		t.Fatalf("full listing = %d / %v, want 2", len(including), err)
	}

	if err := w.DeleteWorkflowDefinition("wf-2"); err != nil {
		t.Fatalf("DeleteWorkflowDefinition: %v", err)
	}
	// An unknown id answers (nil, nil) - the absent-record contract the handlers nil-check against.
	// It is pinned rather than "improved" here because the migration must not move the contract.
	deleted, err := w.GetWorkflowDefinition("wf-2")
	if err != nil || deleted != nil {
		t.Fatalf("deleted definition = %#v / %v, want nil with no error", deleted, err)
	}
	if blank, err := w.GetWorkflowDefinition("   "); err != nil || blank != nil {
		t.Fatalf("blank id = %#v / %v, want the same (nil, nil) answer", blank, err)
	}
}

// The run ledger is what a restart resumes from, so the columns the HITL handshake writes have to
// survive a read, and the awaiting-list is what the console's approval page polls.
func TestWorkflowsRunLedgerLifecycle(t *testing.T) {
	w, _ := newWorkflowStore(t)
	if err := w.UpsertWorkflowDefinition(&WorkflowDefinition{ID: "wf-r", Name: "Flow", GraphJSON: `{}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Truncate(time.Second)
	run := &WorkflowRun{ID: "run-1", WorkflowID: "wf-r", WorkflowVersion: 1, ConversationID: "c1", Status: "running", StartedAt: started}
	if err := w.CreateWorkflowRun(run); err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	if err := w.SetWorkflowRunAwaitingHITL("run-1", "node-approve", `{"node_id":"node-approve","tool":"nmap"}`); err != nil {
		t.Fatalf("SetWorkflowRunAwaitingHITL: %v", err)
	}

	got, err := w.GetWorkflowRun("run-1")
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if got.Status != "awaiting_hitl" || got.PendingHITLNodeID != "node-approve" {
		t.Fatalf("run after the HITL pause = %+v", got)
	}
	if got.ConversationID != "c1" || got.StartedAt.IsZero() {
		t.Fatalf("run lost its identity columns: %+v", got)
	}

	pending, err := w.ListWorkflowRunsAwaitingHITLFiltered("", 10)
	if err != nil || len(pending) != 1 || pending[0].ID != "run-1" {
		t.Fatalf("awaiting list = %+v / %v, want run-1", pending, err)
	}
	// The filter is a conversation filter, and a blank one means "any".
	if mine, err := w.ListWorkflowRunsAwaitingHITLFiltered("c1", 10); err != nil || len(mine) != 1 {
		t.Fatalf("conversation-filtered list = %d / %v, want 1", len(mine), err)
	}
	if none, err := w.ListWorkflowRunsAwaitingHITLFiltered("other", 10); err != nil || len(none) != 0 {
		t.Fatalf("other conversation = %d / %v, want none", len(none), err)
	}

	if err := w.RecordWorkflowRunHITLDecision("run-1", true, "批准"); err != nil {
		t.Fatalf("RecordWorkflowRunHITLDecision: %v", err)
	}
	if err := w.SetWorkflowRunStatus("run-1", "running"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if running, err := w.ListWorkflowRunsAwaitingHITLFiltered("", 10); err != nil || len(running) != 0 {
		t.Fatalf("still awaiting after the decision: %+v / %v", running, err)
	}

	finished := time.Now().UTC()
	if err := w.FinishWorkflowRun("run-1", "completed", `{"result":"ok"}`, ""); err != nil {
		t.Fatalf("FinishWorkflowRun: %v", err)
	}
	done, err := w.GetWorkflowRun("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "completed" || done.OutputJSON != `{"result":"ok"}` || done.FinishedAt == nil || !done.FinishedAt.After(finished.Add(-time.Minute)) {
		t.Fatalf("finished run = %+v", done)
	}
	// The handshake columns deliberately survive the finish: the replay view reads them to show what
	// was approved. Pinning it here because a later "cleanup" of these columns would silently break
	// that page.
	if done.PendingHITLNodeID != "node-approve" || done.PendingHITLJSON == "" {
		t.Fatalf("the recorded handshake disappeared on completion: %+v", done)
	}

	// Node runs hang off the run and are listed in insertion order.
	for i, id := range []string{"nr-1", "nr-2"} {
		node := &WorkflowNodeRun{ID: id, RunID: "run-1", NodeID: "node-" + id, Status: "running", StartedAt: time.Now().UTC()}
		if err := w.CreateWorkflowNodeRun(node); err != nil {
			t.Fatalf("CreateWorkflowNodeRun %d: %v", i, err)
		}
	}
	if err := w.FinishWorkflowNodeRun("nr-2", "failed", "", "tool timeout"); err != nil {
		t.Fatalf("FinishWorkflowNodeRun: %v", err)
	}
	nodes, err := w.ListWorkflowNodeRuns("run-1")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("node runs = %+v / %v, want 2", nodes, err)
	}
	if nodes[1].ID != "nr-2" || nodes[1].Status != "failed" || nodes[1].Error != "tool timeout" {
		t.Fatalf("the finished node run = %+v", nodes[1])
	}
	// A run id that never existed reads empty rather than erroring.
	if empty, err := w.ListWorkflowNodeRuns("nope"); err != nil || len(empty) != 0 {
		t.Fatalf("unknown run node list = %+v / %v, want empty", empty, err)
	}
	// Same absent-record contract as the definition read.
	if unknown, err := w.GetWorkflowRun("nope"); err != nil || unknown != nil {
		t.Fatalf("unknown run = %#v / %v, want nil with no error", unknown, err)
	}
}

// Purging is the expiry sweep the app runs on start-up and hourly: a ready inspection past its
// expiry becomes expired, and an expired one with no import attached disappears after a day.
func TestWorkflowsPackageLifecyclePurge(t *testing.T) {
	w, db := newWorkflowStore(t)
	now := time.Now().UTC()
	if err := w.CreateWorkflowPackageInspection(&WorkflowPackageInspection{
		ID: "wpi-old", PackageHash: "sha256:a", ManifestJSON: "{}", WorkflowPayloadJSON: "{}", InspectionJSON: "{}",
		SourceWorkflowID: "wf-x", SourceRevision: 1, SourceContentHash: "sha256:b", SourceGraphHash: "sha256:c",
		LocalConflictState: "none", CreatedBy: "u1", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("CreateWorkflowPackageInspection: %v", err)
	}
	if err := w.CreateWorkflowPackageInspection(&WorkflowPackageInspection{
		ID: "wpi-fresh", PackageHash: "sha256:d", ManifestJSON: "{}", WorkflowPayloadJSON: "{}", InspectionJSON: "{}",
		SourceWorkflowID: "wf-x", SourceRevision: 1, SourceContentHash: "sha256:e", SourceGraphHash: "sha256:f",
		LocalConflictState: "none", CreatedBy: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	if err := w.PurgeWorkflowPackageLifecycle(now); err != nil {
		t.Fatalf("PurgeWorkflowPackageLifecycle: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM workflow_package_inspections WHERE id = 'wpi-old'`).Scan(&status); err != nil {
		t.Fatalf("the expired inspection was deleted outright, which loses the audit trail: %v", err)
	}
	if status != "expired" {
		t.Fatalf("expired inspection status = %q, want expired", status)
	}
	if err := db.QueryRow(`SELECT status FROM workflow_package_inspections WHERE id = 'wpi-fresh'`).Scan(&status); err != nil || status != "ready" {
		t.Fatalf("fresh inspection = %q / %v, want untouched ready", status, err)
	}

	// A day later the expired row is old enough to drop.
	if err := w.PurgeWorkflowPackageLifecycle(now.AddDate(0, 0, 2)); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workflow_package_inspections WHERE id = 'wpi-old'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("the two-day-old expired inspection should have been purged")
	}
}

func TestWorkflowsRefuseAConnectionlessHandle(t *testing.T) {
	// The panic this case prevents is not hypothetical: without the guards a nil *sql.DB was
	// dereferenced, and the handler wiring test is what surfaced it.
	var missing *Workflows
	for _, call := range []struct {
		name string
		run  func() error
	}{
		{"EnsureSchema", func() error { return missing.EnsureSchema() }},
		{"MigrateRunsTable", func() error { return missing.MigrateRunsTable() }},
		{"ListWorkflowDefinitions", func() error { _, err := missing.ListWorkflowDefinitions(false); return err }},
		{"GetWorkflowDefinition", func() error { _, err := missing.GetWorkflowDefinition("x"); return err }},
		{"UpsertWorkflowDefinition", func() error { return missing.UpsertWorkflowDefinition(&WorkflowDefinition{ID: "x"}) }},
		{"DeleteWorkflowDefinition", func() error { return missing.DeleteWorkflowDefinition("x") }},
		{"CreateWorkflowRun", func() error { return missing.CreateWorkflowRun(&WorkflowRun{ID: "x"}) }},
		{"GetWorkflowRun", func() error { _, err := missing.GetWorkflowRun("x"); return err }},
		{"FinishWorkflowRun", func() error { return missing.FinishWorkflowRun("x", "completed", "", "") }},
		{"SetWorkflowRunStatus", func() error { return missing.SetWorkflowRunStatus("x", "running") }},
		{"SetWorkflowRunAwaitingHITL", func() error { return missing.SetWorkflowRunAwaitingHITL("x", "n", "{}") }},
		{"RecordWorkflowRunHITLDecision", func() error { return missing.RecordWorkflowRunHITLDecision("x", true, "") }},
		{"ListWorkflowRunsAwaitingHITLFiltered", func() error { _, err := missing.ListWorkflowRunsAwaitingHITLFiltered("", 10); return err }},
		{"CreateWorkflowNodeRun", func() error { return missing.CreateWorkflowNodeRun(&WorkflowNodeRun{ID: "x"}) }},
		{"FinishWorkflowNodeRun", func() error { return missing.FinishWorkflowNodeRun("x", "failed", "", "") }},
		{"ListWorkflowNodeRuns", func() error { _, err := missing.ListWorkflowNodeRuns("x"); return err }},
		{"CreateWorkflowPackageInspection", func() error { return missing.CreateWorkflowPackageInspection(&WorkflowPackageInspection{ID: "x"}) }},
		{"GetWorkflowPackageInspection", func() error { _, err := missing.GetWorkflowPackageInspection("x", "u"); return err }},
		{"GetWorkflowPackageImport", func() error { _, err := missing.GetWorkflowPackageImport("x", "u"); return err }},
		{"PurgeWorkflowPackageLifecycle", func() error { return missing.PurgeWorkflowPackageLifecycle(time.Now()) }},
	} {
		if err := call.run(); err == nil {
			t.Errorf("%s accepted a store without a database", call.name)
		}
	}
}
