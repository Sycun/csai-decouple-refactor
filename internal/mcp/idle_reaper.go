package mcp

import (
	"time"

	"go.uber.org/zap"
)

// An external MCP server is started by an explicit action - the MCP page's start, or a capability
// unit's switch - and stays up until it is stopped the same way or the idle reap below ends it.
// Nothing starts a server as a side effect of boot or of a settings save: declared is not running,
// and only a running server contributes tools to a conversation.
//
// The reap is the other half of that rule. A server nobody calls for a while is a process and a
// connection nobody needs, and stopping it is reversible - the next explicit start brings it back -
// so it needs no consent beyond the one the start already gave. What it must not do is race a call
// in flight, or leave a reconnect loop believing the server is still wanted; the run-intent gate
// for that half lives in connection_recovery.go.

// ConfigureIdleTimeout sets how long an explicitly started server may sit without a tool call
// before the reap stops it. seconds == 0 keeps the default; a negative value turns the reap off.
// Returns the timeout now in force.
func (m *ExternalMCPManager) ConfigureIdleTimeout(seconds int) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case seconds < 0:
		m.idleTimeout = 0
	case seconds > 0:
		m.idleTimeout = time.Duration(seconds) * time.Second
	default:
		m.idleTimeout = externalDefaultIdleTimeout
	}
	return m.idleTimeout
}

// IdleTimeout reports the idle-reap timeout now in force (0 = off), for boot logging.
func (m *ExternalMCPManager) IdleTimeout() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.idleTimeout
}

// markUsed stamps a server as active for the idle reap. Stamped before a call goes out rather than
// after: a long call must not look idle while it is the very work being waited on.
func (m *ExternalMCPManager) markUsed(name string) {
	if name == "" {
		return
	}
	m.mu.Lock()
	if _, ok := m.clients[name]; ok {
		m.lastUsed[name] = time.Now()
	}
	m.mu.Unlock()
}

// isRunning reports whether a server carries a run intent: it was started explicitly (or by its
// unit switch) and has not been stopped or reaped since. It is the reconnect loop's gate, and -
// through GetAllTools' live client set - the conversation's visibility gate.
func (m *ExternalMCPManager) isRunning(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.clients[name]
	return ok
}

func (m *ExternalMCPManager) startIdleReaper() {
	m.reaperWg.Add(1)
	go func() {
		defer m.reaperWg.Done()
		ticker := time.NewTicker(externalIdleReapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.reapIdle()
			case <-m.reaperStop:
				return
			}
		}
	}()
}

// stopIdleReaper ends the reap loop. Safe to call more than once and on a manager that was built by
// hand and therefore never started one.
func (m *ExternalMCPManager) stopIdleReaper() {
	if m.reaperStop == nil {
		return
	}
	select {
	case <-m.reaperStop:
	default:
		close(m.reaperStop)
	}
	m.reaperWg.Wait()
}

// isReconnecting reports whether a reconnect attempt for this server is in flight. The idle reap
// skips such a server for the round: a reconnect that already passed its run-intent gate holds no
// client for a moment, so a reap landing in that window would delete nothing but would have its
// decision undone - the reconnect would then start a process the reap just decided to end.
func (m *ExternalMCPManager) isReconnecting(name string) bool {
	m.reconnectMu.Lock()
	defer m.reconnectMu.Unlock()
	return m.reconnecting[name]
}

// reapIdle stops servers that were started explicitly but unused past the idle timeout. A server
// with a call in flight is skipped: the reap is about idle processes, not about interrupting work.
// The enabled flag is deliberately left alone - the reap is not a stop the operator asked for, and
// rewriting the config here would make the next settings read answer differently than the operator
// did when they started the server.
func (m *ExternalMCPManager) reapIdle() {
	m.mu.RLock()
	timeout := m.idleTimeout
	if timeout <= 0 {
		m.mu.RUnlock()
		return
	}
	now := time.Now()
	type staleServer struct {
		name string
		idle time.Duration
	}
	var stale []staleServer
	for name := range m.clients {
		last, ok := m.lastUsed[name]
		if !ok {
			continue // started before the reap bookkeeping existed; the next call stamps it
		}
		if idle := now.Sub(last); idle > timeout {
			stale = append(stale, staleServer{name: name, idle: idle})
		}
	}
	m.mu.RUnlock()

	for _, s := range stale {
		if m.executionService != nil && m.executionService.HasActiveExecutionWithToolPrefix(s.name+"::") {
			continue
		}
		if m.isReconnecting(s.name) {
			continue
		}
		if m.logger != nil {
			m.logger.Info("外部MCP空闲超时，自动回收",
				zap.String("name", s.name),
				zap.Duration("idle", s.idle),
				zap.Duration("timeout", timeout),
			)
		}
		m.stopIdleServer(s.name)
	}
}

// stopIdleServer is the reap's stop. Same teardown as StopClient without its operator-side rules:
// it must not rewrite the enabled flag (the reap is not the operator's stop) and it must reach a
// pack-declared server too - idleness knows no ownership.
func (m *ExternalMCPManager) stopIdleServer(name string) {
	m.mu.Lock()
	client, exists := m.clients[name]
	if !exists {
		m.mu.Unlock()
		return
	}
	delete(m.clients, name)
	delete(m.lastUsed, name)
	delete(m.errors, name)
	m.mu.Unlock()

	if client != nil {
		_ = client.Close()
	}

	m.toolCountsMu.Lock()
	m.toolCounts[name] = 0
	m.toolCountsMu.Unlock()

	m.toolCacheMu.Lock()
	delete(m.toolCache, name)
	m.toolCacheMu.Unlock()
	m.notifyInventory(name, nil)

	m.clearReconnectState(name)
}
