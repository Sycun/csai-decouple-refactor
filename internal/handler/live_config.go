package handler

import (
	"sync/atomic"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/settings"
)

// liveSettings is the process's configuration snapshot, installed once during assembly.
//
// Why package level rather than a field on every handler: the role catalog is read from eight
// different handler types (and handed into internal/multiagent as a *config.Config), and giving
// each of them its own settings pointer would mean changing ten constructors plus the ~22 test
// construction points behind them, to arrive at the same thing. Resolution belongs to the
// assembly, so it is installed once and read through here.
//
// The fallback matters for correctness, not convenience: unit tests construct handlers with a
// bare *config.Config and never install a store, so currentRoles must keep serving the boot
// config in that case. internal/app has a gate that fails if the production wiring forgets to
// install the store, which is what keeps this from becoming a silent second source of truth.
var liveSettings atomic.Pointer[settings.Store]

// InstallSettingsStore makes s the live configuration source for the HTTP layer.
// Call it once, before any handler serves. A nil argument is an assembly bug rather than a
// request to clear the source, so it panics instead of quietly leaving every reader on the
// boot config; tests that need to reset use resetSettingsStoreForTest.
func InstallSettingsStore(s *settings.Store) {
	if s == nil {
		panic("InstallSettingsStore: nil settings store")
	}
	liveSettings.Store(s)
}

// resetSettingsStoreForTest puts the package back to "no store installed".
func resetSettingsStoreForTest() { liveSettings.Store(nil) }

// settingsStore returns the installed snapshot store, or nil when none is installed.
func settingsStore() *settings.Store { return liveSettings.Load() }

// currentConfig resolves the snapshot a run should use. Pass this instead of h.config to code
// that reads configuration late in a request (agent runs, workflow runs, role lookups).
func currentConfig(fallback *config.Config) *config.Config {
	if s := settingsStore(); s != nil {
		return s.Current()
	}
	return fallback
}

// currentRoles is the role catalog the process is serving right now.
func currentRoles(fallback *config.Config) map[string]config.RoleConfig {
	if s := settingsStore(); s != nil {
		return s.Current().Roles
	}
	if fallback == nil {
		return nil
	}
	return fallback.Roles
}

// mirrorLiveConfig publishes the boot config a save just wrote into the live snapshot, so a run
// started afterwards reads the change instead of boot-time values. Without this the save only
// reached config.yaml and the writer's own object: editing a channel base_url looked applied in
// the settings page while every run kept calling the old endpoint until a restart.
// With no store installed the readers fall back to this same config object, so there is nothing
// to publish.
func mirrorLiveConfig(src *config.Config) {
	if s := settingsStore(); s != nil {
		s.MirrorFrom(src)
	}
}
