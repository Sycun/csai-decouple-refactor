package database

import "cyberstrike-ai/internal/store"

// NewProjects hands out the project-row ledger: the project itself, the counters the project page
// shows, the dashboard aggregate and the last-activity stamp. The connection wrapper answers none of
// these any more; handlers hold this store instead. DeleteProject's cascades call the other domains'
// stores' own UnlinkProject, so no statement for another table lives here.
//
// Nil-safe the way NewAssets is: a caller built without a database gets nil and can tell it has none.
func NewProjects(db *DB) *store.Projects {
	if db == nil {
		return nil
	}
	projects := store.NewProjects(db.DB)
	dirs := db.dirs
	if dirs.Artifacts == "" {
		dirs.Artifacts = db.conversationArtifactsDir
	}
	projects.SetDirs(dirs)
	return projects
}
