package app

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/store"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

// openBootTestDB opens a bare SQLite file: the stores exercised on the boot path (switches,
// install records) each own their own schema, and nothing in these tests needs the platform
// database.
func openBootTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "boot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Start-up rebuilds the table from disk, so the operator's own switch decisions have to be put back
// after the packs are - and only in the direction that cannot widen what may execute.
func TestPersistedSwitchesAreReappliedAtBoot(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "bundles", "switch-pack")
	writeFileAt(t, filepath.Join(pack, "roles", "开关角色.yaml"),
		"name: 开关角色\nuser_prompt: 包提供的角色\nenabled: true\n")
	writeFileAt(t, filepath.Join(pack, "skills", "switched-skill", "SKILL.md"),
		"---\nname: switched-skill\ndescription: 会被关掉的技能\n---\n\n## Steps\n")
	writeFileAt(t, filepath.Join(pack, "mcp", "switched.yaml"),
		"type: stdio\ncommand: python3\nargs: [\"-c\", \"pass\"]\n")
	writeFileAt(t, filepath.Join(pack, plugin.ManifestFileName),
		"id: switch-pack\nname: 开关包\nversion: 1.0.0\nunits:\n"+
			"  - kind: role\n    path: roles/开关角色.yaml\n"+
			"  - kind: skill\n    path: skills/switched-skill\n"+
			"  - kind: mcp\n    path: mcp/switched.yaml\n")

	db := openBootTestDB(t)
	switches := store.NewCapabilitySwitches(db)
	if err := switches.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	installs := store.NewInstalledBundles(db)
	if err := installs.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema (installs): %v", err)
	}
	if err := installs.Record("switch-pack", "1.0.0", nil); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// First boot: the recorded pack lands in the table, the operator switches two units off.
	table := plugin.NewTable()
	if installed, refused := installBundlesFromDisk(table, filepath.Join(root, "bundles"), installs, zap.NewNop()); installed != 1 {
		t.Fatalf("boot 1 installed %d packs (refused=%v), want 1", installed, refused)
	}
	for _, id := range []string{"role/开关角色", "skill/switched-skill"} {
		u, ok := table.Unit(id)
		if !ok {
			t.Fatalf("unit %s missing after install", id)
		}
		if !u.Enabled {
			t.Fatalf("a bundled unit arrived disabled: %+v", u)
		}
		if _, err := table.SetEnabled(id, false); err != nil {
			t.Fatalf("SetEnabled %s: %v", id, err)
		}
		if err := switches.Record(u.ID, u.Path, false); err != nil {
			t.Fatalf("Record %s: %v", id, err)
		}
	}

	// Second boot: a fresh table rebuilt from the same files, then the overlay.
	restarted := plugin.NewTable()
	if installed, _ := installBundlesFromDisk(restarted, filepath.Join(root, "bundles"), installs, zap.NewNop()); installed != 1 {
		t.Fatalf("boot 2 installed %d packs, want 1", installed)
	}
	if u, _ := restarted.Unit("role/开关角色"); !u.Enabled {
		t.Fatal("the rebuilt table is already disabled, so this test would prove nothing")
	}
	applied, notes := applyPersistedSwitches(restarted, switches, zap.NewNop())
	if len(notes) != 0 {
		t.Fatalf("overlay reported failures: %v", notes)
	}
	if applied != 2 {
		t.Fatalf("overlay applied %d switches, want 2", applied)
	}
	for _, id := range []string{"role/开关角色", "skill/switched-skill"} {
		if u, _ := restarted.Unit(id); u.Enabled {
			t.Fatalf("%s came back serving after a restart the operator had switched off", id)
		}
	}
	// An mcp unit has no saved decision (the console says so), so it stays as the pack declared it
	// and start-up's own rule is what disables it later.
	if u, ok := restarted.Unit("mcp/switched"); !ok || !u.Enabled {
		t.Fatalf("the overlay touched a unit nobody switched: %+v ok=%v", u, ok)
	}
}

// A saved row must never turn something on. The runtime state is `file enabled AND table enabled`
// everywhere else in this layer, and an "on" row would be the one place a stale record could widen
// what may execute.
func TestPersistedSwitchOnNeverOverridesTheFile(t *testing.T) {
	root := t.TempDir()
	db := openBootTestDB(t)
	switches := store.NewCapabilitySwitches(db)
	if err := switches.EnsureSchema(); err != nil {
		t.Fatal(err)
	}

	table := plugin.NewTable()
	u, err := plugin.NewUnit(plugin.KindTool, "offline-tool", filepath.Join(root, "tools", "offline.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	u.Enabled = false // what the scanner records for a recipe whose file says enabled: false
	if err := table.PutLocal(u); err != nil {
		t.Fatalf("PutLocal: %v", err)
	}
	if err := switches.Record(u.ID, u.Path, true); err != nil {
		t.Fatalf("Record: %v", err)
	}

	applied, notes := applyPersistedSwitches(table, switches, zap.NewNop())
	if applied != 0 || len(notes) != 0 {
		t.Fatalf("overlay changed state it must not: applied=%d notes=%v", applied, notes)
	}
	if got, _ := table.Unit(u.ID); got.Enabled {
		t.Fatal("a saved \"on\" enabled a unit the file disables")
	}
}

// Rows for identities nobody owns anymore, or for a unit whose source moved, are pruned rather than
// applied: keeping them is how a later capability inherits somebody else's switch.
func TestStaleSwitchRowsArePrunedAtBoot(t *testing.T) {
	root := t.TempDir()
	db := openBootTestDB(t)
	switches := store.NewCapabilitySwitches(db)
	if err := switches.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(root, "roles", "moved.yaml")
	writeFileAt(t, moved, "name: moved\nuser_prompt: 搬过目录\nenabled: true\n")
	other := filepath.Join(root, "roles", "别的.yaml")
	writeFileAt(t, other, "name: 别的\nuser_prompt: 同一个身份换了文件\nenabled: true\n")

	table := plugin.NewTable()
	movedUnit, err := plugin.NewUnit(plugin.KindRole, "moved", moved)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.PutLocal(movedUnit); err != nil {
		t.Fatal(err)
	}
	renamedUnit, err := plugin.NewUnit(plugin.KindRole, "别的", other)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.PutLocal(renamedUnit); err != nil {
		t.Fatal(err)
	}

	// One row for a unit that is gone, one for a unit whose path no longer matches.
	if err := switches.Record("role/gone", filepath.Join(root, "roles", "gone.yaml"), false); err != nil {
		t.Fatal(err)
	}
	if err := switches.Record("role/别的", filepath.Join(root, "roles", "old-name.yaml"), false); err != nil {
		t.Fatal(err)
	}
	// One row that is still exactly right, so pruning cannot be a blanket "delete everything".
	if err := switches.Record("role/moved", moved, false); err != nil {
		t.Fatal(err)
	}

	applied, notes := applyPersistedSwitches(table, switches, zap.NewNop())
	if len(notes) != 0 {
		t.Fatalf("notes=%v, want none", notes)
	}
	if applied != 1 {
		t.Fatalf("applied=%d, want only the row that still matches its unit", applied)
	}
	if u, _ := table.Unit("role/别的"); !u.Enabled {
		t.Fatal("a row about a different source path disabled the unit now sitting there")
	}
	rows, err := switches.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UnitID != "role/moved" {
		t.Fatalf("stale rows survived: %v", rows)
	}
}

// The accepted identity prefixes live in internal/store, which must not import the capability
// table, so nothing at compile time ties the two lists together. A kind missing from the store's
// list is not a crash: the console's switch for that kind is refused, the response says so, and the
// unit reverts at the next restart - which is the bug this table existed to fix.
func TestSwitchStoreAcceptsEveryCapabilityKind(t *testing.T) {
	accepted := map[string]bool{}
	for _, prefix := range store.SwitchKindPrefixes() {
		kind := strings.TrimSuffix(prefix, "/")
		if accepted[kind] {
			t.Fatalf("prefix %q is listed twice", prefix)
		}
		accepted[kind] = true
	}
	for _, kind := range plugin.Kinds {
		if !accepted[string(kind)] {
			t.Errorf("kind %q is a capability kind but the switch store refuses it: its units would "+
				"revert to the file state on every restart", kind)
		}
		delete(accepted, string(kind))
	}
	for extra := range accepted {
		t.Errorf("the switch store accepts %q, which is not a capability kind", extra)
	}
}

// The same installation reaches the same file through different paths depending on how -config was
// named (`./config.yaml` gives `bundles/p/roles/x.yaml`, an absolute one gives the full path), and
// comparing whole paths threw away a switch the operator had just made. Found on a live server
// started with a relative -config; the second restart came up with the role enabled again.
func TestSwitchRowSurvivesADifferentAbsolutePath(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "bundles", "p", "roles", "开关角色.yaml")
	writeFileAt(t, real, "name: 开关角色\nuser_prompt: 同一次安装换了写法\nenabled: true\n")

	table := plugin.NewTable()
	u, err := plugin.NewUnit(plugin.KindRole, "开关角色", filepath.Join("bundles", "p", "roles", "开关角色.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := table.PutLocal(u); err != nil {
		t.Fatal(err)
	}

	db := openBootTestDB(t)
	switches := store.NewCapabilitySwitches(db)
	if err := switches.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	// Recorded while the server was started with an absolute -config; now booted with a relative one.
	if err := switches.Record(u.ID, real, false); err != nil {
		t.Fatalf("Record: %v", err)
	}

	applied, notes := applyPersistedSwitches(table, switches, zap.NewNop())
	if len(notes) != 0 {
		t.Fatalf("notes=%v", notes)
	}
	if applied != 1 {
		t.Fatalf("applied=%d, want the row to match the same slot under a different root", applied)
	}
	if got, _ := table.Unit(u.ID); got.Enabled {
		t.Fatal("the switch was lost because the absolute path changed")
	}
	rows, _ := switches.All()
	if len(rows) != 1 {
		t.Fatalf("a matching slot must not be pruned: %v", rows)
	}
}
