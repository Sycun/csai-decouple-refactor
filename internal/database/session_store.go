package database

import "cyberstrike-ai/internal/store"

// newSession is the data layer's handle on store.Session, the only writer of `messages` and
// `process_details`. The conversation methods still own their transactions and their logging, so
// they ask this store for the statement rather than writing it - which is what lets the write ledger
// drop its last two debts.
func newSession(db *DB) *store.Session {
	if db == nil {
		return store.NewSession(nil)
	}
	return store.NewSession(db.DB)
}
