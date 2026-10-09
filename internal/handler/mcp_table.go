package handler

import (
	"fmt"
	"os"
	"strings"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"

	"gopkg.in/yaml.v3"
)

// An external MCP server is the last kind whose declaration lived only in config.yaml. A pack can
// now ship one, and installing the pack writes it into the running manager - which is the same
// object PUT /api/external-mcp/{name} drives, so there is one live configuration, not two.
//
// What installing deliberately does *not* do is start the process. Connecting to a server can
// spawn a command, and "the operator installed a pack" is not the same consent as "the operator
// started this process". So a declared server arrives disabled, shows up in the MCP page and in
// this console, and the unit switch (or the MCP page) is what turns it on.
//
// Ownership runs both ways: the manager refuses an operator-side add/delete/start/stop aimed at a
// pack-declared name (those endpoints only know a name, and 启动 would otherwise write an empty
// servers.<name> entry into config.yaml), and it refuses a pack declaration of a name the operator's
// file already holds. config.yaml wins any collision.

// MCPProvisioner is the live external-MCP manager, narrowed to what the plug-in surface needs. The
// pack-side methods are the only ones offered on purpose: a pack declaration is owned by the pack,
// so it is written, removed and looked up through the declaration-of-record path rather than the
// operator-side one (which refuses a pack-owned name).
type MCPProvisioner interface {
	GetConfigs() map[string]config.ExternalMCPServerConfig
	DeclarePackServer(name, bundleID string, serverCfg config.ExternalMCPServerConfig) error
	RemovePackServer(name string) error
	PackOwner(name string) (string, bool)
}

// mcpDeclarationFile is one pack-declared MCP server. The identity is the unit name, which is
// where the table already carries it, so the file has no name field of its own to disagree with it.
type mcpDeclarationFile struct {
	Type        string            `yaml:"type"`
	Command     string            `yaml:"command"`
	Args        []string          `yaml:"args"`
	Env         map[string]string `yaml:"env"`
	URL         string            `yaml:"url"`
	Headers     map[string]string `yaml:"headers"`
	Description string            `yaml:"description"`
	Timeout     int               `yaml:"timeout"`

	// Enabled is read but never honoured at install time; see the comment above. The switch is the
	// unit's, and the unit arrives disabled.
	Enabled bool `yaml:"enabled"`
}

// LoadMCPDeclaration reads and validates one declaration.
//
// Deliberately no ${VAR} expansion, unlike the operator's config.yaml and the MCP page: a pack is
// content somebody else wrote, and expanding it against this process's environment would let the
// pack author read whatever the server happens to have.
func LoadMCPDeclaration(path string) (config.ExternalMCPServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config.ExternalMCPServerConfig{}, fmt.Errorf("读取 MCP 声明失败: %w", err)
	}
	var f mcpDeclarationFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return config.ExternalMCPServerConfig{}, fmt.Errorf("解析 MCP 声明失败: %w", err)
	}
	if strings.TrimSpace(f.Command) == "" && strings.TrimSpace(f.URL) == "" {
		return config.ExternalMCPServerConfig{}, fmt.Errorf("MCP 声明需要 command（stdio）或 url（http/sse）")
	}
	return config.ExternalMCPServerConfig{
		Type:        strings.TrimSpace(f.Type),
		Command:     strings.TrimSpace(f.Command),
		Args:        f.Args,
		Env:         f.Env,
		URL:         strings.TrimSpace(f.URL),
		Headers:     f.Headers,
		Description: f.Description,
		Timeout:     f.Timeout,
	}, nil
}

// mcpUnitsOf returns the pack-declared servers among a set of units.
func mcpUnitsOf(units []plugin.Unit) []plugin.Unit {
	var out []plugin.Unit
	for _, u := range units {
		if u.Kind == plugin.KindMCP {
			out = append(out, u)
		}
	}
	return out
}

// checkMCPConflicts refuses a pack that would overwrite a server the operator already declared.
// The identity check in the table catches two packs claiming the same name; this catches the
// manager, whose contents predate the table and are not all in it.
func (h *PluginHandler) checkMCPConflicts(bundle *plugin.Bundle) error {
	units := mcpUnitsOf(bundle.Units)
	if len(units) == 0 || h.mcp == nil {
		return nil
	}
	existing := h.mcp.GetConfigs()
	for _, u := range units {
		if _, ok := existing[u.Name]; !ok {
			continue
		}
		if prev, ok := h.table.Unit(u.ID); ok && prev.Bundle == bundle.ID {
			continue // an upgrade of the same pack replacing its own declaration
		}
		return fmt.Errorf("MCP 服务器 %q 已由运行中的配置提供，能力包不能覆盖运维者声明的服务器", u.Name)
	}
	return nil
}

// provisionMCP writes the pack's server declarations into the live manager. enable is the state
// the units carry, so a re-install after the operator switched a server on does not switch it off.
func (h *PluginHandler) provisionMCP(bundleID string, units []plugin.Unit) (declared int, message string) {
	servers := mcpUnitsOf(units)
	if len(servers) == 0 {
		return 0, ""
	}
	if h.mcp == nil {
		return 0, "MCP 声明未接入外部 MCP 管理器：装配没有传入管理器，声明只进了能力表"
	}
	for _, u := range servers {
		cfg, err := LoadMCPDeclaration(u.Path)
		if err != nil {
			message = fmt.Sprintf("%s: %v", u.ID, err)
			continue
		}
		cfg.Disabled = !u.Enabled
		cfg.ExternalMCPEnable = u.Enabled
		if err := h.mcp.DeclarePackServer(u.Name, bundleID, cfg); err != nil {
			message = fmt.Sprintf("%s: %v", u.ID, err)
			continue
		}
		declared++
	}
	return declared, message
}

// dropMCP removes the pack's servers from the live manager. It never touches a server the pack
// did not declare, and it deletes no files.
func (h *PluginHandler) dropMCP(units []plugin.Unit) (removed int, message string) {
	if h.mcp == nil {
		return 0, "MCP 声明未接入外部 MCP 管理器：声明仍留在能力表里，需要重启或手工在 MCP 页停用"
	}
	for _, u := range mcpUnitsOf(units) {
		owner, owned := h.mcp.PackOwner(u.Name)
		if !owned {
			// The manager holds no pack declaration under this name: it was shadowed by the
			// operator's config.yaml server, or never took at all. Either way there is nothing
			// of this pack's to remove - and asking would delete the operator's own declaration.
			continue
		}
		if owner != u.Bundle {
			message = fmt.Sprintf("%s: 该服务器现由能力包 %q 声明，本包不删除它", u.ID, owner)
			continue
		}
		if err := h.mcp.RemovePackServer(u.Name); err != nil {
			message = fmt.Sprintf("%s: %v", u.ID, err)
			continue
		}
		removed++
	}
	return removed, message
}
