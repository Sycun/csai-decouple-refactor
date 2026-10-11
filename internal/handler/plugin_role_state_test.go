package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestPluginLocalRoleSwitchSurvivesCatalogPublication(t *testing.T) {
	env := newPluginTestEnv(t, true)
	path := filepath.Join(env.roles.rolesDir(), "内置角色.yaml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true, false} {
		body := `{"enabled":false}`
		if enabled {
			body = `{"enabled":true}`
		}
		rec := env.do(t, http.MethodPost, "/api/plugins/units/role/内置角色/enabled", body)
		if rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		u, _ := env.table.Unit("role/内置角色")
		role, _ := lookupRole(env.roles.config, "内置角色")
		if u.Enabled != enabled || role.Enabled != enabled {
			t.Fatalf("want enabled=%v, table=%v catalog=%v", enabled, u.Enabled, role.Enabled)
		}
	}
	// Installing an unrelated pack also publishes roles and must preserve the switch.
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	role, _ := lookupRole(env.roles.config, "内置角色")
	if role.Enabled {
		t.Fatal("unrelated install re-enabled the local role")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("switch changed the source file")
	}
}

func TestPluginLocalRoleDetachSurvivesCatalogPublication(t *testing.T) {
	env := newPluginTestEnv(t, true)
	rec := env.do(t, http.MethodDelete, "/api/plugins/units/role/内置角色", "")
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if rec = env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if _, found := env.table.Unit("role/内置角色"); found {
		t.Fatal("detached role reappeared in table")
	}
	if _, found := lookupRole(env.roles.config, "内置角色"); found {
		t.Fatal("detached role reappeared in catalog")
	}
	if _, err := os.Stat(filepath.Join(env.roles.rolesDir(), "内置角色.yaml")); err != nil {
		t.Fatal("detach deleted source file")
	}
}

func TestRolePublishPreservesBootSwitchOverlay(t *testing.T) {
	h, _, table, _ := newRoleTestEnv(t)
	if _, err := h.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := table.SetEnabled("role/内置角色", false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Publish(); err != nil {
		t.Fatal(err)
	}
	role, _ := lookupRole(h.config, "内置角色")
	if role.Enabled {
		t.Fatal("publication erased restored switch")
	}
}

func TestPluginInstallRejectsBundleDirectorySymlinkOutsideRoot(t *testing.T) {
	env := newPluginTestEnv(t, false)
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "role.yaml"), "name: external\nenabled: true\n")
	writeTestFile(t, filepath.Join(outside, "bundle.yaml"), "id: external\nversion: '1'\nunits:\n  - kind: role\n    path: role.yaml\n")
	if err := os.MkdirAll(env.bundles, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(env.bundles, "escape")); err != nil {
		t.Skip(err)
	}
	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"escape"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("outside bundle accepted: %d %s", rec.Code, rec.Body.String())
	}
}
