package app

import (
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// The manager's live server set is built from config.yaml at start-up, so a pack's declaration has
// to be re-applied after the packs are re-installed - otherwise the unit is in the table, the
// console calls it installed, and nothing connects to it.
//
// What must NOT happen in the same breath is a process: a pack file saying enabled: true is the
// author's intent, and the switch that means "start this" is the operator's.
func TestBootDeclaresPackServersWithoutStartingThem(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "bundles", "boot-mcp-pack")
	writeFileAt(t, filepath.Join(pack, "mcp", "lab-server.yaml"),
		"type: stdio\ncommand: python3\nargs: [\"-c\", \"pass\"]\ndescription: 实验室\nenabled: true\n")
	writeFileAt(t, filepath.Join(pack, "mcp", "second-server.yaml"),
		"type: http\nurl: http://127.0.0.1:1/mcp\ndescription: 第二个\n")
	// A name the operator's own config.yaml already declares.
	writeFileAt(t, filepath.Join(pack, "mcp", "taken.yaml"),
		"type: stdio\ncommand: /bin/echo\n")
	writeFileAt(t, filepath.Join(pack, plugin.ManifestFileName),
		"id: boot-mcp-pack\nname: 启动声明包\nversion: 1.0.0\nunits:\n"+
			"  - kind: mcp\n    path: mcp/lab-server.yaml\n"+
			"  - kind: mcp\n    path: mcp/second-server.yaml\n"+
			"  - kind: mcp\n    path: mcp/taken.yaml\n")

	table := plugin.NewTable()
	installs := &installedFake{rows: []store.InstalledBundle{{ID: "boot-mcp-pack", Version: "1.0.0"}}}
	if installed, refused := installBundlesFromDisk(table, filepath.Join(root, "bundles"), installs, zap.NewNop()); installed != 1 {
		t.Fatalf("boot installed %d packs (refused=%v), want 1", installed, refused)
	}

	manager := mcp.NewExternalMCPManager(zap.NewNop())
	manager.LoadConfigs(&config.ExternalMCPConfig{Servers: map[string]config.ExternalMCPServerConfig{
		"taken": {Command: "/usr/local/bin/the-operators-binary"},
	}})

	declared, note := provisionDeclaredServers(manager, table)
	if declared != 2 {
		t.Fatalf("provisioned %d servers, want the 2 the pack may declare (note=%q)", declared, note)
	}
	if !strings.Contains(note, "taken") {
		t.Fatalf("the collision with the operator's server is not reported: %q", note)
	}

	live := manager.GetConfigs()
	for _, name := range []string{"lab-server", "second-server"} {
		cfg, ok := live[name]
		if !ok {
			t.Fatalf("%q was never declared; live set is %v", name, live)
		}
		if cfg.ExternalMCPEnable || !cfg.Disabled {
			t.Fatalf("%q arrived started: enable=%v disabled=%v", name, cfg.ExternalMCPEnable, cfg.Disabled)
		}
		if owner, owned := manager.PackOwner(name); !owned || owner != "boot-mcp-pack" {
			t.Fatalf("%q is not credited to the pack that declares it: owner=%q owned=%v", name, owner, owned)
		}
	}
	if got := live["taken"].Command; got != "/usr/local/bin/the-operators-binary" {
		t.Fatalf("the pack replaced the operator's server: %q", got)
	}

	// The table agrees, so the console cannot show a switch that is on for a server nobody started.
	for _, u := range table.Units(plugin.KindMCP) {
		if u.Enabled {
			t.Fatalf("unit %s is enabled while its server was declared disabled: %+v", u.ID, u)
		}
	}
}
