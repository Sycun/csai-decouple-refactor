package database

import "cyberstrike-ai/internal/store"

// NewRBAC hands out the accounts/roles/permissions/assignments ledger: the six rbac_* tables,
// their schema, the access questions the rest of the process asks, and the resource-picker
// lookups. The connection wrapper answers none of these any more; handlers and the auth
// middleware hold this store instead.
//
// Nil-safe the way NewAssets is: a caller built without a database gets nil and can tell it has none.
func NewRBAC(db *DB) *store.RBAC {
	if db == nil {
		return nil
	}
	return store.NewRBAC(db.DB)
}
