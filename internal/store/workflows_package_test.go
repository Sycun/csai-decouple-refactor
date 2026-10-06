package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// These two cases came over with the tables: the import is the one workflow write that spans
// several rows in one transaction, and its two guarantees (apply-or-replay never half-applies, and a
// snapshot that moved underneath the caller is refused) are only observable against a real database.

func newWorkflowTestStore(t *testing.T) *Workflows {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "workflows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w := NewWorkflows(db)
	if err := w.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return w
}

func TestWorkflowPackageApplyOverwriteIsTransactionalAndIdempotent(t *testing.T) {
	w := newWorkflowTestStore(t)
	current := &WorkflowDefinition{ID: "wf-1", Name: "Local", Version: 12, GraphJSON: `{"nodes":[]}`, Enabled: true}
	if err := w.UpsertWorkflowDefinition(current); err != nil {
		t.Fatal(err)
	}
	current, _ = w.GetWorkflowDefinition("wf-1")
	content, graph := workflowDefinitionPackageHashes(current)
	payload, _ := json.Marshal(map[string]any{"id": "wf-1", "name": "Imported", "description": "new", "version": 18, "graph_json": `{"nodes":[]}`, "enabled": false})
	now := time.Now().UTC()
	inspection := &WorkflowPackageInspection{ID: "wpi_test", PackageHash: "sha256:pkg", ManifestJSON: "{}", WorkflowPayloadJSON: string(payload), InspectionJSON: "{}", SourceWorkflowID: "wf-1", SourceRevision: 18, SourceContentHash: "sha256:src", SourceGraphHash: "sha256:graph", LocalConflictState: "id_conflict", LocalWorkflowID: "wf-1", LocalContentHash: content, LocalGraphHash: graph, CreatedBy: "user-1", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := w.CreateWorkflowPackageInspection(inspection); err != nil {
		t.Fatal(err)
	}
	req := WorkflowPackageApplyRequest{InspectionID: inspection.ID, RequestHash: "sha256:req", IdempotencyKey: "key-1", ActorUserID: "user-1", Action: "overwrite", ConfirmOverwrite: true}
	imp, replayed, err := w.ApplyWorkflowPackageImport(context.Background(), req)
	if err != nil || replayed || imp.Result != "overwritten" {
		t.Fatalf("apply = %#v replay=%v err=%v", imp, replayed, err)
	}
	updated, _ := w.GetWorkflowDefinition("wf-1")
	if updated.Version != 13 || updated.Name != "Imported" || updated.Enabled {
		t.Fatalf("updated workflow = %#v", updated)
	}
	replay, replayed, err := w.ApplyWorkflowPackageImport(context.Background(), req)
	if err != nil || !replayed || replay.ID != imp.ID {
		t.Fatalf("replay = %#v replay=%v err=%v", replay, replayed, err)
	}
	gotInspection, err := w.GetWorkflowPackageInspection(inspection.ID, "user-1")
	if err != nil || gotInspection.Status != "consumed" {
		t.Fatalf("inspection=%#v err=%v", gotInspection, err)
	}
}

func TestWorkflowPackageApplyRejectsChangedConflictSnapshot(t *testing.T) {
	w := newWorkflowTestStore(t)
	if err := w.UpsertWorkflowDefinition(&WorkflowDefinition{ID: "wf-2", Name: "Local", GraphJSON: `{"nodes":[]}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	local, _ := w.GetWorkflowDefinition("wf-2")
	content, graph := workflowDefinitionPackageHashes(local)
	payload, _ := json.Marshal(map[string]any{"id": "wf-2", "name": "Imported", "version": 2, "graph_json": `{"nodes":[]}`, "enabled": true})
	now := time.Now().UTC()
	inspection := &WorkflowPackageInspection{ID: "wpi_changed", PackageHash: "sha256:pkg", ManifestJSON: "{}", WorkflowPayloadJSON: string(payload), InspectionJSON: "{}", SourceWorkflowID: "wf-2", SourceRevision: 2, SourceContentHash: "sha256:src", SourceGraphHash: "sha256:graph", LocalConflictState: "id_conflict", LocalWorkflowID: "wf-2", LocalContentHash: content, LocalGraphHash: graph, CreatedBy: "user-1", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := w.CreateWorkflowPackageInspection(inspection); err != nil {
		t.Fatal(err)
	}
	if err := w.UpsertWorkflowDefinition(&WorkflowDefinition{ID: "wf-2", Name: "Changed", GraphJSON: `{"nodes":[]}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, _, err := w.ApplyWorkflowPackageImport(context.Background(), WorkflowPackageApplyRequest{InspectionID: inspection.ID, RequestHash: "sha256:req", IdempotencyKey: "key-2", ActorUserID: "user-1", Action: "overwrite", ConfirmOverwrite: true})
	if e, ok := err.(*WorkflowPackageStoreError); !ok || e.Code != "WFPKG_CONFLICT_CHANGED" {
		t.Fatalf("err=%v", err)
	}
}
