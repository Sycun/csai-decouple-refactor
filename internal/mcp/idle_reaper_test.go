package mcp

import (
	"context"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"

	"go.uber.org/zap"
)

// liveTestClient builds a client that reports itself connected, the shape the reconnect tests use.
func liveTestClient(cfg config.ExternalMCPServerConfig) *lazySDKClient {
	client := newLazySDKClient(cfg, zap.NewNop())
	client.inner = &sdkClient{status: "connected"}
	client.status = "connected"
	return client
}

// startTestServer registers a running server with an injected last-used stamp.
func startTestServer(m *ExternalMCPManager, name string, cfg config.ExternalMCPServerConfig, lastUsed time.Time) {
	m.mu.Lock()
	m.configs[name] = cfg
	m.clients[name] = liveTestClient(cfg)
	m.lastUsed[name] = lastUsed
	m.mu.Unlock()
}

func httpTestServer() config.ExternalMCPServerConfig {
	return config.ExternalMCPServerConfig{
		Type:              "http",
		URL:               "http://127.0.0.1:1/mcp",
		ExternalMCPEnable: true,
	}
}

// The reap must stop an idle server without touching the operator's enable flag: the reap is not
// the operator's stop, and a rewrote flag would make the next settings read answer differently
// than the operator did when they started the server.
func TestReapIdleStopsServerPastTimeoutAndKeepsEnableFlag(t *testing.T) {
	m := NewExternalMCPManager(zap.NewNop())
	name := "idle-lab"
	startTestServer(m, name, httpTestServer(), time.Now().Add(-2*time.Hour))

	m.reapIdle()

	if m.isRunning(name) {
		t.Fatal("idle server was not reaped")
	}
	if got := m.GetToolCounts()[name]; got != 0 {
		t.Fatalf("reaped server still advertises %d tools", got)
	}
	if !m.GetConfigs()[name].ExternalMCPEnable {
		t.Fatal("the reap rewrote the operator's enable flag")
	}
}

func TestReapIdleKeepsRecentlyUsedServer(t *testing.T) {
	m := NewExternalMCPManager(zap.NewNop())
	name := "warm-lab"
	startTestServer(m, name, httpTestServer(), time.Now())

	m.reapIdle()

	if !m.isRunning(name) {
		t.Fatal("a server used a moment ago was reaped")
	}
}

// ConfigureIdleTimeout: 0 keeps the default, a positive value replaces it, a negative value turns
// the reap off. The default is what an installation that never touches the key gets.
func TestConfigureIdleTimeoutSemantics(t *testing.T) {
	m := NewExternalMCPManager(zap.NewNop())
	if got := m.IdleTimeout(); got != externalDefaultIdleTimeout {
		t.Fatalf("fresh manager idle timeout = %v, want the default %v", got, externalDefaultIdleTimeout)
	}
	if got := m.ConfigureIdleTimeout(0); got != externalDefaultIdleTimeout {
		t.Fatalf("0 should keep the default, got %v", got)
	}
	if got := m.ConfigureIdleTimeout(90); got != 90*time.Second {
		t.Fatalf("explicit timeout = %v, want 90s", got)
	}
	if got := m.ConfigureIdleTimeout(-5); got != 0 {
		t.Fatalf("negative timeout should disable the reap, got %v", got)
	}
}

func TestReapIdleSkippedWhenTimeoutDisabled(t *testing.T) {
	m := NewExternalMCPManager(zap.NewNop())
	m.ConfigureIdleTimeout(-1)
	name := "keep-alive"
	startTestServer(m, name, httpTestServer(), time.Now().Add(-100*time.Hour))

	m.reapIdle()

	if !m.isRunning(name) {
		t.Fatal("the reap stopped a server while it was disabled")
	}
}

// A call in flight is not idleness: the reap must leave a server alone while any of its executions
// is still queued or running, or the stop would cut work the operator asked for.
func TestReapIdleSkipsServerWithActiveCall(t *testing.T) {
	m := NewExternalMCPManager(zap.NewNop())
	name := "busy-lab"
	startTestServer(m, name, httpTestServer(), time.Now().Add(-2*time.Hour))

	release := make(chan struct{})
	handle, err := m.executionService.Submit(context.Background(), ExecutionRequest{
		ToolName: name + "::slow-op",
		Run: func(ctx context.Context) (*ToolResult, error) {
			<-release
			return &ToolResult{}, nil
		},
	})
	if err != nil {
		t.Fatalf("submit slow call: %v", err)
	}

	m.reapIdle()

	if !m.isRunning(name) {
		t.Fatal("a server with a call in flight was reaped")
	}

	close(release)
	if _, err := m.executionService.Wait(context.Background(), handle.ID, 5*time.Second); err != nil {
		t.Fatalf("wait for the slow call to finish: %v", err)
	}
}

// The other half of the reap: a server it stopped carries no run intent, so the disconnect-driven
// reconnect loop must not resurrect it - even though the config flag still says enabled.
func TestReapedServerIsNotReconnected(t *testing.T) {
	m := NewExternalMCPManager(zap.NewNop())
	name := "reaped-lab"
	startTestServer(m, name, httpTestServer(), time.Now().Add(-2*time.Hour))

	m.reapIdle()
	if m.isRunning(name) {
		t.Fatal("precondition: the server should be reaped")
	}

	m.tryReconnect(name)

	m.reconnectMu.Lock()
	attempts := m.reconnectAttempts[name]
	m.reconnectMu.Unlock()
	if attempts != 0 {
		t.Fatalf("a reaped server was reconnected (attempts=%d); the recovery loop must not undo the reap", attempts)
	}
}

// The mirror image of the same race: a reconnect that already passed its run-intent gate holds no
// client for a moment, and a reap landing in that window would have its decision undone by the
// reconnect it just raced. The reap defers to the reconnect for this round.
func TestReapIdleSkipsServerBeingReconnected(t *testing.T) {
	m := NewExternalMCPManager(zap.NewNop())
	name := "reconnecting-lab"
	startTestServer(m, name, httpTestServer(), time.Now().Add(-2*time.Hour))

	m.reconnectMu.Lock()
	m.reconnecting[name] = true
	m.reconnectMu.Unlock()

	m.reapIdle()

	if !m.isRunning(name) {
		t.Fatal("the reap stopped a server whose reconnect was in flight")
	}
}

func TestMarkUsedStampsOnlyRunningServers(t *testing.T) {
	m := NewExternalMCPManager(zap.NewNop())
	name := "stamped-lab"
	old := time.Now().Add(-time.Hour)
	startTestServer(m, name, httpTestServer(), old)

	m.markUsed(name)
	if m.lastUsed[name].Before(old) || m.lastUsed[name].Equal(old) {
		t.Fatal("markUsed did not refresh the stamp of a running server")
	}

	m.markUsed("ghost")
	if _, ok := m.lastUsed["ghost"]; ok {
		t.Fatal("markUsed created a stamp for a server that is not running")
	}
}

func TestHasActiveExecutionWithToolPrefix(t *testing.T) {
	service := NewExecutionService(nil, zap.NewNop())
	release := make(chan struct{})
	handle, err := service.Submit(context.Background(), ExecutionRequest{
		ToolName: "alpha::op",
		Run: func(ctx context.Context) (*ToolResult, error) {
			<-release
			return &ToolResult{}, nil
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	if !service.HasActiveExecutionWithToolPrefix("alpha::") {
		t.Fatal("active execution under alpha:: was not reported")
	}
	if service.HasActiveExecutionWithToolPrefix("beta::") {
		t.Fatal("prefix matched another server's execution")
	}
	if service.HasActiveExecutionWithToolPrefix("") {
		t.Fatal("an empty prefix must not match every execution")
	}

	close(release)
	if _, err := service.Wait(context.Background(), handle.ID, 5*time.Second); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if service.HasActiveExecutionWithToolPrefix("alpha::") {
		t.Fatal("finished execution still reported as active")
	}
}

// The conversation's tool list is the running set. A declared-but-not-started server contributes
// nothing - even if a stale list cache still names it - because nothing off the wire reached the
// model while the server was down.
func TestGetAllToolsCoversRunningServersOnly(t *testing.T) {
	m := NewExternalMCPManager(zap.NewNop())
	cfg := httpTestServer()

	m.mu.Lock()
	m.configs["running-lab"] = cfg
	m.clients["running-lab"] = liveTestClient(cfg)
	m.configs["stopped-lab"] = cfg
	m.mu.Unlock()

	// Fresh caches for both, so serving the running one never touches the network.
	stale := toolListCacheEntry{tools: []Tool{{Name: "op"}}, updatedAt: time.Now()}
	m.toolCacheMu.Lock()
	m.toolCache["running-lab"] = stale
	m.toolCache["stopped-lab"] = stale
	m.toolCacheMu.Unlock()

	tools, err := m.GetAllTools(context.Background())
	if err != nil {
		t.Fatalf("GetAllTools: %v", err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if len(names) != 1 || names[0] != "running-lab::op" {
		t.Fatalf("tool list = %v, want only the running server's tool", names)
	}
}
