package handler

import (
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/project"
)

// projectStore pairs the connection wrapper with the blackboard ledger for the functions in
// internal/project that need both halves.
//
// Two arguments because the halves have different owners: the project rows are still the connection
// wrapper's, the fact and edge ledger is store.Facts. Every handler that builds one of these passes
// its own facts field, so this is the only place that knows both exist.
func projectStore(rows database.ProjectRowStore, ledger database.BlackboardLedger) project.Store {
	return project.NewStore(rows, ledger)
}
