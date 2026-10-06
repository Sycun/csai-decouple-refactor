package workflow

import (
	"cyberstrike-ai/internal/project"
	"cyberstrike-ai/internal/store"
)

// Ledger is the workflow engine's own run state: which run is executing, which node is awaiting
// approval, and how each finished. It is satisfied by *store.Workflows, the package that owns those
// five tables.
//
// Before the rows moved, this list was declared in internal/database (WorkflowRunLedger) and the one
// value passed around satisfied it together with everything else, because it was the connection
// wrapper. That is the shape this package exists to not have: a graph node holding a handle to every
// table in the application.
type Ledger interface {
	CreateWorkflowRun(run *store.WorkflowRun) error
	GetWorkflowRun(runID string) (*store.WorkflowRun, error)
	FinishWorkflowRun(runID, status, outputJSON, errText string) error
	SetWorkflowRunStatus(runID, status string) error
	SetWorkflowRunAwaitingHITL(runID, nodeID, pendingJSON string) error
	CreateWorkflowNodeRun(n *store.WorkflowNodeRun) error
	FinishWorkflowNodeRun(nodeRunID, status, outputJSON, errText string) error
	GetWorkflowDefinition(id string) (*store.WorkflowDefinition, error)
	UpsertWorkflowDefinition(wf *store.WorkflowDefinition) error
}

// Store is what a workflow run needs: the ledger above, plus the project fact surface - a workflow
// node can run a deep agent that maintains the project's fact index.
//
// A struct of two providers rather than one interface, because after the ledger moved to its own
// store no single value answers both halves any more, and composing them here is what keeps the
// engine's call sites (`db.GetWorkflowRun`, `db.UpsertProjectFact`) unchanged.
type Store struct {
	project.Store
	Ledger
}

// Missing reports whether either half of the persistence a run needs is absent. It exists because
// Store is a pair of providers rather than the single interface it used to be: the old guard was a
// `db == nil` check on an interface, and the same question now has two halves to ask about.
func (s Store) Missing() bool { return s.Store.Missing() || s.Ledger == nil }
