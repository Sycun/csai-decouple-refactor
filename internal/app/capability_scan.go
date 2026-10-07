package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/handler"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// scanBuiltInCapabilities loads the shipped capability directories into the plug-in table.
//
// This is not bookkeeping. The Eino skill middleware now reads its skills *from the table*
// (internal/einoskill), so if the built-in skills/ directory were not scanned here, switching to
// the table backend would serve zero skills and every agent run would lose them. Scanning makes
// "shipped" and "installed later" the same kind of object, which is what lets one code path serve
// both.
//
// Roles are deliberately absent: RoleHandler.Reload() owns that directory so the scan and the
// published configuration snapshot stay in one place.
func scanBuiltInCapabilities(table *plugin.Table, cfg *config.Config, configPath string, logger *zap.Logger) error {
	if table == nil {
		return errors.New("no capability table installed")
	}
	if cfg == nil {
		return errors.New("no configuration to resolve capability directories from")
	}
	configDir := filepath.Dir(configPath)

	for _, src := range builtInCapabilitySources(cfg, configDir, configPath) {
		if src.dir == "" {
			continue // this kind is not configured in this installation
		}
		units, err := plugin.ScanDir(src.kind, src.dir, src.namer)
		if err != nil {
			return fmt.Errorf("scan %s directory %s: %w", src.kind, src.dir, err)
		}
		scanned := 0
		for _, u := range units {
			if err := table.PutLocal(u); err != nil {
				var conflict *plugin.ErrConflict
				if errors.As(err, &conflict) {
					// An installed bundle owns this identity: the bundle wins, and the next
					// publish would otherwise silently replace a pack the operator chose.
					continue
				}
				return fmt.Errorf("record %s: %w", u.ID, err)
			}
			scanned++
		}
		if logger != nil {
			logger.Info("内置能力已登记到能力表",
				zap.String("kind", string(src.kind)),
				zap.String("dir", src.dir),
				zap.Int("units", scanned))
		}
	}
	return nil
}

type builtInSource struct {
	kind  plugin.Kind
	dir   string
	namer plugin.Namer
}

// builtInCapabilitySources resolves each directory the same way config.Load does, so a relative
// path means the same thing to the scan and to the loader that reads these files today.
func builtInCapabilitySources(cfg *config.Config, configDir, configPath string) []builtInSource {
	toolNamer := func(kind plugin.Kind, path string) (string, error) {
		if kind != plugin.KindTool {
			return "", nil
		}
		tool, err := config.LoadToolFromFile(path)
		if err != nil {
			return "", err
		}
		return tool.Name, nil
	}
	return []builtInSource{
		// No default for skills: an empty skills_dir means "this installation has no skill
		// directory", and inventing one would load skills a config explicitly turned off.
		{kind: plugin.KindSkill, dir: resolveUnderConfig(cfg.SkillsDir, configDir, "")},
		{kind: plugin.KindAgent, dir: resolveUnderConfig(cfg.AgentsDir, configDir, "agents")},
		{kind: plugin.KindTool, dir: config.ResolveToolsDir(cfg.Security.ToolsDir, configPath), namer: toolNamer},
	}
}

func resolveUnderConfig(dir, configDir, fallback string) string {
	name := dir
	if name == "" {
		name = fallback
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(configDir, name)
}

// installRecords is the part of the install store the boot path uses: which packs the operator
// actually installed, and the version refresh when the directory's content moved forward. A nil
// reader is legal Go and means "the recorded decisions are unknown", in which case nothing is
// re-installed - a directory is the catalogue, and only a record makes it part of the installation.
type installRecords interface {
	All() ([]store.InstalledBundle, error)
	Record(bundleID, version string) error
}

// installBundlesFromDisk re-installs exactly the packs the operator installed - the rows in the
// install store - not everything that happens to sit under <configDir>/bundles.
//
// Without this an installed capability lasts until the next restart, which is not what
// "install" means to the person who clicked it: every run path reads the table, and the table is
// rebuilt from disk at start-up. The other direction matters just as much: a directory nobody
// installed must NOT become live at boot, or "shipped next to the app" and "part of this
// installation" would be the same set and the catalogue would install itself.
//
// The built-in scan runs first on purpose. Identity is what makes the merge safe, so a pack that
// shadows a shipped capability must be refused here exactly as the install endpoint refuses it,
// rather than winning because it happened to load before the built-in scan could object.
//
// A recorded pack that cannot be read, or whose directory is gone, is reported and skipped: one
// half-written bundle.yaml must not stop the server from booting, must not be able to take the
// *other* packs' capabilities away, and must not silently drop the operator's decision - the row
// stays, so restoring the directory restores the pack.
func installBundlesFromDisk(table *plugin.Table, root string, records installRecords, logger *zap.Logger) (int, []string) {
	if table == nil || strings.TrimSpace(root) == "" {
		return 0, nil
	}
	if records == nil {
		if logger != nil {
			logger.Warn("未装配能力包安装记录存储，本次启动不重装任何能力包（bundles 目录仅作可安装货架）")
		}
		return 0, nil
	}
	rows, err := records.All()
	if err != nil {
		if logger != nil {
			logger.Warn("读取能力包安装记录失败，本次启动不重装任何能力包", zap.Error(err))
		}
		return 0, nil
	}
	var installed int
	var refused []string
	for _, row := range rows {
		// The id names a directory under root before any manifest has been read; a hand-edited row
		// must not be able to point the join outside the bundles root.
		if !store.ValidBundleID(row.ID) {
			refused = append(refused, fmt.Sprintf("%q: 安装记录里的包名不可用", row.ID))
			continue
		}
		dir := filepath.Join(root, row.ID)
		if _, err := os.Stat(dir); err != nil {
			refused = append(refused, fmt.Sprintf("%s: 目录不存在（安装记录保留，放回目录即恢复）", row.ID))
			continue
		}
		bundle, err := loadBundleForScan(dir)
		if err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", row.ID, err))
			continue
		}
		if bundle.ID != row.ID {
			refused = append(refused, fmt.Sprintf("%s: 目录里的包声明为 %q，与安装记录不一致", row.ID, bundle.ID))
			continue
		}
		if err := table.InstallBundle(bundle); err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", row.ID, err))
			continue
		}
		installed++
		// The shipped tree moving a pack forward (new release in the same directory) is a content
		// update of a decision the operator already made; the recorded version follows so the
		// console can compare installed vs on-disk. A downgrade is recorded the same way - the
		// row describes what is installed, and what is installed is what the directory holds.
		if bundle.Version != row.Version {
			if err := records.Record(row.ID, bundle.Version); err != nil && logger != nil {
				logger.Warn("能力包版本变更未能写回安装记录",
					zap.String("bundle", row.ID), zap.String("from", row.Version), zap.String("to", bundle.Version), zap.Error(err))
			} else if logger != nil {
				logger.Info("能力包内容已随目录更新",
					zap.String("bundle", row.ID), zap.String("from", row.Version), zap.String("to", bundle.Version))
			}
		}
	}
	if logger != nil && (installed > 0 || len(refused) > 0) {
		logger.Info("能力包已按安装记录重新装入能力表",
			zap.String("root", root), zap.Int("installed", installed), zap.Int("refused", len(refused)))
		for _, r := range refused {
			logger.Warn("能力包未能装入，其余包与内置能力不受影响", zap.String("bundle", r))
		}
	}
	return installed, refused
}

func loadBundleForScan(dir string) (*plugin.Bundle, error) {
	m, err := plugin.LoadManifestDir(dir)
	if err != nil {
		return nil, err
	}
	return m.Resolve()
}

// bundleOwnedToolUnits counts the recipes that came from a pack rather than from tools_dir.
//
// The built-in ones are already in the live tool list, because config.Load scanned tools_dir
// before the table existed; only a pack's recipe needs the table-driven rebuild at start-up.
func bundleOwnedToolUnits(table *plugin.Table) int {
	if table == nil {
		return 0
	}
	var n int
	for _, u := range table.Units(plugin.KindTool) {
		if u.Bundle != "" {
			n++
		}
	}
	return n
}

// provisionDeclaredServers re-declares the MCP servers that packs own into the live external-MCP
// manager at start-up. The manager reads its servers from config.yaml, which a pack cannot edit,
// so without this a declaration would exist only in the table until somebody reinstalled the pack.
//
// Every one of them comes back switched off, and the capability unit is flipped off to match. A
// pack's file saying `enabled: true` is the pack author's intent, not the operator's consent to
// spawn a process, and the table has no persisted switch state to honour - so the same rule that
// holds for install holds here: declaring is not starting. A server the operator wants to live
// across restarts belongs in config.yaml, where their own declaration is the consent.
func provisionDeclaredServers(mgr *mcp.ExternalMCPManager, table *plugin.Table) (int, string) {
	if mgr == nil || table == nil {
		return 0, "未启用外部 MCP 管理器，包声明的服务器只留在能力表里"
	}
	var declared int
	var note string
	for _, u := range table.Units(plugin.KindMCP) {
		if u.Enabled {
			off, err := table.SetEnabled(u.ID, false)
			if err != nil {
				note = fmt.Sprintf("%s: %v", u.ID, err)
				continue
			}
			u = off
		}
		cfg, err := handler.LoadMCPDeclaration(u.Path)
		if err != nil {
			note = fmt.Sprintf("%s: %v", u.ID, err)
			continue
		}
		cfg.Disabled = !u.Enabled
		cfg.ExternalMCPEnable = u.Enabled
		if err := mgr.DeclarePackServer(u.Name, u.Bundle, cfg); err != nil {
			note = fmt.Sprintf("%s: %v", u.ID, err)
			continue
		}
		declared++
	}
	return declared, note
}

// declarePackPluginUnits puts every plugin unit the table holds back into the declared state at
// start-up, the same way a pack's MCP server is re-declared rather than started.
//
// A pack's `plugins/ref.yaml` is read by whoever flips the switch, and the trust domain plus the
// discovered capability set only exist after that read. Nothing here performs that read: the host is
// not given a domain, no capabilities are registered, and therefore no process is spawned from a
// pack author's file. The persisted switch follows the same rule as the MCP one - a saved row is
// replayed only in the "off" direction, because re-trusting a binary is a decision the operator makes
// about the thing in front of them, not one inherited silently from a previous run.
//
// What this fixes is the disagreement between the two: without it a pack whose manifest says
// `enabled: true` comes back enabled at boot while the host holds nothing, so the console shows an
// enabled unit whose capabilities nobody can call and reports the reason as a failed enablement that
// never happened.
func declarePackPluginUnits(table *plugin.Table) (int, string) {
	if table == nil {
		return 0, "能力表未装配，包声明的插件单元无处可查"
	}
	var flipped int
	var note string
	for _, u := range table.Units(plugin.KindPlugin) {
		if !u.Enabled {
			continue
		}
		if _, err := table.SetEnabled(u.ID, false); err != nil {
			note = fmt.Sprintf("%s: %v", u.ID, err)
			continue
		}
		flipped++
	}
	return flipped, note
}

// applyPersistedSwitches re-applies the operator's own on/off decisions after the packs have been
// rebuilt from disk, and prunes the rows that no longer describe anything.
//
// Only a saved "off" is acted on. A row that said "on" cannot turn a unit whose source file says
// `enabled: false` back on: the runtime state stays `file enabled AND table enabled`, which is the
// same rule the tool layer runs, and it is what keeps a stale row from widening what may execute.
// A row whose unit is gone, or whose source slot no longer matches, is forgotten - otherwise
// shipping a different capability under an identity somebody switched off would inherit that
// switch, and the console would show a unit as unavailable with no reason anyone could name.
// The comparison is on the slot (`roles/x.yaml`), not the whole path, because the same
// installation reaches different absolute paths depending on how config.yaml was named.
func applyPersistedSwitches(table *plugin.Table, switches *store.CapabilitySwitches, logger *zap.Logger) (int, []string) {
	if table == nil || switches == nil {
		return 0, nil
	}
	rows, err := switches.All()
	if err != nil {
		if logger != nil {
			logger.Warn("读取能力单元开关失败，本次启动只按文件状态服务", zap.Error(err))
		}
		return 0, nil
	}
	var applied int
	var notes []string
	for _, sw := range rows {
		unit, ok := table.Unit(sw.UnitID)
		if !ok || store.SwitchPathKey(unit.Path) != store.SwitchPathKey(sw.Path) {
			if err := switches.Forget(sw.UnitID); err != nil {
				notes = append(notes, fmt.Sprintf("%s: %v", sw.UnitID, err))
			}
			continue
		}
		if sw.Enabled || !unit.Enabled {
			continue // already in the state the operator chose
		}
		if _, err := table.SetEnabled(sw.UnitID, false); err != nil {
			notes = append(notes, fmt.Sprintf("%s: %v", sw.UnitID, err))
			continue
		}
		applied++
	}
	if logger != nil && applied > 0 {
		logger.Info("已按保存的开关重新停用能力单元", zap.Int("units", applied))
	}
	return applied, notes
}

// ensureSkillStatsSchema creates the skill_stats table through the store that owns it. The data
// layer used to create every table in one start-up sweep; a domain whose SQL lives elsewhere has to
// take its schema with it, or the store queries a table nobody makes.
func ensureSkillStatsSchema(db *database.DB) error {
	if db == nil {
		return nil
	}
	return store.NewSkillStats(db.DB).EnsureSchema()
}

// ensureChatUploadArtifactSchema creates chat_upload_artifacts through its own store. It runs after
// the data layer has opened every base table, because the artifact rows carry a foreign key onto
// conversations.
func ensureChatUploadArtifactSchema(db *database.DB) error {
	if db == nil {
		return nil
	}
	return store.NewChatUploads(db.DB).EnsureSchema()
}

// ensureAuditLogsSchema creates audit_logs through the store that owns it, before the audit service
// can be asked to write a record.
func ensureAuditLogsSchema(db *database.DB) error {
	if db == nil {
		return nil
	}
	return store.NewAuditLogs(db.DB).EnsureSchema()
}

// ensureC2PayloadArtifactSchema creates c2_payload_artifacts through the store that owns it. The table
// is what the payload download gate reads, so a base without it refuses every download.
func ensureC2PayloadArtifactSchema(db *database.DB) error {
	if db == nil {
		return nil
	}
	return store.NewC2PayloadArtifacts(db.DB).EnsureSchema()
}

// ensureRobotIdentitySchema creates robot_user_bindings and robot_binding_codes through the store that
// owns them. Both carry a foreign key onto rbac_users, which NewDB has just created.
func ensureRobotIdentitySchema(db *database.DB) error {
	if db == nil {
		return nil
	}
	return store.NewRobotIdentity(db.DB).EnsureSchema()
}

// ensureRobotSessionSchema creates robot_user_sessions through the store that owns it, including the
// agent_mode column a base predating that field does not have.
func ensureRobotSessionSchema(db *database.DB) error {
	if db == nil {
		return nil
	}
	return store.NewRobotSessions(db.DB).EnsureSchema()
}

// ensureModelTokenUsageSchema creates model_token_usage through the store that owns it and carries
// over the usage events written before the table existed. The carry-over walks process_details, so it
// has to run after the timeline tables are there - which is why it belongs to start-up rather than to
// the data layer's own init, where it used to sit.
func ensureModelTokenUsageSchema(db *database.DB) error {
	if db == nil {
		return nil
	}
	usage := store.NewModelTokenUsage(db.DB)
	if err := usage.EnsureSchema(); err != nil {
		return err
	}
	return usage.BackfillFromProcessDetails()
}

// ensureKnowledgeRetrievalSchema creates knowledge_retrieval_logs through the store that owns it.
func ensureKnowledgeRetrievalSchema(db *database.DB) error {
	if db == nil {
		return nil
	}
	return store.NewKnowledgeRetrieval(db.DB).EnsureSchema()
}
