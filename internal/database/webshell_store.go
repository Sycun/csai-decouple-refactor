package database

import "cyberstrike-ai/internal/store"

// NewWebshell hands out the webshell connection ledger: the connection rows, their workspace state,
// both tables' schema and the column backfill. The connection wrapper no longer answers any of these
// calls, so handlers that need them hold this instead.
//
// Nil-safe on purpose: handlers are constructed with a nil database in the disabled paths, and a
// connectionless store refuses every call with an error rather than panicking on db.DB.
func NewWebshell(db *DB) *store.Webshell {
	if db == nil {
		return store.NewWebshell(nil)
	}
	return store.NewWebshell(db.DB)
}
