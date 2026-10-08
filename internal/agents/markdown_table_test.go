package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/plugin"
)

func writeAgent(t *testing.T, path, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: " + id + "\nname: " + id + "\ndescription: agent " + id + "\ntools: []\nmax_iterations: 0\n---\n\n## 职责\n\n" + id + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func installAgentBundle(t *testing.T, table *plugin.Table, root, id, version string) {
	t.Helper()
	writeAgent(t, filepath.Join(root, "agents", id+".md"), id)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, plugin.ManifestFileName), []byte(
		"id: "+id+"\nversion: "+version+"\nunits:\n  - kind: agent\n    path: agents/"+id+".md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := plugin.LoadManifestDir(root)
	if err != nil {
		t.Fatalf("LoadManifestDir: %v", err)
	}
	b, err := m.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := table.InstallBundle(b); err != nil {
		t.Fatalf("InstallBundle: %v", err)
	}
}

func agentFileNames(load *MarkdownDirLoad) []string {
	out := make([]string, 0, len(load.FileEntries))
	for _, fa := range load.FileEntries {
		out = append(out, fa.Filename)
	}
	return out
}

// TestLoadMarkdownAgentPathsMatchesDirectoryLoad is the parity clause for feeding the parser
// from a file list instead of a directory scan: the two must agree, including which file is the
// orchestrator and which are sub-agents.
//
// The subject is the catalogue (bundles/*/agents), not agents/ - the factory directory ships no
// agent definitions any more, and a comparison over an empty tree would be the one thing this
// test exists to refuse. Staging every pack's file into one directory is what makes the two
// loaders comparable at all: the run path reads a flat path list, the fallback reads a directory.
func TestLoadMarkdownAgentPathsMatchesDirectoryLoad(t *testing.T) {
	root := moduleRootForAgents(t)
	sources, err := filepath.Glob(filepath.Join(root, "bundles", "*", "agents", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) < 20 {
		t.Fatalf("the catalogue yielded %d agent files (measured 20); the comparison would be vacuous", len(sources))
	}
	dir := t.TempDir()
	var paths []string
	for _, src := range sources {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, filepath.Base(src))
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, dst)
	}

	byDir, err := LoadMarkdownAgentsDir(dir)
	if err != nil {
		t.Fatalf("directory load: %v", err)
	}
	if len(byDir.FileEntries) != len(paths) {
		t.Fatalf("the staged directory yielded %d entries for %d files", len(byDir.FileEntries), len(paths))
	}
	if byDir.Orchestrator == nil {
		t.Fatalf("expected the catalogue's orchestration pack to define a Deep orchestrator")
	}

	byPaths, err := LoadMarkdownAgentPaths(paths)
	if err != nil {
		t.Fatalf("paths load: %v", err)
	}

	want, _ := json.Marshal(byDir)
	got, _ := json.Marshal(byPaths)
	if string(want) != string(got) {
		t.Fatalf("path-driven load differs from the directory scan\n  dir:   %d entries %v\n  paths: %d entries %v",
			len(byDir.FileEntries), agentFileNames(byDir), len(byPaths.FileEntries), agentFileNames(byPaths))
	}
}

func TestLoadMarkdownAgentsPrefersTheTable(t *testing.T) {
	builtIn := t.TempDir()
	writeAgent(t, filepath.Join(builtIn, "recon.md"), "recon")

	table := plugin.NewTable()
	units, err := plugin.ScanDir(plugin.KindAgent, builtIn, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		if err := table.PutLocal(u); err != nil {
			t.Fatalf("PutLocal: %v", err)
		}
	}
	plugin.Install(table)
	t.Cleanup(func() { plugin.Install(nil) })

	load, err := LoadMarkdownAgents(builtIn)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(load.SubAgents) != 1 {
		t.Fatalf("sub-agents = %d, want the one built-in file", len(load.SubAgents))
	}

	installAgentBundle(t, table, filepath.Join(t.TempDir(), "pack"), "mobile-app-analyst", "1.0.0")
	load, err = LoadMarkdownAgents(builtIn)
	if err != nil {
		t.Fatalf("load after install: %v", err)
	}
	if len(load.SubAgents) != 2 {
		t.Fatalf("sub-agents = %d, want the built-in plus the bundled one: %v", len(load.SubAgents), agentFileNames(load))
	}

	if err := table.UninstallBundle("mobile-app-analyst"); err != nil {
		t.Fatal(err)
	}
	load, err = LoadMarkdownAgents(builtIn)
	if err != nil {
		t.Fatalf("load after unplug: %v", err)
	}
	if len(load.SubAgents) != 1 {
		t.Fatalf("unplugged agent is still loaded: %v", agentFileNames(load))
	}
}

// TestEmptyTableFallsBackToTheDirectory guards the dangerous interpretation: "the table is
// installed but holds no agents" must not become "this installation has no markdown agents",
// which would silently strip every sub-agent from every run.
func TestEmptyTableFallsBackToTheDirectory(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, filepath.Join(dir, "recon.md"), "recon")

	plugin.Install(plugin.NewTable())
	t.Cleanup(func() { plugin.Install(nil) })

	load, err := LoadMarkdownAgents(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(load.SubAgents) != 1 {
		t.Fatalf("sub-agents = %d, want the directory scan to have been used", len(load.SubAgents))
	}
}

func TestDisabledAgentUnitIsNotLoaded(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, filepath.Join(dir, "recon.md"), "recon")
	writeAgent(t, filepath.Join(dir, "pentest.md"), "pentest")

	table := plugin.NewTable()
	units, err := plugin.ScanDir(plugin.KindAgent, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Fatalf("scanned %d units, want 2", len(units))
	}
	for _, u := range units {
		if err := table.PutLocal(u); err != nil {
			t.Fatal(err)
		}
	}
	plugin.Install(table)
	t.Cleanup(func() { plugin.Install(nil) })

	if _, err := table.SetEnabled("agent/pentest", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	load, err := LoadMarkdownAgents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(load.SubAgents) != 1 || load.SubAgents[0].ID != "recon" {
		t.Fatalf("sub-agents = %+v, want only the enabled one", load.SubAgents)
	}
}

func moduleRootForAgents(t *testing.T) string {
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
