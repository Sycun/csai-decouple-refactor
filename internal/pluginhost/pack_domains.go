package pluginhost

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// A capability pack can ship an executable plugin, and the trust domain it runs in has to live in
// the same live service the operator's config.yaml domains live in - otherwise the console, the
// grant ceiling and the reaper would each see a different set of processes.
//
// The rules are the ones the external-MCP manager already established for pack-declared servers,
// because the collision surface is identical:
//
//   - config.yaml wins. A name the operator's file declares is refused for a pack, in both
//     directions: the pack cannot take the domain over, and the operator cannot write the pack's
//     domain away from a side that does not own it.
//   - One owner per name. Two packs claiming one domain would mean one process serving two
//     publishers' grants.
//   - Declaring is not starting. Nothing here spawns a process: instances are created lazily by
//     the first invoke or discovery call, so installing a pack does not launch its binary.
//   - Replacing a declaration closes the live instance. A pack that upgraded itself to a narrower
//     grant set must not keep running the process that was started under the wider one.

// ErrPackOwnedDomain is returned when an operator-side write targets a domain a pack declared.
var ErrPackOwnedDomain = errors.New("该插件信任域由能力包声明")

type packDomainEntry struct {
	owner string
	cfg   Config
}

// DeclarePackDomain installs one pack-declared trust domain in the live service.
func (s *Service) DeclarePackDomain(name, bundleID string, cfg Config) error {
	if s == nil {
		return ErrNotConfigured
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("pluginhost: a pack-declared domain needs a name")
	}
	if strings.TrimSpace(bundleID) == "" {
		return fmt.Errorf("pluginhost: domain %q was declared without an owning bundle", name)
	}
	// The capability ids a plugin advertises are prefixed with its publisher id, and Invoke routes
	// by that prefix, so a domain whose publisher differs from its own name would produce
	// capabilities that resolve to a different instance - or to none.
	if cfg.PluginID != "" && cfg.PluginID != name {
		return fmt.Errorf("pluginhost: domain %q cannot declare publisher %q; capability ids would route to %q", name, cfg.PluginID, cfg.PluginID)
	}
	cfg.PluginID = name
	if cfg.TrustDomain == "" {
		cfg.TrustDomain = name
	}
	if strings.TrimSpace(cfg.Binary) == "" {
		return fmt.Errorf("pluginhost: domain %q has no plugin binary", name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("pluginhost: service is closed")
	}
	if s.packDomains == nil {
		s.packDomains = map[string]packDomainEntry{}
	}
	if existing, owned := s.packDomains[name]; owned && existing.owner != bundleID {
		return fmt.Errorf("pluginhost: 信任域 %q 已由能力包 %q 声明，请先卸载该包", name, existing.owner)
	}
	if _, fileOwned := s.fileDomains[name]; fileOwned {
		return fmt.Errorf("pluginhost: 信任域 %q 已由配置文件声明，能力包不能覆盖运维者声明的域", name)
	}

	// Drop any live instance first: the process that is running was started under the previous
	// configuration's grants, environment and binary, and none of those may outlive the declaration.
	if instance, live := s.instances[name]; live {
		delete(s.instances, name)
		_ = instance.Close()
	}
	s.packDomains[name] = packDomainEntry{owner: bundleID, cfg: cfg}
	s.configs[name] = cfg
	return nil
}

// RemovePackDomain forgets a pack's declaration and stops its instance. A domain the pack does not
// own is not this method's to remove - including one config.yaml owns, which is the other half of
// "unplugging a pack cannot disable part of the platform".
func (s *Service) RemovePackDomain(name string) error {
	if s == nil {
		return ErrNotConfigured
	}
	s.mu.Lock()
	entry, owned := s.packDomains[name]
	if !owned {
		s.mu.Unlock()
		if _, fileOwned := s.fileDomains[name]; fileOwned {
			return fmt.Errorf("%w: %q 由配置文件声明，卸载能力包不会移除它", ErrPackOwnedDomain, name)
		}
		return fmt.Errorf("pluginhost: %q is not a pack-declared trust domain", name)
	}
	delete(s.packDomains, name)
	delete(s.configs, name)
	instance, live := s.instances[name]
	if live {
		delete(s.instances, name)
	}
	s.mu.Unlock()

	// Close outside the lock: shutdown joins a child process and may wait on it, and a reaper or
	// another domain's invoke must not queue behind one pack's unplug.
	if instance != nil && entry.owner != "" {
		_ = instance.Close()
	}
	return nil
}

// PackDomainOwner reports which capability pack, if any, declared this trust domain.
func (s *Service) PackDomainOwner(name string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.packDomains[name]
	if !ok {
		return "", false
	}
	return entry.owner, true
}

// PackDomainNames lists the domains packs declared, sorted, for the console and for the boot
// report that says whether a pack's plugins came back.
func (s *Service) PackDomainNames() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.packDomains))
	for name := range s.packDomains {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
