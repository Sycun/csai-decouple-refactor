package app

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"cyberstrike-ai/internal/agents"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"
)

// The claim this file tests is the one that makes "完全插件化" more than a new package: the
// capabilities that already ship (roles, markdown agents, skills, tool recipes) must be
// addressable through the plugin table *with the same identity the existing loaders give
// them*. If the two disagreed, installing or unplugging would operate on one name while the
// server served another - and both sides would look green.
//
// Truth source is the shipped loaders themselves (config.LoadRolesFromDir,
// agents.LoadMarkdownAgentsDir, config.LoadToolsFromDir), not a hand-written list, so this
// fails the day somebody adds or renames a capability without the table agreeing.

func pluginRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test working directory")
		}
		dir = parent
	}
}

// roleNamer mirrors config.LoadRolesFromDir: the `name:` field wins, the file name is the
// fallback, and an unloadable file is a hard error here rather than the skipped-with-warning
// the runtime path accepts (a parity test cannot compare against a set it silently dropped).
func roleNamer(kind plugin.Kind, path string) (string, error) {
	if kind != plugin.KindRole {
		return "", nil
	}
	role, err := config.LoadRoleFromFile(path)
	if err != nil {
		return "", err
	}
	return role.Name, nil
}

func toolNamer(kind plugin.Kind, path string) (string, error) {
	if kind != plugin.KindTool {
		return "", nil
	}
	tool, err := config.LoadToolFromFile(path)
	if err != nil {
		return "", err
	}
	return tool.Name, nil
}

func TestShippedRolesScanWithTheSameIdentityAsTheLoader(t *testing.T) {
	root := pluginRepoRoot(t)
	dir := filepath.Join(root, "roles")

	loaded, err := config.LoadRolesFromDir(dir)
	if err != nil {
		t.Fatalf("LoadRolesFromDir: %v", err)
	}
	units, err := plugin.ScanDir(plugin.KindRole, dir, roleNamer)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	// The factory shape, asserted exactly: one role. Every professional role ships in a bundle and
	// only exists after somebody installed it - a role reappearing here would undo that quietly,
	// because the catalogue can carry the same identity.
	if len(units) != 1 {
		t.Fatalf("the factory roles/ directory yields %d units (%v), want exactly the default role",
			len(units), sortedOf(nameSet(units)))
	}
	if !nameSet(units)["默认"] {
		t.Fatalf("the one factory role is %v, want 默认", sortedOf(nameSet(units)))
	}
	if len(units) != len(nameSet(units)) {
		// Two files claiming one identity would make the table keep whichever scanned last,
		// so an unplug would remove a role the operator never installed.
		t.Fatalf("roles/ yields %d units but only %d distinct identities", len(units), len(nameSet(units)))
	}
	if got, want := nameSet(units), keySet(loaded); !equalSets(got, want) {
		t.Fatalf("role identities disagree\n  loader: %v\n  table:  %v\n  only in loader: %v\n  only in table:  %v",
			sortedOf(want), sortedOf(got), missing(want, got), missing(got, want))
	}
	for _, u := range units {
		if u.ID != "role/"+u.Name {
			t.Errorf("role unit identity %q is not role/%q", u.ID, u.Name)
		}
		if len(u.Digest) != 64 {
			t.Errorf("role %s carries no digest", u.Name)
		}
	}
}

func TestShippedToolRecipesScanWithTheSameIdentityAsTheLoader(t *testing.T) {
	root := pluginRepoRoot(t)
	dir := filepath.Join(root, "tools")

	loaded, err := config.LoadToolsFromDir(dir)
	if err != nil {
		t.Fatalf("LoadToolsFromDir: %v", err)
	}
	units, err := plugin.ScanDir(plugin.KindTool, dir, toolNamer)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	if len(units) < 90 {
		t.Fatalf("only %d tool units scanned (floor 90)", len(units))
	}
	want := map[string]bool{}
	for i := range loaded {
		want[loaded[i].Name] = true
	}
	if got := nameSet(units); !equalSets(got, want) {
		t.Fatalf("tool identities disagree\n  only in loader: %v\n  only in table:  %v",
			sortedOf(missing(want, got)), sortedOf(missing(got, want)))
	}
}

func TestShippedMarkdownAgentsScanAsTheLoaderSeesThem(t *testing.T) {
	root := pluginRepoRoot(t)
	dir := filepath.Join(root, "agents")

	loaded, err := agents.LoadMarkdownAgentsDir(dir)
	if err != nil {
		t.Fatalf("LoadMarkdownAgentsDir: %v", err)
	}
	units, err := plugin.ScanDir(plugin.KindAgent, dir, nil)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	// The factory shape, asserted exactly: no agents. Every orchestrator and specialist ships in a
	// bundle (README.md in this directory is skipped by both the scanner and the loader on purpose).
	if len(units) != 0 {
		t.Fatalf("the factory agents/ directory yields %d units (%v), want none: every agent ships in a pack",
			len(units), sortedOf(nameSet(units)))
	}
	if len(loaded.FileEntries) != 0 {
		t.Fatalf("the agent loader sees %d factory definitions (%v), want none", len(loaded.FileEntries), loaded.FileEntries)
	}
	want := map[string]bool{}
	for _, entry := range loaded.FileEntries {
		want[strings.TrimSuffix(entry.Filename, filepath.Ext(entry.Filename))] = true
	}
	if got := nameSet(units); !equalSets(got, want) {
		t.Fatalf("agent identities disagree\n  loader: %v\n  table:  %v", sortedOf(want), sortedOf(got))
	}
}

// TestShippedSkillsScanAsDirectoryUnits checks the one kind whose source is a directory, and
// that a stray directory under skills/ (no SKILL.md) is not counted as a capability.
func TestShippedSkillsScanAsDirectoryUnits(t *testing.T) {
	root := pluginRepoRoot(t)
	dir := filepath.Join(root, "skills")

	units, err := plugin.ScanDir(plugin.KindSkill, dir, nil)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var dirs, withSkillMD int
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dirs++
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err == nil {
			withSkillMD++
		}
	}
	// The factory shape, asserted exactly: the kept skills. The professional methods ship in
	// bundles and only exist after an install.
	kept := map[string]bool{
		"pentest-agent-os":         true, // 总纲/路由：把渗透抽象成状态空间搜索
		"pentest-blackboard":       true, // 项目黑板：跨会话 Fact 图与多代理协调
		"pentest-output-standards": true, // 输出规范：报告模板与黑板状态总览
		"pentest-verification":     true, // 验证铁律：confirmed 须证据、负结果也落库
		"cyberstrike-eino-demo":    true, // 技能包格式的演示样例（带 scripts/references/assets）
	}
	if got := nameSet(units); !equalSets(got, kept) {
		t.Fatalf("the factory skills/ directory yields %v, want exactly the kept set %v",
			sortedOf(got), sortedOf(kept))
	}
	if len(units) != withSkillMD {
		t.Fatalf("scanned %d skills but %d directories hold a SKILL.md", len(units), withSkillMD)
	}
	if withSkillMD == dirs {
		// Not a failure: every skill directory is well-formed. The assertion above is the
		// one that matters, and it holds either way.
		t.Logf("every one of the %d skills/ directories holds a SKILL.md, so the exclusion branch is idle", dirs)
	}
	t.Logf("skills: %d scanned / %d directories / %d with SKILL.md", len(units), dirs, withSkillMD)
}

// TestEveryShippedCapabilityBecomesAnAddressableUnit is the end state the objective asks for:
// plug all four shipped directories into one table and read any of them back by identity.
func TestEveryShippedCapabilityBecomesAnAddressableUnit(t *testing.T) {
	root := pluginRepoRoot(t)
	table := plugin.NewTable()

	scans := []struct {
		kind plugin.Kind
		dir  string
		name plugin.Namer
	}{
		{plugin.KindRole, "roles", roleNamer},
		{plugin.KindTool, "tools", toolNamer},
		{plugin.KindAgent, "agents", nil},
		{plugin.KindSkill, "skills", nil},
	}
	total := 0
	for _, scan := range scans {
		units, err := plugin.ScanDir(scan.kind, filepath.Join(root, scan.dir), scan.name)
		if err != nil {
			t.Fatalf("ScanDir(%s): %v", scan.kind, err)
		}
		for _, u := range units {
			if err := table.PutLocal(u); err != nil {
				t.Fatalf("PutLocal %s: %v", u.ID, err)
			}
		}
		total += len(units)
	}
	if total < 96 {
		t.Fatalf("only %d shipped units in the table (floor 96 = 1 role + 90 tools + 0 agents + 5 skills): "+
			"a directory stopped scanning", total)
	}
	if table.Generation() == 0 {
		t.Fatalf("installing %d units did not move the generation", total)
	}
	for _, id := range []string{"role/默认"} {
		if _, ok := table.Unit(id); !ok {
			t.Errorf("shipped capability %s is not addressable by identity", id)
		}
	}
	if got := table.Drifted(); len(got) != 0 {
		t.Errorf("the shipped tree does not match its own digests: %v", got[:min(len(got), 5)])
	}
	t.Logf("plugin table holds %d shipped capability units across %d kinds", total, len(scans))
}

// TestFactoryTreeShipsNoProfessionalCapabilities is the negative half of the minimal-factory
// shape: the professional roles, agents and skills are bundle content now, and only exist after
// an install. A representative identity from each shelf reappearing in the shipped directories
// would quietly undo that - and no count above would notice, because every count here is a floor
// and a bundle can carry the same identity (that collision is what the catalogue test refuses).
func TestFactoryTreeShipsNoProfessionalCapabilities(t *testing.T) {
	root := pluginRepoRoot(t)
	have := unitIDs(scanShippedUnits(t, root))
	for _, id := range []string{
		"role/CTF", "role/渗透测试", "role/后渗透测试", "role/云安全审计",
		"agent/recon", "agent/orchestrator", "agent/penetration",
		"skill/web-attack-methods", "skill/active-directory-attack", "skill/cloud-attack-methods",
	} {
		if have[id] {
			t.Errorf("%s is back in the factory tree: professional capabilities ship in a bundle and "+
				"must stay out until the operator installs one", id)
		}
	}
}

func nameSet(units []plugin.Unit) map[string]bool {
	out := map[string]bool{}
	for _, u := range units {
		out[u.Name] = true
	}
	return out
}

func keySet[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func equalSets(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func missing(from, have map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range from {
		if !have[k] {
			out[k] = true
		}
	}
	return out
}

func sortedOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
