package handler

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/pluginhost"
	"cyberstrike-ai/internal/security"

	"go.uber.org/zap"
)

// ToolLayer owns the recipe list, the recipe capability layer and the MCP tool surface. It is a
// collaborator of ConfigHandler rather than four more methods on it, because the tool surface is a
// thing with its own state and its own serialisation, and the size ratchet in internal/layering
// says new capability goes here.
//
// Tool recipes were the last capability kind whose run path read a directory instead of the
// capability table. Rebuild makes the table the source, so a bundle that ships a recipe becomes
// executable on install - by running the sequence POST /config/apply already ran, which is what
// keeps the operator approval floor untouched: the recipe layer is rebuilt from the loaded
// recipes, and a recipe with no `capability:` manifest is still refused there and still fails
// closed when something tries to execute it.
type ToolLayer struct {
	// mu serialises the whole rebuild: ClearTools empties the server before the re-registration
	// refills it, so two overlapping rebuilds would let one caller's ClearTools land inside the
	// other's window. The window *inside* one rebuild is pre-existing (it is what /config/apply
	// has always done) and is not widened here.
	//
	// injectMu guards the injected closures, and a rebuild never holds both: a registrar closure
	// that turns around and calls a Set* method would otherwise self-deadlock, because this mutex
	// is not reentrant. Rebuild takes a snapshot and releases it before calling anything.
	mu         sync.Mutex
	injectMu   sync.RWMutex
	cfg        *config.Config
	configPath string
	srv        *mcp.Server
	exec       *security.Executor
	ext        *mcp.ExternalMCPManager
	log        *zap.Logger

	capabilityRefresher CapabilityRefreshFunc
	vulnerability       VulnerabilityToolRegistrar
	webshell            WebshellToolRegistrar
	skills              SkillsToolRegistrar
	batch               BatchTaskToolRegistrar
	c2                  C2ToolRegistrar
	knowledge           KnowledgeToolRegistrar
}

// toolInject is a snapshot of the injected closures.
type toolInject struct {
	refresh       CapabilityRefreshFunc
	vulnerability VulnerabilityToolRegistrar
	webshell      WebshellToolRegistrar
	skills        SkillsToolRegistrar
	batch         BatchTaskToolRegistrar
	c2            C2ToolRegistrar
	knowledge     KnowledgeToolRegistrar
}

func (l *ToolLayer) snapshot() toolInject {
	l.injectMu.RLock()
	defer l.injectMu.RUnlock()
	return toolInject{
		refresh:       l.capabilityRefresher,
		vulnerability: l.vulnerability,
		webshell:      l.webshell,
		skills:        l.skills,
		batch:         l.batch,
		c2:            l.c2,
		knowledge:     l.knowledge,
	}
}

func newToolLayer(cfg *config.Config, configPath string, srv *mcp.Server, exec *security.Executor, ext *mcp.ExternalMCPManager, logger *zap.Logger) *ToolLayer {
	return &ToolLayer{cfg: cfg, configPath: configPath, srv: srv, exec: exec, ext: ext, log: logger}
}

// The registrars are injected after construction (the assembly builds the MCP server, then the
// handlers, then wires them), so they share the rebuild mutex: a Rebuild must not read a half-
// updated set. They were ConfigHandler setters before this collaborator existed; the count of
// injection points did not change, only their owner.

func (l *ToolLayer) SetCapabilityRefresher(fn CapabilityRefreshFunc) {
	l.injectMu.Lock()
	defer l.injectMu.Unlock()
	l.capabilityRefresher = fn
}

func (l *ToolLayer) SetVulnerabilityToolRegistrar(fn VulnerabilityToolRegistrar) {
	l.injectMu.Lock()
	defer l.injectMu.Unlock()
	l.vulnerability = fn
}

func (l *ToolLayer) SetWebshellToolRegistrar(fn WebshellToolRegistrar) {
	l.injectMu.Lock()
	defer l.injectMu.Unlock()
	l.webshell = fn
}

func (l *ToolLayer) SetSkillsToolRegistrar(fn SkillsToolRegistrar) {
	l.injectMu.Lock()
	defer l.injectMu.Unlock()
	l.skills = fn
}

func (l *ToolLayer) SetBatchTaskToolRegistrar(fn BatchTaskToolRegistrar) {
	l.injectMu.Lock()
	defer l.injectMu.Unlock()
	l.batch = fn
}

func (l *ToolLayer) SetC2ToolRegistrar(fn C2ToolRegistrar) {
	l.injectMu.Lock()
	defer l.injectMu.Unlock()
	l.c2 = fn
}

func (l *ToolLayer) SetKnowledgeToolRegistrar(fn KnowledgeToolRegistrar) {
	l.injectMu.Lock()
	defer l.injectMu.Unlock()
	l.knowledge = fn
}

// KnowledgeRegistrarInstalled reports whether a knowledge tool registrar is wired. ApplyConfig
// uses it to decide whether the knowledge base still has to be initialised on this request.
func (l *ToolLayer) KnowledgeRegistrarInstalled() bool {
	l.injectMu.RLock()
	defer l.injectMu.RUnlock()
	return l.knowledge != nil
}

// toolRecipeSourcesFromTable lists every recipe file the table knows, carrying its switch.
// nil means "the table holds no tool units at all", which is not the same statement as "this
// installation has no recipes" - the boot scan may simply not have run.
func toolRecipeSourcesFromTable() []config.ToolRecipeSource {
	table := plugin.Global()
	if table == nil {
		return nil
	}
	units := table.Units(plugin.KindTool)
	if len(units) == 0 {
		return nil
	}
	srcs := make([]config.ToolRecipeSource, 0, len(units))
	for _, u := range units {
		srcs = append(srcs, config.ToolRecipeSource{Path: u.Path, Enabled: u.Enabled})
	}
	// Sorted by path, not by the table's name order: the directory loader listed recipes in
	// ReadDir order, and cfg.Security.Tools order is what the tools list page shows.
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].Path < srcs[j].Path })
	return srcs
}

// toolRecipeOwner names the bundle that provided a recipe file, for reporting a broken or refused
// recipe against the pack that shipped it.
func toolRecipeOwner(path string) string {
	table := plugin.Global()
	if table == nil {
		return ""
	}
	for _, u := range table.Units(plugin.KindTool) {
		if u.Path == path {
			return u.Bundle
		}
	}
	return ""
}

// toolUnitSwitchedOff reports whether the capability table holds a runtime switch that turns this
// recipe off. The table's flag and the recipe file's `enabled:` combine with AND, so the table can
// only ever narrow what runs.
func toolUnitSwitchedOff(name string) bool {
	table := plugin.Global()
	if table == nil {
		return false
	}
	u, ok := table.Unit("tool/" + name)
	return ok && !u.Enabled
}

// reloadSecurityTools refreshes the live recipe list: from the table when it lists recipes,
// otherwise from tools_dir exactly as before. It returns the recipes that did not load.
func (l *ToolLayer) reloadSecurityTools() ([]config.ToolLoadFailure, error) {
	srcs := toolRecipeSourcesFromTable()
	if len(srcs) == 0 {
		return nil, config.ReloadSecurityToolsFromDir(l.cfg, l.configPath)
	}
	return config.ReloadSecurityToolsFromSources(l.cfg, l.configPath, srcs)
}

func (l *ToolLayer) refreshCapabilityLayer(inj toolInject) error {
	if inj.refresh == nil {
		return nil
	}
	return inj.refresh(l.cfg.Security.Tools)
}

// ErrToolLayerStep says which step of a tool-layer rebuild failed, so the HTTP layer can keep its
// distinct messages instead of matching on error strings.
type ErrToolLayerStep struct {
	Step string // "reload" | "capability"
	Err  error
}

func (e ErrToolLayerStep) Error() string { return e.Step + ": " + e.Err.Error() }
func (e ErrToolLayerStep) Unwrap() error { return e.Err }

// toolLayerUserError maps a rebuild failure onto the operator-facing message and the audit action
// POST /config/apply has always used for that step.
func toolLayerUserError(err error) (message, auditAction, detail string) {
	var step ErrToolLayerStep
	if !errors.As(err, &step) {
		return "重新加载工具配置失败", "应用配置失败：重新加载工具", err.Error()
	}
	if step.Step == "capability" {
		return "重建能力策略注册表失败", "应用配置失败：能力策略注册表", step.Err.Error()
	}
	return "重新加载工具配置失败", "应用配置失败：重新加载工具", step.Err.Error()
}

// Rebuild re-reads the recipe list from the capability table, rebuilds the recipe capability layer,
// then rebuilds the MCP tool surface. It is the one entry point both POST /config/apply and the
// plug-in endpoints use.
func (l *ToolLayer) Rebuild() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	inj := l.snapshot()
	failed, err := l.reloadSecurityTools()
	if err != nil {
		return ErrToolLayerStep{Step: "reload", Err: err}
	}
	for _, f := range failed {
		fields := []zap.Field{zap.String("recipe", f.Path), zap.String("error", f.Reason)}
		if b := toolRecipeOwner(f.Path); b != "" {
			fields = append(fields, zap.String("bundle", b))
		}
		l.log.Warn("工具配方无法加载，该工具不会存在而非半生效", fields...)
	}
	if err := l.refreshCapabilityLayer(inj); err != nil {
		return ErrToolLayerStep{Step: "capability", Err: err}
	}
	l.reregisterToolSurface(inj)
	l.log.Info("工具层已按能力表重建", zap.Int("tools_count", len(l.cfg.Security.Tools)))
	return nil
}

// reregisterToolSurface is the tail of a rebuild: wipe the MCP tool table, then re-register the
// recipe tools and every built-in registrar. ClearTools wipes the built-ins too, so this list is
// the whole surface and the order is the one ApplyConfig has always used. Pack plugin capabilities
// come last, so a pack can never take over a name the shipped binary already answers to.
func (l *ToolLayer) reregisterToolSurface(inj toolInject) {
	l.srv.ClearTools()

	l.exec.SetToolOutputMaxBytes(l.cfg.MultiAgent.EinoMiddleware.ReductionMaxLengthForTruncEffective())
	l.exec.SetToolOutputSpillRoot(l.cfg.MultiAgent.EinoMiddleware.ReductionRootDir)
	l.exec.RegisterTools(l.srv)
	mcp.RegisterExecutionControlTools(l.srv, l.ext)

	if inj.vulnerability != nil {
		l.log.Info("重新注册漏洞记录工具")
		if err := inj.vulnerability(); err != nil {
			l.log.Error("重新注册漏洞记录工具失败", zap.Error(err))
		} else {
			l.log.Info("漏洞记录工具已重新注册")
		}
	}

	if inj.webshell != nil {
		l.log.Info("重新注册 WebShell 工具")
		if err := inj.webshell(); err != nil {
			l.log.Error("重新注册 WebShell 工具失败", zap.Error(err))
		} else {
			l.log.Info("WebShell 工具已重新注册")
		}
	}

	if inj.skills != nil {
		l.log.Info("重新注册Skills工具")
		if err := inj.skills(); err != nil {
			l.log.Error("重新注册Skills工具失败", zap.Error(err))
		} else {
			l.log.Info("Skills工具已重新注册")
		}
	}

	if inj.batch != nil {
		l.log.Info("重新注册批量任务 MCP 工具")
		if err := inj.batch(); err != nil {
			l.log.Error("重新注册批量任务 MCP 工具失败", zap.Error(err))
		} else {
			l.log.Info("批量任务 MCP 工具已重新注册")
		}
	}

	if inj.c2 != nil {
		l.log.Info("重新注册 C2 MCP 工具")
		if err := inj.c2(); err != nil {
			l.log.Error("重新注册 C2 MCP 工具失败", zap.Error(err))
		} else {
			l.log.Info("C2 MCP 工具已处理")
		}
	}

	if l.cfg.Knowledge.Enabled && inj.knowledge != nil {
		l.log.Info("重新注册知识库工具")
		if err := inj.knowledge(); err != nil {
			l.log.Error("重新注册知识库工具失败", zap.Error(err))
		} else {
			l.log.Info("知识库工具已重新注册")
		}
	}

	if n := l.registerPackPluginTools(); n > 0 {
		l.log.Info("包内插件能力已挂上工具面", zap.Int("capabilities", n))
	}
}

// registerPackPluginTools puts the capabilities a pack's plugin binary proved it provides onto the
// MCP tool surface. A pack plugin has no recipe - the reviewed declaration in the pack is what
// names its entry points, and the executor already routes a `plugin-host:*` runtime out of process -
// so without this step the capability would sit in the table authorized, revocable and invisible to
// the model, which is a long way of saying the pack installed and did nothing.
//
// It is recomposed from the registry on every rebuild, so switching a unit off (which drops its
// subset) takes its tools back with no separate unregister path to forget. It runs last so the
// shipped surface wins any name clash: a pack may add an entry point, never answer one itself.
func (l *ToolLayer) registerPackPluginTools() int {
	if l.srv == nil || l.exec == nil {
		return 0
	}
	taken := map[string]bool{}
	for _, tool := range l.srv.GetAllTools() {
		taken[tool.Name] = true
	}
	registered := 0
	for _, spec := range capability.Global().Specs() {
		if !pluginhost.IsPluginRuntime(string(spec.Runtime)) || spec.Source != packPluginSource {
			continue
		}
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		if taken[name] {
			l.log.Warn("包内插件能力与既有工具同名，保留内置实现", zap.String("capability", spec.ID), zap.String("tool", name))
			continue
		}
		taken[name] = true
		description := strings.TrimSpace(spec.Description)
		if description == "" {
			description = strings.TrimSpace(spec.Title)
		}
		schema := map[string]interface{}{"type": "object"}
		if len(spec.ParamsSchema) > 0 {
			var declared map[string]interface{}
			if err := json.Unmarshal(spec.ParamsSchema, &declared); err == nil && len(declared) > 0 {
				schema = declared
			}
		}
		toolName := name
		handler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
			return l.exec.ExecuteTool(ctx, toolName, args)
		}
		l.srv.RegisterTool(mcp.Tool{
			Name:             toolName,
			Description:      description,
			ShortDescription: strings.TrimSpace(spec.Title),
			InputSchema:      schema,
		}, handler)
		registered++
	}
	return registered
}
