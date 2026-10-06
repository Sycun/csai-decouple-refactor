package database

import (
	"cyberstrike-ai/internal/store"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestConversationProjectFilter(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "conversations.db")
	db, err := NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	defer db.Close()

	p, err := db.CreateProject(&Project{Name: "target-a", Status: "active"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	convNone, err := NewConversations(db).CreateConversation("unbound", ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation unbound: %v", err)
	}
	convBound, err := NewConversations(db).CreateConversation("bound", ConversationCreateMeta{ProjectID: p.ID})
	if err != nil {
		t.Fatalf("CreateConversation bound: %v", err)
	}

	totalAll, err := NewConversations(db).CountConversationsForAccess("", "", "", "")
	if err != nil || totalAll < 2 {
		t.Fatalf("CountConversations all: total=%d err=%v", totalAll, err)
	}

	totalBound, err := NewConversations(db).CountConversationsForAccess("", p.ID, "", "")
	if err != nil || totalBound != 1 {
		t.Fatalf("CountConversations project: total=%d err=%v", totalBound, err)
	}

	totalUnbound, err := NewConversations(db).CountConversationsForAccess("", store.ProjectUnbound, "", "")
	if err != nil || totalUnbound != 1 {
		t.Fatalf("CountConversations unbound: total=%d err=%v", totalUnbound, err)
	}

	listBound, err := NewConversations(db).ListConversations(10, 0, "", "", p.ID)
	if err != nil || len(listBound) != 1 || listBound[0].ID != convBound.ID {
		t.Fatalf("ListConversations project: %+v err=%v", listBound, err)
	}

	listUnbound, err := NewConversations(db).ListConversations(10, 0, "", "", store.ProjectUnbound)
	if err != nil || len(listUnbound) != 1 || listUnbound[0].ID != convNone.ID {
		t.Fatalf("ListConversations unbound: %+v err=%v", listUnbound, err)
	}

	_ = convNone
	_ = convBound
}
