package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/handler"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/pluginhost"

	"go.uber.org/zap"
)

// The real chain: a pack's declaration, the plug-in host starting the pack's binary, the binary
// answering capabilities/list, the cross-check, and the capability table ending up with identities
// that route to that process. Nothing here is a stub, because the claim being tested is exactly
// that the reviewed manifest and the running thing agree.

func buildReferencePlugin(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "refplugin")
	cmd := exec.Command("go", "build", "-o", binary, "../pluginhost/testdata/refplugin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build reference plugin: %v\n%s", err, out)
	}
	return binary
}

// declarePlugin writes one pack into `dir`: the binary is copied to a path inside it, so the
// declaration's containment rule is exercised rather than bypassed.
func declarePlugin(t *testing.T, dir, binary, capabilities string) {
	t.Helper()
	inside := filepath.Join(dir, "bin", "refplugin")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	declaration := "pluginId: ref\nbinary: ./bin/refplugin\nversion: 1.0.0\n" + capabilities
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins", "ref.yaml"), []byte(declaration), 0o600); err != nil {
		t.Fatal(err)
	}
}

func reviewedCapabilities() string {
	return `capabilities:
  - id: ref.echo
    title: 回显
    class: readonly
    approval: never
  - id: ref.env
    title: 环境
    class: readonly
    approval: never
  - id: ref.grant
    title: 授权检查
    class: readonly
    approval: never
  - id: ref.egress
    title: 出网
    class: mutating
    permission: agent:ref.egress
  - id: ref.crash
    title: 崩溃
    class: mutating
    permission: agent:ref.crash
  - id: ref.badcallback
    title: 非法回调
    class: readonly
    approval: never
`
}

func usePluginHost(t *testing.T) *pluginhost.Service {
	t.Helper()
	previous := pluginhost.Global()
	service := pluginhost.NewService(nil, zap.NewNop())
	pluginhost.InstallService(service)
	t.Cleanup(func() {
		service.Close()
		pluginhost.InstallService(previous)
	})
	return service
}

func pluginUnit(dir string) plugin.Unit {
	return plugin.Unit{
		ID: "plugin/ref", Kind: plugin.KindPlugin, Name: "ref", Bundle: "code-pack", Enabled: true,
		Path: filepath.Join(dir, "plugins", "ref.yaml"),
	}
}

func TestPackPluginIsDiscoveredVerifiedAndCallable(t *testing.T) {
	host := usePluginHost(t)
	binary := buildReferencePlugin(t)
	dir := t.TempDir()
	declarePlugin(t, dir, binary, reviewedCapabilities())

	unit := pluginUnit(dir)
	decl, err := handler.LoadPluginUnitDeclaration(unit.Path, dir)
	if err != nil {
		t.Fatalf("declaration: %v", err)
	}
	provisioner := newPackPluginProvisioner(zap.NewNop())

	ids, err := provisioner.ProvisionPackPlugin(unit, decl)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if len(ids) != 6 {
		t.Fatalf("discovered %v, want the six the plugin actually provides", ids)
	}
	if owner, owned := host.PackDomainOwner("ref"); !owned || owner != "code-pack" {
		t.Fatalf("the host does not hold the pack's domain: %q/%v", owner, owned)
	}
	// Each reviewed entry point is now an addressable capability, routed out of process.
	for _, c := range decl.Capability {
		spec, err := capability.Global().Lookup(c.ID)
		if err != nil {
			t.Errorf("capability %s is not registered: %v", c.ID, err)
			continue
		}
		if spec.Runtime != capability.RuntimePluginAbi {
			t.Errorf("%s runtime = %q, must route out of process", c.ID, spec.Runtime)
		}
		if spec.Source != "pack-plugin" || spec.Publisher != "ref" {
			t.Errorf("%s spec = %+v", c.ID, spec)
		}
	}
	// The permission the manifest named is what authorization will ask for, so a capability that
	// mutates has to carry it and a read-only one must not inherit a write rule.
	writable, err := capability.Global().Lookup("ref.egress")
	if err != nil {
		t.Fatal(err)
	}
	if writable.Permission != "agent:ref.egress" || writable.Class != capability.ClassMutating {
		t.Fatalf("ref.egress spec = %+v", writable)
	}

	// And the capability actually runs in the pack's own process.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	params, _ := json.Marshal(map[string]string{"text": "night build"})
	result, err := host.Invoke(ctx, "ref", "ref.echo", params, 20*time.Second)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !strings.Contains(result.Content, "night build") {
		t.Fatalf("the pack's plugin did not answer: %+v", result)
	}

	// Dropping the unit removes its identities: unplugging a pack cannot leave a callable name
	// behind that resolves to no process.
	removed, message := provisioner.DropPackPlugin(unit)
	if removed != 6 || message != "" {
		t.Fatalf("drop = %d %q, want 6 capabilities and no message", removed, message)
	}
	if _, err := capability.Global().Lookup("ref.echo"); err == nil {
		t.Fatal("ref.echo is still callable after the pack's plugin was dropped")
	}
	if _, owned := host.PackDomainOwner("ref"); owned {
		t.Fatal("the trust domain survived the drop")
	}
}

func TestPackPluginRefusedWhenTheBinaryDisagrees(t *testing.T) {
	usePluginHost(t)
	binary := buildReferencePlugin(t)

	t.Run("provides less than reviewed", func(t *testing.T) {
		dir := t.TempDir()
		declarePlugin(t, dir, binary, `capabilities:
  - id: ref.echo
    class: readonly
    approval: never
  - id: ref.notprovided
    class: readonly
    approval: never
`)
		unit := pluginUnit(dir)
		decl, err := handler.LoadPluginUnitDeclaration(unit.Path, dir)
		if err != nil {
			t.Fatalf("declaration: %v", err)
		}
		_, err = newPackPluginProvisioner(zap.NewNop()).ProvisionPackPlugin(unit, decl)
		if err == nil {
			t.Fatal("a plugin missing a reviewed entry point was accepted")
		}
		if !strings.Contains(err.Error(), "缺少") {
			t.Fatalf("refusal does not name the missing entries: %v", err)
		}
	})

	t.Run("provides more than reviewed", func(t *testing.T) {
		dir := t.TempDir()
		// Only five of the six declared: the plugin still answers with six, so one entry point is
		// something no reviewer wrote down. Its class, permission and grants would be invented.
		declarePlugin(t, dir, binary, strings.Replace(reviewedCapabilities(),
			"  - id: ref.badcallback\n    title: 非法回调\n    class: readonly\n    approval: never\n", "", 1))
		unit := pluginUnit(dir)
		decl, err := handler.LoadPluginUnitDeclaration(unit.Path, dir)
		if err != nil {
			t.Fatalf("declaration: %v", err)
		}
		_, err = newPackPluginProvisioner(zap.NewNop()).ProvisionPackPlugin(unit, decl)
		if err == nil || !strings.Contains(err.Error(), "未经审阅") {
			t.Fatalf("an unreviewed capability was accepted: %v", err)
		}
		if _, lookupErr := capability.Global().Lookup("ref.echo"); lookupErr == nil {
			t.Fatal("a refused provision registered capabilities anyway")
		}
		if _, owned := pluginhost.Global().PackDomainOwner("ref"); owned {
			t.Fatal("a refused provision left its trust domain declared")
		}
	})
}

func TestPackPluginNeedsAConfiguredHost(t *testing.T) {
	previous := pluginhost.Global()
	pluginhost.InstallService(nil)
	t.Cleanup(func() { pluginhost.InstallService(previous) })

	dir := t.TempDir()
	declarePlugin(t, dir, buildReferencePlugin(t), reviewedCapabilities())
	unit := pluginUnit(dir)
	decl, err := handler.LoadPluginUnitDeclaration(unit.Path, dir)
	if err != nil {
		t.Fatalf("declaration: %v", err)
	}
	if _, err := newPackPluginProvisioner(zap.NewNop()).ProvisionPackPlugin(unit, decl); err == nil ||
		!strings.Contains(err.Error(), "plugin_host") {
		t.Fatalf("provisioning without a configured host must be refused, got: %v", err)
	}
}

func TestCompareCapabilitySetsChecksBothDirections(t *testing.T) {
	reviewed := []string{"ref.echo", "ref.scan"}

	missing, extra := compareCapabilitySets(reviewed, []string{"ref.echo", "ref.scan"})
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("an exact match reported %v / %v", missing, extra)
	}
	missing, extra = compareCapabilitySets(reviewed, []string{"ref.echo"})
	if strings.Join(missing, ",") != "ref.scan" || len(extra) != 0 {
		t.Fatalf("missing entry: %v / %v", missing, extra)
	}
	missing, extra = compareCapabilitySets(reviewed, []string{"ref.echo", "ref.scan", "ref.swarm"})
	if len(missing) != 0 || strings.Join(extra, ",") != "ref.swarm" {
		t.Fatalf("unreviewed entry: %v / %v", missing, extra)
	}
	// A plugin that answers with nothing is both failures at once.
	missing, extra = compareCapabilitySets(reviewed, nil)
	if len(missing) != 2 || len(extra) != 0 {
		t.Fatalf("empty answer: %v / %v", missing, extra)
	}
}

func TestPackPluginProvisionerSatisfiesTheHandlerContract(t *testing.T) {
	// Compile-time proof that the pack surface and the assembly agree on the interface; if this
	// stops holding, the plug-in kind silently degrades to "declared, nobody can run it".
	var _ handler.PluginProvisioner = newPackPluginProvisioner(nil)
}
