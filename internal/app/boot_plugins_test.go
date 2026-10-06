package app

import (
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/pluginhost"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// A pack's plugin unit is the one kind whose serving state lives in a process the platform starts, so
// start-up has to say plainly that it did not start one. The trust domain, the capabilities/list
// round trip and the capability registrations all happen behind the console switch; a manifest
// claiming `enabled: true` is the author writing about their own file, not the operator agreeing to
// run it.
//
// What this pins is the disagreement the flip removes: without it the table comes back enabled, the
// console shows an enabled unit whose capabilities cannot be called, and the reason it gives is a
// failed enablement that never happened.

func writePluginPack(t *testing.T, root string) string {
	t.Helper()
	pack := filepath.Join(root, "bundles", "boot-plugin-pack")
	binary := filepath.Join(pack, "bin", "refplugin")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	// Never executed by this test: it only has to exist and carry the exec bit, which is what the
	// pack's digest and the declaration's containment rule read.
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileAt(t, filepath.Join(pack, "plugins", "ref.yaml"),
		"pluginId: ref\nbinary: ./bin/refplugin\nversion: 1.0.0\ncapabilities:\n"+
			"  - id: ref.echo\n    title: 回显\n    class: readonly\n    approval: never\n")
	writeFileAt(t, filepath.Join(pack, plugin.ManifestFileName),
		"id: boot-plugin-pack\nname: 启动插件包\nversion: 1.0.0\nunits:\n"+
			"  - kind: plugin\n    path: plugins/ref.yaml\n")
	return pack
}

func useEmptyPluginHost(t *testing.T) *pluginhost.Service {
	t.Helper()
	previous := pluginhost.Global()
	service := pluginhost.NewService(nil, zap.NewNop())
	pluginhost.InstallService(service)
	t.Cleanup(func() { service.Close(); pluginhost.InstallService(previous) })
	return service
}

func TestBootDeclaresPackPluginUnitsWithoutRunningThem(t *testing.T) {
	host := useEmptyPluginHost(t)
	root := t.TempDir()
	pack := writePluginPack(t, root)

	table := plugin.NewTable()
	if installed, refused := installBundlesFromDisk(table, filepath.Join(root, "bundles"), zap.NewNop()); installed != 1 {
		t.Fatalf("boot installed %d packs (refused=%v), want 1", installed, refused)
	}
	unit, ok := table.Unit("plugin/ref")
	if !ok {
		t.Fatalf("the plugin unit is not in the table after installing %s", pack)
	}
	// The premise of the whole test: the pack's own file enabled this unit.
	if !unit.Enabled {
		t.Fatal("the manifest's plugin unit did not come in enabled, so there is nothing to correct")
	}

	flipped, note := declarePackPluginUnits(table)
	if flipped != 1 || note != "" {
		t.Fatalf("declared %d units with note %q, want 1 and no note", flipped, note)
	}
	after, _ := table.Unit("plugin/ref")
	if after.Enabled {
		t.Fatal("the pack's plugin unit is still enabled while nothing runs it")
	}
	if names := host.PackDomainNames(); len(names) != 0 {
		t.Fatalf("start-up handed the pack a trust domain: %v", names)
	}
	if spec, err := capability.Global().Lookup("ref.echo"); err == nil {
		t.Fatalf("a capability from an unstarted plugin is registered: %+v", spec)
	}
	// Second boot pass over the same table state must not report work it did not do.
	if flipped, _ := declarePackPluginUnits(table); flipped != 0 {
		t.Fatalf("re-declaring flipped %d units, want 0", flipped)
	}
}

// A saved switch is replayed only in the "off" direction, and for this kind that rule needs stating
// twice: a row that says "on" cannot bring the unit back either, because re-trusting a binary is a
// decision about the bytes in front of the operator, not a preference to inherit from a past run.
func TestPersistedOnSwitchDoesNotRestartAPackPlugin(t *testing.T) {
	host := useEmptyPluginHost(t)
	root := t.TempDir()
	writePluginPack(t, root)

	db := openSwitchTestDB(t)
	switches := store.NewCapabilitySwitches(db)
	if err := switches.EnsureSchema(); err != nil {
		t.Fatal(err)
	}

	table := plugin.NewTable()
	if installed, _ := installBundlesFromDisk(table, filepath.Join(root, "bundles"), zap.NewNop()); installed != 1 {
		t.Fatalf("installed %d packs, want 1", installed)
	}
	unit, _ := table.Unit("plugin/ref")
	if err := switches.Record(unit.ID, unit.Path, true); err != nil {
		t.Fatal(err)
	}

	if applied, notes := applyPersistedSwitches(table, switches, zap.NewNop()); applied != 0 || len(notes) != 0 {
		t.Fatalf("saved switches applied %d (notes=%v), want 0: only an off decision is replayed", applied, notes)
	}
	if flipped, _ := declarePackPluginUnits(table); flipped != 1 {
		t.Fatalf("the enabled-by-author plugin unit was not brought to declared state")
	}
	after, _ := table.Unit("plugin/ref")
	if after.Enabled {
		t.Fatal("a persisted \"on\" row re-armed a pack's binary")
	}
	if names := host.PackDomainNames(); len(names) != 0 {
		t.Fatalf("the pack owns a trust domain after a restart: %v", names)
	}
}
