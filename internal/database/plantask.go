package database

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"cyberstrike-ai/internal/storage"
)

// ConversationPlanTask is the transport model for the agent's task board; the reading of those files
// lives in internal/storage, which is where conversation directories are a first-class thing.
// The alias keeps every consumer's type name while removing the duplicate struct.
type ConversationPlanTask = storage.PlanTask

// planTaskDir resolves one conversation's task-board directory, and reports false when the board was
// never configured (the console then shows an empty list, exactly as before).
func planTaskDir(db *DB, conversationID string) (string, bool) {
	id := strings.TrimSpace(conversationID)
	if id == "" {
		return "", false
	}
	base := strings.TrimSpace(db.einoPlantaskBaseDir)
	if base == "" {
		return "", false
	}
	return filepath.Join(base, sanitizeConversationPathSegment(id)), true
}

// ListConversationPlanTasksSince limits the board to files written during the current agent run.
// The Eino backend intentionally keeps older task files for model continuity, but the conversation UI
// must not surface those files before the new run has called TaskCreate.
func (db *DB) ListConversationPlanTasksSince(conversationID string, since time.Time) ([]ConversationPlanTask, error) {
	if db == nil {
		return []ConversationPlanTask{}, nil
	}
	if strings.TrimSpace(conversationID) == "" {
		return nil, fmt.Errorf("conversation id is required")
	}
	dir, ok := planTaskDir(db, conversationID)
	if !ok {
		return []ConversationPlanTask{}, nil
	}
	return storage.ReadPlanTasks(dir, since, db.logger)
}

// ListConversationPlanTasks returns the live Eino task board for one conversation. A missing task
// directory is the normal state for short or legacy conversations and therefore returns an empty list.
func (db *DB) ListConversationPlanTasks(conversationID string) ([]ConversationPlanTask, error) {
	return db.ListConversationPlanTasksSince(conversationID, time.Time{})
}
