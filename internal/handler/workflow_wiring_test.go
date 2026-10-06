package handler

import (
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/project"
	"cyberstrike-ai/internal/store"
	workflowrunner "cyberstrike-ai/internal/workflow"

	"go.uber.org/zap"
)

// The workflow tables moved out of the connection wrapper, so each handler now holds a second
// collaborator for them. A field like that is invisible to the compiler when it is left unset: the
// type checks, the binary builds, and the first role-bound run fails at runtime with "store:
// workflows require a database". These are the assertions that make that mistake a test failure.
func TestWorkflowHandlersAreWiredToTheirRunLedger(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "workflow-wiring.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	workflows := NewWorkflowHandler(db, zap.NewNop())
	if workflows.runs == nil {
		t.Fatal("NewWorkflowHandler left runs unset: the endpoints would answer 500 on every workflow request")
	}
	if workflows.db == nil {
		t.Fatal("NewWorkflowHandler left its connection-wrapper surface unset")
	}

	agent := NewAgentHandler(nil, db, &config.Config{}, zap.NewNop())
	if agent.runs == nil {
		t.Fatal("NewAgentHandler left runs unset: a role-bound workflow run could not persist its state")
	}
	// This is exactly the value the integration path hands the engine; composing it here is what
	// proves the engine will not report its persistence as missing.
	composed := workflowrunner.Store{Store: project.NewStore(agent.db, agent.facts), Ledger: agent.runs}
	if composed.Missing() {
		t.Fatal("the engine store the agent handler composes reports itself missing, so workflow runs would refuse to start")
	}

	// And the ledger is not merely non-nil - it writes through to this connection, and the other
	// handler's ledger reads the same rows: one table, one owner, two readers of it.
	if err := agent.runs.UpsertWorkflowDefinition(newWorkflowDefinitionFixture()); err != nil {
		t.Fatalf("the wired ledger could not write a definition: %v", err)
	}
	got, err := workflows.runs.GetWorkflowDefinition("wiring-fixture")
	if err != nil || got == nil || got.Name != "wiring" {
		t.Fatalf("read back through the workflow handler's ledger = %#v / %v", got, err)
	}
}

func newWorkflowDefinitionFixture() *store.WorkflowDefinition {
	return &store.WorkflowDefinition{ID: "wiring-fixture", Name: "wiring", GraphJSON: `{"nodes":[]}`, Enabled: true}
}

// A handler built without a connection must fail closed rather than panic: the store it holds has no
// database, and every method says so.
func TestWorkflowHandlerWithoutAConnectionRefusesRatherThanPanics(t *testing.T) {
	h := NewWorkflowHandler(nil, zap.NewNop())
	if h.runs == nil {
		t.Fatal("expected a connectionless ledger rather than a nil field, so the calls answer with an error")
	}
	if _, err := h.runs.ListWorkflowDefinitions(false); err == nil {
		t.Fatal("ListWorkflowDefinitions accepted a store without a database")
	}
	if err := NewAgentHandler(nil, nil, &config.Config{}, zap.NewNop()).runs.UpsertWorkflowDefinition(newWorkflowDefinitionFixture()); err == nil {
		t.Fatal("an agent handler built without a connection wrote a definition")
	}
}
