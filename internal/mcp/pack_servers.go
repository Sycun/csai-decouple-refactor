package mcp

import (
	"errors"
	"fmt"

	"cyberstrike-ai/internal/config"
)

// A capability pack can declare an external MCP server, and that declaration lives in the pack's
// files rather than in config.yaml. Keeping the two sets of servers separate inside one live
// manager is what makes both of these hold:
//
//   - 应用配置 reloads the file's servers wholesale (LoadConfigs) and must not erase a server a
//     pack declared. The pack set is re-applied after the reload.
//   - A pack must not take over a server the operator declared. A name the file owns is refused,
//     and where both sides claim a name the file wins: config.yaml is the slower-moving authority,
//     and the plug-in console reports the shadowed unit as not served.

// ErrPackOwnedServer is returned when an operator-side write targets a server a pack declared.
// The switch for such a server is the capability unit's, so the write has to go through the
// plug-in surface instead of silently replacing (or emptying) the pack's declaration.
var ErrPackOwnedServer = errors.New("该外部 MCP 服务器由能力包声明")

type packServerEntry struct {
	owner string
	cfg   config.ExternalMCPServerConfig
}

// DeclarePackServer installs one pack-declared server in the live configuration and remembers that
// a pack owns it. The caller decides the enable state; nothing here starts a process that the
// declaration itself did not ask for.
func (m *ExternalMCPManager) DeclarePackServer(name, bundleID string, serverCfg config.ExternalMCPServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, owned := m.packServers[name]; owned {
		if existing.owner != bundleID {
			return fmt.Errorf("外部 MCP 服务器 %q 已由能力包 %q 声明，请先卸载该包", name, existing.owner)
		}
	} else if _, fileOwned := m.configs[name]; fileOwned {
		return fmt.Errorf("外部 MCP 服务器 %q 已由配置文件声明，能力包不能覆盖运维者声明的服务器", name)
	}
	if m.packServers == nil {
		m.packServers = make(map[string]packServerEntry)
	}
	m.packServers[name] = packServerEntry{owner: bundleID, cfg: serverCfg}
	m.applyConfigLocked(name, serverCfg, m.isEnabled(serverCfg))
	return nil
}

// RemovePackServer forgets the declaration and the server. The pack's files are left alone, and a
// server the pack never owned is not this method's to remove.
func (m *ExternalMCPManager) RemovePackServer(name string) error {
	m.mu.Lock()
	delete(m.packServers, name)
	m.mu.Unlock()
	return m.RemoveConfig(name)
}

// PackOwner reports which capability pack, if any, declared this server.
func (m *ExternalMCPManager) PackOwner(name string) (string, bool) {
	if m == nil {
		return "", false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.packServers[name]
	if !ok {
		return "", false
	}
	return entry.owner, true
}

// checkNotPackOwned is the fail-closed half of the rule: an operator-side write aimed at a name a
// pack declared is refused here, not merely discouraged by the handler that happens to call it.
func (m *ExternalMCPManager) checkNotPackOwned(name, action string) error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	entry, owned := m.packServers[name]
	m.mu.RUnlock()
	if !owned {
		return nil
	}
	return fmt.Errorf("%w：无法%s %q，它由能力包 %q 声明；请在能力控制台启停该单元或卸载该包",
		ErrPackOwnedServer, action, name, entry.owner)
}

// applyConfigLocked replaces one live configuration. start is the caller's run-intent decision:
//
//   - the unit switch passes its own new state, so opening it starts the server and closing it
//     stops the process;
//   - a plain config write passes "was running", so saving a definition neither starts a server
//     that was not running (starting is an explicit action) nor leaves a running one behind old
//     values - it reconnects on the new definition.
func (m *ExternalMCPManager) applyConfigLocked(name string, serverCfg config.ExternalMCPServerConfig, start bool) {
	if client, exists := m.clients[name]; exists {
		client.Close()
		delete(m.clients, name)
	}
	m.configs[name] = serverCfg
	if start {
		go m.connectClient(name, serverCfg)
	}
}
