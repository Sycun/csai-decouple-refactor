package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/settings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// These tests cover the one capability kind that could not change while the process ran.
// Roles used to be parsed once inside config.Load and then mutated in place by this package's
// own HTTP handlers, with no lock, while eight other files read the same map.

func newRoleTestEnv(t *testing.T) (*RoleHandler, *settings.Store, *plugin.Table, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	rolesDir := filepath.Join(dir, "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(rolesDir, "内置角色.yaml"), "name: 内置角色\ndescription: shipped\nuser_prompt: shipped prompt\nenabled: true\n")

	cfg := &config.Config{RolesDir: rolesDir, Roles: map[string]config.RoleConfig{}}
	store := settings.New(cfg)
	InstallSettingsStore(store)
	t.Cleanup(resetSettingsStoreForTest)

	table := plugin.NewTable()
	h := NewRoleHandler(cfg, configPath, zap.NewNop(), table, nil)
	return h, store, table, dir
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// callRoleEndpoint drives the role HTTP handlers through a real router, so the path parameter
// binding (and therefore the response codes the endpoints actually return) is what gets tested.
func callRoleEndpoint(t *testing.T, h *RoleHandler, method, target, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	switch target {
	case "create":
		r.POST("/api/roles", h.CreateRole)
	case "update":
		r.PUT("/api/roles/:name", h.UpdateRole)
	case "delete":
		r.DELETE("/api/roles/:name", h.DeleteRole)
	case "get":
		r.GET("/api/roles", h.GetRoles)
	default:
		t.Fatalf("unknown role endpoint target %q", target)
	}
	path := "/api/roles"
	if name != "" {
		path += "/" + name
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestBootPublishServesBuiltInRoles(t *testing.T) {
	h, store, _, _ := newRoleTestEnv(t)
	if store.Current().Roles != nil && len(store.Current().Roles) != 0 {
		t.Fatalf("snapshot should start empty before the first publish")
	}
	published, err := h.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if published != 1 {
		t.Fatalf("published %d roles, want 1", published)
	}
	role, ok := lookupRole(h.config, "内置角色")
	if !ok || !role.Enabled || role.UserPrompt != "shipped prompt" {
		t.Fatalf("built-in role not served: %#v", role)
	}
	// The boot config must not have been written: publishing replaces the snapshot only.
	if len(h.config.Roles) != 0 {
		t.Fatalf("Reload wrote back into the boot config: %v", h.config.Roles)
	}
}

// TestBundledRoleIsServedWithoutARestart is the hot-plug claim in one test: install a pack,
// and the very next lookup finds its role; unplug it and it is gone again.
func TestBundledRoleIsServedWithoutARestart(t *testing.T) {
	h, _, table, dir := newRoleTestEnv(t)
	if _, err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	bundleDir := filepath.Join(dir, "bundles", "pack")
	writeTestFile(t, filepath.Join(bundleDir, "roles", "打包角色.yaml"), "name: 打包角色\ndescription: from a pack\nuser_prompt: pack prompt\nenabled: true\n")
	writeTestFile(t, filepath.Join(bundleDir, plugin.ManifestFileName), "id: pack\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/打包角色.yaml\n")

	m, err := plugin.LoadManifestDir(bundleDir)
	if err != nil {
		t.Fatalf("LoadManifestDir: %v", err)
	}
	b, err := m.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := table.InstallBundle(b); err != nil {
		t.Fatalf("InstallBundle: %v", err)
	}
	if _, err := h.publishRoles(); err != nil {
		t.Fatalf("publishRoles: %v", err)
	}

	role, ok := lookupRole(h.config, "打包角色")
	if !ok {
		t.Fatalf("bundled role not served after install")
	}
	if role.UserPrompt != "pack prompt" || !role.Enabled {
		t.Fatalf("bundled role served wrong: %#v", role)
	}
	if _, ok := lookupRole(h.config, "内置角色"); !ok {
		t.Fatalf("installing a pack dropped a built-in role")
	}

	if err := table.UninstallBundle("pack"); err != nil {
		t.Fatalf("UninstallBundle: %v", err)
	}
	if _, err := h.publishRoles(); err != nil {
		t.Fatalf("publishRoles after unplug: %v", err)
	}
	if _, ok := lookupRole(h.config, "打包角色"); ok {
		t.Fatalf("unplugged role is still served")
	}
	if _, ok := lookupRole(h.config, "内置角色"); !ok {
		t.Fatalf("unplugging removed a built-in role")
	}
}

// TestRoleAPICannotClobberABundledRole: the table refuses the identity, and the HTTP layer has
// to surface that as a conflict rather than writing a file that the bundle would shadow.
func TestRoleAPICannotClobberABundledRole(t *testing.T) {
	h, _, table, dir := newRoleTestEnv(t)
	bundleDir := filepath.Join(dir, "bundles", "pack")
	writeTestFile(t, filepath.Join(bundleDir, "roles", "打包角色.yaml"), "name: 打包角色\nuser_prompt: pack\nenabled: true\n")
	writeTestFile(t, filepath.Join(bundleDir, plugin.ManifestFileName), "id: pack\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/打包角色.yaml\n")
	m, err := plugin.LoadManifestDir(bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := table.InstallBundle(b); err != nil {
		t.Fatalf("InstallBundle: %v", err)
	}
	if _, err := h.publishRoles(); err != nil {
		t.Fatalf("publishRoles: %v", err)
	}

	rec := callRoleEndpoint(t, h, http.MethodPost, "create", "", `{"name":"打包角色","user_prompt":"mine","enabled":true}`)
	if rec.Code != http.StatusConflict && rec.Code != http.StatusBadRequest {
		t.Fatalf("creating over a bundled role returned %d, want 409/400: %s", rec.Code, rec.Body.String())
	}
	if _, statErr := os.Stat(filepath.Join(h.rolesDir(), "打包角色.yaml")); statErr == nil {
		t.Fatalf("the refused create still wrote a file into the built-in roles dir")
	}

	rec = callRoleEndpoint(t, h, http.MethodDelete, "delete", "打包角色", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("deleting a bundled role returned %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if _, ok := table.Unit(plugin.UnitIDFor(plugin.KindRole, "打包角色")); !ok {
		t.Fatalf("the refused delete still removed the unit")
	}
	if _, err := os.Stat(filepath.Join(bundleDir, "roles", "打包角色.yaml")); err != nil {
		t.Fatalf("the refused delete touched the bundle's own file: %v", err)
	}
}

// TestRoleCreateUpdateDeleteRoundTrip is the ordinary path, now going through file + unit +
// snapshot instead of mutating a shared map.
func TestRoleCreateUpdateDeleteRoundTrip(t *testing.T) {
	h, store, _, dir := newRoleTestEnv(t)
	if _, err := h.Reload(); err != nil {
		t.Fatal(err)
	}

	rec := callRoleEndpoint(t, h, http.MethodPost, "create", "", `{"name":"新建角色","description":"d1","user_prompt":"p1","enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(h.rolesDir(), "新建角色.yaml")); err != nil {
		t.Fatalf("created role has no file: %v", err)
	}
	got, ok := lookupRole(h.config, "新建角色")
	if !ok || got.UserPrompt != "p1" {
		t.Fatalf("created role not served: %#v", got)
	}

	rec = callRoleEndpoint(t, h, http.MethodPut, "update", "新建角色", `{"name":"新建角色","description":"d2","user_prompt":"p2","enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update returned %d: %s", rec.Code, rec.Body.String())
	}
	got, _ = lookupRole(h.config, "新建角色")
	if got.Description != "d2" || got.UserPrompt != "p2" {
		t.Fatalf("update not served: %#v", got)
	}

	rec = callRoleEndpoint(t, h, http.MethodGet, "get", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list returned %d", rec.Code)
	}
	var payload struct {
		Roles []config.RoleConfig `json:"roles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("list payload: %v", err)
	}
	if len(payload.Roles) != 2 {
		t.Fatalf("list returned %d roles, want 2: %v", len(payload.Roles), payload.Roles)
	}
	if payload.Roles[0].Name > payload.Roles[1].Name {
		t.Errorf("list is not sorted by name: %q then %q", payload.Roles[0].Name, payload.Roles[1].Name)
	}

	rec = callRoleEndpoint(t, h, http.MethodDelete, "delete", "新建角色", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := lookupRole(h.config, "新建角色"); ok {
		t.Fatalf("deleted role still served")
	}
	if _, err := os.Stat(filepath.Join(h.rolesDir(), "新建角色.yaml")); !os.IsNotExist(err) {
		t.Fatalf("deleted role's file is still on disk: %v", err)
	}
	// Renaming onto an existing identity is refused, not merged into it.
	writeTestFile(t, filepath.Join(dir, "unused.txt"), "x")
	rec = callRoleEndpoint(t, h, http.MethodPut, "update", "内置角色", `{"name":"CTF","enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename to a free name returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := lookupRole(h.config, "内置角色"); ok {
		t.Fatalf("rename left the old identity served")
	}
	if store.Current().Roles == nil {
		t.Fatalf("snapshot lost its roles map")
	}
}

// TestConcurrentPublishAndRoleReadsNeverTear: publishing replaces the map, so a reader can
// never observe a half-built catalog. The old code assigned into the live map, which the race
// detector reports the moment a GET and a write overlap.
func TestConcurrentPublishAndRoleReadsNeverTear(t *testing.T) {
	h, _, table, dir := newRoleTestEnv(t)
	dirs := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		bundleDir := filepath.Join(dir, "bundles", "pack"+string(rune('a'+i)))
		name := "角色" + string(rune('a'+i))
		writeTestFile(t, filepath.Join(bundleDir, "roles", name+".yaml"), "name: "+name+"\nuser_prompt: p\nenabled: true\n")
		writeTestFile(t, filepath.Join(bundleDir, plugin.ManifestFileName), "id: pack"+string(rune('a'+i))+"\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/"+name+".yaml\n")
		dirs = append(dirs, bundleDir)
	}
	if _, err := h.Reload(); err != nil {
		t.Fatal(err)
	}

	const rounds = 120
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < rounds; n++ {
				role, ok := lookupRole(h.config, "内置角色")
				if !ok || !role.Enabled {
					t.Errorf("the shipped role vanished from a concurrent read")
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < rounds; n++ {
			bundleDir := dirs[n%len(dirs)]
			m, err := plugin.LoadManifestDir(bundleDir)
			if err != nil {
				t.Errorf("manifest: %v", err)
				return
			}
			b, err := m.Resolve()
			if err != nil {
				t.Errorf("resolve: %v", err)
				return
			}
			if err := table.InstallBundle(b); err != nil {
				t.Errorf("install: %v", err)
				return
			}
			if _, err := h.publishRoles(); err != nil {
				t.Errorf("publish: %v", err)
				return
			}
			if err := table.UninstallBundle(m.ID); err != nil {
				t.Errorf("uninstall: %v", err)
				return
			}
			if _, err := h.publishRoles(); err != nil {
				t.Errorf("publish: %v", err)
				return
			}
			select {
			case <-stop:
				return
			default:
			}
		}
		close(stop)
	}()
	wg.Wait()
	if _, ok := lookupRole(h.config, "内置角色"); !ok {
		t.Fatalf("the shipped role did not survive the churn")
	}
}
