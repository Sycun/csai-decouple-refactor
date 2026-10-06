package handler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/plugin"

	"gopkg.in/yaml.v3"
)

// A `plugin` unit is the one kind that ships executable code rather than content, so its
// declaration is where two rules have to be enforced before anything runs:
//
//   - The binary stays inside the pack. A manifest may not point at /usr/bin/curl or a sibling
//     installation's file; the path is resolved against the pack directory and must land in it,
//     the same containment every other unit path already has.
//   - The capability list is reviewed content, not the plugin's own claim. What ships here is the
//     set a human approved with its class, permission and grants; the running plugin is asked what
//     it provides and the two sets must agree exactly (see internal/app). A plugin that could
//     describe itself could describe its way into `destructive`.
//
// Nothing in this file starts a process. Loading a declaration is reading a file.

// pluginCapabilityID is "<publisher>.<name>", the shape the host's routing depends on: the first
// segment is the trust domain a call is dispatched to.
var pluginCapabilityID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*\.[a-z0-9][a-z0-9_-]*$`)

// packPluginSource marks a capability that a pack's plugin binary provides. It is how the tool
// surface tells those apart from recipe tools without re-reading every pack manifest.
const packPluginSource = "pack-plugin"

type pluginCapabilityFile struct {
	ID           string         `yaml:"id"`
	Title        string         `yaml:"title"`
	Description  string         `yaml:"description"`
	Class        string         `yaml:"class"`
	Permission   string         `yaml:"permission"`
	Approval     string         `yaml:"approval"`
	Timeout      int            `yaml:"timeoutSeconds"`
	ParamsSchema map[string]any `yaml:"paramsSchema"`
}

type pluginUnitFile struct {
	PluginID     string                 `yaml:"pluginId"`
	Binary       string                 `yaml:"binary"`
	Args         []string               `yaml:"args"`
	EnvAllow     []string               `yaml:"envAllow"`
	Grants       []string               `yaml:"grants"`
	CallTimeout  int                    `yaml:"callTimeoutSeconds"`
	IdleTimeout  int                    `yaml:"idleTimeoutSeconds"`
	MaxRestarts  int                    `yaml:"maxRestarts"`
	Version      string                 `yaml:"version"`
	Capabilities []pluginCapabilityFile `yaml:"capabilities"`
}

// PluginUnitDeclaration is a validated plugin unit: the publisher, the binary inside the pack, and
// the reviewed capability list.
type PluginUnitDeclaration struct {
	Publisher string
	Binary    string // absolute, inside the pack directory
	// BinaryDigest fingerprints the executable alone, not the declaration-plus-binary pair: a
	// revocation names a build, and rewriting a description line must not change which build a
	// block list entry points at.
	BinaryDigest string
	Args         []string
	EnvAllow     []string
	Grants       []string
	CallIdle     time.Duration
	CallExec     time.Duration
	Restarts     int
	Version      string
	Capability   []PluginCapabilityDeclaration
}

// PluginCapabilityDeclaration is one reviewed entry point of the plugin.
type PluginCapabilityDeclaration struct {
	ID           string
	Title        string
	Description  string
	Class        capability.Class
	Permission   string
	Approval     capability.Approval
	Timeout      time.Duration
	ParamsSchema map[string]any
}

// Names lists the declared capability ids, sorted - the set the discovered answer is compared to.
func (d *PluginUnitDeclaration) Names() []string {
	out := make([]string, 0, len(d.Capability))
	for _, c := range d.Capability {
		out = append(out, c.ID)
	}
	return out
}

// LoadPluginUnitDeclaration reads and validates one plugin unit. `packDir` is the bundle's own
// directory; every relative path is contained in it.
func LoadPluginUnitDeclaration(path, packDir string) (*PluginUnitDeclaration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取插件声明失败: %w", err)
	}
	var f pluginUnitFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("解析插件声明失败: %w", err)
	}

	unitName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	publisher := strings.TrimSpace(f.PluginID)
	if publisher == "" {
		publisher = unitName
	}
	// The host routes by the first segment of a capability id and keys instances by the domain, so
	// publisher, domain and capability prefix must be one name. Allowing a pack to split them would
	// produce capabilities that resolve to a different instance than the one that provides them.
	if publisher != unitName {
		return nil, fmt.Errorf("插件声明 %s 的 pluginId %q 与单元名 %q 不一致：能力会路由到另一个信任域", path, publisher, unitName)
	}
	binary, err := resolveInsidePack(packDir, f.Binary)
	if err != nil {
		return nil, fmt.Errorf("插件 %s: %w", publisher, err)
	}

	grants := make([]capability.CapabilityGrant, 0, len(f.Grants))
	for _, raw := range f.Grants {
		grant, err := capability.ParseGrant(raw)
		if err != nil {
			return nil, fmt.Errorf("插件 %s: %w", publisher, err)
		}
		grants = append(grants, grant)
	}

	if len(f.Capabilities) == 0 {
		return nil, fmt.Errorf("插件 %s 没有声明任何能力：没有已审阅入口的插件不可调用", publisher)
	}
	binaryDigest, err := plugin.Digest(binary)
	if err != nil {
		return nil, fmt.Errorf("插件 %s: 无法为 binary 取摘要: %w", publisher, err)
	}
	decl := &PluginUnitDeclaration{
		Publisher:    publisher,
		Binary:       binary,
		BinaryDigest: binaryDigest,
		Args:         f.Args,
		EnvAllow:     f.EnvAllow,
		Grants:       f.Grants,
		CallExec:     time.Duration(f.CallTimeout) * time.Second,
		CallIdle:     time.Duration(f.IdleTimeout) * time.Second,
		Restarts:     f.MaxRestarts,
		Version:      strings.TrimSpace(f.Version),
		Capability:   make([]PluginCapabilityDeclaration, 0, len(f.Capabilities)),
	}
	seen := map[string]bool{}
	for _, c := range f.Capabilities {
		id := strings.TrimSpace(c.ID)
		if !pluginCapabilityID.MatchString(id) {
			return nil, fmt.Errorf("插件 %s 声明了能力 %q，它不是 <发布者>.<名字> 的形式", publisher, c.ID)
		}
		head, _, _ := strings.Cut(id, ".")
		if head != publisher {
			return nil, fmt.Errorf("插件 %s 声明了 %q：能力必须落在自己的发布者命名空间里", publisher, id)
		}
		if seen[id] {
			return nil, fmt.Errorf("插件 %s 重复声明能力 %q", publisher, id)
		}
		seen[id] = true

		class, err := classOf(c.Class)
		if err != nil {
			return nil, fmt.Errorf("插件能力 %s: %w", id, err)
		}
		permission := strings.TrimSpace(c.Permission)
		// A capability a rule cannot name is a capability nobody can revoke. Read-only ones may
		// stay unpermissioned (same floor as the shipped metadata tools).
		if class != capability.ClassReadonly && permission == "" {
			return nil, fmt.Errorf("插件能力 %s 非只读，必须声明 permission", id)
		}
		approval, err := approvalOf(c.Approval, class)
		if err != nil {
			return nil, fmt.Errorf("插件能力 %s: %w", id, err)
		}
		decl.Capability = append(decl.Capability, PluginCapabilityDeclaration{
			ID:           id,
			Title:        strings.TrimSpace(c.Title),
			Description:  strings.TrimSpace(c.Description),
			Class:        class,
			Permission:   permission,
			Approval:     approval,
			Timeout:      time.Duration(c.Timeout) * time.Second,
			ParamsSchema: c.ParamsSchema,
		})
	}
	return decl, nil
}

// resolveInsidePack turns a declared path into an existing file inside the pack, and refuses
// anything that resolves outside it.
func resolveInsidePack(packDir, declared string) (string, error) {
	declared = strings.TrimSpace(declared)
	if declared == "" {
		return "", fmt.Errorf("binary 不能为空")
	}
	if filepath.IsAbs(declared) {
		return "", fmt.Errorf("binary %q 必须是包内相对路径，不能是绝对路径", declared)
	}
	cleaned := filepath.Clean(declared)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("binary %q 逃出能力包目录", declared)
	}
	absolute := filepath.Join(packDir, cleaned)
	// Compare after resolving symlinks on the pack side too: a link inside a pack must not be a
	// door to a file the pack does not contain.
	resolvedPack, err := filepath.EvalSymlinks(packDir)
	if err != nil {
		return "", fmt.Errorf("能力包目录不可用: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("binary %q 不存在: %w", declared, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("binary %q 是目录", declared)
	}
	resolvedTarget, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("binary %q 无法解析: %w", declared, err)
	}
	if inside, err := filepath.Rel(resolvedPack, resolvedTarget); err != nil ||
		inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("binary %q 解析到能力包之外", declared)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("binary %q 没有可执行权限", declared)
	}
	return resolvedTarget, nil
}

func classOf(raw string) (capability.Class, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "readonly", "read-only", "read":
		return capability.ClassReadonly, nil
	case "", "mutating", "mutation", "write":
		return capability.ClassMutating, nil
	case "destructive":
		return capability.ClassDestructive, nil
	default:
		return "", fmt.Errorf("未知的 class %q（readonly / mutating / destructive）", raw)
	}
}

func approvalOf(raw string, class capability.Class) (capability.Approval, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "inherited":
		return capability.ApprovalInherited, nil
	case "always", "required":
		if class == capability.ClassReadonly {
			return 0, fmt.Errorf("只读能力不能强制人工审批")
		}
		return capability.ApprovalAlways, nil
	case "never", "none":
		if class != capability.ClassReadonly {
			return 0, fmt.Errorf("只有只读能力可以免除人工审批")
		}
		return capability.ApprovalNever, nil
	default:
		return 0, fmt.Errorf("未知的 approval %q（inherited / always / never）", raw)
	}
}

// SpecFor builds the capability identity for one declared entry point. The runtime is the
// plugin ABI one, so the executor routes it out of process by the same prefix rule that already
// governs recipe-plugin capabilities - there is no "plugin" branch to get wrong.
func (d *PluginUnitDeclaration) SpecFor(c PluginCapabilityDeclaration) *capability.Spec {
	return &capability.Spec{
		ID:          c.ID,
		Version:     d.Version,
		Name:        c.ID,
		Title:       c.Title,
		Description: c.Description,
		Class:       c.Class,
		Runtime:     capability.RuntimePluginAbi,
		Permission:  c.Permission,
		Approval:    c.Approval,
		Grants:      parseGrantsOrEmpty(d.Grants),
		Timeout:     c.Timeout,
		Source:      packPluginSource,
		Publisher:   d.Publisher,
		// Provenance is what the execution-path revocation stage keys on
		// (app.capability_policy.go: CheckProvenance(Publisher, ArtifactDigest)). Without the
		// digest here a revoked pack build would stay callable, because nothing else on the call
		// path knows which bytes the plugin it is starting was installed from.
		ArtifactDigest: d.BinaryDigest,
		ParamsSchema:   schemaOrNull(c.ParamsSchema),
	}
}

// parseGrantsOrEmpty re-validates the ceiling. The declaration was checked at load, so a failure
// here means the file changed on disk after install - in which case the capability must not be
// registered with no ceiling at all.
func parseGrantsOrEmpty(raw []string) []capability.CapabilityGrant {
	out := make([]capability.CapabilityGrant, 0, len(raw))
	for _, item := range raw {
		grant, err := capability.ParseGrant(item)
		if err != nil {
			return nil
		}
		out = append(out, grant)
	}
	return out
}

// schemaOrNull encodes a declared JSON Schema for the parameter surface. An empty map is "no
// schema", which the tool layer then answers with its own hand-written shape; a schema that will
// not encode is reported as none rather than as a half-built one, because a capability whose
// argument rules silently differ from the reviewed declaration is worse than an unvalidated one.
func schemaOrNull(schema map[string]any) capability.Params {
	if len(schema) == 0 {
		return nil
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	return encoded
}

// A plugin unit is the one kind that ships executable code, so the pack surface needs a home for
// its live state the way MCP declarations have one: the trust domain in the plug-in host, and the
// reviewed entry points in the capability table. Both are reached through this interface so the
// handler stays free of the host and the registry.
//
// The consent rule is the one every process-spawning kind already follows: installing a pack
// declares, it does not run. What makes a plugin unit's switch different from an MCP server's is
// that turning it on *verifies* - the running plugin is asked what it provides and the answer has
// to match the reviewed list exactly, or nothing gets registered and the unit stays off. A plugin
// that cannot be checked against what a human approved must not become callable.
type PluginProvisioner interface {
	ProvisionPackPlugin(unit plugin.Unit, decl *PluginUnitDeclaration) ([]string, error)
	DropPackPlugin(unit plugin.Unit) (int, string)
	PackPluginServed(name string) (bool, []string)
	PackPluginRuntimes() []PluginRuntimeState
}

// PluginRuntimeState is one live plug-in host instance. A platform that can run a pack's binary has
// to answer "what is running, whose is it, and under which grants" from the same screen that
// installed it; without that row the operator's only evidence about third-party processes is the
// switch they last touched.
type PluginRuntimeState struct {
	Domain    string   `json:"domain"`
	Bundle    string   `json:"bundle,omitempty"`
	Running   bool     `json:"running"`
	Restarts  int      `json:"restarts"`
	Grants    []string `json:"grants,omitempty"`
	ProxyAddr string   `json:"proxyAddr,omitempty"`
	FromPack  bool     `json:"fromPack"`
}

// pluginRuntimes is the state surface's view: nil when no host is wired, so the console says
// "nothing is running" rather than showing an empty table that might mean "not configured".
func (h *PluginHandler) pluginRuntimes() []PluginRuntimeState {
	if h.plugins == nil {
		return nil
	}
	return h.plugins.PackPluginRuntimes()
}

// checkPluginUnits validates every plugin declaration in a bundle before anything is written.
//
// Refusing the install outright, rather than installing and reporting a broken unit, is deliberate:
// a pack whose code-bearing unit cannot be loaded is a pack that will never do what its manifest
// says, and half-installing it leaves the console showing an installed thing that cannot be turned
// on for a reason nobody recorded.
func (h *PluginHandler) checkPluginUnits(bundle *plugin.Bundle) error {
	var problems []string
	for _, u := range bundle.Units {
		if u.Kind != plugin.KindPlugin {
			continue
		}
		// Validated against the bundle as it is being installed: the table does not hold it yet, and
		// the pack's own directory is the only thing a binary path can be contained in.
		if _, err := loadPluginDeclarationFrom(u, bundle.Dir); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("能力包的插件单元不可用：%s", strings.Join(problems, "；"))
	}
	return nil
}

// loadPluginDeclaration reads one installed unit's declaration, resolving its binary inside the
// pack that shipped it.
func (h *PluginHandler) loadPluginDeclaration(u plugin.Unit) (*PluginUnitDeclaration, error) {
	if u.Bundle == "" {
		// Plugin units arrive from packs only: there is no built-in plugins directory, so a
		// standalone one has no containing directory to resolve its binary against and no owner to
		// remove it for.
		return nil, fmt.Errorf("插件单元 %s 不属于任何能力包：可执行代码只能随包安装", u.ID)
	}
	packDir, ok := h.bundleDir(u.Bundle)
	if !ok {
		return nil, fmt.Errorf("插件单元 %s 的能力包 %q 已不在表里", u.ID, u.Bundle)
	}
	return loadPluginDeclarationFrom(u, packDir)
}

// loadPluginDeclarationFrom is the same read against a directory the caller already holds, used by
// the install path where the bundle has not reached the table yet.
func loadPluginDeclarationFrom(u plugin.Unit, packDir string) (*PluginUnitDeclaration, error) {
	if strings.TrimSpace(packDir) == "" {
		return nil, fmt.Errorf("插件单元 %s 的能力包没有目录", u.ID)
	}
	return LoadPluginUnitDeclaration(u.Path, packDir)
}

// bundleDir is the pack's own directory, which is the only place a pack's binary may live.
func (h *PluginHandler) bundleDir(bundleID string) (string, bool) {
	if h.table == nil {
		return "", false
	}
	b, ok := h.table.Bundle(bundleID)
	if !ok || strings.TrimSpace(b.Dir) == "" {
		return "", false
	}
	return b.Dir, true
}

// declarePluginUnits puts a freshly installed pack's plugin units into the disabled state, without
// provisioning anything. Same reasoning as installMCPDeclarations: a pack's file saying
// `enabled: true` is the author's intent, not the operator's consent to run a binary.
func (h *PluginHandler) declarePluginUnits(bundle *plugin.Bundle) (int, string) {
	var flipped int
	var message string
	for _, u := range bundle.Units {
		if u.Kind != plugin.KindPlugin {
			continue
		}
		if u.Enabled {
			off, err := h.table.SetEnabled(u.ID, false)
			if err != nil {
				message = fmt.Sprintf("%s: %v", u.ID, err)
				continue
			}
			u = off
		}
		flipped++
	}
	return flipped, message
}

// applyPluginSwitch runs the operator's decision: on means declare the domain, discover what the
// binary provides, register only if it matches the reviewed list; off means take it all back.
//
// A failure leaves the unit off rather than installed-but-lying, so the caller can report the
// reason and the console can show the switch as it actually is.
func (h *PluginHandler) applyPluginSwitch(unit plugin.Unit) ([]string, string, bool) {
	if h.plugins == nil {
		return nil, "插件宿主未接入：装配没有传入 provisioner，插件单元无法登记或运行", false
	}
	if !unit.Enabled {
		removed, message := h.plugins.DropPackPlugin(unit)
		if message != "" {
			return nil, message, removed > 0
		}
		return nil, "", true
	}
	decl, err := h.loadPluginDeclaration(unit)
	if err != nil {
		return nil, err.Error(), false
	}
	ids, err := h.plugins.ProvisionPackPlugin(unit, decl)
	if err != nil {
		return nil, err.Error(), false
	}
	return ids, "", true
}

func (h *PluginHandler) dropPluginUnits(units []plugin.Unit) (int, string) {
	var total int
	var message string
	for _, u := range units {
		if u.Kind != plugin.KindPlugin {
			continue
		}
		removed, msg := h.plugins.DropPackPlugin(u)
		total += removed
		if msg != "" {
			message = msg
		}
	}
	return total, message
}

// pluginServedState reports what the console should say about a plugin unit: a declared-but-not-
// provisioned unit is not served, and the reason names the missing step rather than leaving the
// switch to look like it did nothing.
func (h *PluginHandler) pluginServedState(u plugin.Unit) (bool, string) {
	if h.plugins == nil {
		return false, "未接入插件宿主：包声明的插件只写在能力表里，无人执行"
	}
	owned, ids := h.plugins.PackPluginServed(u.Name)
	if !owned {
		if u.Enabled {
			return false, "插件宿主未持有该信任域：启用未成功，能力未登记"
		}
		return false, "已声明、未启用：插件二进制只在能力包里，运行它是运维者的决定"
	}
	if len(ids) == 0 {
		return false, "插件已接入但没有登记任何能力"
	}
	return true, ""
}
