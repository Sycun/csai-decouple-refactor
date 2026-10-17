package handler

import (
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/project"
	"cyberstrike-ai/internal/store"
)

// projectStore pairs the connection wrapper with the blackboard ledger for the functions in
// internal/project that need both halves.
//
// Three arguments because the halves have different owners: the project rows are still the connection
// wrapper's, the fact and edge ledger is store.Facts, and the resume preview reads the findings domain.
// Every handler that builds one of these passes its own fields, so this is the only place that knows
// all three exist.
func projectStore(rows *store.Projects, ledger database.BlackboardLedger, findings project.Findings, conversations project.Conversations) project.Store {
	return project.NewStore(rows, ledger, findings, conversations)
}
