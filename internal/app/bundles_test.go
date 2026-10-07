package app

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"cyberstrike-ai/internal/agents"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"
)

// scanShippedUnits scans the four built-in directories the way the server would at start-up.
func scanShippedUnits(t *testing.T, root string) []plugin.Unit {
	t.Helper()
	var out []plugin.Unit
	for _, scan := range []struct {
		kind plugin.Kind
		dir  string
		name plugin.Namer
	}{
		{plugin.KindRole, "roles", roleNamer},
		{plugin.KindTool, "tools", toolNamer},
		{plugin.KindAgent, "agents", nil},
		{plugin.KindSkill, "skills", nil},
	} {
		units, err := plugin.ScanDir(scan.kind, filepath.Join(root, scan.dir), scan.name)
		if err != nil {
			t.Fatalf("ScanDir(%s): %v", scan.kind, err)
		}
		out = append(out, units...)
	}
	return out
}

func unitIDs(units []plugin.Unit) map[string]bool {
	out := make(map[string]bool, len(units))
	for _, u := range units {
		out[u.ID] = true
	}
	return out
}

func exampleBundleDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "bundles"))
	if err != nil {
		t.Fatalf("read bundles/: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, "bundles", e.Name(), plugin.ManifestFileName)
		if _, err := os.Stat(path); err == nil {
			out = append(out, filepath.Dir(path))
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatalf("no bundle under bundles/ - the empty case would make every assertion below vacuous")
	}
	return out
}

// TestExampleBundlesInstallAlongsideShippedCapabilities is the end-to-end shape of "one click to
// extend": the shipped tree is in the table, the catalogue under bundles/ installs on top of it
// without disturbing anything, and unplugging returns the table to exactly the identity set it
// started from. The catalogue is also where the professional content lives now, so this test is
// what proves the factory tree still has a home for every role/agent/skill it no longer ships.
func TestExampleBundlesInstallAlongsideShippedCapabilities(t *testing.T) {
	root := pluginRepoRoot(t)
	shipped := scanShippedUnits(t, root)
	before := unitIDs(shipped)
	if len(before) != len(shipped) {
		t.Fatalf("the built-in directories yield %d units but only %d distinct identities", len(shipped), len(before))
	}
	if len(before) < 96 {
		t.Fatalf("only %d shipped units scanned (measured 96: 1 role + 90 tools + 0 agents + 5 skills)", len(before))
	}

	table := plugin.NewTable()
	for _, u := range shipped {
		if err := table.PutLocal(u); err != nil {
			t.Fatalf("PutLocal %s: %v", u.ID, err)
		}
	}
	genAfterScan := table.Generation()

	added := map[string]bool{}
	var addedUnits []plugin.Unit
	dirs := exampleBundleDirs(t, root)
	for _, dir := range dirs {
		m, err := plugin.LoadManifestDir(dir)
		if err != nil {
			t.Fatalf("LoadManifestDir %s: %v", dir, err)
		}
		b, err := m.Resolve()
		if err != nil {
			t.Fatalf("Resolve %s: %v", dir, err)
		}
		if b.ID != filepath.Base(dir) {
			t.Errorf("bundle id %q does not match its directory name %q", b.ID, filepath.Base(dir))
		}
		for _, u := range b.Units {
			if before[u.ID] {
				t.Fatalf("bundle %s would collide with shipped %s; the example packs must be additive", b.ID, u.ID)
			}
			added[u.ID] = true
			addedUnits = append(addedUnits, u)
		}
		if err := table.InstallBundle(b); err != nil {
			t.Fatalf("InstallBundle %s: %v", b.ID, err)
		}
	}
	if len(added) < 55 {
		t.Fatalf("the shipped catalogue delivers only %d units (measured 59 across 16 packs); the "+
			"professional content has gone thin or lost a pack", len(added))
	}
	// The catalogue is where the professional content lives now: these identities left the factory
	// tree on purpose, so a pack has to carry each of them.
	for _, id := range []string{
		"role/CTF", "role/渗透测试", "role/后渗透测试", "role/云安全审计",
		"agent/recon", "agent/orchestrator", "agent/penetration", "agent/opsec-evasion",
		"skill/web-attack-methods", "skill/active-directory-attack", "skill/cloud-attack-methods",
		"skill/ai-llm-app-attack", "skill/source-code-hunting", "skill/wireless-hardware-attack",
	} {
		if !added[id] {
			t.Errorf("no shipped pack delivers %s: it is no longer in the factory tree, so the "+
				"catalogue must carry it", id)
		}
	}
	if table.Generation() <= genAfterScan {
		t.Fatalf("installing %d example units did not move the generation", len(added))
	}
	if got := table.Drifted(); len(got) != 0 {
		t.Errorf("installed bundles do not match their own digests: %v", got)
	}

	// The delivered role has to be readable by the *existing* loader, not just by the table:
	// that is what makes "install a pack" and "the server serves the role" the same event.
	var roleUnits []plugin.Unit
	for _, u := range table.Units(plugin.KindRole) {
		if added[u.ID] {
			roleUnits = append(roleUnits, u)
		}
	}
	if len(roleUnits) == 0 {
		t.Fatalf("no example bundle delivered a role")
	}
	if len(roleUnits) < 16 {
		t.Fatalf("the catalogue delivers %d roles (measured 16: the 12 factory roles that moved into "+
			"packages plus the 4 pack-only roles)", len(roleUnits))
	}
	for _, u := range roleUnits {
		role, err := config.LoadRoleFromFile(u.Path)
		if err != nil {
			t.Fatalf("the shipped role loader cannot read %s: %v", u.Path, err)
		}
		if role.Name != u.Name {
			t.Errorf("bundle role identity %q is not the name the loader sees (%q)", u.Name, role.Name)
		}
		if !role.Enabled {
			t.Errorf("bundle role %s is not enabled, so it would install invisibly", u.Name)
		}
	}

	// Every other delivered unit has to be readable by the loader that actually serves it, not
	// just present in the table: a pack whose agent does not parse, or whose tool recipe has no
	// enforceable capability manifest, is a content bug that only shows up as a silent absence
	// (or a fail-closed execution) long after the install said "ok".
	var agentPaths, toolPaths []string
	for _, u := range addedUnits {
		switch u.Kind {
		case plugin.KindAgent:
			agentPaths = append(agentPaths, u.Path)
		case plugin.KindTool:
			toolPaths = append(toolPaths, u.Path)
		}
	}
	if len(agentPaths) > 0 {
		load, err := agents.LoadMarkdownAgentPaths(agentPaths)
		if err != nil {
			t.Fatalf("the shipped markdown loader cannot read the example packs' agents: %v", err)
		}
		if got := len(load.FileEntries); got != len(agentPaths) {
			t.Errorf("loaded %d agent definitions from %d delivered agent files", got, len(agentPaths))
		}
	}
	if len(agentPaths) < 20 {
		t.Fatalf("the catalogue delivers only %d agents (measured 20: the 16 that moved out of the "+
			"factory agents/ directory plus the 4 pack-only specialists)", len(agentPaths))
	}
	if len(toolPaths) < 1 {
		t.Fatalf("no example pack ships a tool recipe, so the table-driven recipe path has no shipped " +
			"content proving it")
	}
	if len(toolPaths) > 0 {
		recipes := make([]config.ToolConfig, 0, len(toolPaths))
		for _, p := range toolPaths {
			tool, err := config.LoadToolFromFile(p)
			if err != nil {
				t.Fatalf("the shipped recipe loader cannot read %s: %v", p, err)
			}
			recipes = append(recipes, *tool)
		}
		specs, rejections := RecipeSpecs(recipes)
		if len(rejections) != 0 {
			t.Errorf("an example pack ships a recipe with no enforceable manifest: %v", rejections)
		}
		if len(specs) != len(recipes) {
			t.Errorf("%d bundled recipes yielded %d capability specs", len(recipes), len(specs))
		}
	}

	for _, dir := range dirs {
		m, err := plugin.LoadManifestDir(dir)
		if err != nil {
			t.Fatalf("LoadManifestDir: %v", err)
		}
		if err := table.UninstallBundle(m.ID); err != nil {
			t.Fatalf("UninstallBundle %s: %v", m.ID, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dirs[0], plugin.ManifestFileName)); err != nil {
		t.Errorf("uninstall removed files from disk: %v", err)
	}

	after := map[string]bool{}
	for _, kind := range []plugin.Kind{plugin.KindRole, plugin.KindTool, plugin.KindAgent, plugin.KindSkill} {
		for _, u := range table.Units(kind) {
			after[u.ID] = true
		}
	}
	if len(after) != len(before) {
		t.Fatalf("after unplugging the example packs the table holds %d units, started with %d", len(after), len(before))
	}
	for id := range before {
		if !after[id] {
			t.Errorf("shipped unit %s did not survive the install/unplug round trip", id)
		}
	}
}
