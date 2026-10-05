package pluginhost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// buildRefPlugin compiles the reference plugin once per test that needs a real
// process, so the ABI, crash and egress guarantees are exercised end to end
// rather than against a mock.
func buildRefPlugin(t *testing.T, extraEnv []string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "refplugin")
	cmd := exec.Command("go", "build", "-o", binary, "./testdata/refplugin")
	cmd.Env = append(os.Environ(), extraEnv...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build reference plugin: %v\n%s", err, output)
	}
	return binary
}

func newInstance(t *testing.T, binary string, cfg Config) *Instance {
	t.Helper()
	if cfg.Binary == "" {
		cfg.Binary = binary
	}
	if cfg.PluginID == "" {
		cfg.PluginID = "acme"
	}
	cfg.AllowedEnvKeys = append(cfg.AllowedEnvKeys, "REF_BAD_PROTOCOL", "REF_BAD_CAPS")
	instance, err := NewInstance(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new instance: %v", err)
	}
	t.Cleanup(func() { instance.Close() })
	return instance
}

func invoke(t *testing.T, instance *Instance, capabilityID string, args map[string]string) (*InvokeResult, error) {
	t.Helper()
	params, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return instance.Invoke(ctx, capabilityID, params, 20*time.Second)
}

func TestHandshakeAndEchoRoundTrip(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	instance := newInstance(t, binary, Config{})

	result, err := invoke(t, instance, "ref.echo", map[string]string{"text": "hello plugin"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if result.Content != "hello plugin" {
		t.Fatalf("content = %q", result.Content)
	}
	if !instance.Running() {
		t.Fatal("instance should be live after a successful call")
	}
}

func TestProtocolMismatchIsRefused(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	if err := os.Setenv("REF_BAD_PROTOCOL", "1"); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("REF_BAD_PROTOCOL")

	instance := newInstance(t, binary, Config{})
	_, err := invoke(t, instance, "ref.echo", map[string]string{"text": "x"})
	if err == nil {
		t.Fatal("a plugin speaking an older ABI was accepted")
	}
	if !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("expected a protocol refusal, got %v", err)
	}
	if instance.Running() {
		t.Fatal("a mismatched plugin process was left attached")
	}
}

// TestChildEnvironmentHasNoCredentials is the report's P4 gate "凭据不出现在子进程环境".
func TestChildEnvironmentHasNoCredentials(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	secrets := map[string]string{
		"OPENAI_API_KEY":          "sk-should-not-leak",
		"AWS_SECRET_ACCESS_KEY":   "aws-should-not-leak",
		"CYBERSTRIKE_DB_PASSWORD": "db-should-not-leak",
		"GITHUB_TOKEN":            "gh-should-not-leak",
	}
	for key, value := range secrets {
		if err := os.Setenv(key, value); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for key := range secrets {
			os.Unsetenv(key)
		}
	}()

	// Even a permissive allowlist cannot pull a credential through: the name is
	// screened, and inheritance is allowlist-based to begin with.
	instance := newInstance(t, binary, Config{AllowedEnvKeys: []string{"PATH", "OPENAI_API_KEY", "GITHUB_TOKEN"}})

	result, err := invoke(t, instance, "ref.env", nil)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	env := map[string]string{}
	if err := json.Unmarshal([]byte(result.Content), &env); err != nil {
		t.Fatalf("decode child env: %v (%s)", err, result.Content)
	}
	for key, value := range secrets {
		if got, present := env[key]; present {
			t.Errorf("credential %s reached the plugin environment (value %q)", key, redact(got, value))
		}
	}
	if _, ok := env["HTTP_PROXY"]; !ok {
		t.Error("the child was not given the egress proxy, so it would need direct network access")
	}
	if _, ok := env["PATH"]; !ok {
		t.Error("PATH was not forwarded, which breaks plugins that shell out")
	}
}

func redact(got, secret string) string {
	if got == secret {
		return "<the real secret>"
	}
	return "<different value>"
}

// TestProxyEnforcesTheApprovedTupleSet drives the proxy directly with CONNECT,
// because the Go HTTP client deliberately never proxies loopback traffic - routing
// through it would test the client, not the policy.
func TestProxyEnforcesTheApprovedTupleSet(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "origin-served")
	}))
	defer origin.Close()
	originAddr := strings.TrimPrefix(origin.URL, "http://")

	// Nothing approved: refused before a socket is ever opened upstream.
	strict := NewAllowlist([]string{"net.connect(target)"}, nil, true)
	proxy, err := NewProxy(strict)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	if code := connectThroughProxy(t, proxy.Addr(), originAddr); code != 403 {
		t.Fatalf("unapproved CONNECT returned %d, want 403", code)
	}

	// The exact approved tuple is relayed.
	approved := NewAllowlist([]string{"net.connect(target)"}, []Tuple{{
		Host: "127.0.0.1", Ports: []int{portOf(t, originAddr)}, Method: "CONNECT",
	}}, true)
	proxy2, err := NewProxy(approved)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy2.Close()
	if code := connectThroughProxy(t, proxy2.Addr(), originAddr); code != 200 {
		t.Fatalf("approved CONNECT returned %d, want 200", code)
	}

	// A port outside the approved port set is refused even for an approved host.
	if code := connectThroughProxy(t, proxy2.Addr(), "127.0.0.1:1"); code != 403 {
		t.Fatalf("out-of-set port returned %d, want 403", code)
	}
}

// connectThroughProxy issues one CONNECT to the proxy and returns the status code.
func connectThroughProxy(t *testing.T, proxyAddr, target string) int {
	t.Helper()
	connection, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer connection.Close()
	_, err = fmt.Fprintf(connection, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT", Host: target})
	if err != nil {
		t.Fatalf("read proxy response: %v", err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

// TestPluginCannotInflateItsOwnGrantCeiling checks the mediated callback path with
// the same intersection rule the proxy applies.
func TestPluginGrantCeilingIsIntersected(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	instance := newInstance(t, binary, Config{
		Grants: []string{"net.connect(10.0.0.0/8)"}, StrictEgress: true,
	})
	// Boot the instance so the allowlist exists, then ask for a target the manifest
	// never declared.
	if _, err := invoke(t, instance, "ref.echo", map[string]string{"text": "warm"}); err != nil {
		t.Fatal(err)
	}
	allowed, reason := instance.grantAllowed("net.connect", "evil.example:443")
	if allowed {
		t.Fatal("a target outside the declared grant was allowed")
	}
	if reason == "" {
		t.Fatal("a denial must carry an auditable reason")
	}
	if allowed, _ := instance.grantAllowed("fs.write", "/etc/passwd"); allowed {
		t.Fatal("fs.write was allowed although only net.connect was declared")
	}
}

func portOf(t *testing.T, hostPort string) int {
	t.Helper()
	parts := strings.Split(hostPort, ":")
	if len(parts) != 2 {
		t.Fatalf("unexpected host:port %q", hostPort)
	}
	var port int
	if _, err := fmt.Sscanf(parts[1], "%d", &port); err != nil {
		t.Fatal(err)
	}
	return port
}

// TestPluginCrashLeavesTheHostAlive is the report's P4 gate "插件崩溃不影响主进程".
func TestPluginCrashLeavesTheHostAlive(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	instance := newInstance(t, binary, Config{Grants: []string{"process.exec(自杀)"}})

	if _, err := invoke(t, instance, "ref.echo", map[string]string{"text": "warm"}); err != nil {
		t.Fatal(err)
	}

	if _, err := invoke(t, instance, "ref.crash", nil); err == nil {
		t.Fatal("a self-terminating plugin call reported success")
	}
	if instance.Running() {
		t.Fatal("the dead process was still considered running")
	}

	// The next call must transparently boot a fresh process rather than wedge.
	result, err := invoke(t, instance, "ref.echo", map[string]string{"text": "after restart"})
	if err != nil {
		t.Fatalf("call after crash was not retried against a fresh process: %v", err)
	}
	if result.Content != "after restart" {
		t.Fatalf("content = %q", result.Content)
	}

	// And the test process itself is obviously still alive to assert any of this.
	if os.Getpid() <= 1 {
		t.Fatal("nonsense assertion")
	}
}

func TestGrantCeilingIsTheIntersectionOfManifestAndApproval(t *testing.T) {
	instance := &Instance{cfg: Config{
		PluginID: "acme", Grants: []string{"net.connect(10.0.0.0/8)"},
		Approved: []Tuple{{Host: "internal.example", Ports: []int{443}}},
	}, logger: zap.NewNop()}

	// Declared ceiling refuses a target the manifest never named.
	allowed, reason := instance.grantAllowed("net.connect", "other.example:443")
	if allowed {
		t.Fatal("a target outside the declared grant was allowed")
	}
	if reason == "" {
		t.Fatal("denial without a reason cannot be audited")
	}

	// An undeclared mediated capability is refused even if a tuple exists.
	if allowed, _ := instance.grantAllowed("fs.write", "/etc/passwd"); allowed {
		t.Fatal("fs.write was allowed although the manifest only declares net.connect")
	}

	// A declared name with a wildcard ceiling defers to the approval set.
	wildcard := &Instance{cfg: Config{
		PluginID: "acme", Grants: []string{"net.connect(target)"},
		Approved: []Tuple{{Host: "api.example", Ports: []int{443}}},
	}, logger: zap.NewNop()}
	wildcard.proxy = &Proxy{allowlist: NewAllowlist(wildcard.cfg.Grants, wildcard.cfg.Approved, true)}
	if allowed, reason := wildcard.grantAllowed("net.connect", "api.example:443"); !allowed {
		t.Fatalf("approved tuple was refused: %s", reason)
	}
	if allowed, _ := wildcard.grantAllowed("net.connect", "unapproved.example:443"); allowed {
		t.Fatal("the store widened beyond the operator-approved target")
	}
}

func TestAllowlistHonoursMethodAndTimeWindow(t *testing.T) {
	list := NewAllowlist([]string{"net.connect(target)"}, []Tuple{
		{Host: ".example", Ports: []int{443}, Method: "CONNECT", Expires: time.Now().Add(time.Hour)},
		{Host: "expired.internal", Ports: []int{443}, Expires: time.Now().Add(-time.Minute)},
	}, true)

	if allowed, _ := list.Allow("deep.sub.example:443", "CONNECT"); !allowed {
		t.Error("suffix host match was refused")
	}
	if allowed, _ := list.Allow("example:8443", "CONNECT"); allowed {
		t.Error("a port outside the approved port set was allowed")
	}
	if allowed, _ := list.Allow("expired.internal:443", "CONNECT"); allowed {
		t.Error("an expired approval still authorized egress")
	}
	if allowed, _ := list.Allow("evil.example.com:443", "CONNECT"); allowed {
		t.Error("suffix matching leaked to a look-alike domain")
	}
}

func TestServiceRoutesByPublisherAndFailsClosed(t *testing.T) {
	binary := buildRefPlugin(t, nil)
	service := NewService(map[string]Config{
		"acme": {Binary: binary, Grants: []string{"net.connect(target)"}},
	}, zap.NewNop())
	defer service.Close()

	if !service.Enabled() {
		t.Fatal("a configured service reported itself disabled")
	}
	if _, err := service.Invoke(context.Background(), "other-vendor", "core.x", json.RawMessage(`{}`), time.Second); err == nil {
		t.Fatal("an unconfigured trust domain was served")
	}

	result, err := service.Invoke(context.Background(), "acme", "ref.echo", json.RawMessage(`{"text":"routed"}`), 20*time.Second)
	if err != nil {
		t.Fatalf("routed invoke: %v", err)
	}
	if result.Content != "routed" {
		t.Fatalf("content = %q", result.Content)
	}

	// Trust domain derivation is the publisher half of the identity, not the tool name.
	if got := DomainForTrust("acme.sub.capability"); got != "acme" {
		t.Fatalf("DomainForTrust = %q", got)
	}
	if DomainForTrust("core") != "" {
		t.Fatal("a non-namespaced identity must not yield a domain")
	}
	if !IsPluginRuntime(string(RuntimePluginHostAlias)) {
		t.Fatal("plugin runtime detection failed")
	}
}

// RuntimePluginHostAlias mirrors the capability runtime string so this test does
// not have to import the capability package.
const RuntimePluginHostAlias = "plugin-host:python"
