package mcp

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"

	"go.uber.org/zap"
)

// A capability pack declares an MCP server without touching config.yaml, so the manager holds two
// sets of servers with different authorities. These tests pin the two rules that make that safe:
// the file is never overwritten by a pack, and neither set can be erased by a write aimed at the
// other one.

func disabledServer(command string) config.ExternalMCPServerConfig {
	return config.ExternalMCPServerConfig{
		Type: "stdio", Command: command, Disabled: true, ExternalMCPEnable: false,
	}
}

func TestPackDeclarationCannotTakeOverAFileDeclaredServer(t *testing.T) {
	manager := NewExternalMCPManager(zap.NewNop())
	manager.LoadConfigs(&config.ExternalMCPConfig{Servers: map[string]config.ExternalMCPServerConfig{
		"lab": {Command: "/usr/local/bin/the-operators-binary"},
	}})

	err := manager.DeclarePackServer("lab", "ai-app-redteam", disabledServer("python3"))
	if err == nil {
		t.Fatal("a pack replaced the server the operator declared in config.yaml")
	}
	if !strings.Contains(err.Error(), "配置文件") {
		t.Fatalf("refusal says %q, want it to name the configuration file as the authority", err)
	}
	if got := manager.GetConfigs()["lab"].Command; got != "/usr/local/bin/the-operators-binary" {
		t.Fatalf("the operator's command changed anyway: %q", got)
	}
	if _, owned := manager.PackOwner("lab"); owned {
		t.Fatal("a refused declaration still marks the server as pack-owned")
	}
}

func TestPackDeclarationCannotTakeOverAnotherPacksServer(t *testing.T) {
	manager := NewExternalMCPManager(zap.NewNop())
	if err := manager.DeclarePackServer("lab", "ai-app-redteam", disabledServer("python3")); err != nil {
		t.Fatalf("first declaration: %v", err)
	}
	if err := manager.DeclarePackServer("lab", "wireless-hardware", disabledServer("bash")); err == nil {
		t.Fatal("a second pack replaced the first one's server")
	}
	// The same pack re-declaring its own server is an upgrade, not a takeover.
	if err := manager.DeclarePackServer("lab", "ai-app-redteam", disabledServer("python3.11")); err != nil {
		t.Fatalf("re-declaring own server: %v", err)
	}
	if got := manager.GetConfigs()["lab"].Command; got != "python3.11" {
		t.Fatalf("the upgrade did not land: %q", got)
	}
	if owner, owned := manager.PackOwner("lab"); !owned || owner != "ai-app-redteam" {
		t.Fatalf("ownership after upgrade: owner=%q owned=%v", owner, owned)
	}
}

func TestReloadKeepsPackServersAndLetsTheFileWinACollision(t *testing.T) {
	manager := NewExternalMCPManager(zap.NewNop())
	if err := manager.DeclarePackServer("lab", "mcp-pack", disabledServer("python3")); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if err := manager.DeclarePackServer("second", "mcp-pack", disabledServer("node")); err != nil {
		t.Fatalf("declare second: %v", err)
	}

	// 应用配置 rebuilds the live map from config.yaml. Without the pack overlay both declarations
	// would vanish while their capability units still claimed to be served.
	manager.LoadConfigs(&config.ExternalMCPConfig{Servers: map[string]config.ExternalMCPServerConfig{
		"file-only": {Command: "/bin/true"},
	}})
	live := manager.GetConfigs()
	for _, name := range []string{"lab", "second", "file-only"} {
		if _, ok := live[name]; !ok {
			t.Fatalf("reload dropped %q; live set is %v", name, configNames(live))
		}
	}

	// The operator can also name a server the pack declared, and then the file wins outright: the
	// pack loses the live slot so the console stops reporting the unit as served.
	manager.LoadConfigs(&config.ExternalMCPConfig{Servers: map[string]config.ExternalMCPServerConfig{
		"lab": {Command: "/usr/local/bin/the-operators-binary"},
	}})
	live = manager.GetConfigs()
	if got := live["lab"].Command; got != "/usr/local/bin/the-operators-binary" {
		t.Fatalf("the pack's declaration shadowed the operator's: %q", got)
	}
	if _, owned := manager.PackOwner("lab"); owned {
		t.Fatal("the pack still owns a server the file took over, so unplug would delete the operator's")
	}
	if _, ok := live["second"]; !ok {
		t.Fatal("losing one collision erased the pack's other servers too")
	}
}

func TestOperatorSideWritesRefuseAPackServer(t *testing.T) {
	manager := NewExternalMCPManager(zap.NewNop())
	if err := manager.DeclarePackServer("lab", "mcp-pack", disabledServer("python3")); err != nil {
		t.Fatalf("declare: %v", err)
	}

	cases := []struct {
		name string
		call func() error
	}{
		{"AddOrUpdateConfig", func() error {
			return manager.AddOrUpdateConfig("lab", disabledServer("/bin/echo"))
		}},
		{"RemoveConfig", func() error { return manager.RemoveConfig("lab") }},
		{"StartClient", func() error { return manager.StartClient("lab") }},
		{"StopClient", func() error { return manager.StopClient("lab") }},
	}
	for _, tc := range cases {
		err := tc.call()
		if err == nil {
			t.Errorf("%s changed a pack-declared server from the operator side", tc.name)
			continue
		}
		if !errors.Is(err, ErrPackOwnedServer) {
			t.Errorf("%s returned %v, want it to wrap ErrPackOwnedServer", tc.name, err)
		}
	}

	if _, ok := manager.GetConfigs()["lab"]; !ok {
		t.Fatal("a refused write removed the declaration")
	}
	if manager.GetConfigs()["lab"].Command != "python3" {
		t.Fatalf("a refused write replaced the declaration: %+v", manager.GetConfigs()["lab"])
	}

	// Unplugging the pack is the one path allowed to remove it, and it forgets the ownership too.
	if err := manager.RemovePackServer("lab"); err != nil {
		t.Fatalf("RemovePackServer: %v", err)
	}
	if _, ok := manager.GetConfigs()["lab"]; ok {
		t.Fatal("the server survived the unplug")
	}
	if _, owned := manager.PackOwner("lab"); owned {
		t.Fatal("the manager still treats the name as pack-owned, blocking the operator")
	}
	// Once the pack is gone, the operator's own writes work again.
	if err := manager.AddOrUpdateConfig("lab", disabledServer("/bin/true")); err != nil {
		t.Fatalf("AddOrUpdateConfig after unplug: %v", err)
	}
}

// RemovePackServer's own contract says a server the pack never owned is not its to remove. The
// implementation has to hold that line: a name that was never pack-declared (the operator's own
// config.yaml server, say, after it shadowed a pack's declaration) must be refused and left
// alone - otherwise unplugging the shadowed pack closes the operator's server with it.
func TestRemovePackServerRefusesANameThePackNeverOwned(t *testing.T) {
	manager := NewExternalMCPManager(zap.NewNop())
	if err := manager.AddOrUpdateConfig("lab", disabledServer("/usr/local/bin/the-operators-binary")); err != nil {
		t.Fatalf("AddOrUpdateConfig: %v", err)
	}
	if err := manager.RemovePackServer("lab"); err == nil {
		t.Fatal("RemovePackServer deleted a server no pack ever declared")
	}
	if _, ok := manager.GetConfigs()["lab"]; !ok {
		t.Fatal("the operator's config-owned server was removed")
	}
	if got := manager.GetConfigs()["lab"].Command; got != "/usr/local/bin/the-operators-binary" {
		t.Fatalf("the operator's server was replaced: %+v", manager.GetConfigs()["lab"])
	}
	// The owned direction keeps working: the refusal is about ownership, not about removing.
	if err := manager.DeclarePackServer("lab", "mcp-pack", disabledServer("python3")); err == nil {
		t.Fatal("a pack declaration overwrote the operator's server while the refusal was tested")
	}
}

func configNames(m map[string]config.ExternalMCPServerConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
