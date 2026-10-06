package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func newArtifactStore(t *testing.T) (*C2PayloadArtifacts, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "c2-payload.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	artifacts := NewC2PayloadArtifacts(db)
	if err := artifacts.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	return artifacts, db
}

func TestC2PayloadArtifactsEnsureSchemaIsIdempotent(t *testing.T) {
	artifacts, db := newArtifactStore(t)
	if err := artifacts.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_c2_payload_artifacts_listener'`).Scan(&name); err != nil {
		t.Fatalf("the listener index was not created: %v", err)
	}
}

func TestC2PayloadArtifactsRecordThenLookup(t *testing.T) {
	artifacts, _ := newArtifactStore(t)
	if err := artifacts.Record("beacon_1.bin", "pay-1", "lis-1", "u-1"); err != nil {
		t.Fatal(err)
	}
	got, found, err := artifacts.Lookup("beacon_1.bin")
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	if got.Filename != "beacon_1.bin" || got.PayloadID != "pay-1" || got.ListenerID != "lis-1" || got.OwnerUserID != "u-1" {
		t.Fatalf("row came back changed: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("created_at did not read back as an instant")
	}
	// The lookup trims, because the download path passes a filename taken from a route parameter.
	if again, found, _ := artifacts.Lookup("  beacon_1.bin  "); !found || again.PayloadID != "pay-1" {
		t.Fatalf("a padded filename missed the record: %+v found=%v", again, found)
	}
}

// Rebuilding a payload under the same file name replaces the ownership: the bytes on disk are the new
// build's, and a stale owner would let somebody download what they did not ask for.
func TestC2PayloadArtifactsRebuildReplacesTheRecord(t *testing.T) {
	artifacts, db := newArtifactStore(t)
	if err := artifacts.Record("beacon_1.bin", "pay-1", "lis-1", "u-1"); err != nil {
		t.Fatal(err)
	}
	if err := artifacts.Record("beacon_1.bin", "pay-2", "lis-2", "u-2"); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM c2_payload_artifacts`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d rows for one file name, want 1", rows)
	}
	got, _, _ := artifacts.Lookup("beacon_1.bin")
	if got.PayloadID != "pay-2" || got.ListenerID != "lis-2" || got.OwnerUserID != "u-2" {
		t.Fatalf("the rebuild did not take: %+v", got)
	}
}

// Incomplete ownership is refused quietly: the build path ignores this error, and a record with no
// owner or no listener would be worse than none - it would answer the download gate with garbage.
func TestC2PayloadArtifactsIgnoreIncompleteRecords(t *testing.T) {
	artifacts, db := newArtifactStore(t)
	cases := [][3]string{
		{"", "pay-1", "u-1"},          // no file name
		{"beacon_1.bin", "pay-1", ""}, // no owner
	}
	for _, c := range cases {
		if err := artifacts.Record(c[0], c[1], "lis-1", c[2]); err != nil {
			t.Fatalf("Record(%q,%q) returned %v, want the silence the build path relies on", c[0], c[2], err)
		}
	}
	// A listener-less record is refused the same way even with a name and an owner.
	if err := artifacts.Record("beacon_2.bin", "pay-2", "", "u-1"); err != nil {
		t.Fatalf("Record without a listener: %v", err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM c2_payload_artifacts`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("%d rows written from incomplete ownership, want 0", rows)
	}
}

func TestC2PayloadArtifactsLookupSeparatesUnrecorded(t *testing.T) {
	artifacts, _ := newArtifactStore(t)
	got, found, err := artifacts.Lookup("beacon_never_built.bin")
	if err != nil {
		t.Fatalf("an unrecorded file must not be an error: %v", err)
	}
	if found {
		t.Fatalf("an unrecorded file resolved as %+v", got)
	}
}

func TestC2PayloadArtifactsRefusesNoConnection(t *testing.T) {
	none := NewC2PayloadArtifacts(nil)
	if err := none.EnsureSchema(); err == nil {
		t.Fatal("a connectionless store created the table")
	}
	if err := none.Record("f", "p", "l", "u"); err == nil {
		t.Fatal("a connectionless store recorded an artifact")
	}
	if _, _, err := none.Lookup("f"); err == nil {
		t.Fatal("a connectionless store answered an ownership lookup")
	}
	_ = time.Now()
}
