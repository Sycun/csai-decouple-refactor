package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/agents"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// installedFake is the boot path's view of the install store: a fixed set of recorded decisions
// plus the writes it observed. Tests that need no database use it; the real store is exercised
// where a restart's durability is the claim being tested.
type installedFake struct {
	rows     []store.InstalledBundle
	allError error
	writes   []store.InstalledBundle
}

func (f *installedFake) All() ([]store.InstalledBundle, error) {
	if f.allError != nil {
		return nil, f.allError
	}
	return f.rows, nil
}

func (f *installedFake) Record(bundleID, version string) error {
	f.writes = append(f.writes, store.InstalledBundle{ID: bundleID, Version: version})
	return nil
}

// An installed pack that only exists until the next restart is not installed; it is session
// state. Every run path reads the capability table, and the table is rebuilt from disk at
// start-up, so the packs the install store records have to be put back into it here.
//
// The other direction matters just as much: a directory nobody installed must NOT come back at
// boot. The same root holds the catalogue, and "shipped next to the app" becoming "part of this
// installation" on its own is exactly the self-installing catalogue the record exists to end.
func TestBundlesOnDiskAreReinstalledAtBoot(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"roles", "agents", "skills", "tools", "bundles"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFileAt(t, filepath.Join(root, "roles", "内置角色.yaml"),
		"name: 内置角色\nuser_prompt: 内置\nenabled: true\n")
	writeFileAt(t, filepath.Join(root, "agents", "recon.md"), "---\nid: recon\nname: 侦察\ndescription: 侦察\n---\n\n# 侦察\n")

	pack := filepath.Join(root, "bundles", "restart-pack")
	writeFileAt(t, filepath.Join(pack, "roles", "重启仍在.yaml"),
		"name: 重启仍在\nuser_prompt: 包提供的角色\nenabled: true\n")
	writeFileAt(t, filepath.Join(pack, "agents", "pack-agent.md"),
		"---\nid: pack-agent\nname: 包代理\ndescription: 包提供的子代理\n---\n\n# 包代理\n")
	writeFileAt(t, filepath.Join(pack, plugin.ManifestFileName),
		"id: restart-pack\nname: 重启仍在包\nversion: 1.0.0\nunits:\n"+
			"  - kind: role\n    path: roles/重启仍在.yaml\n"+
			"  - kind: agent\n    path: agents/pack-agent.md\n")

	// A pack that tries to shadow a shipped role: the built-in wins, and the pack is refused by
	// name - not silently preferred because it happened to load first.
	shadow := filepath.Join(root, "bundles", "shadow-pack")
	writeFileAt(t, filepath.Join(shadow, "roles", "内置角色.yaml"),
		"name: 内置角色\nuser_prompt: 冒充内置\nenabled: true\n")
	writeFileAt(t, filepath.Join(shadow, plugin.ManifestFileName),
		"id: shadow-pack\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/内置角色.yaml\n")

	broken := filepath.Join(root, "bundles", "broken-pack")
	writeFileAt(t, filepath.Join(broken, plugin.ManifestFileName),
		"id: broken-pack\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/缺失.yaml\n")

	// A directory nobody installed. It sits in the same root the boot path reads, and it must stay
	// out of the table: this is the catalogue.
	catalog := filepath.Join(root, "bundles", "catalog-pack")
	writeFileAt(t, filepath.Join(catalog, "roles", "目录里等待.yaml"),
		"name: 目录里等待\nuser_prompt: 没点安装\nenabled: true\n")
	writeFileAt(t, filepath.Join(catalog, plugin.ManifestFileName),
		"id: catalog-pack\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/目录里等待.yaml\n")

	db := openBootTestDB(t)
	installs := store.NewInstalledBundles(db)
	if err := installs.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	// restart-pack was installed at an older version, so the version refresh is observable.
	for id, version := range map[string]string{
		"restart-pack": "0.9.0", "shadow-pack": "1.0.0", "broken-pack": "1.0.0",
		// A row with no directory: the decision is kept (putting the directory back restores the
		// pack), and the boot reports it rather than silently dropping it.
		"ghost-pack": "1.0.0",
	} {
		if err := installs.Record(id, version); err != nil {
			t.Fatalf("Record %s: %v", id, err)
		}
	}

	table := plugin.NewTable()
	cfg := &config.Config{AgentsDir: "agents"}
	cfg.Security.ToolsDir = "tools"
	configPath := filepath.Join(root, "config.yaml")
	writeFileAt(t, configPath, "server:\n  port: 0\n")

	if err := scanBuiltInCapabilities(table, cfg, configPath, zap.NewNop()); err != nil {
		t.Fatalf("scanBuiltInCapabilities: %v", err)
	}
	// What RoleHandler.Reload does first at boot: the shipped roles go into the table before any
	// pack is re-installed, which is what makes a shadowing pack refuseable rather than winning.
	roleUnits, err := plugin.ScanDir(plugin.KindRole, filepath.Join(root, "roles"), func(kind plugin.Kind, path string) (string, error) {
		role, err := config.LoadRoleFromFile(path)
		if err != nil {
			return "", err
		}
		return role.Name, nil
	})
	if err != nil {
		t.Fatalf("ScanDir(roles): %v", err)
	}
	for _, u := range roleUnits {
		if err := table.PutLocal(u); err != nil {
			t.Fatalf("PutLocal %s: %v", u.ID, err)
		}
	}
	installed, refused := installBundlesFromDisk(table, filepath.Join(root, "bundles"), installs, zap.NewNop())
	if installed != 1 {
		t.Fatalf("boot re-installed %d packs, want 1 (refused=%v)", installed, refused)
	}
	if len(refused) != 3 {
		t.Fatalf("refused %d packs, want shadow-pack, broken-pack and ghost-pack: %v", len(refused), refused)
	}
	joined := strings.Join(refused, "\n")
	if !strings.Contains(joined, "shadow-pack") || !strings.Contains(joined, "内置角色") {
		t.Errorf("the shadowing pack is not refused by naming the identity it clashes with: %v", refused)
	}
	if !strings.Contains(joined, "broken-pack") {
		t.Errorf("a pack with an unreadable manifest is not reported: %v", refused)
	}
	if !strings.Contains(joined, "ghost-pack") {
		t.Errorf("a record whose directory is gone is not reported: %v", refused)
	}

	// The pack's units are in the table and attributed to it, so the console and every run path
	// see them after a restart without anybody clicking install again.
	unit, ok := table.Unit("role/重启仍在")
	if !ok {
		t.Fatal("the pack's role is missing from the table after boot")
	}
	if unit.Bundle != "restart-pack" {
		t.Errorf("boot-installed unit lost its bundle ownership: %+v", unit)
	}
	if _, ok := table.Unit("agent/pack-agent"); !ok {
		t.Error("the pack's agent is missing from the table after boot")
	}
	if builtIn, ok := table.Unit("role/内置角色"); !ok || builtIn.Bundle != "" {
		t.Fatalf("the shipped role was replaced by the pack: %+v ok=%v", builtIn, ok)
	}
	// The catalogue pack is the negative half of the same rule: on disk, in no table.
	if _, ok := table.Unit("role/目录里等待"); ok {
		t.Error("a pack nobody installed came back at boot - the catalogue installed itself")
	}
	// The directory moved the pack forward, so the recorded version follows: the row describes
	// what is installed, not what was installed the first time.
	rows, err := installs.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	versionOf := map[string]string{}
	for _, row := range rows {
		versionOf[row.ID] = row.Version
	}
	if versionOf["restart-pack"] != "1.0.0" {
		t.Errorf("the recorded version did not follow the directory: %v", versionOf)
	}
	if versionOf["ghost-pack"] != "1.0.0" {
		t.Errorf("a missing directory dropped its install decision: %v", versionOf)
	}

	// And the run path actually serves them: install means "the next agent run has it", and a
	// restart must not be able to take that away.
	previous := plugin.Global()
	plugin.Install(table)
	t.Cleanup(func() { plugin.Install(previous) })
	load, err := agents.LoadMarkdownAgents(filepath.Join(root, "agents"))
	if err != nil {
		t.Fatalf("LoadMarkdownAgents: %v", err)
	}
	var names []string
	for _, entry := range load.FileEntries {
		names = append(names, entry.Filename)
	}
	if len(load.SubAgents) != 2 {
		t.Fatalf("after boot the run path sees %d sub-agents (%v), want the shipped one plus the pack's", len(load.SubAgents), names)
	}
}

// With no readable install store, nothing is re-installed. "Everything on disk" is the tempting
// fallback and the wrong one: it would make the shipped catalogue live again on any database
// hiccup, which is the behaviour the record exists to end.
func TestBootInstallsNothingWithoutAReadableInstallRecord(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "bundles", "waiting-pack")
	writeFileAt(t, filepath.Join(pack, "roles", "货架角色.yaml"),
		"name: 货架角色\nuser_prompt: 等待安装\nenabled: true\n")
	writeFileAt(t, filepath.Join(pack, plugin.ManifestFileName),
		"id: waiting-pack\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/货架角色.yaml\n")

	for name, records := range map[string]installRecords{
		"nil records":      nil,
		"erroring records": &installedFake{allError: errors.New("no such table: installed_bundles")},
	} {
		table := plugin.NewTable()
		installed, refused := installBundlesFromDisk(table, filepath.Join(root, "bundles"), records, zap.NewNop())
		if installed != 0 || len(refused) != 0 {
			t.Errorf("%s: installed=%d refused=%v, want nothing attempted", name, installed, refused)
		}
		if _, ok := table.Unit("role/货架角色"); ok {
			t.Errorf("%s: a pack nobody installed became live", name)
		}
	}

	// A row that is not a directory name is refused before the join, never resolved into a path.
	fake := &installedFake{rows: []store.InstalledBundle{{ID: "../evil", Version: "1.0.0"}}}
	table := plugin.NewTable()
	installed, refused := installBundlesFromDisk(table, filepath.Join(root, "bundles"), fake, zap.NewNop())
	if installed != 0 || len(refused) != 1 || !strings.Contains(refused[0], "../evil") {
		t.Fatalf("installed=%d refused=%v, want the unusable id refused by name", installed, refused)
	}
}

func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
