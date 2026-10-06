package database

import "cyberstrike-ai/internal/store"

// NewC2 hands out the C2 ledger: listeners, sessions, tasks, files, events, the malleable profiles,
// the six tables' schema and the project_id backfill. The connection wrapper answers none of these
// reads or writes any more; internal/c2 drives this store, and the C2 handler and MCP tools reach it
// through the manager.
//
// Nil-safe the way NewAssets is: a caller built without a database gets nil and can tell it has none.
// Every method on store.C2 opens with a nil guard, so even a nil pointer refuses calls with an error
// instead of panicking.
func NewC2(db *DB) *store.C2 {
	if db == nil {
		return nil
	}
	return store.NewC2(db.DB)
}
