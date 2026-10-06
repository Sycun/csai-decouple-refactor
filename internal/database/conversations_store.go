package database

import "cyberstrike-ai/internal/store"

// NewConversations hands out the conversation ledger: the conversations table, the read side of
// messages / process_details, the agent trace columns and the create hook. The connection wrapper
// answers none of these any more; handlers and the finalizers hold this store instead.
//
// Two collaborators are wired here rather than reached for from the store: the findings backfill
// (store.Vulnerabilities' write, run before a delete) and the directory cleanup snapshot (the same
// storage.ConversationDirs the connection wrapper carries for projects until that cut lands).
//
// Nil-safe the way NewAssets is: a caller built without a database gets nil and can tell it has none.
func NewConversations(db *DB) *store.Conversations {
	if db == nil {
		return nil
	}
	conversations := store.NewConversations(db.DB)
	dirs := db.dirs
	if dirs.Artifacts == "" {
		dirs.Artifacts = db.conversationArtifactsDir
	}
	conversations.SetDirs(dirs)
	conversations.SetSourceTagBackfill(func(conversationID string) {
		_, _ = NewFindings(db).BackfillSourceTag(conversationID)
	})
	return conversations
}
