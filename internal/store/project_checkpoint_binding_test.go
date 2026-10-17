package store

import (
	"testing"
)

func TestCheckpointWriteRechecksBindingAtomically(t *testing.T) {
	f, db := newFactStore(t)
	if _, err := db.Exec(`CREATE TABLE conversations (id TEXT PRIMARY KEY, project_id TEXT)`); err != nil {
		t.Fatalf("create conversations: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name) VALUES ('a', 'a'), ('b', 'b')`); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO conversations (id, project_id) VALUES ('run', 'a')`); err != nil {
		t.Fatalf("bind conversation: %v", err)
	}
	checkpoint := &ProjectFact{ProjectID: "a", FactKey: "note/context-test", SourceConversationID: "run", Summary: "first", Body: "first body"}
	if err := f.SaveContextCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	first, err := f.GetProjectFactByKey("a", checkpoint.FactKey)
	if err != nil {
		t.Fatal(err)
	}
	// A running agent has already read project a; the binding changes before its write.
	if _, err := db.Exec(`UPDATE conversations SET project_id = 'b' WHERE id = 'run'`); err != nil {
		t.Fatal(err)
	}
	checkpoint.Body = "wrong-project update"
	if err := f.SaveContextCheckpoint(checkpoint); err == nil {
		t.Fatal("stale run updated old-project memory")
	}
	checkpoint.FactKey = "note/context-new"
	if err := f.SaveContextCheckpoint(checkpoint); err == nil {
		t.Fatal("stale run inserted old-project memory")
	}
	existing, err := f.GetProjectFactByKey("a", first.FactKey)
	if err != nil || existing.Body != "first body" {
		t.Fatalf("failed write changed evidence: %#v %v", existing, err)
	}
	if _, err := f.GetProjectFactByKey("a", checkpoint.FactKey); err == nil {
		t.Fatal("failed write left a row")
	}
	checkpoint.ProjectID = "b"
	if err := f.SaveContextCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE conversations SET project_id = NULL WHERE id = 'run'`); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveContextCheckpoint(checkpoint); err == nil {
		t.Fatal("unbound conversation still persisted project memory")
	}
}
