package database

import "cyberstrike-ai/internal/store"

// NewMonitor hands out the tool-execution ledger: tool_executions and tool_stats, their schema and
// the aggregations the console reads. The connection wrapper answers none of these reads or writes
// any more; the monitor handler, the runtime reconciler, the audit availability check and the MCP
// servers hold this store instead.
//
// The access check is the RBAC rule the monitor surface composes with an execution's owner column.
// It is passed as a closure so the store never holds the connection wrapper - and store.Monitor
// uses it only for that one question, never to answer permissions on its own.
//
// Nil-safe the way NewAssets is: a caller built without a database gets nil and can tell it has none.
func NewMonitor(db *DB) *store.Monitor {
	if db == nil {
		return nil
	}
	return store.NewMonitor(db.DB, db.UserCanAccessResource)
}
