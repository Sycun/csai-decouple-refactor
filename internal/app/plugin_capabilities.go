package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/handler"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/pluginhost"

	"go.uber.org/zap"
)

// A capability pack's plugin unit becomes real here: the pack's reviewed declaration turns into a
// trust domain in the live plug-in host, the running binary is asked what it provides, and only a
// match against that declaration registers callable capabilities.
//
// This is the last mile of the plug-in design and the reason it is written as a verification
// rather than a registration. Up to now a pack could ship content of five kinds and the platform
// could execute out-of-process code only for a recipe somebody hand-wrote into config: the ABI
// carried capabilities/list, but nothing called it. Turning a pack's own binary into an addressable
// set of capabilities without that call would mean trusting a plugin's self-description for its
// class, permission and grants - the exact three fields a reviewer is supposed to own.

// discoveryTimeout bounds one handshake + one capabilities/list round trip. A plugin that cannot
// answer inside this window is refused rather than left half-declared: the switch that asked for
// verification is the operator's, and hanging the console on a wedged child would make the safe
// outcome the annoying one.
const discoveryTimeout = 30 * time.Second

type packPluginProvisioner struct {
	logger *zap.Logger
}

func newPackPluginProvisioner(logger *zap.Logger) *packPluginProvisioner {
	return &packPluginProvisioner{logger: logger}
}

// ProvisionPackPlugin declares the domain, verifies the plugin against the reviewed list, and
// registers the capability identities. Every failure path removes what it just declared, so a
// refused switch leaves no trust domain behind.
func (p *packPluginProvisioner) ProvisionPackPlugin(unit plugin.Unit, decl *handler.PluginUnitDeclaration) ([]string, error) {
	// Only a nil service means "no host at all". An enabled host with no domains yet is a live host
	// that happens to have nothing in it yet, and refusing there would mean an operator could never
	// run a pack's plugin without first hand-writing a domain for it.
	host := pluginhost.Global()
	if host == nil {
		return nil, fmt.Errorf("插件宿主未启用：config.yaml 的 plugin_host.enabled 没有打开，包里的二进制无法被声明为信任域")
	}
	if err := host.DeclarePackDomain(unit.Name, unit.Bundle, p.hostConfig(unit, decl)); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()
	provided, err := host.ListCapabilities(ctx, unit.Name, 15*time.Second)
	if err != nil {
		_ = host.RemovePackDomain(unit.Name)
		return nil, fmt.Errorf("向插件 %s 询问能力清单失败：%w", unit.Name, err)
	}

	missing, unexpected := compareCapabilitySets(decl.Names(), provided)
	if len(missing) > 0 || len(unexpected) > 0 {
		_ = host.RemovePackDomain(unit.Name)
		return nil, fmt.Errorf("插件 %s 与包里审阅过的清单不一致：缺少 %s；未经审阅 %s。能力未登记，因此该插件现在不可调用",
			unit.Name, joinOrNone(missing), joinOrNone(unexpected))
	}

	specs := make([]*capability.Spec, 0, len(decl.Capability))
	for _, c := range decl.Capability {
		spec := decl.SpecFor(c)
		if err := spec.Validate(); err != nil {
			_ = host.RemovePackDomain(unit.Name)
			_ = capability.Global().UnregisterSubset(capability.LayerPlugin, unit.Name)
			return nil, fmt.Errorf("插件能力 %s 的登记被拒绝：%w", c.ID, err)
		}
		specs = append(specs, spec)
	}
	if err := capability.Global().RegisterSubset(capability.LayerPlugin, unit.Name, specs); err != nil {
		_ = host.RemovePackDomain(unit.Name)
		return nil, fmt.Errorf("插件 %s 的能力登记失败：%w", unit.Name, err)
	}
	if p.logger != nil {
		p.logger.Info("pack plugin provisioned",
			zap.String("unit", unit.ID), zap.String("bundle", unit.Bundle), zap.Int("capabilities", len(specs)))
	}
	return provided, nil
}

// DropPackPlugin forgets one plugin unit: its capability identities first, then its trust domain.
// The order is the safe one - a capability that cannot resolve to a domain is refused at execution,
// whereas a domain without capabilities merely sits there, so unregistering first never leaves a
// window where a name is callable and unidentified.
func (p *packPluginProvisioner) DropPackPlugin(unit plugin.Unit) (int, string) {
	removed := capability.Global().UnregisterSubset(capability.LayerPlugin, unit.Name)
	if p.logger != nil && removed > 0 {
		p.logger.Info("pack plugin capabilities unregistered", zap.String("unit", unit.ID), zap.Int("count", removed))
	}
	if unit.Bundle == "" {
		return removed, ""
	}
	if err := pluginhost.Global().RemovePackDomain(unit.Name); err != nil {
		return removed, fmt.Sprintf("%s: 信任域未移除：%v", unit.ID, err)
	}
	return removed, ""
}

// PackPluginServed reports whether the live host holds this pack's domain, and which capabilities
// are currently registered for it.
func (p *packPluginProvisioner) PackPluginServed(name string) (bool, []string) {
	host := pluginhost.Global()
	if host == nil {
		return false, nil
	}
	if _, owned := host.PackDomainOwner(name); !owned {
		return false, nil
	}
	var ids []string
	for _, spec := range capability.Global().Specs() {
		if spec.Runtime == capability.RuntimePluginAbi && spec.Publisher == name {
			ids = append(ids, spec.ID)
		}
	}
	sort.Strings(ids)
	return true, ids
}

// PackPluginRuntimes reports every live plug-in instance, labelled with the pack that declared it
// when the declaration came from a pack. Sorted so the console order is stable across calls.
func (p *packPluginProvisioner) PackPluginRuntimes() []handler.PluginRuntimeState {
	host := pluginhost.Global()
	if host == nil {
		return nil
	}
	out := make([]handler.PluginRuntimeState, 0, len(host.Stats()))
	for _, stats := range host.Stats() {
		bundle, fromPack := host.PackDomainOwner(stats.PluginID)
		out = append(out, handler.PluginRuntimeState{
			Domain:    stats.PluginID,
			Bundle:    bundle,
			Running:   stats.Running,
			Restarts:  stats.Restarts,
			Grants:    stats.Grants,
			ProxyAddr: stats.ProxyAddr,
			FromPack:  fromPack,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

func (p *packPluginProvisioner) hostConfig(unit plugin.Unit, decl *handler.PluginUnitDeclaration) pluginhost.Config {
	return pluginhost.Config{
		PluginID:       unit.Name,
		TrustDomain:    unit.Name,
		Binary:         decl.Binary,
		Args:           decl.Args,
		AllowedEnvKeys: decl.EnvAllow,
		Grants:         decl.Grants,
		// Strict egress is the shipped default for code that arrived in a pack, and there is no
		// field to loosen it: the operator's approved tuples live in config.yaml, and a pack that
		// could set strict:false would be granting itself network.
		StrictEgress: true,
		CallTimeout:  decl.CallExec,
		IdleTimeout:  decl.CallIdle,
		MaxRestarts:  decl.Restarts,
	}
}

// compareCapabilitySets is the cross-check, in both directions.
//
// Missing entries mean the pack ships an entry point the binary does not provide, so the console
// would advertise a capability that fails at call time. Extra ones mean the binary offers something
// no reviewer wrote down - and since class, permission and grants come from the manifest, an
// unreviewed capability has no defensible values for any of them. Both refuse the whole unit rather
// than registering the part that happens to agree.
func compareCapabilitySets(reviewed, provided []string) (missing, unexpected []string) {
	inReviewed := map[string]bool{}
	for _, id := range reviewed {
		inReviewed[id] = true
	}
	inProvided := map[string]bool{}
	for _, id := range provided {
		inProvided[id] = true
	}
	for _, id := range reviewed {
		if !inProvided[id] {
			missing = append(missing, id)
		}
	}
	for _, id := range provided {
		if !inReviewed[id] {
			unexpected = append(unexpected, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	return missing, unexpected
}

func joinOrNone(ids []string) string {
	if len(ids) == 0 {
		return "（无）"
	}
	return strings.Join(ids, ", ")
}
