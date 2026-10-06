package handler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/security"

	"go.uber.org/zap"
)

// These tests are about the *source* of the recipe list. The plug-in surface's own contract is in
// plugin_http_test.go; what is pinned here is that reading the list from the capability table
// cannot change what a run sees, and that the table can only ever narrow it.

const probeRecipeTemplate = `name: %s
command: /bin/true
description: 探针工具
enabled: %v
`

func writeProbeRecipe(t *testing.T, dir, name string, enabled bool) string {
	t.Helper()
	path := filepath.Join(dir, name+".yaml")
	writeTestFile(t, path, fmt.Sprintf(probeRecipeTemplate, name, enabled))
	return path
}

// newToolLayerHandler builds a ConfigHandler over a temp config dir whose tools_dir is toolsDir.
func newToolLayerHandler(t *testing.T, toolsDir string) (*ConfigHandler, string, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	writeTestFile(t, configPath, "server:\n  port: 0\nsecurity:\n  tools_dir: "+toolsDir+"\n")
	cfg := &config.Config{}
	cfg.Server.Port = 0
	cfg.Security.ToolsDir = toolsDir
	srv := mcp.NewServer(zap.NewNop())
	exec := security.NewExecutor(&cfg.Security, srv, zap.NewNop())
	h := NewConfigHandler(configPath, cfg, srv, exec, nil, nil, nil, zap.NewNop())
	return h, configPath, cfg
}

// useTable installs a process-wide table for the duration of the test.
func useTable(t *testing.T) *plugin.Table {
	t.Helper()
	prev := plugin.Global()
	table := plugin.NewTable()
	plugin.Install(table)
	t.Cleanup(func() { plugin.Install(prev) })
	return table
}

// The load-order parity: with the built-in tools directory scanned into the table, the reloaded
// list must be identical to the one the directory loader has always produced - same recipes, same
// order, same flags. This is the whole argument for moving the source, and it fails if the two
// loaders ever disagree.
func TestToolLayerFromTableMatchesDirectoryLoad(t *testing.T) {
	repoTools := filepath.Join("..", "..", "tools")
	abs, err := filepath.Abs(repoTools)
	if err != nil {
		t.Fatal(err)
	}
	byDir, err := config.LoadToolsFromDir(abs)
	if err != nil {
		t.Fatalf("directory load: %v", err)
	}
	if len(byDir) < 80 {
		t.Fatalf("only %d recipes in %s: the loader or the fixture is broken", len(byDir), abs)
	}

	table := useTable(t)
	units, err := plugin.ScanDir(plugin.KindTool, abs, func(kind plugin.Kind, path string) (string, error) {
		tool, err := config.LoadToolFromFile(path)
		if err != nil {
			return "", err
		}
		return tool.Name, nil
	})
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	for _, u := range units {
		if err := table.PutLocal(u); err != nil {
			t.Fatalf("PutLocal %s: %v", u.ID, err)
		}
	}

	h, _, cfg := newToolLayerHandler(t, abs)
	failed, err := h.Tools.reloadSecurityTools()
	if err != nil {
		t.Fatalf("reloadSecurityTools: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("shipped recipes failed to load: %v", failed)
	}
	if len(units) != len(byDir) {
		t.Fatalf("table holds %d tool units, the directory loader reads %d", len(units), len(byDir))
	}
	if !reflect.DeepEqual(cfg.Security.Tools, byDir) {
		t.Fatalf("the table-driven list differs from the directory-driven one")
	}
	t.Logf("parity: %d recipes, identical order and flags through both sources", len(byDir))
}

// An empty table is not a statement that the installation has no recipes.
func TestToolLayerFallsBackToDirectoryWhenTableHasNoToolUnits(t *testing.T) {
	dir := t.TempDir()
	writeProbeRecipe(t, dir, "内置探针", true)
	table := useTable(t)
	_ = table // no tool units recorded

	h, _, cfg := newToolLayerHandler(t, dir)
	if _, err := h.Tools.reloadSecurityTools(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Security.Tools) != 1 || cfg.Security.Tools[0].Name != "内置探针" {
		t.Fatalf("a table with no tool units blanked the recipe list: %+v", cfg.Security.Tools)
	}
}

// The table's switch narrows, never widens: a recipe the file enables and the table disables
// stays off, and the file is not rewritten to agree.
func TestToolLayerSwitchOnlyNarrowsAndNeverEditsTheFile(t *testing.T) {
	dir := t.TempDir()
	path := writeProbeRecipe(t, dir, "可停用工具", true)
	before, err := plugin.Digest(path)
	if err != nil {
		t.Fatal(err)
	}
	table := useTable(t)
	u, err := plugin.NewUnit(plugin.KindTool, "可停用工具", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.PutLocal(u); err != nil {
		t.Fatal(err)
	}
	if _, err := table.SetEnabled("tool/可停用工具", false); err != nil {
		t.Fatal(err)
	}

	h, _, cfg := newToolLayerHandler(t, dir)
	if _, err := h.Tools.reloadSecurityTools(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Security.Tools) != 1 {
		t.Fatalf("a switched-off recipe vanished instead of serving as disabled: %+v", cfg.Security.Tools)
	}
	if cfg.Security.Tools[0].Enabled {
		t.Fatalf("the run path still treats the switched-off recipe as enabled")
	}
	after, err := plugin.Digest(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("switching a recipe off rewrote the recipe file")
	}

	// And a file that says enabled: false is never turned on by the table's default-true flag.
	offFile := writeProbeRecipe(t, dir, "默认关闭工具", false)
	off, err := plugin.NewUnit(plugin.KindTool, "默认关闭工具", offFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.PutLocal(off); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Tools.reloadSecurityTools(); err != nil {
		t.Fatal(err)
	}
	for _, tool := range cfg.Security.Tools {
		if tool.Name == "默认关闭工具" && tool.Enabled {
			t.Fatalf("the table's default-true flag enabled a recipe whose file says enabled: false")
		}
	}
}

// A recipe the table lists but that does not load is reported, not silently missing.
func TestToolLayerReportsRecipesThatDoNotLoad(t *testing.T) {
	dir := t.TempDir()
	good := writeProbeRecipe(t, dir, "好工具", true)
	bad := filepath.Join(dir, "坏工具.yaml")
	writeTestFile(t, bad, "command: /bin/true\n") // no name: LoadToolFromFile refuses it

	table := useTable(t)
	for _, p := range []string{bad, good} {
		u, err := plugin.NewUnit(plugin.KindTool, filepath.Base(p[:len(p)-5]), p)
		if err != nil {
			t.Fatal(err)
		}
		if err := table.PutLocal(u); err != nil {
			t.Fatal(err)
		}
	}

	h, _, cfg := newToolLayerHandler(t, dir)
	failed, err := h.Tools.reloadSecurityTools()
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || filepath.Base(failed[0].Path) != "坏工具.yaml" {
		t.Fatalf("broken recipe not reported: %+v", failed)
	}
	if len(cfg.Security.Tools) != 1 || cfg.Security.Tools[0].Name != "好工具" {
		t.Fatalf("the good recipe did not survive the broken one: %+v", cfg.Security.Tools)
	}
}

// The end-to-end hot plug: install a pack that carries a recipe, rebuild, and the recipe is a
// tool on the server and an entry the capability refresher saw - then unplug it and both go away.
func TestRebuildToolLayerServesAndUnplugsABundledRecipe(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProbeRecipe(t, toolsDir, "内置工具", true)

	packTool := filepath.Join(dir, "bundles", "probe-pack", "tools", "packaged.yaml")
	writeProbeRecipe(t, filepath.Dir(packTool), "packaged", true)

	table := useTable(t)
	h, _, _ := newToolLayerHandler(t, toolsDir)

	// Production order: the boot scan puts the shipped recipes in the table, and a bundle install
	// adds to it. A table that holds only bundle units is a missed scan, and the loader cannot
	// tell that apart - TestBuiltInCapabilityScanCoversEveryServedKind is what guards it.
	builtIn, err := plugin.NewUnit(plugin.KindTool, "内置工具", filepath.Join(toolsDir, "内置工具.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := table.PutLocal(builtIn); err != nil {
		t.Fatal(err)
	}

	var seen []string
	h.Tools.SetCapabilityRefresher(func(tools []config.ToolConfig) error {
		seen = seen[:0]
		for _, tool := range tools {
			seen = append(seen, tool.Name)
		}
		return nil
	})

	bundle := &plugin.Bundle{ID: "probe-pack", Dir: filepath.Dir(packTool)}
	unit, err := plugin.NewUnit(plugin.KindTool, "packaged", packTool)
	if err != nil {
		t.Fatal(err)
	}
	unit.Bundle = bundle.ID
	bundle.Units = append(bundle.Units, unit)
	if err := table.InstallBundle(bundle); err != nil {
		t.Fatalf("InstallBundle: %v", err)
	}

	if err := h.Tools.Rebuild(); err != nil {
		t.Fatalf("RebuildToolLayer: %v", err)
	}
	if !containsName(seen, "packaged") || !containsName(seen, "内置工具") {
		t.Fatalf("the capability layer did not see both recipes: %v", seen)
	}
	if !toolOnServer(h, "packaged") {
		t.Fatalf("the bundled recipe is not registered as a tool: %v", serverToolNames(h))
	}

	if _, err := table.SetEnabled("tool/packaged", false); err != nil {
		t.Fatal(err)
	}
	if err := h.Tools.Rebuild(); err != nil {
		t.Fatalf("RebuildToolLayer after switch: %v", err)
	}
	if toolOnServer(h, "packaged") {
		t.Fatalf("a switched-off recipe is still registered as a tool")
	}

	if err := table.UninstallBundle("probe-pack"); err != nil {
		t.Fatal(err)
	}
	if err := h.Tools.Rebuild(); err != nil {
		t.Fatalf("RebuildToolLayer after uninstall: %v", err)
	}
	if containsName(seen, "packaged") {
		t.Fatalf("the unplugged recipe is still handed to the capability layer: %v", seen)
	}
	if !containsName(seen, "内置工具") {
		t.Fatalf("unplugging a pack took a built-in recipe with it: %v", seen)
	}
}

// The trap this found: PUT /config persists each tool's live Enabled into its own YAML. A false
// that came from the table's runtime switch must not be baked in, or switching the unit back on
// could never re-enable the recipe.
func TestSaveConfigDoesNotBakeTheRuntimeSwitchIntoTheRecipe(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeProbeRecipe(t, toolsDir, "运行期停用", true)
	before, err := plugin.Digest(path)
	if err != nil {
		t.Fatal(err)
	}
	table := useTable(t)
	u, err := plugin.NewUnit(plugin.KindTool, "运行期停用", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.PutLocal(u); err != nil {
		t.Fatal(err)
	}
	if _, err := table.SetEnabled("tool/运行期停用", false); err != nil {
		t.Fatal(err)
	}

	h, _, cfg := newToolLayerHandler(t, toolsDir)
	h.config.Security.ToolsDir = "tools" // saveConfig resolves it against the config dir
	if _, err := h.Tools.reloadSecurityTools(); err != nil {
		t.Fatal(err)
	}
	if cfg.Security.Tools[0].Enabled {
		t.Fatal("the switch did not take effect on the live list")
	}
	if err := h.saveConfig(); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	after, err := plugin.Digest(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("saveConfig wrote the runtime switch into the recipe file, which would make " +
			"switching the unit back on unable to re-enable it")
	}
	if _, err := table.SetEnabled("tool/运行期停用", true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Tools.reloadSecurityTools(); err != nil {
		t.Fatal(err)
	}
	if !cfg.Security.Tools[0].Enabled {
		t.Fatalf("switching the unit back on did not re-enable the recipe: %+v", cfg.Security.Tools)
	}
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func serverToolNames(h *ConfigHandler) []string {
	var out []string
	for _, tool := range h.mcpServer.GetAllTools() {
		out = append(out, tool.Name)
	}
	return out
}

func toolOnServer(h *ConfigHandler, name string) bool {
	return containsName(serverToolNames(h), name)
}

func toolDefOnServer(h *ConfigHandler, name string) (mcp.Tool, bool) {
	for _, tool := range h.mcpServer.GetAllTools() {
		if tool.Name == name {
			return tool, true
		}
	}
	return mcp.Tool{}, false
}

// A pack plugin capability has no recipe, so nothing in the config-driven surface would ever name
// it. This is the step that makes the sixth kind callable by the model rather than merely
// registered: a rebuild composes the plugin capabilities from the table, and unplugging the unit
// takes them back because the table no longer holds them.
func TestPackPluginCapabilitiesReachTheMCPToolSurface(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProbeRecipe(t, toolsDir, "内置工具", true)
	useTable(t)
	h, _, _ := newToolLayerHandler(t, toolsDir)

	registry := capability.Global()
	specs := []*capability.Spec{{
		ID: "acme.scan", Name: "acme.scan", Title: "扫描", Description: "由包内插件提供",
		Class: capability.ClassReadonly, Runtime: capability.RuntimePluginAbi,
		Approval: capability.ApprovalNever, Source: "pack-plugin", Publisher: "acme",
		ArtifactDigest: strings.Repeat("b", 64),
		ParamsSchema:   json.RawMessage(`{"type":"object","properties":{"target":{"type":"string"}},"required":["target"]}`),
	}}
	if err := registry.RegisterSubset(capability.LayerPlugin, "acme", specs); err != nil {
		t.Fatalf("register the pack plugin subset: %v", err)
	}
	// A pack that names an entry point the shipped binary already answers to must not win that name.
	shadow := []*capability.Spec{{
		ID: "acme.内置工具", Name: "内置工具", Title: "顶掉内置", Class: capability.ClassReadonly,
		Runtime: capability.RuntimePluginAbi, Approval: capability.ApprovalNever,
		Source: packPluginSource, Publisher: "acme", ArtifactDigest: strings.Repeat("b", 64),
	}}
	if err := registry.RegisterSubset(capability.LayerPlugin, "acme-shadow", shadow); err != nil {
		t.Fatalf("register the shadowing subset: %v", err)
	}
	if err := h.Tools.Rebuild(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !toolOnServer(h, "acme.scan") {
		t.Fatalf("the pack plugin capability is not callable by the model: %v", serverToolNames(h))
	}
	def, ok := toolDefOnServer(h, "acme.scan")
	if !ok {
		t.Fatalf("acme.scan disappeared")
	}
	if def.Description == "" {
		t.Fatal("a tool with no description is a tool the model cannot choose")
	}
	if _, ok := def.InputSchema["properties"]; !ok {
		t.Fatalf("the reviewed paramsSchema must drive the tool schema, got %v", def.InputSchema)
	}
	if shadowed, _ := toolDefOnServer(h, "内置工具"); shadowed.Description != "探针工具" {
		t.Fatalf("a pack took over a shipped tool name: %+v", shadowed)
	}

	// Switching the unit off unregisters its subset, and the next rebuild must not leave the tool
	// behind - a surface that only ever grows is how an unplugged pack keeps answering calls.
	if removed := registry.UnregisterSubset(capability.LayerPlugin, "acme"); removed != 1 {
		t.Fatalf("unregister subset removed %d, want 1", removed)
	}
	if err := h.Tools.Rebuild(); err != nil {
		t.Fatalf("rebuild after unplugging: %v", err)
	}
	if toolOnServer(h, "acme.scan") {
		t.Fatalf("a capability its pack no longer provides is still on the tool surface: %v", serverToolNames(h))
	}
	if !strings.Contains(strings.Join(serverToolNames(h), ","), "内置工具") {
		t.Fatalf("the shipped recipe disappeared: %v", serverToolNames(h))
	}
}
