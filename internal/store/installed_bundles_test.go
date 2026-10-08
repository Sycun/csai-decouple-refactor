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

	if err := installs.Record("web-pentest", "1.0.0", nil); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := installs.Record("ad-internal", "1.0.0", nil); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Re-recording the same id is an upgrade: one row per pack, carrying the newest version.
	if err := installs.Record("web-pentest", "1.1.0", nil); err != nil {
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
		if err := installs.Record(bad, "1.0.0", nil); err == nil {
			t.Errorf("Record(%q) was accepted", bad)
		}
		if err := installs.Forget(bad); err == nil {
			t.Errorf("Forget(%q) was accepted", bad)
		}
	}
	// A version is the whole point of the row; an empty one is refused rather than stored as a lie.
	if err := installs.Record("web-pentest", "  ", nil); err == nil {
		t.Error("Record with an empty version was accepted")
	}
	rows, _ := installs.All()
	if len(rows) != 0 {
		t.Fatalf("rejected records left rows behind: %v", rows)
	}
}

// A partial install is a decision like any other, so the selection it was made with has to survive
// the round trip. Two states must stay apart: an explicit list, and "the whole pack" (nil) - if the
// latter came back as an empty list, a restart would re-install a pack the operator narrowed.
func TestInstalledBundlesRoundTripKeepsUnitSelection(t *testing.T) {
	installs := NewInstalledBundles(openDB(t))
	if err := installs.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	if err := installs.Record("source-code-audit", "1.0.0", []string{"skill/sink-driven-audit", "role/源码与供应链审计", "skill/sink-driven-audit"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := installs.Record("web-pentest", "1.0.0", nil); err != nil {
		t.Fatalf("Record (whole pack): %v", err)
	}
	rows, err := installs.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	byID := map[string]InstalledBundle{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	got := byID["source-code-audit"].Units
	want := []string{"role/源码与供应链审计", "skill/sink-driven-audit"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("selection round-tripped as %v, want %v (sorted, de-duplicated)", got, want)
	}
	if got := byID["web-pentest"].Units; got != nil {
		t.Fatalf("a whole-pack row came back as %v; nil is what start-up reads as \"follow the pack\"", got)
	}
	// Narrowing an existing row replaces the selection rather than accumulating.
	if err := installs.Record("source-code-audit", "1.0.0", []string{"skill/sink-driven-audit"}); err != nil {
		t.Fatalf("Record (narrow): %v", err)
	}
	rows, _ = installs.All()
	for _, r := range rows {
		if r.ID == "source-code-audit" && (len(r.Units) != 1 || r.Units[0] != "skill/sink-driven-audit") {
			t.Fatalf("narrowing left %v", r.Units)
		}
	}
}

// A database created before the selection column existed gains it without losing the rows: the
// upgrade is an ALTER, not a rebuild, and the existing rows mean "the whole pack" - which is exactly
// what they meant when they were written.
func TestInstalledBundlesMigratesUnitSelectionColumn(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`
		CREATE TABLE installed_bundles (
			bundle_id TEXT PRIMARY KEY,
			version TEXT NOT NULL,
			installed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`); err != nil {
		t.Fatalf("create old-shaped table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO installed_bundles (bundle_id, version) VALUES ('ctf', '1.0.0');`); err != nil {
		t.Fatalf("seed old row: %v", err)
	}
	installs := NewInstalledBundles(db)
	if err := installs.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema on an old database: %v", err)
	}
	if err := installs.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema is not idempotent after the migration: %v", err)
	}
	rows, err := installs.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "ctf" || rows[0].Version != "1.0.0" {
		t.Fatalf("migration lost the existing row: %v", rows)
	}
	if rows[0].Units != nil {
		t.Fatalf("a pre-migration row should mean the whole pack, got %v", rows[0].Units)
	}
	// And the migrated table accepts a selection, which is the whole point of the column.
	if err := installs.Record("ctf", "1.0.1", []string{"role/CTF"}); err != nil {
		t.Fatalf("Record into the migrated table: %v", err)
	}
}
