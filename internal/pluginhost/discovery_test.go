package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// Capability discovery is exercised against the real reference plugin, not a mock: the thing being
// proven is that the host speaks the ABI on the wire (framing, correlation, one process per
// publisher), and a mock would prove only that the caller matches its own stub.

// discover runs discovery against a real child process. The bad-answer switch travels the same
// way the protocol-mismatch test does it: through the host environment, filtered by the
// instance's AllowedEnvKeys - which is itself part of what these tests prove (a plugin gets only
// the environment the host lets it have).
func discover(t *testing.T, badCaps string, pluginID string) ([]string, error) {
	t.Helper()
	if badCaps != "" {
		t.Setenv("REF_BAD_CAPS", badCaps)
	}
	binary := buildRefPlugin(t, nil)
	instance := newInstance(t, binary, Config{PluginID: pluginID, TrustDomain: pluginID})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return instance.Capabilities(ctx, 20*time.Second)
}

func TestCapabilitiesDiscoversFromARealPlugin(t *testing.T) {
	ids, err := discover(t, "", "ref")
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	want := []string{"ref.badcallback", "ref.crash", "ref.echo", "ref.egress", "ref.env", "ref.grant"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("discovered %v, want %v (sorted, deduped)", ids, want)
	}
	// The invariant the routing depends on: every discovered id resolves back to this publisher's
	// own trust domain, because Service.Invoke derives the domain from the first segment.
	for _, id := range ids {
		if got := DomainForTrust(id); got != "ref" {
			t.Errorf("%s routes to domain %q, not this instance's ref", id, got)
		}
	}
}

func TestCapabilitiesRefusesAForeignPublisherNamespace(t *testing.T) {
	// A plugin that advertises vendor2.tool is advertising a capability the host would route into a
	// *different* publisher's process - a different grant set, possibly a wider one. Refused.
	_, err := discover(t, "foreign", "ref")
	if err == nil {
		t.Fatal("discovery accepted a capability outside the plugin's own publisher namespace")
	}
	if !strings.Contains(err.Error(), "vendor2.tool") || !strings.Contains(err.Error(), "publisher namespace") {
		t.Fatalf("error does not name the offending id and the rule: %v", err)
	}
}

func TestCapabilitiesRefusesAnObjectShapedAnswer(t *testing.T) {
	// The ABI says an array of strings. Accepting [{"id": ...}] would yield an empty list, and an
	// empty list means "provides nothing" - a shape bug would read as a plugin with no capabilities
	// rather than as a broken answer.
	_, err := discover(t, "objects", "ref")
	if err == nil {
		t.Fatal("discovery accepted an answer that is not an array of capability ids")
	}
	if !strings.Contains(err.Error(), "array of capability ids") {
		t.Fatalf("error does not state the expected ABI shape: %v", err)
	}
}

func TestCapabilitiesRefusesABlankID(t *testing.T) {
	if _, err := discover(t, "blank", "ref"); err == nil {
		t.Fatal("discovery accepted a blank capability id")
	} else if !strings.Contains(err.Error(), "blank") {
		t.Fatalf("error does not name the blank entry: %v", err)
	}
}

func TestDecodeCapabilityListDedupesSortsAndValidates(t *testing.T) {
	raw, err := json.Marshal([]string{"ref.b", "ref.a", "ref.b", "ref.a"})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := decodeCapabilityList(raw, "ref")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Join(ids, ",") != "ref.a,ref.b" {
		t.Fatalf("decode gave %v, want sorted and deduped", ids)
	}

	for _, tc := range []struct{ name, payload, want string }{
		{"no result", "", "returned no result"},
		{"undotted id", `["echo"]`, "not a dotted capability id"},
		{"path shaped id", `["ref/echo"]`, "not a dotted capability id"},
		{"uppercase id", `["REF.Echo"]`, "not a dotted capability id"},
		{"wrong type", `{"capabilities":["ref.a"]}`, "array of capability ids"},
	} {
		_, err := decodeCapabilityList(json.RawMessage(tc.payload), "ref")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}

	// Empty is a legitimate answer - the pack's cross-check is what turns "the binary provides
	// nothing the manifest claims" into a refusal, and discovery must not decide that alone.
	empty, err := decodeCapabilityList(json.RawMessage(`[]`), "ref")
	if err != nil || len(empty) != 0 {
		t.Fatalf("an empty list must decode to no error and no ids, got %v / %v", empty, err)
	}
}

func TestListCapabilitiesSharesOneInstanceWithInvoke(t *testing.T) {
	// Discovery and invocation must land on the same instance: two instances would mean two grant
	// sets and two processes for one publisher, which is exactly what the per-domain table prevents.
	binary := buildRefPlugin(t, nil)
	service := NewService(map[string]Config{
		"ref": {PluginID: "ref", Binary: binary, CallTimeout: 20 * time.Second},
	}, zap.NewNop())
	t.Cleanup(service.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ids, err := service.ListCapabilities(ctx, "ref", 20*time.Second)
	if err != nil {
		t.Fatalf("ListCapabilities: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("ListCapabilities returned nothing for a plugin that advertises six capabilities")
	}
	params, _ := json.Marshal(map[string]string{"text": "hi"})
	result, err := service.Invoke(ctx, "ref", "ref.echo", params, 20*time.Second)
	if err != nil {
		t.Fatalf("Invoke after discovery: %v", err)
	}
	if !strings.Contains(result.Content, "hi") {
		t.Fatalf("echo did not round-trip on the discovered instance: %+v", result)
	}
	if stats := service.Stats(); len(stats) != 1 || stats[0].PluginID != "ref" {
		t.Fatalf("discovery and invoke created more than one instance: %+v", stats)
	}
}

func TestUnknownDomainIsRefusedForBothPaths(t *testing.T) {
	// Invoke used to inline its own get-or-create; factoring it out must not have softened the
	// refusal for a domain nobody configured (executor relies on it being ErrNotConfigured so a
	// capability is never run in-process by accident).
	service := NewService(map[string]Config{}, zap.NewNop())
	ctx := context.Background()
	if _, err := service.Invoke(ctx, "ghost", "ghost.tool", json.RawMessage(`{}`), time.Second); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Invoke unknown domain: %v", err)
	}
	if _, err := service.ListCapabilities(ctx, "ghost", time.Second); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("ListCapabilities unknown domain: %v", err)
	}
}
