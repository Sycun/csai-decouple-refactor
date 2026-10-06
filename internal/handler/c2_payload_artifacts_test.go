package handler

import (
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/c2"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// The download gate is two questions now - does this file belong to somebody, and can this caller
// reach the listener it was built for. These cases pin that the composition answers the same way the
// single method it replaced did, including the denial for a file with no ownership record.
func TestPayloadArtifactDownloadGate(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "c2-gate.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Assignments point at real accounts, so the three identities this gate distinguishes have to
	// exist as users rather than as bare ids.
	owner, err := db.CreateRBACUser("c2-owner", "C2 Owner", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := db.CreateRBACUser("c2-assigned", "C2 Assigned", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := db.CreateRBACUser("c2-stranger", "C2 Stranger", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr := c2.NewManager(db, zap.NewNop(), t.TempDir())
	artifacts := c2PayloadArtifacts(mgr)
	if err := artifacts.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	if err := artifacts.Record("beacon_owner.bin", "pay-1", "lis-1", owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := artifacts.Record("beacon_other.bin", "pay-2", "lis-2", owner.ID); err != nil {
		t.Fatal(err)
	}
	// Assignments are validated against the listener table, so the two listeners the artifacts name
	// have to exist as records - which is exactly what makes this an end-to-end check of the gate.
	for _, id := range []string{"lis-1", "lis-2"} {
		if err := database.NewC2(db).CreateC2Listener(&store.C2Listener{ID: id, Name: id, Type: "http", BindHost: "127.0.0.1", BindPort: 9001}); err != nil {
			t.Fatalf("create listener %s: %v", id, err)
		}
	}
	if err := db.AssignResourceToUser(assigned.ID, "c2_listener", "lis-2"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		session security.Session
		file    string
		want    bool
	}{
		{"owner reaches their own payload", security.Session{UserID: owner.ID, Scope: database.RBACScopeOwn}, "beacon_owner.bin", true},
		{"stranger does not", security.Session{UserID: stranger.ID, Scope: database.RBACScopeOwn}, "beacon_owner.bin", false},
		{"being assigned the listener reaches it", security.Session{UserID: assigned.ID, Scope: database.RBACScopeAssigned}, "beacon_other.bin", true},
		{"an assignment does not cross listeners", security.Session{UserID: assigned.ID, Scope: database.RBACScopeAssigned}, "beacon_owner.bin", false},
		{"unrestricted scope reaches a recorded file", security.Session{UserID: stranger.ID, Scope: database.RBACScopeAll}, "beacon_owner.bin", true},
		// Denial for an unrecorded file. What this case does NOT prove: removing the store's no-record
		// guard leaves it passing, because an empty owner and an empty listener are denied by the two
		// checks after it. The outcome is pinned here, not the branch.
		{"a file with no ownership record is refused to a scoped caller", security.Session{UserID: owner.ID, Scope: database.RBACScopeOwn}, "beacon_unrecorded.bin", false},
		// Preserved behaviour, not a hole introduced here: an unrestricted scope never consulted the
		// record, so an unrecorded file still passes this gate and is settled by whether the bytes exist.
		{"an unrestricted scope is not gated by the record at all", security.Session{UserID: stranger.ID, Scope: database.RBACScopeAll}, "beacon_unrecorded.bin", true},
	}
	for _, tc := range cases {
		if got := userMayFetchPayloadArtifact(mgr, tc.session, tc.file); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
