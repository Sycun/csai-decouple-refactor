package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/settings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The defect this pins: the settings page writes config.yaml and the boot config object, but a
// run reads the live snapshot (configForAIChannel -> currentConfig). The snapshot is frozen the
// moment its first publish clones it, so an operator who fixed a base_url watched the file
// change while runs kept calling the old endpoint until a restart. A save must reach both.
func TestSettingsSaveReachesTheLiveSnapshot(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	writeTestFile(t, configPath, `ai:
  default_channel: main
  channels:
    main:
      provider: openai_compatible
      api_key: sk-old
      base_url: https://old.example/v1
      model: old-model
`)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load fixture config: %v", err)
	}

	store := settings.New(cfg)
	InstallSettingsStore(store)
	t.Cleanup(resetSettingsStoreForTest)

	// Boot as app.go does it: publishing the role catalog replaces the snapshot with a clone,
	// which is the moment the snapshot stops tracking writes into the boot config object.
	rolesDir := filepath.Join(dir, "roles")
	cfg.RolesDir = rolesDir
	writeTestFile(t, filepath.Join(rolesDir, "内置角色.yaml"), "name: 内置角色\ndescription: shipped\nuser_prompt: shipped\nenabled: true\n")
	roles := NewRoleHandler(cfg, configPath, zap.NewNop(), plugin.NewTable())
	if _, err := roles.Reload(); err != nil {
		t.Fatalf("boot role publish: %v", err)
	}
	store.UpdateHitl(func(hitl *config.HitlConfig) { hitl.DefaultMode = "auto" })

	h := &ConfigHandler{configPath: configPath, config: cfg, logger: zap.NewNop()}
	h.SetSettings(store)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/api/config", h.UpdateConfig)
	body := `{"ai":{"default_channel":"main","channels":{"main":{"provider":"openai_compatible","api_key":"sk-new","base_url":"https://new.example/v1","model":"new-model"}}}}`
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save returned %d: %s", rec.Code, rec.Body.String())
	}

	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "https://new.example/v1") {
		t.Fatalf("the save did not reach config.yaml:\n%s", saved)
	}

	// What a run reads.
	oa, id, ok := currentConfig(h.config).ResolveAIChannel("")
	if !ok || id != "main" {
		t.Fatalf("run path resolved channel %q (ok=%v), want main", id, ok)
	}
	if oa.BaseURL != "https://new.example/v1" {
		t.Fatalf("run path resolves base_url %q after saving https://new.example/v1: the save never reached the live snapshot, so it only works after a restart", oa.BaseURL)
	}

	// A later in-place write into the boot object (NormalizeAIProviderProfiles rewrites channel
	// entries) must not surface inside the snapshot a reader is holding.
	h.config.AI.Channels["main"] = config.AIChannelConfig{BaseURL: "https://mutated.example/v1"}
	if got := currentConfig(h.config).AI.Channels["main"].BaseURL; got != "https://new.example/v1" {
		t.Fatalf("the snapshot shares the boot object's channel map: read %q", got)
	}

	// The save must not roll back the sections the snapshot itself owns.
	if _, ok := currentRoles(h.config)["内置角色"]; !ok {
		t.Fatalf("the save rolled the published role catalog back to the boot copy")
	}
	if got := currentConfig(h.config).Hitl.DefaultMode; got != "auto" {
		t.Fatalf("the save rolled the HITL section back to %q", got)
	}
}
