package security

import (
	"context"
	"strings"
	"testing"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/pluginhost"

	"go.uber.org/zap"
)

// The rule this pins is the one the whole plug-in chain rests on: a capability whose runtime says
// "run out of process" is *never* answered in-process. Before it, the store could widen what the
// host runs by naming a tool the recipe table also has; after it, forgetting the host means the
// tool stops working rather than silently falling back to a command line.

func pluginRoutingExecutor(t *testing.T, tools []config.ToolConfig) *Executor {
	t.Helper()
	previous := pluginhost.Global()
	service := pluginhost.NewService(nil, zap.NewNop())
	pluginhost.InstallService(service)
	t.Cleanup(func() { pluginhost.InstallService(previous) })

	logger := zap.NewNop()
	return NewExecutor(&config.SecurityConfig{Tools: tools}, mcp.NewServer(logger), logger)
}

func registerPluginRoutedCapability(t *testing.T, id, name string, runtime capability.Runtime) {
	t.Helper()
	registry := capability.Global()
	specs := []*capability.Spec{{
		ID: id, Name: name, Runtime: runtime,
		Class: capability.ClassReadonly, Approval: capability.ApprovalNever,
	}}
	if err := registry.RegisterSubset(capability.LayerPlugin, "security-routing", specs); err != nil {
		t.Fatalf("register the subset: %v", err)
	}
	t.Cleanup(func() {
		if removed := registry.UnregisterSubset(capability.LayerPlugin, "security-routing"); removed != 1 {
			t.Errorf("unregistering removed %d specs, want 1", removed)
		}
	})
}

func TestPluginRuntimeIsRefusedWhenNoHostIsConfigured(t *testing.T) {
	// A recipe with the same name exists and would run. The plugin runtime must win over it, because
	// "the reviewed binary answers this" and "a command line answers this" are not the same promise.
	executor := pluginRoutingExecutor(t, []config.ToolConfig{{
		Name: "sectest.echo", Command: "/bin/echo", Enabled: true,
	}})
	registerPluginRoutedCapability(t, "sectest.echo", "sectest.echo", capability.RuntimePluginAbi)
	result, err := executor.ExecuteTool(context.Background(), "sectest.echo", map[string]interface{}{"text": "hi"})
	if err == nil {
		t.Fatalf("a plugin capability ran without a host: %+v", result)
	}
	if !strings.Contains(err.Error(), "requires the plugin runtime") {
		t.Fatalf("error = %v, want the fail-closed refusal naming the plugin runtime", err)
	}
	if result != nil {
		t.Fatalf("a refused call must not carry a result: %+v", result)
	}
}

func TestNonPluginRuntimeStillTakesTheRecipePath(t *testing.T) {
	executor := pluginRoutingExecutor(t, nil)
	registerPluginRoutedCapability(t, "sectest.recipe", "sectest.recipe", capability.RuntimeGoBuiltin)

	// The complement of the test above: the host branch must not swallow every lookup it makes, so
	// an ordinary capability still lands where it always did - the recipe table, which reports the
	// tool as missing here rather than being asked of the plug-in host.
	_, err := executor.ExecuteTool(context.Background(), "sectest.recipe", nil)
	if err == nil {
		t.Fatal("a builtin capability should not be answered by the plug-in host")
	}
	if strings.Contains(err.Error(), "requires the plugin runtime") {
		t.Fatalf("the host branch took a capability it does not own: %v", err)
	}
	if !strings.Contains(err.Error(), "未找到或未启用") {
		t.Fatalf("error = %v, want the recipe table's own missing-tool error", err)
	}
}
