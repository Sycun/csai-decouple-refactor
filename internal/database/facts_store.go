package database

import "cyberstrike-ai/internal/store"

// NewFacts hands out the blackboard's ledger - the fact rows, the edges between them, and the unlink
// the findings domain owes that table. The SQL, the two tables and their indexes all live in
// internal/store; the connection wrapper deliberately stopped answering these calls, which is why
// every consumer holds a *store.Facts field instead of reaching through db.
//
// A nil wrapper answers as a connectionless store rather than panicking here: the handlers are built
// with a nil database in the disabled paths, and store.Facts refuses every call on such a handle
// with an error. Dereferencing db.DB would have turned that降级 into a crash in the constructor.
func NewFacts(db *DB) *store.Facts {
	if db == nil {
		return store.NewFacts(nil)
	}
	return store.NewFacts(db.DB)
}
