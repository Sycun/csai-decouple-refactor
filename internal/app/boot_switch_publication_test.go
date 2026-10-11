package app

import (
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/handler"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

func TestBootSwitchPublicationSurvivesUnrelatedCleanupFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles", "disabled.yaml")
	writeFileAt(t, path, "name: disabled\nenabled: true\nuser_prompt: test\n")
	cfg := &config.Config{RolesDir: "roles"}
	table := plugin.NewTable()
	roles := handler.NewRoleHandler(cfg, filepath.Join(dir, "config.yaml"), zap.NewNop(), table, nil)
	if _, err := roles.Reload(); err != nil {
		t.Fatal(err)
	}
	if !cfg.Roles["disabled"].Enabled {
		t.Fatal("initial publication did not enable the fixture")
	}
	db := openBootTestDB(t)
	switches := store.NewCapabilitySwitches(db)
	if err := switches.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	for id, source := range map[string]string{"role/disabled": path, "role/stale": filepath.Join(dir, "roles", "stale.yaml")} {
		if err := switches.Record(id, source, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TRIGGER deny_switch_delete BEFORE DELETE ON capability_unit_switches BEGIN SELECT RAISE(FAIL, 'cleanup unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if applied := restoreSwitchCatalog(table, switches, roles, zap.NewNop()); applied != 1 {
		t.Fatalf("restored %d switches, want 1", applied)
	}
	if cfg.Roles["disabled"].Enabled {
		t.Fatal("cleanup failure kept serving a role the operator disabled")
	}
	rows, err := switches.All()
	if err != nil || len(rows) != 2 {
		t.Fatalf("cleanup trigger was not exercised: rows=%v err=%v", rows, err)
	}
}
