package pluginhost

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// These tests are about ownership and process lifecycle, and the last one is the point of the
// whole feature: a capability pack whose binary is discovered and invoked through the same live
// service the operator's config.yaml domains use, with neither side able to overwrite the other.

func packConfig(binary string) Config {
	return Config{Binary: binary, CallTimeout: 20 * time.Second, Grants: []string{"net.connect(10.0.0.0/8)"}}
}

func TestPackDomainDeclarationDoesNotStartAnything(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	service := NewService(nil, zap.NewNop())
	t.Cleanup(service.Close)

	if err := service.DeclarePackDomain("ref", "pack-a", packConfig(binary)); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if names := service.DomainNames(); len(names) != 1 || names[0] != "ref" {
		t.Fatalf("DomainNames = %v, want the declared domain to be live", names)
	}
	// Installing a pack must not spawn a process: instances are created by the first call.
	if stats := service.Stats(); len(stats) != 0 {
		t.Fatalf("declaring a pack domain started %d instance(s): %+v", len(stats), stats)
	}
	if owner, owned := service.PackDomainOwner("ref"); !owned || owner != "pack-a" {
		t.Fatalf("PackDomainOwner = %q/%v, want pack-a/true", owner, owned)
	}
}

func TestPackDomainCannotShadowTheOperatorsFile(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	service := NewService(map[string]Config{"acme": packConfig(binary)}, zap.NewNop())
	t.Cleanup(service.Close)

	err := service.DeclarePackDomain("acme", "pack-a", packConfig(binary))
	if err == nil || !strings.Contains(err.Error(), "配置文件") {
		t.Fatalf("a pack took over an operator domain, or refused for the wrong reason: %v", err)
	}
	// And the other direction: unplugging a pack cannot remove a domain the file owns.
	if err := service.RemovePackDomain("acme"); err == nil || !strings.Contains(err.Error(), "配置文件") {
		t.Fatalf("RemovePackDomain reached a file-owned domain: %v", err)
	}
	if _, owned := service.PackDomainOwner("acme"); owned {
		t.Fatal("a refused declaration still marked the operator's domain pack-owned")
	}
}

func TestPackDomainHasExactlyOneOwner(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	service := NewService(nil, zap.NewNop())
	t.Cleanup(service.Close)

	if err := service.DeclarePackDomain("ref", "pack-a", packConfig(binary)); err != nil {
		t.Fatal(err)
	}
	err := service.DeclarePackDomain("ref", "pack-b", packConfig(binary))
	if err == nil || !strings.Contains(err.Error(), "pack-a") {
		t.Fatalf("a second pack claimed the same domain, or refused without naming the owner: %v", err)
	}
	// The same pack replacing its own declaration is an upgrade, not a collision.
	if err := service.DeclarePackDomain("ref", "pack-a", packConfig(binary)); err != nil {
		t.Fatalf("re-declaring one's own domain must be allowed: %v", err)
	}
	if err := service.RemovePackDomain("other"); err == nil || !strings.Contains(err.Error(), "not a pack-declared") {
		t.Fatalf("removing an unknown domain: %v", err)
	}
}

func TestPackDomainPublisherMustMatchItsName(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	service := NewService(nil, zap.NewNop())
	t.Cleanup(service.Close)

	// Invoke routes by the first segment of a capability id, so a domain whose publisher differs
	// from its own name would host capabilities that resolve somewhere else.
	cfg := packConfig(binary)
	cfg.PluginID = "someone-else"
	if err := service.DeclarePackDomain("ref", "pack-a", cfg); err == nil ||
		!strings.Contains(err.Error(), "route") {
		t.Fatalf("mismatched publisher accepted: %v", err)
	}
	missing := packConfig("")
	if err := service.DeclarePackDomain("ref", "pack-a", missing); err == nil ||
		!strings.Contains(err.Error(), "binary") {
		t.Fatalf("a domain with no binary accepted: %v", err)
	}
	if err := service.DeclarePackDomain("ref", "", packConfig(binary)); err == nil {
		t.Fatal("a domain declared with no owning bundle accepted")
	}
}

func TestRedeclaringAPackDomainClosesTheRunningInstance(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	service := NewService(nil, zap.NewNop())
	t.Cleanup(service.Close)

	if err := service.DeclarePackDomain("ref", "pack-a", packConfig(binary)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if _, err := service.ListCapabilities(ctx, "ref", 20*time.Second); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if stats := service.Stats(); len(stats) != 1 {
		t.Fatalf("expected one live instance, got %+v", stats)
	}

	// The upgrade path: narrower grants. The process that is running was started under the wide
	// set, so keeping it alive would make the new declaration a lie.
	narrow := packConfig(binary)
	narrow.Grants = nil
	if err := service.DeclarePackDomain("ref", "pack-a", narrow); err != nil {
		t.Fatal(err)
	}
	if stats := service.Stats(); len(stats) != 0 {
		t.Fatalf("re-declaring a domain left the old instance running: %+v", stats)
	}

	if err := service.RemovePackDomain("ref"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if names := service.DomainNames(); len(names) != 0 {
		t.Fatalf("the domain survived its removal: %v", names)
	}
	if _, owned := service.PackDomainOwner("ref"); owned {
		t.Fatal("ownership survived removal")
	}
}

func TestPackDeclaredPluginDiscoversAndInvokes(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	service := NewService(nil, zap.NewNop())
	t.Cleanup(service.Close)
	if err := service.DeclarePackDomain("ref", "pack-a", packConfig(binary)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ids, err := service.ListCapabilities(ctx, "ref", 20*time.Second)
	if err != nil {
		t.Fatalf("discovery through a pack-declared domain: %v", err)
	}
	if len(ids) != 6 || ids[2] != "ref.echo" {
		t.Fatalf("discovered %v through a pack domain, want the six ref.* ids", ids)
	}
	params, _ := json.Marshal(map[string]string{"text": "from a pack"})
	result, err := service.Invoke(ctx, "ref", "ref.echo", params, 20*time.Second)
	if err != nil {
		t.Fatalf("invoke through a pack-declared domain: %v", err)
	}
	if !strings.Contains(result.Content, "from a pack") {
		t.Fatalf("the plugin did not answer the pack's call: %+v", result)
	}
	// One domain, one process - the same instance served discovery and the call.
	if stats := service.Stats(); len(stats) != 1 || stats[0].PluginID != "ref" {
		t.Fatalf("a pack domain produced more than one instance: %+v", stats)
	}
}
