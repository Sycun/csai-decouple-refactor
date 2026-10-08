package store

import (
	"testing"
)

// The install table is read at start-up, before any manifest has been parsed, and every row is
// joined onto the bundles root to find the pack. So the shape of the id is checked on the way in,
// and what the table returns is one decision per pack, never an accumulation of attempts.

func TestInstalledBundlesRoundTrip(t *testing.T) {
	installs := NewInstalledBundles(openDB(t))
	if err := installs.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := installs.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema is not idempotent: %v", err)
	}

	if err := installs.Record("web-pentest", "1.0.0"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := installs.Record("ad-internal", "1.0.0"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Re-recording the same id is an upgrade: one row per pack, carrying the newest version.
	if err := installs.Record("web-pentest", "1.1.0"); err != nil {
		t.Fatalf("Record (upgrade): %v", err)
	}

	rows, err := installs.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%v, want 2 packs", rows)
	}
	if rows[0].ID != "ad-internal" || rows[1].ID != "web-pentest" {
		t.Fatalf("rows are not ordered by id: %v", rows)
	}
	if rows[1].Version != "1.1.0" {
		t.Fatalf("the upgrade did not replace the stored version: %+v", rows[1])
	}

	if err := installs.Forget("web-pentest"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	rows, _ = installs.All()
	if len(rows) != 1 || rows[0].ID != "ad-internal" {
		t.Fatalf("after Forget rows=%v", rows)
	}
}

func TestInstalledBundlesRejectIdsThatAreNotDirectoryNames(t *testing.T) {
	installs := NewInstalledBundles(openDB(t))
	if err := installs.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	// Each of these, joined onto the bundles root at start-up, would name something the install
	// endpoint would never have accepted - so none of them may be stored.
	for _, bad := range []string{"", "   ", ".", "..", "../evil", "a/b", "a\\b", ".hidden", "pack/../..", "真\u0000包"} {
		if err := installs.Record(bad, "1.0.0"); err == nil {
			t.Errorf("Record(%q) was accepted", bad)
		}
		if err := installs.Forget(bad); err == nil {
			t.Errorf("Forget(%q) was accepted", bad)
		}
	}
	// A version is the whole point of the row; an empty one is refused rather than stored as a lie.
	if err := installs.Record("web-pentest", "  "); err == nil {
		t.Error("Record with an empty version was accepted")
	}
	rows, _ := installs.All()
	if len(rows) != 0 {
		t.Fatalf("rejected records left rows behind: %v", rows)
	}
}
