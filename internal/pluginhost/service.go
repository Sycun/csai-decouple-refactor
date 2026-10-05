package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Service holds one instance per trust domain. Publishers are isolated from each
// other by process, so a plugin from "acme" cannot see the file descriptors,
// environment or crash state of one from "other-vendor".
type Service struct {
	mu        sync.RWMutex
	configs   map[string]Config
	instances map[string]*Instance
	logger    *zap.Logger
	closed    bool
}

// NewService prepares the domain table without starting anything.
func NewService(configs map[string]Config, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	domains := map[string]Config{}
	for name, cfg := range configs {
		if cfg.PluginID == "" {
			cfg.PluginID = name
		}
		if cfg.TrustDomain == "" {
			cfg.TrustDomain = name
		}
		domains[name] = cfg
	}
	return &Service{configs: domains, instances: map[string]*Instance{}, logger: logger}
}

// Enabled reports whether any trust domain is configured. A capability that asks
// for plugin-host execution while this is false must be refused, not run
// in-process.
func (s *Service) Enabled() bool { return s != nil && len(s.configs) > 0 }

// DomainNames lists configured trust domains, sorted.
func (s *Service) DomainNames() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.configs))
	for name := range s.configs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Invoke routes one capability call to the domain that owns it. The domain comes
// from the capability identity, so a manifest cannot redirect its execution into a
// better-privileged instance.
func (s *Service) Invoke(ctx context.Context, domain, capabilityID string, params json.RawMessage, timeout time.Duration) (*InvokeResult, error) {
	if s == nil {
		return nil, ErrNotConfigured
	}
	instance, err := s.instanceFor(domain)
	if err != nil {
		return nil, err
	}
	return instance.Invoke(ctx, capabilityID, params, timeout)
}

// ReapIdle stops instances that have been unused past their idle timeout, so a
// long-lived server does not accumulate interpreter processes.
func (s *Service) ReapIdle() int {
	if s == nil {
		return 0
	}
	now := time.Now()
	s.mu.RLock()
	type entry struct {
		domain   string
		instance *Instance
	}
	entries := make([]entry, 0, len(s.instances))
	for domain, instance := range s.instances {
		entries = append(entries, entry{domain, instance})
	}
	s.mu.RUnlock()

	reaped := 0
	for _, item := range entries {
		if !item.instance.Running() {
			continue
		}
		cfg, ok := s.configs[item.domain]
		if !ok {
			continue
		}
		if item.instance.IdleSince(now) <= cfg.IdleTimeout {
			continue
		}
		item.instance.stop(errors.New("idle timeout"))
		reaped++
	}
	return reaped
}

// Stats returns one row per live instance for the monitor surface.
func (s *Service) Stats() []Stats {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Stats, 0, len(s.instances))
	for _, instance := range s.instances {
		out = append(out, instance.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PluginID < out[j].PluginID })
	return out
}

// Close stops every instance.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	instances := make([]*Instance, 0, len(s.instances))
	for _, instance := range s.instances {
		instances = append(instances, instance)
	}
	s.mu.Unlock()

	for _, instance := range instances {
		_ = instance.Close()
	}
}

var (
	globalMu      sync.RWMutex
	globalService *Service
)

// InstallService publishes the process-wide host used by execution paths that do
// not receive it by injection. Exactly one is installed, from app assembly.
func InstallService(s *Service) {
	globalMu.Lock()
	globalService = s
	globalMu.Unlock()
}

// Global returns the installed service, or nil when plugin execution is off.
func Global() *Service {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalService
}

// DomainForTrust derives the trust domain from a capability identity: the
// publisher is the trust boundary.
func DomainForTrust(capabilityID string) string {
	parts := strings.Split(strings.TrimSpace(capabilityID), ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// IsPluginRuntime reports whether a runtime string asks for out-of-process plugin
// execution rather than a recipe or in-process handler.
func IsPluginRuntime(runtime string) bool {
	return strings.HasPrefix(strings.TrimSpace(runtime), "plugin-host:")
}
