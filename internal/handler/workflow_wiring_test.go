package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
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
	// 连接包装字段已随 project/会话域迁走；同一意图（端点不会 500）现在钉在它持有的三个 store 上。
	if workflows.projects == nil || workflows.conversations == nil || workflows.rbac == nil {
		t.Fatal("NewWorkflowHandler left one of its stores unset: the endpoints would answer 500 on every workflow request")
	}

	agent := NewAgentHandler(nil, db, &config.Config{}, zap.NewNop())
	if agent.runs == nil {
		t.Fatal("NewAgentHandler left runs unset: a role-bound workflow run could not persist its state")
	}
	// This is exactly the value the integration path hands the engine; composing it here is what
	// proves the engine will not report its persistence as missing.
	composed := workflowrunner.Store{Store: project.NewStore(agent.projects, agent.facts), Ledger: agent.runs}
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

// RunArgs.CheckAgentMode is what makes the workflow engine ask the mode catalog before an agent
// node dispatches (chat, robot and batch each ask on their own entry). A construction site that
// forgets it still compiles - the field is just nil - and that entry silently keeps running
// multi-agent after the orchestration pack was unplugged. Parse the sources and name any
// RunArgs literal that leaves the checker unset, so the omission is a test failure instead of
// another runtime 403-style discovery.
func TestEveryWorkflowRunArgsWiresTheModeCatalog(t *testing.T) {
	sources := []string{"workflow_integration.go", "workflow_run.go"}
	literals := 0
	for _, file := range sources {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "RunArgs" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "workflowrunner" {
				return true
			}
			literals++
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "CheckAgentMode" {
					return true
				}
			}
			t.Errorf("%s: workflowrunner.RunArgs at %s leaves CheckAgentMode unset", file, fset.Position(lit.Pos()))
			return true
		})
	}
	if literals == 0 {
		t.Fatal("found no workflowrunner.RunArgs literal in the parsed sources - the gate would pass vacuously if the call sites moved")
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
