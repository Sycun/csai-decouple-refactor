package database

import "cyberstrike-ai/internal/store"

// The assets SQL lives in store.Assets, and both consumers - the console handler and the MCP tool
// path - hold that store directly. Nothing on the connection wrapper can reach these rows any more,
// which is what TestDatabaseSurfaceOnlyShrinks plus the write ledger now check together.

// NewAssets answers a nil store for a nil connection, the same way database.Narrow answers a nil
// interface: a handler built without a database must be able to tell it has none. Every method on
// store.Assets opens with `s == nil` in its guard, so the nil pointer refuses calls with an error
// instead of panicking, and `h.assets == nil` stays readable.
func NewAssets(db *DB) *store.Assets {
	if db == nil {
		return nil
	}
	return store.NewAssets(db.DB)
}
