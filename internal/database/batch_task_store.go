package database

import (
	"cyberstrike-ai/internal/store"
)

// The batch run ledger SQL lives in store.BatchTasks, and the one consumer that had a storage
// field - BatchTaskManager - holds that store directly. Nothing on the connection wrapper can
// reach these two tables now, in either direction.

// NewBatchTasks answers a nil store for a nil connection, the way database.Narrow answers a nil
// interface: a consumer built without a database must be able to tell it has none, and every method
// on store.BatchTasks opens with an `s == nil` guard, so the nil pointer refuses instead of panicking.
func NewBatchTasks(db *DB) *store.BatchTasks {
	if db == nil {
		return nil
	}
	return store.NewBatchTasks(db.DB)
}
