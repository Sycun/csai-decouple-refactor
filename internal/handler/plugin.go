package handler

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/plugin"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// PluginHandler is the "one click to extend" surface: list what is plugged in, install a bundle,
// unplug it, switch a unit on or off - all against the live capability table, so none of it
// restarts the process.
type PluginHandler struct {
	table     *plugin.Table
	bundles   string // the only directory a bundle may be installed from
	republish catalogPublisher
	// tools rebuilds the recipe layer plus the MCP tool surface after a mutation that touched a
	// tool unit. It is a constructor argument for the same reason the audit service is: leaving
	// it out is not an error, it is a bundle whose recipe silently never becomes executable
	// while the table says it is installed.
	tools toolLayerRebuilder
	// mcp is the live external-MCP manager. A pack that declares a server writes it there and
	// removes it from there, so the declaration has one home; installing never starts it.
	mcp MCPProvisioner
	// plugins is the live plug-in host plus capability table for the kind that ships executable
	// code: a pack's plugin unit declares a trust domain, gets checked against what its binary
	// actually provides, and only then becomes callable.
	plugins PluginProvisioner
	// switches remembers the operator's on/off decision outside this process. A bundle's files
	// must not be edited after install, so the console's switch had nowhere durable to go, and a
	// restart rebuilt every bundled unit as enabled.
	switches switchMemory
	// installs remembers the install decision itself. Start-up re-installs exactly the recorded
	// packs, so without this a click would last until the next restart - and, in the other
	// direction, a pack directory nobody chose would install itself at boot.
	installs bundleInstallMemory
	// trust answers where installed executable code came from and what the client-enforced block
	// list holds. A constructor argument for the same reason as the rest: a nil here compiles and
	// serves, and the operator simply never sees that a revoked build is still installed.
	trust  capabilityTrust
	logger *zap.Logger
	audit  *audit.Service
}

// switchMemory is the part of the capability switch store the console writes to. A nil recorder is
// legal Go and means the switches are session-only, so the response has to say that rather than
// leaving "已更新" to read as durable.
type switchMemory interface {
	Record(unitID, path string, enabled bool) error
	Forget(unitIDs ...string) error
}

// bundleInstallMemory is the part of the install store the console writes to. The nil case follows
// the same honesty rule as switchMemory: an install that cannot be recorded is a session install,
// and the response says so instead of letting the next restart look like a random uninstall.
//
// units is the operator's selection, or nil for "the whole pack" - the store keeps the two apart,
// because a whole-pack row follows the directory (a version bump that adds a unit installs it) while
// an explicit list means exactly those units.
type bundleInstallMemory interface {
	Record(bundleID, version string, units []string) error
	Forget(bundleID string) error
}

// toolLayerRebuilder is the piece of the tool layer that owns the recipe list and the MCP tool
// surface; *ToolLayer implements it.
type toolLayerRebuilder interface {
	Rebuild() error
}

// catalogPublisher is the piece that turns table state into served configuration. RoleHandler
// owns it today; the point of naming it here is that installing must never be able to leave the
// table and the live catalog disagreeing.
type catalogPublisher interface {
	Reload() (int, error)
}

// NewPluginHandler takes the audit service as a constructor argument rather than through a
// SetAudit method on purpose: every other handler is wired with a setter that the assembly has
// to remember, which is why an audit-completeness gate exists for them. Here forgetting is a
// compile error, so the handler is also deliberately outside that gate's scope.
func NewPluginHandler(table *plugin.Table, bundlesDir string, publisher catalogPublisher, tools toolLayerRebuilder, mcpManager MCPProvisioner, pluginProvisioner PluginProvisioner, switches switchMemory, installs bundleInstallMemory, trust capabilityTrust, auditSvc *audit.Service, logger *zap.Logger) *PluginHandler {
	return &PluginHandler{
		table: table, bundles: bundlesDir, republish: publisher, tools: tools,
		mcp: mcpManager, plugins: pluginProvisioner, switches: switches, installs: installs, trust: trust, audit: auditSvc, logger: logger,
	}
}

type unitView struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Bundle  string `json:"bundle,omitempty"`
	Enabled bool   `json:"enabled"`
	Served  bool   `json:"served"`
	Reason  string `json:"reason,omitempty"`
	// Installed says whether this unit is in the capability table. It matters for the units that are
	// only *declared*: a pack's manifest lists everything it could deliver, and since a pack can be
	// installed partially, "in the manifest" and "in the table" are two different states. Without
	// this field the console would show an unselected unit exactly like a selected one.
	Installed bool `json:"installed"`
	// Conflict names the holder when this unit's identity belongs to somebody else (the built-in
	// scan, or another pack). An install of this identity would be refused, so the console disables
	// the choice and says who has it instead of letting the click fail.
	Conflict string `json:"conflict,omitempty"`
	// Digest is the content fingerprint taken at install time. It travels so the console can show
	// what is installed (short form) and diff an upgrade ("this unit's bytes change") without a
	// second endpoint.
	Digest string `json:"digest,omitempty"`
	// Provenance is where a unit that ships executable code came from: the publisher and the
	// artifact digest revocation keys on. Empty for content kinds and for a plugin unit that was
	// never registered.
	Publisher      string `json:"publisher,omitempty"`
	ArtifactDigest string `json:"artifactDigest,omitempty"`
	// Revoked marks a unit matched by the client-enforced block list (by publisher or by the
	// digest of the build it runs).
	Revoked bool `json:"revoked,omitempty"`
}

// servedKinds lists the kinds whose run path reads the table. Every kind is in it now: a role,
// agent, skill or recipe is served from the table's paths, and an MCP declaration is written into
// the live external-MCP manager by install and removed by unplug. What stays separate is
// *started*: a declared server arrives disabled, because running a process is the operator's call
// and not a side effect of clicking install - the unit's own enabled flag carries that.
var servedKinds = map[plugin.Kind]string{
	plugin.KindRole:  "",
	plugin.KindAgent: "",
	plugin.KindSkill: "",
	plugin.KindTool:  "",
	plugin.KindMCP:   "",
	// A mode unit is served by the agent-mode catalog: /api/agent-modes answers from the table, and
	// the run paths of chat, robot, batch and workflow resolve modes through the same catalog.
	plugin.KindMode: "",
	// A plugin unit is served once its binary's advertised entry points have been checked against
	// the reviewed list and registered; until then the table holds a declaration nobody can call.
	// liveUnitView reports that per unit, which is why this entry is empty rather than a reason.
	plugin.KindPlugin: "",
}

func unitServed(u plugin.Unit) (bool, string) {
	if gap, ok := servedKinds[u.Kind]; ok {
		return true, gap
	}
	return false, "unknown capability kind: no run path is wired to it"
}

func toUnitView(u plugin.Unit) unitView {
	served, reason := unitServed(u)
	return unitView{
		ID: u.ID, Kind: string(u.Kind), Name: u.Name, Path: u.Path,
		Bundle: u.Bundle, Enabled: u.Enabled, Served: served, Reason: reason,
		Installed: true,
		Digest:    u.Digest,
	}
}

// manifestUnitView renders one manifest entry, which is not necessarily an installed unit: a pack
// can be installed partially, so the view answers the two questions the manifest alone cannot -
// is this identity in the table, and if not, does somebody else hold it.
func (h *PluginHandler) manifestUnitView(u plugin.Unit, bundleID string) unitView {
	v := h.liveUnitView(u)
	cur, ok := h.table.Unit(u.ID)
	switch {
	case !ok:
		v.Installed = false
	case cur.Bundle == bundleID:
		v.Installed = true
	default:
		v.Installed = false
		if cur.Bundle == "" {
			v.Conflict = "该身份已由内置目录登记"
		} else {
			v.Conflict = fmt.Sprintf("该身份已由能力包 %q 提供", cur.Bundle)
		}
	}
	return v
}

// liveUnitView is toUnitView for a unit that is actually installed, where an MCP unit's served flag
// has a second condition: the live manager has to still hold the declaration the pack made.
//
// Without this the console would keep claiming a server is served after config.yaml grew a
// declaration of the same name (the file wins there, and the pack loses the live slot).
func (h *PluginHandler) liveUnitView(u plugin.Unit) unitView {
	v := toUnitView(u)
	h.markPluginProvenance(u, &v)
	if u.Kind == plugin.KindPlugin && v.Served {
		v.Served, v.Reason = h.pluginServedState(u)
		return v
	}
	if u.Kind != plugin.KindMCP || !v.Served {
		return v
	}
	if h.mcp == nil {
		v.Served, v.Reason = false, "未接入外部 MCP 管理器：包声明的服务器只写在能力表里，无人连接"
		return v
	}
	if _, owned := h.mcp.PackOwner(u.Name); !owned {
		v.Served, v.Reason = false, "外部 MCP 管理器已不再持有本包对该名称的声明（配置文件里有同名服务器）"
	}
	return v
}

func (h *PluginHandler) bundleView(b *plugin.Bundle) gin.H {
	units := make([]unitView, 0, len(b.Units))
	installed := 0
	for _, u := range b.Units {
		view := h.manifestUnitView(u, b.ID)
		if view.Installed {
			installed++
		}
		units = append(units, view)
	}
	return gin.H{
		"id": b.ID, "name": b.Name, "version": b.Version,
		"description": b.Description, "dir": b.Dir, "units": units,
		"unitsInstalled": installed, "unitsTotal": len(b.Units),
		"categories": b.Categories, "author": b.Author, "homepage": b.Homepage,
		"license": b.License, "compatibility": b.Compatibility, "changelog": b.Changelog,
	}
}

// GetState answers GET /api/plugins: bundles, standalone units, table generation, and drift.
func (h *PluginHandler) GetState(c *gin.Context) {
	if h == nil || h.table == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "capability table unavailable"})
		return
	}
	bundles := make([]gin.H, 0)
	for _, b := range h.table.Bundles() {
		view := h.bundleView(b)
		// Where this pack can be rolled back to: the snapshot versions kept under .previous from
		// every install. Read-only, so the console can offer the choice before anything moves.
		view["rollbacks"] = h.rollbackVersions(b.ID)
		bundles = append(bundles, view)
	}
	standalone := make([]unitView, 0)
	for _, kind := range plugin.Kinds {
		for _, u := range h.table.Units(kind) {
			if u.Bundle == "" {
				standalone = append(standalone, h.liveUnitView(u))
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"bundles":     bundles,
		"standalone":  standalone,
		"generation":  h.table.Generation(),
		"drift":       h.table.Drifted(),
		"bundlesRoot": h.bundles,
		"servedKinds": servedKindNames(),
		// Which plug-in processes exist right now, with the pack that shipped each one.
		"pluginHost": h.pluginRuntimes(),
		// The client-enforced block list as this process holds it: source, entries, and whether a
		// list was loaded at all. A revocation the operator cannot see is a revocation they cannot
		// explain, and this is the only read surface it has today.
		"revocations": h.revocationView(),
	})
}

func servedKindNames() []string {
	out := make([]string, 0, len(servedKinds))
	for k := range servedKinds {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

type installRequest struct {
	// Bundle is a directory name *inside* the configured bundles root, or that directory's path
	// when it is inside the root. Anything else is refused: this endpoint installs files, so the
	// set of places it can read from has to be small and explicit.
	Bundle string `json:"bundle"`
	// Units narrows the install to named units ("skill/sink-driven-audit"). Omitted or empty means
	// the whole pack, which is the original behaviour spelled by absence. When present the set is
	// the *desired state* of this pack - units not listed are taken out of the table - so the same
	// request both installs and uninstalls, and re-sending it changes nothing.
	Units []string `json:"units,omitempty"`
	// FromVersion names a snapshot under <root>/.previous/<id>/<version>: the rollback path. The
	// request then means "restore these bytes over the pack directory, then install", which is why
	// it shares this endpoint - the install half is identical and a second route would be a second
	// place for the record/declaration/rebuild sequence to be forgotten.
	FromVersion string `json:"from_version,omitempty"`
}

// Install handles POST /api/plugins/install: the one-click extend, whole pack or a chosen subset.
func (h *PluginHandler) Install(c *gin.Context) {
	var req installRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求参数（需要 {\"bundle\":\"<包名>\"}）: " + err.Error()})
		return
	}
	dir, err := h.resolveBundleDir(req.Bundle)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rolledBack := ""
	if from := strings.TrimSpace(req.FromVersion); from != "" {
		if err := h.restoreRollback(dir, from); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		rolledBack = from
	}
	bundle, err := loadBundle(dir)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	sel, err := resolveUnitSelection(bundle, req.Units)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// The pre-flight checks and the declare step see only the units this call installs: the manifest
	// may list units that are staying out, and a check aimed at one of those would refuse a request
	// whose own units are fine.
	narrowed := withUnits(bundle, sel.units)
	if err := h.checkMCPConflicts(narrowed); err != nil {
		// Refused before anything changed: a pack must not be able to replace a server the
		// operator declared in config.yaml, whose tools may be in use right now.
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if err := h.checkPluginUnits(narrowed); err != nil {
		// Same rule for code: a pack whose plugin declaration does not resolve, or whose capability
		// list is unusable, is refused before it can be half-installed.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.checkModeUnits(narrowed); err != nil {
		// Same rule for modes: a declaration whose id does not match its file name, or names a
		// mode the kernel does not know, is a unit that would never appear in the catalog.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out, err := h.applySelection(c, sel)
	if err != nil {
		h.replyMutationError(c, "install", bundle.ID, err)
		return
	}
	if h.audit != nil {
		details := map[string]interface{}{
			"version":   bundle.Version,
			"units":     len(out.installed),
			"requested": len(sel.units),
			"roles":     out.roles,
			"recorded":  out.recorded,
		}
		if !sel.whole {
			details["selection"] = out.selection
		}
		if rolledBack != "" {
			details["rollback_from"] = rolledBack
		}
		h.audit.RecordOK(c, "plugin", "bundle_install", "安装能力包", "plugin_bundle", bundle.ID, details)
	}
	body := mergeToolLayerReport(gin.H{
		"message":          "能力包已安装并生效",
		"bundle":           h.bundleView(bundle),
		"roles":            out.roles,
		"refreshed":        out.refreshed,
		"install_recorded": out.recorded,
		"units_installed":  len(out.installed),
		"units_total":      len(bundle.Units),
	}, reportOf(out))
	if out.recordMsg != "" {
		body["install_message"] = out.recordMsg
	}
	if rolledBack != "" {
		body["rolled_back_to"] = rolledBack
	}
	if out.snapshot != "" {
		body["snapshot_version"] = out.snapshot
	}
	if out.snapshotMsg != "" {
		body["snapshot_error"] = out.snapshotMsg
	}
	if !sel.whole {
		body["selection"] = out.selection
	}
	if len(out.removed) > 0 {
		body["units_removed"] = unitIDs(out.removed)
	}
	if out.mcpDeclared > 0 || out.mcpMessage != "" {
		body["mcp_declared"] = out.mcpDeclared
		body["mcp_started"] = false
		if out.mcpMessage != "" {
			body["mcp_message"] = out.mcpMessage
		}
	}
	if out.plugDeclared > 0 || out.plugMessage != "" {
		body["plugin_declared"] = out.plugDeclared
		body["plugin_started"] = false
		if out.plugMessage != "" {
			body["plugin_message"] = out.plugMessage
		}
	}
	if out.switchMsg != "" {
		body["switch_message"] = out.switchMsg
	}
	c.JSON(http.StatusOK, body)
}

// reportOf rebuilds the tool-layer part of a response from the outcome, so the two paths that
// mutate the table state it the same way.
func reportOf(out mutationOutcome) republishReport {
	return republishReport{roles: out.roles, tools: out.tools, refreshed: out.refreshed, toolMessage: out.toolMessage}
}

// installMCPDeclarations writes a pack's server declarations into the live manager and switches
// the units off, so the table, the manager and the console all say "declared, not started".
func (h *PluginHandler) installMCPDeclarations(bundle *plugin.Bundle) (int, string) {
	servers := mcpUnitsOf(bundle.Units)
	if len(servers) == 0 {
		return 0, ""
	}
	updated := make([]plugin.Unit, 0, len(servers))
	for _, u := range servers {
		off, err := h.table.SetEnabled(u.ID, false)
		if err != nil {
			return 0, fmt.Sprintf("%s: %v", u.ID, err)
		}
		updated = append(updated, off)
	}
	bundle.Units = replaceUnits(bundle.Units, updated)
	return h.provisionMCP(bundle.ID, updated)
}

// replaceUnits swaps the refreshed copies of a few units back into a bundle's unit list, so a
// response built from the bundle reports the state the table actually holds.
func replaceUnits(units []plugin.Unit, refreshed []plugin.Unit) []plugin.Unit {
	byID := make(map[string]plugin.Unit, len(refreshed))
	for _, u := range refreshed {
		byID[u.ID] = u
	}
	out := make([]plugin.Unit, 0, len(units))
	for _, u := range units {
		if r, ok := byID[u.ID]; ok {
			out = append(out, r)
			continue
		}
		out = append(out, u)
	}
	return out
}

// Uninstall handles DELETE /api/plugins/bundles/:id.
func (h *PluginHandler) Uninstall(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "能力包 ID 不能为空"})
		return
	}
	if _, ok := h.table.Bundle(id); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("能力包 %q 未安装", id)})
		return
	}
	out, err := h.applyUninstall(c, id)
	if err != nil {
		h.replyMutationError(c, "uninstall", id, err)
		return
	}
	if h.audit != nil {
		h.audit.RecordOK(c, "plugin", "bundle_uninstall", "卸载能力包", "plugin_bundle", id, map[string]interface{}{
			"roles":    out.roles,
			"recorded": out.forgotten,
			"units":    len(out.removed),
		})
	}
	body := mergeToolLayerReport(gin.H{
		"message": "能力包已卸载", "id": id, "roles": out.roles, "refreshed": out.refreshed,
		"install_forgotten": out.forgotten,
		"units_removed":     len(out.removed),
	}, reportOf(out))
	if out.forgetMsg != "" {
		body["install_message"] = out.forgetMsg
	}
	if out.switchMsg != "" {
		body["switch_message"] = out.switchMsg
	}
	if out.mcpRemoved > 0 || out.mcpMessage != "" {
		body["mcp_removed"] = out.mcpRemoved
		if out.mcpMessage != "" {
			body["mcp_message"] = out.mcpMessage
		}
	}
	if out.plugRemoved > 0 || out.plugMessage != "" {
		body["plugin_removed"] = out.plugRemoved
		if out.plugMessage != "" {
			body["plugin_message"] = out.plugMessage
		}
	}
	c.JSON(http.StatusOK, body)
}

// EnableUnit handles POST /api/plugins/units/:kind/:name/enabled - flip without touching
// sources.
//
// For a unit inside the built-in directories this is the switch the UI already exposes; for a
// bundle unit it is the only switch available, because editing a pack's file would make the
// installed content disagree with its recorded digest.
func (h *PluginHandler) EnableUnit(c *gin.Context) {
	id, ok := unitIDFromPath(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind 与 name 不能为空"})
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求参数: " + err.Error()})
		return
	}
	if body.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled 不能为空"})
		return
	}
	unit, err := h.table.SetEnabled(id, *body.Enabled)
	if err != nil {
		h.replyMutationError(c, "enable", id, err)
		return
	}
	mcpStarted, mcpMessage := false, ""
	pluginCaps, pluginMessage := []string(nil), ""
	if unitIDKind(id) == plugin.KindPlugin {
		// The switch is the operator's consent to run the binary, so this is where a plugin's trust
		// domain is declared and its entry points verified. A refusal reverts the unit: the console
		// must not show a plugin as enabled that nothing can call.
		ids, message, ok := h.applyPluginSwitch(unit)
		if !ok {
			if _, err := h.table.SetEnabled(id, false); err != nil {
				message = fmt.Sprintf("%s；且开关回滚失败：%v", message, err)
			}
			unit.Enabled = false
			if h.audit != nil {
				h.audit.RecordOK(c, "plugin", "unit_enable_refused", "拒绝启用插件单元", "plugin_unit", id, map[string]interface{}{
					"reason": message,
				})
			}
			c.JSON(http.StatusConflict, gin.H{"error": message, "unit": h.liveUnitView(unit)})
			return
		}
		pluginCaps, pluginMessage = ids, message
	}
	if unitIDKind(id) == plugin.KindMCP {
		// The switch is the operator's decision to run the process, so this is where a declared
		// server is allowed to start - never install.
		declared, msg := h.provisionMCP(unit.Bundle, []plugin.Unit{unit})
		mcpStarted = declared > 0 && unit.Enabled
		mcpMessage = msg
	}
	// The tool surface is recomposed *after* the switch has been applied. A plugin's capabilities only
	// exist once its trust domain is declared and verified, so rebuilding earlier would compose a
	// surface that misses the very entry points this request just made callable - and the console
	// would answer with the capability list while nothing can be called.
	report := h.republishCatalog(c, unitIDKind(id) == plugin.KindTool || unitIDKind(id) == plugin.KindPlugin)
	if h.audit != nil {
		h.audit.RecordOK(c, "plugin", "unit_enabled", "启停能力单元", "plugin_unit", id, map[string]interface{}{
			"enabled": *body.Enabled, "roles": report.roles,
		})
	}
	persisted, switchMessage := h.rememberSwitch(unit)
	respBody := mergeToolLayerReport(gin.H{
		"message": "已更新", "unit": h.liveUnitView(unit), "roles": report.roles, "refreshed": report.refreshed,
		"switch_persisted": persisted,
	}, report)
	if unitIDKind(id) == plugin.KindMCP {
		respBody["mcp_applied"] = mcpStarted
		if mcpMessage != "" {
			respBody["mcp_message"] = mcpMessage
		}
	}
	if unitIDKind(id) == plugin.KindPlugin {
		// What came back is the set the running binary proved it provides, so the console can show
		// the operator the entry points they just made callable.
		respBody["plugin_applied"] = unit.Enabled
		respBody["plugin_capabilities"] = pluginCaps
		if pluginMessage != "" {
			respBody["plugin_message"] = pluginMessage
		}
	}
	if switchMessage != "" {
		respBody["switch_message"] = switchMessage
	}
	c.JSON(http.StatusOK, respBody)
}

// rememberSwitch writes the operator's decision outside the process so a restart does not undo it.
// The unit copy SetEnabled returned carries the source path, which is what lets a row be recognised
// as stale later instead of hiding a capability somebody shipped under the same identity.
//
// A pack-declared MCP server is deliberately not recorded: start-up re-declares those disabled no
// matter what this row says, so recording an "on" would promise a durability the boot rule refuse
// to give.
func (h *PluginHandler) rememberSwitch(u plugin.Unit) (bool, string) {
	if u.Kind == plugin.KindMCP {
		return false, "包声明的 MCP 服务器每次启动都回到停用状态，这个开关只在本次进程内有效；要跨重启常驻请把它写进 config.yaml"
	}
	if u.Kind == plugin.KindPlugin {
		// Same rule, stricter reason: a pack's binary is code, and re-verifying it against the
		// reviewed list at every start is what keeps an updated pack from running something nobody
		// re-approved. So an "on" here is not durable, and saying so beats recording a promise the
		// boot path will not keep.
		return false, "包声明的插件每次启动都回到停用状态（需要重新核对二进制提供的能力），这个开关只在本次进程内有效"
	}
	if h.switches == nil {
		return false, "开关未落库：装配没有传入开关存储，重启后本单元回到源文件声明的状态"
	}
	if err := h.switches.Record(u.ID, u.Path, u.Enabled); err != nil {
		return false, fmt.Sprintf("开关写入失败：%v", err)
	}
	return true, ""
}

// forgetSwitches drops the saved decisions for identities that left the table on purpose. A row
// nobody forgets would come back as an unexplained "disabled" the next time somebody ships a unit
// under that name.
func (h *PluginHandler) forgetSwitches(units []plugin.Unit) string {
	if h.switches == nil || len(units) == 0 {
		return ""
	}
	ids := make([]string, 0, len(units))
	for _, u := range units {
		ids = append(ids, u.ID)
	}
	if err := h.switches.Forget(ids...); err != nil {
		return fmt.Sprintf("开关记录未清理：%v", err)
	}
	return ""
}

// rememberInstall records the install decision itself. Unlike a unit switch this is the widening
// direction - start-up replays exactly these rows - so a pack whose install cannot be recorded is
// a session install, and saying so beats letting the next restart read as a random uninstall.
//
// The selection travels with it: a row without one means "the whole pack", and losing the selection
// of a partial install would come back as an install of everything.
func (h *PluginHandler) rememberInstall(bundleID, version string, units []string) (bool, string) {
	if h.installs == nil {
		return false, "安装记录未落库：装配没有传入安装记录存储，重启后本包需要重新安装"
	}
	if err := h.installs.Record(bundleID, version, units); err != nil {
		return false, fmt.Sprintf("安装记录写入失败：%v；重启后本包需要重新安装", err)
	}
	return true, ""
}

// forgetInstall drops the decision together with the pack. A row nobody forgets re-installs the
// pack at the next boot, so a failed cleanup has to name that consequence in the response.
func (h *PluginHandler) forgetInstall(bundleID string) (bool, string) {
	if h.installs == nil {
		// Nothing was ever recorded through this process, so there is nothing to take back.
		return true, ""
	}
	if err := h.installs.Forget(bundleID); err != nil {
		return false, fmt.Sprintf("安装记录未清理：%v；本包会在下次启动时重新装入", err)
	}
	return true, ""
}

// RemoveLocalUnit handles DELETE /api/plugins/units/:kind/:name for one unit.
//
// A scanned unit (the built-in directories) is dropped from the table as before. A unit a pack
// owns is not refused any more: it is taken out of that pack's selection - the table entry goes, the
// pack keeps delivering the rest, and the install record narrows so a restart does not bring it
// back. Removing a pack's last unit is an uninstall, and it is performed as one instead of being
// half-done (the record would otherwise claim an installation with nothing in it).
func (h *PluginHandler) RemoveLocalUnit(c *gin.Context) {
	id, ok := unitIDFromPath(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind 与 name 不能为空"})
		return
	}
	victim, _ := h.table.Unit(id)
	if victim.Bundle != "" {
		out, uninstalled, err := h.applyUnitRemoval(c, victim)
		if err != nil {
			h.replyMutationError(c, "remove", id, err)
			return
		}
		if h.audit != nil {
			h.audit.RecordOK(c, "plugin", "unit_removed_from_bundle", "从能力包选择中移除单元", "plugin_unit", id, map[string]interface{}{
				"bundle": victim.Bundle, "uninstalled_bundle": uninstalled, "roles": out.roles,
			})
		}
		body := mergeToolLayerReport(gin.H{
			"message": "已从能力包的选择中移除（不删除文件）", "id": id, "bundle": victim.Bundle,
			"roles": out.roles, "refreshed": out.refreshed, "units_installed": len(out.installed),
			"selection": out.selection, "install_recorded": out.recorded,
			"bundle_uninstalled": uninstalled,
		}, reportOf(out))
		if out.recordMsg != "" {
			body["install_message"] = out.recordMsg
		}
		if out.switchMsg != "" {
			body["switch_message"] = out.switchMsg
		}
		if out.mcpRemoved > 0 || out.mcpMessage != "" {
			body["mcp_removed"] = out.mcpRemoved
			if out.mcpMessage != "" {
				body["mcp_message"] = out.mcpMessage
			}
		}
		if out.plugRemoved > 0 || out.plugMessage != "" {
			body["plugin_removed"] = out.plugRemoved
			if out.plugMessage != "" {
				body["plugin_message"] = out.plugMessage
			}
		}
		c.JSON(http.StatusOK, body)
		return
	}
	if err := h.table.RemoveLocal(id); err != nil {
		h.replyMutationError(c, "remove", id, err)
		return
	}
	dropped, mcpMessage := 0, ""
	if victim.Kind == plugin.KindMCP {
		dropped, mcpMessage = h.dropMCP([]plugin.Unit{victim})
	}
	report := h.republishCatalog(c, unitIDKind(id) == plugin.KindTool || unitIDKind(id) == plugin.KindPlugin)
	if h.audit != nil {
		h.audit.RecordOK(c, "plugin", "unit_detach", "从能力表摘除单元", "plugin_unit", id, map[string]interface{}{"roles": report.roles})
	}
	body := mergeToolLayerReport(gin.H{
		"message": "已从能力表摘除（不删除文件）", "id": id, "roles": report.roles, "refreshed": report.refreshed,
	}, report)
	if msg := h.forgetSwitches([]plugin.Unit{victim}); msg != "" {
		body["switch_message"] = msg
	}
	if victim.Kind == plugin.KindMCP {
		body["mcp_removed"] = dropped
		if mcpMessage != "" {
			body["mcp_message"] = mcpMessage
		}
	}
	c.JSON(http.StatusOK, body)
}

// mergeToolLayerReport states the tool-layer outcome in the body, including why it did not happen.
func mergeToolLayerReport(body gin.H, r republishReport) gin.H {
	body["tools_rebuilt"] = r.tools
	if r.toolMessage != "" {
		body["tool_layer_error"] = r.toolMessage
	}
	return body
}

type republishReport struct {
	roles       int
	tools       bool
	refreshed   bool
	toolMessage string
}

// republishCatalog rebuilds the served configuration from the table after any mutation.
//
// A failure here is reported in the response rather than turning a successful install into a 500:
// the table *is* the source of truth, so the install did land, and rolling it back silently
// would be its own surprise. The operator sees "installed, catalog not refreshed" and can retry.
//
// wantTools is decided by the caller from what actually changed: rebuilding the tool surface runs
// ClearTools and re-registers every built-in tool, so a role-only pack must not pay for it.
func (h *PluginHandler) republishCatalog(c *gin.Context, wantTools bool) republishReport {
	report := republishReport{refreshed: true}
	if h.republish == nil {
		// No publisher wired: the roles catalog is not being refreshed, and claiming otherwise
		// would be the paper contract this response exists to avoid.
		report.refreshed = false
	} else {
		roles, err := h.republish.Reload()
		if err != nil {
			h.logger.Warn("能力表已变更，但角色目录未能刷新", zap.Error(err))
			c.Set("pluginRefreshError", err.Error())
			return republishReport{refreshed: false}
		}
		report.roles = roles
	}
	if !wantTools {
		return report
	}
	if h.tools == nil {
		h.logger.Warn("能力表已变更，但装配未接入工具层重建，配方不会生效")
		c.Set("pluginRefreshError", "未接入工具层重建")
		return republishReport{roles: report.roles, refreshed: false,
			toolMessage: "工具层未重建：装配没有接入工具层重建器，配方需重启或 POST /config/apply"}
	}
	if err := h.tools.Rebuild(); err != nil {
		h.logger.Warn("能力表已变更，但工具层未能重建", zap.Error(err))
		c.Set("pluginRefreshError", err.Error())
		return republishReport{roles: report.roles, refreshed: false,
			toolMessage: "工具层未重建: " + err.Error()}
	}
	report.tools = true
	return report
}

// bundleHasKind reports whether any unit of the bundle is of this kind.
func bundleHasKind(b *plugin.Bundle, kind plugin.Kind) bool {
	if b == nil {
		return false
	}
	for _, u := range b.Units {
		if u.Kind == kind {
			return true
		}
	}
	return false
}

func unitIDKind(id string) plugin.Kind {
	kind, _, ok := strings.Cut(id, "/")
	if !ok {
		return ""
	}
	return plugin.Kind(kind)
}

// resolveBundleDir confines every install to the configured bundles root.
func (h *PluginHandler) resolveBundleDir(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("bundle 不能为空：传 bundles 目录下的包名或其路径")
	}
	if h.bundles == "" {
		return "", fmt.Errorf("未配置能力包目录")
	}
	absRoot, err := filepath.Abs(h.bundles)
	if err != nil {
		return "", fmt.Errorf("解析能力包根目录失败: %w", err)
	}
	candidate := filepath.Join(absRoot, filepath.Base(filepath.Clean("/"+ref)))
	if strings.ContainsRune(ref, os.PathSeparator) || filepath.IsAbs(ref) {
		candidate, err = filepath.Abs(ref)
		if err != nil {
			return "", fmt.Errorf("解析能力包路径失败: %w", err)
		}
	}
	rel, err := filepath.Rel(absRoot, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("能力包必须位于 %s 之内", absRoot)
	}
	if _, err := os.Stat(filepath.Join(candidate, plugin.ManifestFileName)); err != nil {
		return "", fmt.Errorf("在 %s 找不到 %s", candidate, plugin.ManifestFileName)
	}
	return candidate, nil
}

// unitIDFromPath rebuilds "<kind>/<name>" from the two route segments, rejecting an unknown kind
// rather than letting it reach the table as an identity nothing else can name.
func unitIDFromPath(c *gin.Context) (string, bool) {
	kind := plugin.Kind(strings.TrimSpace(c.Param("kind")))
	name := strings.TrimSpace(c.Param("name"))
	if !kind.Valid() || name == "" {
		return "", false
	}
	return plugin.UnitIDFor(kind, name), true
}

func loadBundle(dir string) (*plugin.Bundle, error) {
	m, err := plugin.LoadManifestDir(dir)
	if err != nil {
		return nil, err
	}
	return m.Resolve()
}

// replyMutationError keeps a conflict (409) distinguishable from a server fault (500): the
// message names the bundle that already holds the identity, which is the answer the operator
// needs in order to act.
func (h *PluginHandler) replyMutationError(c *gin.Context, op, subject string, err error) {
	var conflict *plugin.ErrConflict
	if errors.As(err, &conflict) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "not installed") {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	h.logger.Error("能力表变更失败", zap.String("op", op), zap.String("subject", subject), zap.Error(err))
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

// availableBundle is one directory under the bundles root, whether or not it is installed.
type availableBundle struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Version       string     `json:"version"`
	Description   string     `json:"description,omitempty"`
	Categories    []string   `json:"categories,omitempty"`
	Author        string     `json:"author,omitempty"`
	Homepage      string     `json:"homepage,omitempty"`
	License       string     `json:"license,omitempty"`
	Compatibility string     `json:"compatibility,omitempty"`
	Changelog     string     `json:"changelog,omitempty"`
	Installed     bool       `json:"installed"`
	Units         []unitView `json:"units"`
	// Preview is what installing this pack would register: per-unit capability metadata, class
	// counters and the fail-closed marks. It is computed per request from the pack's own files;
	// an unreadable one degrades to per-unit errors rather than failing the listing.
	Preview *bundlePreview `json:"preview,omitempty"`
	// Error is a manifest that could not be read or resolved. It is per pack: one half-written
	// bundle.yaml must not turn the list of "what can I install" into a 500.
	Error string `json:"error,omitempty"`
}

// ListAvailable answers GET /api/plugins/available - the catalogue the console offers for one
// click. It reads only directory names and their manifests under the configured bundles root, and
// never returns a path that escapes it.
func (h *PluginHandler) ListAvailable(c *gin.Context) {
	if h == nil || h.table == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "capability table unavailable"})
		return
	}
	root := h.bundles
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusOK, gin.H{"bundlesRoot": root, "bundles": []availableBundle{}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]availableBundle, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, statErr := os.Stat(filepath.Join(dir, plugin.ManifestFileName)); statErr != nil {
			continue
		}
		item := availableBundle{ID: entry.Name()}
		bundle, loadErr := loadBundle(dir)
		if loadErr != nil {
			item.Error = loadErr.Error()
			out = append(out, item)
			continue
		}
		item.Name = bundle.Name
		item.Version = bundle.Version
		item.Description = bundle.Description
		item.Categories = bundle.Categories
		item.Author = bundle.Author
		item.Homepage = bundle.Homepage
		item.License = bundle.License
		item.Compatibility = bundle.Compatibility
		item.Changelog = bundle.Changelog
		if _, installed := h.table.Bundle(bundle.ID); installed {
			item.Installed = true
		}
		item.Units = make([]unitView, 0, len(bundle.Units))
		for _, u := range bundle.Units {
			// The catalogue states what installing *would* wire, so it uses the static verdict for
			// served - nothing is declared yet and an MCP row must not read as shadowed before it
			// exists. Whether the unit is installed *now*, and whether somebody else already holds
			// its identity, are read from the live table: a partially installed pack has to show
			// which units it actually took.
			view := toUnitView(u)
			if cur, held := h.table.Unit(u.ID); held {
				if cur.Bundle == bundle.ID {
					view.Installed = true
				} else {
					view.Installed = false
					if cur.Bundle == "" {
						view.Conflict = "该身份已由内置目录登记"
					} else {
						view.Conflict = fmt.Sprintf("该身份已由能力包 %q 提供", cur.Bundle)
					}
				}
			} else {
				view.Installed = false
			}
			item.Units = append(item.Units, view)
		}
		item.Preview = buildBundlePreview(bundle)
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	c.JSON(http.StatusOK, gin.H{"bundlesRoot": root, "bundles": out})
}
