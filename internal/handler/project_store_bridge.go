package handler

import (
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/project"
)

// projectStore pairs the connection wrapper with the blackboard ledger for the functions in
// internal/project that need both halves.
//
// One argument today because *database.DB still answers the ledger half - through the one-line
// delegations in facts_store.go. When those go, the ledger argument becomes the handler's own
// *store.Facts field and this is the place that changes, once per handler instead of once per call.
func projectStore(db database.ProjectFactStore) project.Store {
	return project.NewStore(db, db)
}
