package app

import (
	"context"
	"path/filepath"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/pluginhost"

	"go.uber.org/zap"
)

// buildPluginHost installs the out-of-process capability runtime from config. A
// disabled or empty section leaves the global service unset, which makes every
// capability that declares a plugin runtime fail closed at execution instead of
// silently running in-process.
func buildPluginHost(cfg *config.Config, configPath string, logger *zap.Logger) *pluginhost.Service {
	if cfg == nil || !cfg.PluginHost.Enabled {
		pluginhost.InstallService(nil)
		return nil
	}
	if len(cfg.PluginHost.Domains) == 0 {
		// Enabled with no domains is not "no host": a capability pack may declare the first trust
		// domain itself, which is the whole point of letting a pack ship code. Returning nil here
		// would make the operator's own config a prerequisite for somebody else's plug-in.
		service := pluginhost.NewService(nil, logger)
		pluginhost.InstallService(service)
		return service
	}

	domains := map[string]pluginhost.Config{}
	for name, domain := range cfg.PluginHost.Domains {
		// The host requires an absolute binary path, so a relative one is resolved
		// against the config directory rather than the process working directory.
		binary := filepath.Clean(domain.Binary)
		if !filepath.IsAbs(binary) {
			binary = filepath.Join(filepath.Dir(configPath), binary)
		}
		strict := true
		if domain.StrictEgress != nil {
			strict = *domain.StrictEgress
		}
		domains[name] = pluginhost.Config{
			PluginID:       name,
			TrustDomain:    name,
			Binary:         binary,
			Args:           domain.Args,
			WorkDir:        domain.WorkDir,
			AllowedEnvKeys: domain.InheritedEnvKeys,
			Grants:         domain.Grants,
			Approved:       approvedTuples(domain.ApprovedTuples),
			StrictEgress:   strict,
			IdleTimeout:    time.Duration(domain.IdleTimeoutSeconds) * time.Second,
			CallTimeout:    time.Duration(domain.CallTimeoutSeconds) * time.Second,
			StartTimeout:   time.Duration(domain.StartTimeoutSeconds) * time.Second,
			MaxRestarts:    domain.MaxRestarts,
		}
	}

	service := pluginhost.NewService(domains, logger)
	pluginhost.InstallService(service)
	if logger != nil {
		logger.Info("plugin host installed", zap.Strings("domains", service.DomainNames()))
	}
	return service
}

func approvedTuples(in []config.EgressTuple) []pluginhost.Tuple {
	out := make([]pluginhost.Tuple, 0, len(in))
	for _, tuple := range in {
		converted := pluginhost.Tuple{
			Host:   tuple.Host,
			Ports:  tuple.Ports,
			Method: tuple.Method,
		}
		if tuple.ValidMinutes > 0 {
			converted.Expires = time.Now().Add(time.Duration(tuple.ValidMinutes) * time.Minute)
		}
		out = append(out, converted)
	}
	return out
}

// startPluginHostReaper stops idle plugin processes so a domain that gets no calls
// does not hold an interpreter open for the life of the server.
func startPluginHostReaper(ctx context.Context, service *pluginhost.Service, logger *zap.Logger) {
	if service == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				service.Close()
				return
			case <-ticker.C:
				if reaped := service.ReapIdle(); reaped > 0 && logger != nil {
					logger.Info("stopped idle plugin hosts", zap.Int("count", reaped))
				}
			}
		}
	}()
}
