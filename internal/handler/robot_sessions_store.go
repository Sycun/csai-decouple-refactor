package handler

import (
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/store"
)

// newRobotSessionsStore is the only way this package comes by robot_user_sessions. A handler built
// without a database keeps a connectionless store, whose methods answer an error instead of panicking.
func newRobotSessionsStore(db *database.DB) *store.RobotSessions {
	if db == nil {
		return store.NewRobotSessions(nil)
	}
	return store.NewRobotSessions(db.DB)
}
