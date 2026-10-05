package app

import (
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"

	"go.uber.org/zap"
)

// The boot scan is what makes "shipped" and "installed later" the same object, and three run paths
// now read it as their source: skills, markdown agents, and the recipe list. A kind that is in
// plugin.Kinds but not scanned here is not a gap in bookkeeping - for tools it means installing any
// bundle replaces the whole built-in recipe list with the pack's one entry, because a table that
// holds *some* tool units is treated as the source.
//
// So this is a gate, not documentation.
func TestBuiltInCapabilityScanCoversEveryServedKind(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []struct{ name, body string }{
		{filepath.Join("tools", "probe.yaml"), "name: probe\ncommand: /bin/true\ndescription: 探针\nenabled: true\n"},
		{filepath.Join("agents", "recon.md"), "---\ndescription: 侦察\n---\n\n# 侦察\n"},
		{filepath.Join("skills", "triage", "SKILL.md"), "---\nname: triage\ndescription: 分诊\n---\n\n## Steps\n"},
	} {
		p := filepath.Join(dir, sub.name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(sub.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{SkillsDir: "skills", AgentsDir: "agents"}
	cfg.Security.ToolsDir = "tools"
	configPath := filepath.Join(dir, "config.yaml")

	table := plugin.NewTable()
	if err := scanBuiltInCapabilities(table, cfg, configPath, zap.NewNop()); err != nil {
		t.Fatalf("scanBuiltInCapabilities: %v", err)
	}

	// Every kind whose units a run path reads from the table must actually land here.
	for _, kind := range []plugin.Kind{plugin.KindTool, plugin.KindAgent, plugin.KindSkill} {
		if got := len(table.Units(kind)); got != 1 {
			t.Fatalf("kind %q: the scan produced %d units, want 1 - a run path that reads this kind "+
				"from the table would see nothing (or only what a bundle added)", kind, got)
		}
	}

	// A kind added to plugin.Kinds has to be classified: scanned, or exempt with a reason.
	sources := map[plugin.Kind]bool{}
	for _, src := range builtInCapabilitySources(cfg, dir, configPath) {
		sources[src.kind] = src.dir != ""
	}
	exempt := map[plugin.Kind]string{
		// Roles are published by RoleHandler.Reload, which owns the file->unit->snapshot chain;
		// MCP servers are live-managed declarations, not files in a scanned directory.
		plugin.KindRole: "RoleHandler.Reload owns the roles directory",
		plugin.KindMCP:  "external MCP servers are runtime declarations, not a scanned directory",
		// A plugin unit is code that arrives inside a pack and is provisioned when the operator
		// switches it on, after its binary has been checked against the reviewed list. There is no
		// built-in plugins directory to scan, and adding one would mean the server ships a binary
		// nobody reviewed.
		plugin.KindPlugin: "pack plugins are declared by a bundle and verified on the switch",
	}
	for _, kind := range plugin.Kinds {
		if sources[kind] || exempt[kind] != "" {
			continue
		}
		t.Fatalf("kind %q is in plugin.Kinds but is neither scanned at boot nor declared exempt in "+
			"this test: any run path that starts reading it from the table would find only bundle "+
			"units and lose every shipped capability", kind)
	}
}
