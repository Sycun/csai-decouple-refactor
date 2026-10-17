package handler

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"

	"go.uber.org/zap"
)

// The role catalog is served from the plugin table, not from the roles/ directory scan that
// config.Load performs once at start-up. That single change is what makes a role hot:
//
//	roles dir  ->  plugin.Table units (kind=role)  ->  map[string]RoleConfig  ->  settings snapshot
//	bundles   -/                                          ^
//
// Reads go through currentRoles, so a run sees whatever was last published. Writes go through
// publishRoles, so nothing mutates the map a concurrent reader is holding - which is what
// role.go used to do, unguarded, from a GET handler included.

// lookupRole resolves one role against the catalog the process is serving right now.
//
// Every run path goes through this rather than reaching for h.config.Roles, so "which roles
// exist" has one answer for the whole process instead of one per struct field - and so plugging
// a bundle in changes what the next request sees without a restart.
func lookupRole(fallback *config.Config, name string) (config.RoleConfig, bool) {
	roles := currentRoles(fallback)
	if roles == nil {
		return config.RoleConfig{}, false
	}
	role, ok := roles[name]
	if !ok {
		return config.RoleConfig{}, false
	}
	if strings.TrimSpace(role.Name) == "" {
		role.Name = name
	}
	return role, true
}

// rolesDir resolves the built-in role directory.
//
// config.Load and the old save path disagreed here: loading skipped roles entirely when
// roles_dir was empty, while creating a role wrote into "roles". That combination loses work -
// the file is written and then never read back after a restart. Both sides now resolve the same
// way, so what the API writes is what the next publish serves.
func (h *RoleHandler) rolesDir() string {
	dir := ""
	if h.config != nil {
		dir = strings.TrimSpace(h.config.RolesDir)
	}
	if dir == "" {
		dir = "roles"
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(filepath.Dir(h.configPath), dir)
}

// scanRoles reads the built-in directory into the table as local units.
//
// A role a bundle already owns is not an error: PutLocal refuses it, the bundle's copy stays
// authoritative, and the scan moves on. Anything else (unreadable directory, invalid identity)
// is returned so the caller can decide, because a half-scanned catalog is how a role silently
// disappears from the UI.
func (h *RoleHandler) scanRoles() error {
	if h.plugins == nil {
		return errors.New("role handler has no plugin table")
	}
	units, err := plugin.ScanDir(plugin.KindRole, h.rolesDir(), roleUnitNamer)
	if err != nil {
		return err
	}
	for _, u := range units {
		if err := h.plugins.PutLocal(u); err != nil {
			var conflict *plugin.ErrConflict
			if errors.As(err, &conflict) {
				h.logger.Warn("内置角色与已安装能力包同名，保留包提供的版本",
					zap.String("unit", u.ID), zap.String("bundle", conflict.Owner))
				continue
			}
			return err
		}
	}
	return nil
}

// putRoleUnit records (or refreshes) the table entry for one built-in role file.
func (h *RoleHandler) putRoleUnit(name, path string) error {
	digest, err := plugin.Digest(path)
	if err != nil {
		return fmt.Errorf("digest %s: %w", path, err)
	}
	u, err := plugin.NewUnit(plugin.KindRole, name, path)
	if err != nil {
		return err
	}
	u.Digest = digest
	u.Enabled = roleEnabledAt(path)
	if err := h.plugins.PutLocal(u); err != nil {
		var conflict *plugin.ErrConflict
		if errors.As(err, &conflict) {
			return fmt.Errorf("角色 %q 由能力包 %q 提供，请先卸载该包", name, conflict.Owner)
		}
		return err
	}
	return nil
}

// removeRoleUnit drops a built-in role from the table. Same ownership rule as putRoleUnit:
// a bundle's role is not the role API's to delete.
func (h *RoleHandler) removeRoleUnit(name string) error {
	err := h.plugins.RemoveLocal(plugin.UnitIDFor(plugin.KindRole, name))
	var conflict *plugin.ErrConflict
	if errors.As(err, &conflict) {
		return fmt.Errorf("角色 %q 由能力包 %q 提供，请先卸载该包", name, conflict.Owner)
	}
	return err
}

// publishRoles rebuilds the live catalog from every role unit in the table and installs it as a
// new configuration snapshot. Returns how many roles are now served.
//
// The map is built from scratch rather than patched, so a role that vanished from disk (or from
// a bundle) disappears from the catalog in the same step - an append-only update would keep
// serving a role whose source is gone.
func (h *RoleHandler) publishRoles() (int, error) {
	if h.plugins == nil {
		return 0, errors.New("role handler has no plugin table")
	}
	next := make(map[string]config.RoleConfig)
	for _, u := range h.plugins.Units(plugin.KindRole) {
		role, err := config.LoadRoleFromFile(u.Path)
		if err != nil {
			// Parity with config.LoadRolesFromDir, which warns and skips: one broken file
			// must not take the whole catalog down.
			h.logger.Warn("加载角色配置文件失败", zap.String("file", u.Path), zap.Error(err))
			continue
		}
		if strings.TrimSpace(role.Name) == "" {
			role.Name = u.Name
		}
		// role.Enabled is what the file says, which is what the old directory scan served;
		// u.Enabled adds the one thing the table can express that a file cannot - "this
		// bundle's role is switched off" without editing files that belong to the pack.
		role.Enabled = role.Enabled && u.Enabled
		next[role.Name] = *role
	}
	published := len(next)
	if store := settingsStore(); store != nil {
		store.Update(func(cfg *config.Config) { cfg.Roles = next })
		return published, nil
	}
	// No snapshot store (unit tests, or assembly before the store exists): the boot config is
	// what readers see, so write the freshly built map there. This is a whole-map replacement,
	// not an in-place entry edit, so even this path leaves no partially updated map behind.
	if h.config != nil {
		h.config.Roles = next
	}
	return published, nil
}

// Publish rebuilds the catalog from the current table without reintroducing detached units
// or replacing operator switches with the directory scanner's defaults.
func (h *RoleHandler) Publish() (int, error) {
	return h.publishRoles()
}

// Reload scans built-in files at startup and publishes the initial catalog.
func (h *RoleHandler) Reload() (int, error) {
	if err := h.scanRoles(); err != nil {
		return 0, err
	}
	return h.Publish()
}

// bundleOwnedRole reports whether a role identity comes from an installed bundle, which is how
// the CRUD handlers decide to refuse a mutation instead of writing a file that would then be
// shadowed by the bundle's copy.
func (h *RoleHandler) bundleOwnedRole(name string) (string, bool) {
	if h.plugins == nil {
		return "", false
	}
	u, ok := h.plugins.Unit(plugin.UnitIDFor(plugin.KindRole, name))
	if !ok || u.Bundle == "" {
		return "", false
	}
	return u.Bundle, true
}

func roleUnitNamer(kind plugin.Kind, path string) (string, error) {
	if kind != plugin.KindRole {
		return "", nil
	}
	role, err := config.LoadRoleFromFile(path)
	if err != nil {
		return "", err
	}
	return role.Name, nil
}

// roleEnabledAt reads just the enabled flag from a role file, so a unit's Enabled matches what
// the loader would report. A file that cannot be parsed reports false rather than true: the
// publish step skips it anyway, and defaulting to "enabled" would show a role in the UI that no
// run can use.
func roleEnabledAt(path string) bool {
	role, err := config.LoadRoleFromFile(path)
	if err != nil {
		return false
	}
	return role.Enabled
}
