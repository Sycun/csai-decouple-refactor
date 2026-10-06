package handler

import (
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/store"
)

// newRobotIdentityStore is the only way this package comes by robot_user_bindings and
// robot_binding_codes. A handler built without a database keeps a connectionless store, whose methods
// answer an error instead of panicking.
func newRobotIdentityStore(db *database.DB) *store.RobotIdentity {
	if db == nil {
		return store.NewRobotIdentity(nil)
	}
	return store.NewRobotIdentity(db.DB)
}
