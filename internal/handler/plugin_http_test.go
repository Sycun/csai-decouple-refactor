package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The HTTP contract of "one click to extend": install by name, see it served immediately, unplug
// it, and get an honest served/not-served flag per unit.

type pluginTestEnv struct {
	plugins *PluginHandler
	roles   *RoleHandler
	tools   *recordingToolLayer
	mcp     *recordingMCP
	// pluginsProvisioner stands in for the live plug-in host + capability table. It is nil in the
	// shared environment because most tests never touch a plugin unit; a plugin test installs its
	// own recording provisioner and rebuilds the handler with it.
	pluginsProvisioner PluginProvisioner
	switches           *recordingSwitches
	installs           *recordingInstalls
	trust              capabilityTrust
	table              *plugin.Table
	bundles            string
	recorder           *httptest.ResponseRecorder
}

// recordingMCP stands in for the external MCP manager so the test can see what a pack declared,
// with which enable state, under which owner, and what an unplug took away.
type recordingMCP struct {
	configs map[string]config.ExternalMCPServerConfig
	owners  map[string]string
	adds    []string
	removes []string
}

// recordingToolLayer stands in for the tool-surface rebuild so the test can see whether the
// plug-in surface asked for one, and for which pack.
type recordingToolLayer struct {
	calls int
	err   error
}

func (r *recordingMCP) GetConfigs() map[string]config.ExternalMCPServerConfig {
	out := make(map[string]config.ExternalMCPServerConfig, len(r.configs))
	for k, v := range r.configs {
		out[k] = v
	}
	return out
}

func (r *recordingMCP) DeclarePackServer(name, bundleID string, serverCfg config.ExternalMCPServerConfig) error {
	if name == "" || bundleID == "" {
		return fmt.Errorf("recordingMCP: declaration needs both a name and an owning bundle")
	}
	r.configs[name] = serverCfg
	r.owners[name] = bundleID
	r.adds = append(r.adds, name)
	return nil
}

func (r *recordingMCP) RemovePackServer(name string) error {
	if _, owned := r.owners[name]; !owned {
		// The real manager refuses too, but there the pack bookkeeping is what made it owned; an
		// unowned removal here means the test asked the plug-in surface to delete a server it
		// never declared.
		return fmt.Errorf("recordingMCP: %q is not a pack declaration", name)
	}
	delete(r.owners, name)
	delete(r.configs, name)
	r.removes = append(r.removes, name)
	return nil
}

func (r *recordingMCP) PackOwner(name string) (string, bool) {
	owner, ok := r.owners[name]
	return owner, ok
}

func (r *recordingToolLayer) Rebuild() error {
	r.calls++
	return r.err
}

// recordingSwitches stands in for the capability switch store so a test can see exactly which
// decisions were written outside the process, and which identities were forgotten.
type recordingSwitches struct {
	recorded map[string]bool
	forgot   []string
	err      error
}

func (r *recordingSwitches) Record(unitID, path string, enabled bool) error {
	if r.err != nil {
		return r.err
	}
	if path == "" {
		return errors.New("recordingSwitches: a switch needs the source path it was about")
	}
	if r.recorded == nil {
		r.recorded = map[string]bool{}
	}
	r.recorded[unitID] = enabled
	return nil
}

func (r *recordingSwitches) Forget(unitIDs ...string) error {
	if r.err != nil {
		return r.err
	}
	r.forgot = append(r.forgot, unitIDs...)
	return nil
}

// recordingInstalls stands in for the install store so a test can see which packs the click
// recorded (with which version and which unit selection) and which were taken back - the facts
// start-up replays.
type recordingInstalls struct {
	recorded map[string]string
	// selections holds the unit selection of the last record per pack; nil means "the whole pack"
	// and a missing key means nothing was recorded at all.
	selections map[string][]string
	forgot     []string
	err        error
}

func (r *recordingInstalls) Record(bundleID, version string, units []string) error {
	if r.err != nil {
		return r.err
	}
	if version == "" {
		return errors.New("recordingInstalls: an install needs the version it installed")
	}
	if r.recorded == nil {
		r.recorded = map[string]string{}
	}
	if r.selections == nil {
		r.selections = map[string][]string{}
	}
	r.recorded[bundleID] = version
	r.selections[bundleID] = units
	return nil
}

func (r *recordingInstalls) Forget(bundleID string) error {
	if r.err != nil {
		return r.err
	}
	r.forgot = append(r.forgot, bundleID)
	return nil
}

func newPluginTestEnv(t *testing.T, withBuiltInBundle bool) *pluginTestEnv {
	t.Helper()
	roles, _, table, dir := newRoleTestEnv(t)
	bundlesDir := filepath.Join(dir, "bundles")
	env := &pluginTestEnv{
		roles:   roles,
		table:   table,
		bundles: bundlesDir,
		tools:   &recordingToolLayer{},
		// Pre-initialised because a test has to be able to write an operator-declared server in
		// before any handler code runs.
		mcp: &recordingMCP{
			configs: map[string]config.ExternalMCPServerConfig{},
			owners:  map[string]string{},
		},
		switches: &recordingSwitches{},
		installs: &recordingInstalls{},
	}
	env.plugins = NewPluginHandler(table, bundlesDir, roles, env.tools, env.mcp, env.pluginsProvisioner, env.switches, env.installs, env.trust, nil, zap.NewNop())

	if withBuiltInBundle {
		// The real example pack, copied next to the test config so the install path is exercised
		// with a manifest somebody else would actually ship.
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", "roles", "报告撰写.yaml"),
			"name: 报告撰写\ndescription: 交付视角\nuser_prompt: 以交付视角撰写\nenabled: true\n")
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", "skills", "finding-writeup", "SKILL.md"),
			"---\nname: finding-writeup\ndescription: 漏洞报告撰写\n---\n\n## Format\n")
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", "tools", "pandoc.yaml"),
			"name: pandoc\ncommand: /bin/true\n")
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", "agents", "report-analyst.md"),
			"---\ndescription: 报告分析子代理\n---\n\n# 报告分析\n\n汇总发现并成稿。\n")
		writeTestFile(t, filepath.Join(bundlesDir, "reporting-pack", plugin.ManifestFileName),
			"id: reporting-pack\nname: 报告角色包\nversion: 1.0.0\ndescription: role+agent+skill+tool\nunits:\n  - kind: role\n    path: roles/报告撰写.yaml\n  - kind: agent\n    path: agents/report-analyst.md\n  - kind: skill\n    path: skills/finding-writeup\n  - kind: tool\n    path: tools/pandoc.yaml\n")
	}
	return env
}

func (e *pluginTestEnv) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/plugins", e.plugins.GetState)
	r.GET("/api/plugins/available", e.plugins.ListAvailable)
	r.POST("/api/plugins/install", e.plugins.Install)
	r.DELETE("/api/plugins/bundles/:id", e.plugins.Uninstall)
	r.POST("/api/plugins/units/:kind/:name/enabled", e.plugins.EnableUnit)
	r.DELETE("/api/plugins/units/:kind/:name", e.plugins.RemoveLocalUnit)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	e.recorder = rec
	return rec
}

func decodeState(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("payload is not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return out
}

func TestPluginInstallServesTheBundleImmediately(t *testing.T) {
	env := newPluginTestEnv(t, true)
	if _, err := env.roles.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	rec := env.do(t, http.MethodGet, "/api/plugins", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/plugins returned %d: %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if len(state["bundles"].([]interface{})) != 0 {
		t.Fatalf("a bundle is installed before the install call")
	}

	rec = env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install returned %d: %s", rec.Code, rec.Body.String())
	}
	installed := decodeState(t, rec)
	if installed["refreshed"] != true {
		t.Fatalf("install did not refresh the served catalog: %v", installed)
	}
	if roles, _ := installed["roles"].(float64); roles < 2 {
		t.Fatalf("roles after install = %v, want the shipped role plus the bundled one", installed["roles"])
	}
	// The click is the decision the next start-up replays, so it has to be recorded with the pack
	// - not left to a later scan of the directory.
	if installed["install_recorded"] != true {
		t.Fatalf("install did not report a recorded decision: %v", installed)
	}
	if got := env.installs.recorded["reporting-pack"]; got != "1.0.0" {
		t.Fatalf("the install decision was not written with the pack's version: %q", got)
	}

	// The role is live for a run, not just recorded in the table.
	if _, ok := lookupRole(env.roles.config, "报告撰写"); !ok {
		t.Fatalf("bundled role not served after the HTTP install")
	}
	if _, ok := lookupRole(env.roles.config, "内置角色"); !ok {
		t.Fatalf("installing a pack removed a shipped role")
	}
	// Asserted here as present so the same id asserted as absent after the uninstall below
	// cannot be satisfied by an agent unit that was never registered in the first place.
	if _, ok := env.table.Unit("agent/report-analyst"); !ok {
		t.Fatalf("install did not register the bundled agent unit: %v", env.table.Units(plugin.KindAgent))
	}

	rec = env.do(t, http.MethodGet, "/api/plugins", "")
	state = decodeState(t, rec)
	bundles := state["bundles"].([]interface{})
	if len(bundles) != 1 {
		t.Fatalf("bundles = %v", state["bundles"])
	}
	bundle := bundles[0].(map[string]interface{})
	if bundle["id"] != "reporting-pack" || bundle["version"] != "1.0.0" {
		t.Fatalf("bundle view wrong: %v", bundle)
	}
	units := bundle["units"].([]interface{})
	if len(units) != 4 {
		t.Fatalf("bundle reports %d units, want 4", len(units))
	}
	servedByKind := map[string]bool{}
	reasonByKind := map[string]string{}
	for _, raw := range units {
		u := raw.(map[string]interface{})
		servedByKind[u["kind"].(string)] = u["served"].(bool)
		if r, ok := u["reason"].(string); ok {
			reasonByKind[u["kind"].(string)] = r
		}
	}
	if !servedByKind["role"] || !servedByKind["skill"] {
		t.Errorf("role/skill must report served: %v", servedByKind)
	}
	// The agent row is the one this clause caught being claimed wrong: the live server said
	// agent/served=false after the run path had already moved onto the table, and no assertion
	// covered it because the fixture pack had no agent unit.
	if !servedByKind["agent"] {
		t.Errorf("agent must report served: the run path loads agent paths from the table")
	}
	if reasonByKind["agent"] != "" {
		t.Errorf("a served unit carries a not-served reason: %q", reasonByKind["agent"])
	}
	if !servedByKind["tool"] {
		t.Errorf("tool must report served: the recipe list is rebuilt from the table, %v", servedByKind)
	}
	// The pack declares a tool unit, so installing it must rebuild the recipe layer and the MCP
	// tool surface - the same sequence POST /config/apply runs.
	if !installed["tools_rebuilt"].(bool) {
		t.Errorf("installing a pack with a tool recipe did not rebuild the tool layer: %v", installed)
	}
	if env.tools.calls != 1 {
		t.Fatalf("tool-layer rebuild calls after install = %d, want 1", env.tools.calls)
	}
	// The honesty clause that is left: an MCP declaration is tracked and listed, but the live path
	// for external servers is the MCP manager, not this table. See
	// TestEveryKindReportsItsActualServedState for the per-kind verdict.

	// Unplug: both the table and the served catalog go back.
	rec = env.do(t, http.MethodDelete, "/api/plugins/bundles/reporting-pack", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall returned %d: %s", rec.Code, rec.Body.String())
	}
	if env.tools.calls != 2 {
		t.Fatalf("tool-layer rebuild calls after uninstall = %d, want 2 (the pack's recipe must stop "+
			"being executable, not just leave the table)", env.tools.calls)
	}
	uninstalled := decodeState(t, rec)
	if got := uninstalled["tools_rebuilt"]; got != true {
		t.Fatalf("uninstall did not report a tool-layer rebuild: %v", got)
	}
	// And the record goes with the pack: a row left behind would re-install it at the next boot.
	if uninstalled["install_forgotten"] != true {
		t.Fatalf("uninstall did not report the install record taken back: %v", uninstalled)
	}
	if len(env.installs.forgot) != 1 || env.installs.forgot[0] != "reporting-pack" {
		t.Fatalf("the install record was not forgotten with the pack: %v", env.installs.forgot)
	}
	if _, ok := lookupRole(env.roles.config, "报告撰写"); ok {
		t.Fatalf("unplugged role is still served")
	}
	if _, ok := lookupRole(env.roles.config, "内置角色"); !ok {
		t.Fatalf("uninstall took a shipped role with it")
	}
	if _, ok := env.table.Unit("agent/report-analyst"); ok {
		t.Fatalf("uninstall left the bundled agent unit in the table, so a run would still load it")
	}
	if _, err := plugin.Digest(filepath.Join(env.bundles, "reporting-pack", "roles", "报告撰写.yaml")); err != nil {
		t.Fatalf("uninstall deleted the pack's own file: %v", err)
	}
}

func TestPluginInstallRejectsPathsOutsideTheBundlesRoot(t *testing.T) {
	env := newPluginTestEnv(t, true)
	outside := filepath.Join(filepath.Dir(env.bundles), "..", "elsewhere")
	writeTestFile(t, filepath.Join(outside, "escape", plugin.ManifestFileName),
		"id: escape\nversion: 1.0.0\nunits:\n  - kind: role\n    path: r.yaml\n")
	writeTestFile(t, filepath.Join(outside, "escape", "r.yaml"), "name: escape\nenabled: true\n")

	for _, ref := range []string{"../../etc", "/etc", outside + "/escape", ""} {
		rec := env.do(t, http.MethodPost, "/api/plugins/install", fmt.Sprintf(`{"bundle":%q}`, ref))
		if ref == "" {
			if rec.Code == http.StatusOK {
				t.Fatalf("an empty bundle reference installed something")
			}
			continue
		}
		if rec.Code == http.StatusOK {
			t.Fatalf("install accepted a reference outside the bundles root: %q -> %s", ref, rec.Body.String())
		}
	}
	if len(env.table.Bundles()) != 0 {
		t.Fatalf("a refused install still landed in the table")
	}
}

func TestPluginInstallConflictNamesTheOwner(t *testing.T) {
	env := newPluginTestEnv(t, true)
	// A second pack claiming the same role identity as the first.
	writeTestFile(t, filepath.Join(env.bundles, "rival", "roles", "报告撰写.yaml"), "name: 报告撰写\nenabled: true\n")
	writeTestFile(t, filepath.Join(env.bundles, "rival", plugin.ManifestFileName),
		"id: rival\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/报告撰写.yaml\n")

	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("first install failed: %s", rec.Body.String())
	}
	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"rival"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("rival install returned %d, want 409: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "reporting-pack") {
		t.Fatalf("the conflict does not name the owning bundle: %s", body)
	}
	// The winner keeps serving; the loser changed nothing.
	if _, ok := lookupRole(env.roles.config, "报告撰写"); !ok {
		t.Fatalf("a refused install disturbed the served catalog")
	}
	if _, ok := env.table.Bundle("rival"); ok {
		t.Fatalf("the refused bundle is recorded as installed")
	}
}

func TestPluginEnableSwitchIsServedWithoutMovingFiles(t *testing.T) {
	env := newPluginTestEnv(t, true)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	path := filepath.Join(env.bundles, "reporting-pack", "roles", "报告撰写.yaml")
	before, err := plugin.Digest(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := env.table.SetEnabled("role/报告撰写", true); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, http.MethodPost, "/api/plugins/units/role/报告撰写/enabled", `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := lookupRole(env.roles.config, "报告撰写"); !ok {
		t.Fatalf("a disabled role vanished from the listing instead of serving as disabled")
	}
	role, _ := lookupRole(env.roles.config, "报告撰写")
	if role.Enabled {
		t.Fatalf("the run path still treats the disabled role as enabled")
	}
	// The console reads the same switch through the bundle view. A bundle keeps its own copy of
	// its units, so if the view did not re-resolve them the row would stay "enabled" while the run
	// path had already stopped serving it - the exact disagreement a real click produced.
	rec = env.do(t, http.MethodGet, "/api/plugins", "")
	found := false
	for _, raw := range decodeState(t, rec)["bundles"].([]interface{}) {
		b := raw.(map[string]interface{})
		if b["id"] != "reporting-pack" {
			continue
		}
		for _, rawU := range b["units"].([]interface{}) {
			u := rawU.(map[string]interface{})
			if u["id"] == "role/报告撰写" {
				found = true
				if u["enabled"] != false {
					t.Fatalf("the bundle view still reports the switched-off unit as enabled: %v", u)
				}
			}
		}
	}
	if !found {
		t.Fatal("the installed bundle view lost the role unit entirely")
	}
	after, err := plugin.Digest(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("switching a bundle unit's state rewrote the pack's file")
	}

	// An unknown *kind* is a malformed request; an unknown name under a real kind is a miss.
	rec = env.do(t, http.MethodPost, "/api/plugins/units/bogus/x/enabled", `{"enabled":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind returned %d, want 400: %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodPost, "/api/plugins/units/role/no-such-role/enabled", `{"enabled":false}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown unit returned %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// The unit-level DELETE used to refuse anything a pack owned. It now means "take this one unit out
// of the pack's selection": the pack keeps delivering the rest, the install record narrows so a
// restart does not bring it back, and removing the last unit is performed as the uninstall it is
// rather than leaving a row that claims an installation with nothing in it.
func TestPluginUnitDetachNarrowsAPackSelection(t *testing.T) {
	env := newPluginTestEnv(t, true)
	local, err := plugin.NewUnit(plugin.KindRole, "临时角色", filepath.Join(env.bundles, "loose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.table.PutLocal(local); err != nil {
		t.Fatalf("PutLocal: %v", err)
	}
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}

	// One unit out of the pack: the table loses it, the pack stays, the record narrows.
	rec := env.do(t, http.MethodDelete, "/api/plugins/units/role/报告撰写", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("removing a bundle-owned unit returned %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeState(t, rec)
	if body["bundle"] != "reporting-pack" || body["bundle_uninstalled"] != false {
		t.Fatalf("removal did not report a narrowed pack: %v", body)
	}
	if _, ok := env.table.Unit("role/报告撰写"); ok {
		t.Fatalf("the removed unit is still in the table")
	}
	if _, ok := env.table.Bundle("reporting-pack"); !ok {
		t.Fatalf("removing one unit uninstalled the whole pack")
	}
	want := []string{"agent/report-analyst", "skill/finding-writeup", "tool/pandoc"}
	got := env.installs.selections["reporting-pack"]
	if len(got) != len(want) {
		t.Fatalf("recorded selection = %v, want the three remaining units %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recorded selection = %v, want %v (sorted by identity)", got, want)
		}
	}

	// The scanned-unit half is unchanged.
	rec = env.do(t, http.MethodDelete, "/api/plugins/units/role/临时角色", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("detaching a scanned unit returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := env.table.Unit("role/临时角色"); ok {
		t.Fatalf("the detached unit is still in the table")
	}

	// Taking the last units out one by one ends in the uninstall, record and all - a row that said
	// "installed" over an empty table would come back as an install of nothing at the next boot.
	for _, id := range []string{"agent/report-analyst", "skill/finding-writeup"} {
		rec = env.do(t, http.MethodDelete, "/api/plugins/units/"+id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("removing %s returned %d: %s", id, rec.Code, rec.Body.String())
		}
		if decodeState(t, rec)["bundle_uninstalled"] != false {
			t.Fatalf("removing %s uninstalled the pack while units remained", id)
		}
	}
	rec = env.do(t, http.MethodDelete, "/api/plugins/units/tool/pandoc", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("removing the last unit returned %d: %s", rec.Code, rec.Body.String())
	}
	body = decodeState(t, rec)
	if body["bundle_uninstalled"] != true || body["install_recorded"] == true {
		t.Fatalf("the last unit did not end as an uninstall: %v", body)
	}
	if _, ok := env.table.Bundle("reporting-pack"); ok {
		t.Fatalf("the pack is still installed after its last unit was removed")
	}
	found := false
	for _, id := range env.installs.forgot {
		if id == "reporting-pack" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the install record was not forgotten with the last unit: %v", env.installs.forgot)
	}
}

func TestPluginHandlerWithoutATableIsUnavailableNotPanic(t *testing.T) {
	h := NewPluginHandler(nil, "", nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/plugins", h.GetState)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}

// A pack that contributes only roles must not pay for a tool-surface rebuild: that path runs
// ClearTools and re-registers every built-in tool, so doing it for nothing would be a visible
// stall for concurrent runs.
func TestPluginInstallWithoutToolUnitsSkipsTheToolLayer(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeTestFile(t, filepath.Join(env.bundles, "role-only", "roles", "只加角色.yaml"),
		"name: 只加角色\nuser_prompt: 只有角色\nenabled: true\n")
	writeTestFile(t, filepath.Join(env.bundles, "role-only", plugin.ManifestFileName),
		"id: role-only\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/只加角色.yaml\n")

	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"role-only"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if state["refreshed"] != true {
		t.Fatalf("role catalog was not refreshed: %v", state)
	}
	if state["tools_rebuilt"] != false {
		t.Fatalf("a role-only pack rebuilt the whole tool surface: %v", state)
	}
	if env.tools.calls != 0 {
		t.Fatalf("RebuildToolLayer called %d times for a pack with no tool unit", env.tools.calls)
	}

	// The same pack's role unit must still switch without touching the tool layer.
	if rec := env.do(t, http.MethodPost, "/api/plugins/units/role/只加角色/enabled", `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	if env.tools.calls != 0 {
		t.Fatalf("switching a role rebuilt the tool surface %d time(s)", env.tools.calls)
	}
}

// Forgetting the rebuilder at assembly time is not an error the endpoint can raise - the install
// did land - so it has to be a loud field in the response instead of a quiet "installed".
func TestPluginInstallReportsAMissingToolLayerRebuilder(t *testing.T) {
	env := newPluginTestEnv(t, true)
	env.plugins.tools = nil

	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if state["refreshed"] != false {
		t.Fatalf("a missing rebuilder reported refreshed=true: %v", state)
	}
	if state["tools_rebuilt"] != false {
		t.Fatalf("tools_rebuilt true without a rebuilder: %v", state)
	}
	msg, _ := state["tool_layer_error"].(string)
	if !strings.Contains(msg, "工具层未重建") {
		t.Fatalf("the response does not say the tool layer was not rebuilt: %q", msg)
	}
}

// A rebuild that fails must be reported the same way, not swallowed after the table changed.
func TestPluginInstallReportsAFailedToolLayerRebuild(t *testing.T) {
	env := newPluginTestEnv(t, true)
	env.tools.err = fmt.Errorf("注册表拒绝该配方")

	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	msg, _ := state["tool_layer_error"].(string)
	if !strings.Contains(msg, "注册表拒绝该配方") || state["refreshed"] != false {
		t.Fatalf("a failed rebuild was not reported: %v", state)
	}
}

func TestPluginToolUnitSwitchRebuildsTheToolLayer(t *testing.T) {
	env := newPluginTestEnv(t, true)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	before := env.tools.calls

	rec := env.do(t, http.MethodPost, "/api/plugins/units/tool/pandoc/enabled", `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	if env.tools.calls != before+1 {
		t.Fatalf("switching a tool unit rebuilt the layer %d extra time(s), want 1", env.tools.calls-before)
	}
	if got := decodeState(t, rec)["tools_rebuilt"]; got != true {
		t.Fatalf("the switch response does not report the rebuild: %v", got)
	}
}

// The per-kind served verdict, walked from the kind list itself: every kind plugin.Kinds declares
// must have a run path wired to it, and a kind added there without wiring fails here rather than
// shipping a served flag that quietly lies.
func TestEveryKindReportsItsActualServedState(t *testing.T) {
	if len(servedKinds) != len(plugin.Kinds) {
		t.Fatalf("servedKinds covers %v but plugin.Kinds declares %d kinds: a kind without a run path "+
			"is offered for install and never served", servedKindNames(), len(plugin.Kinds))
	}
	for _, kind := range plugin.Kinds {
		isServed, reason := unitServed(plugin.Unit{Kind: kind})
		if !isServed || reason != "" {
			t.Errorf("kind %q: served=%v reason=%q, want served with no reason", kind, isServed, reason)
		}
		if _, ok := servedKinds[kind]; !ok {
			t.Errorf("kind %q is in plugin.Kinds but not in servedKinds", kind)
		}
	}
	// The other direction: an kind nobody wired must say so out loud, not report served.
	if isServed, reason := unitServed(plugin.Unit{Kind: plugin.Kind("widget")}); isServed || reason == "" {
		t.Errorf("unknown kind reported served=%v reason=%q", isServed, reason)
	}
}

// The console needs "what can I install" before it can offer one click. Two properties are
// pinned here: the listing must mark what is already in, and one half-written pack must not
// take the whole list down with it.
func TestPluginAvailableListsPacksAndSurvivesABrokenManifest(t *testing.T) {
	env := newPluginTestEnv(t, true)
	// A pack whose manifest points at a file that is not there: unusable, but it must show up as
	// one broken row rather than a 500 on the catalogue.
	writeTestFile(t, filepath.Join(env.bundles, "broken-pack", plugin.ManifestFileName),
		"id: broken-pack\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/缺失.yaml\n")
	// A directory with no manifest is not a pack: skipped, not reported.
	writeTestFile(t, filepath.Join(env.bundles, "notes", "README.md"), "just notes\n")

	rec := env.do(t, http.MethodGet, "/api/plugins/available", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("available returned %d: %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	rows := map[string]map[string]interface{}{}
	for _, raw := range state["bundles"].([]interface{}) {
		b := raw.(map[string]interface{})
		rows[b["id"].(string)] = b
	}
	if len(rows) != 2 {
		t.Fatalf("catalogue lists %v, want reporting-pack and broken-pack only", catalogIDs(rows))
	}
	if _, ok := rows["notes"]; ok {
		t.Errorf("a directory without a manifest is offered as an installable pack")
	}
	good := rows["reporting-pack"]
	if good["installed"] != false {
		t.Fatalf("an uninstalled pack reported installed: %v", good)
	}
	if len(good["units"].([]interface{})) != 4 {
		t.Fatalf("reporting-pack lists %v units, want the 4 it declares", good["units"])
	}
	broken := rows["broken-pack"]
	msg, _ := broken["error"].(string)
	if !strings.Contains(msg, "缺失") {
		t.Fatalf("the broken pack row carries no usable reason: %q", msg)
	}

	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	rec = env.do(t, http.MethodGet, "/api/plugins/available", "")
	for _, raw := range decodeState(t, rec)["bundles"].([]interface{}) {
		b := raw.(map[string]interface{})
		if b["id"] == "reporting-pack" && b["installed"] != true {
			t.Fatalf("after install the catalogue still reports the pack as available: %v", b)
		}
	}
}

func catalogIDs(m map[string]map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// A pack that declares an MCP server: installing writes the declaration into the live manager and
// leaves it switched off. Starting a process is not a side effect of clicking install.
func writeMCPPack(t *testing.T, env *pluginTestEnv) {
	t.Helper()
	writeTestFile(t, filepath.Join(env.bundles, "mcp-pack", "mcp", "lab-server.yaml"),
		"type: stdio\ncommand: python3\nargs: [\"-c\", \"pass\"]\ndescription: 实验室 MCP\nenabled: true\n")
	writeTestFile(t, filepath.Join(env.bundles, "mcp-pack", plugin.ManifestFileName),
		"id: mcp-pack\nname: MCP 声明包\nversion: 1.0.0\nunits:\n  - kind: mcp\n    path: mcp/lab-server.yaml\n")
}

func TestPluginInstallDeclaresMCPServerWithoutStartingIt(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeMCPPack(t, env)

	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-pack"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if state["mcp_declared"] != float64(1) {
		t.Fatalf("the declaration was not written into the manager: %v", state)
	}
	if state["mcp_started"] != false {
		t.Fatalf("installing a pack started a server: %v", state)
	}
	cfg, ok := env.mcp.configs["lab-server"]
	if !ok {
		t.Fatal("the manager never received the declared server")
	}
	if cfg.ExternalMCPEnable || !cfg.Disabled {
		t.Fatalf("the declared server arrived enabled: enable=%v disabled=%v", cfg.ExternalMCPEnable, cfg.Disabled)
	}
	if cfg.Command != "python3" {
		t.Fatalf("the declaration lost its command: %+v", cfg)
	}
	// The table agrees with the manager, so the console cannot show one state and serve another.
	unit, ok := env.table.Unit("mcp/lab-server")
	if !ok {
		t.Fatal("the declared unit is missing from the table")
	}
	if unit.Enabled {
		t.Fatalf("the table says enabled while the manager says disabled: %+v", unit)
	}

	// The switch is where starting becomes the operator's decision.
	rec = env.do(t, http.MethodPost, "/api/plugins/units/mcp/lab-server/enabled", `{"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeState(t, rec)["mcp_applied"]; got != true {
		t.Fatalf("the switch did not report applying the declaration: %v", got)
	}
	if cfg := env.mcp.configs["lab-server"]; !cfg.ExternalMCPEnable || cfg.Disabled {
		t.Fatalf("switching the unit on did not enable the server: %+v", cfg)
	}

	rec = env.do(t, http.MethodDelete, "/api/plugins/bundles/mcp-pack", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall: %d %s", rec.Code, rec.Body.String())
	}
	if len(env.mcp.removes) != 1 || env.mcp.removes[0] != "lab-server" {
		t.Fatalf("unplug left the server declared: %v", env.mcp.removes)
	}
	if _, ok := env.mcp.configs["lab-server"]; ok {
		t.Fatal("the manager still holds a server whose pack was unplugged")
	}
}

func TestPluginInstallCannotOverwriteAnOperatorsMCPServer(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeMCPPack(t, env)
	// The operator declared this server in config.yaml; it is not in the table.
	env.mcp.configs["lab-server"] = config.ExternalMCPServerConfig{Command: "/usr/local/bin/the-operators-binary"}

	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-pack"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("install over an operator-declared server returned %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "lab-server") {
		t.Fatalf("the refusal does not name the server: %s", rec.Body.String())
	}
	if len(env.mcp.adds) != 0 {
		t.Fatalf("a refused install still wrote to the manager: %v", env.mcp.adds)
	}
	if got := env.mcp.configs["lab-server"].Command; got != "/usr/local/bin/the-operators-binary" {
		t.Fatalf("the operator's command was replaced by the pack: %q", got)
	}
	if _, ok := env.table.Unit("mcp/lab-server"); ok {
		t.Fatal("a refused install still reached the table")
	}
	if _, ok := env.table.Bundle("mcp-pack"); ok {
		t.Fatal("a refused install is recorded as installed")
	}
}

// Forgetting the manager is not an error the endpoint can raise after the table changed, so it has
// to be a field in the response: the pack looks installed while no server was ever declared.
func TestPluginInstallReportsAMissingMCProvisioner(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeMCPPack(t, env)
	env.plugins.mcp = nil

	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-pack"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if state["mcp_declared"] == float64(1) {
		t.Fatalf("declared a server with no manager wired: %v", state)
	}
	msg, _ := state["mcp_message"].(string)
	if !strings.Contains(msg, "未接入") {
		t.Fatalf("the response does not say the declaration was not applied: %q", msg)
	}
}

func pluginUnitRow(t *testing.T, env *pluginTestEnv, unitID string) map[string]interface{} {
	t.Helper()
	rec := env.do(t, http.MethodGet, "/api/plugins", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("state: %d %s", rec.Code, rec.Body.String())
	}
	for _, raw := range decodeState(t, rec)["bundles"].([]interface{}) {
		for _, uraw := range raw.(map[string]interface{})["units"].([]interface{}) {
			u := uraw.(map[string]interface{})
			if u["id"] == unitID {
				return u
			}
		}
	}
	t.Fatalf("unit %q is not in the console state", unitID)
	return nil
}

// An MCP unit's served flag has a condition the table cannot see: the live manager still has to
// hold the declaration. config.yaml wins a name collision, and until the console checks the manager
// it would keep calling a shadowed declaration served while nothing connected.
func TestPluginConsoleReportsAShadowedMCPServer(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeMCPPack(t, env)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	if row := pluginUnitRow(t, env, "mcp/lab-server"); row["served"] != true {
		t.Fatalf("a freshly declared server reported not served: %v", row)
	}

	// What LoadConfigs does when the operator's file grows a declaration of the same name.
	delete(env.mcp.owners, "lab-server")
	row := pluginUnitRow(t, env, "mcp/lab-server")
	if row["served"] == true {
		t.Fatalf("the console calls a shadowed declaration served: %v", row)
	}
	if reason, _ := row["reason"].(string); !strings.Contains(reason, "配置文件") {
		t.Fatalf("the reason does not name the configuration file as the one holding the name: %q", reason)
	}

	// The catalogue states what installing *would* wire, so an unclaimed name is not shadowed
	// there; making that row read as broken would hide the real conflict behind a false one.
	rec := env.do(t, http.MethodGet, "/api/plugins/available", "")
	for _, raw := range decodeState(t, rec)["bundles"].([]interface{}) {
		b := raw.(map[string]interface{})
		if b["id"] != "mcp-pack" {
			continue
		}
		for _, uraw := range b["units"].([]interface{}) {
			u := uraw.(map[string]interface{})
			if u["id"] == "mcp/lab-server" && u["served"] != true {
				t.Fatalf("the installable catalogue reports a pre-install shadow: %v", u)
			}
		}
	}
}

// Unplug removes what the pack declared. A server the manager credits to a different pack stays
// where it is, with the response saying who owns it.
func TestUninstallDoesNotRemoveAnotherPacksServer(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeMCPPack(t, env)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	env.mcp.owners["lab-server"] = "someone-elses-pack"

	rec := env.do(t, http.MethodDelete, "/api/plugins/bundles/mcp-pack", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if removed, ok := state["mcp_removed"]; ok && removed != float64(0) {
		t.Fatalf("unplug deleted a server another pack owns: %v", state)
	}
	if len(env.mcp.removes) != 0 {
		t.Fatalf("unplug reached the manager: %v", env.mcp.removes)
	}
	if _, ok := env.mcp.configs["lab-server"]; !ok {
		t.Fatal("the other pack's server was removed")
	}
	msg, _ := state["mcp_message"].(string)
	if !strings.Contains(msg, "someone-elses-pack") {
		t.Fatalf("the response does not name the real owner: %q", msg)
	}
}

// A capability pack is content somebody else wrote. config.yaml and the MCP page both expand
// ${VAR} in a server declaration; doing that here would hand the pack the platform process
// environment, and a declaration like Authorization: "Bearer ${CSAI_LLM_API_KEY}" is then a
// credential shipped to a server the pack author chose. So the bytes stay as written.
func TestPackMCPDeclarationDoesNotExpandEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lab.yaml")
	writeTestFile(t, path, "type: http\nurl: https://example.invalid/sse\n"+
		"headers:\n  Authorization: \"Bearer ${CSAI_LLM_API_KEY}\"\n"+
		"env:\n  TOKEN: \"${CSAI_LLM_API_KEY}\"\ntimeout: 45\n")
	t.Setenv("CSAI_LLM_API_KEY", "sk-should-never-travel")

	cfg, err := LoadMCPDeclaration(path)
	if err != nil {
		t.Fatalf("LoadMCPDeclaration: %v", err)
	}
	if cfg.Timeout != 45 {
		t.Errorf("timeout lost: %d", cfg.Timeout)
	}
	for label, got := range map[string]string{
		"header": cfg.Headers["Authorization"],
		"env":    cfg.Env["TOKEN"],
	} {
		if strings.Contains(got, "sk-should-never-travel") {
			t.Errorf("%s was expanded against the platform environment: %q", label, got)
		}
		if !strings.Contains(got, "${CSAI_LLM_API_KEY}") {
			t.Errorf("%s lost its literal reference: %q", label, got)
		}
	}
}

// A declaration with neither half of a transport is not a server: installing it would put a row on
// the MCP page that can never connect and gives no reason why.
func TestLoadMCPDeclarationNeedsACommandOrURL(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadMCPDeclaration(filepath.Join(dir, "empty.yaml"))
	if err == nil || !strings.Contains(err.Error(), "读取 MCP 声明失败") {
		t.Fatalf("a missing declaration file returned %v, want a readable file error", err)
	}
	writeTestFile(t, filepath.Join(dir, "no-transport.yaml"), "type: stdio\ndescription: 什么都没声明\n")
	if _, err := LoadMCPDeclaration(filepath.Join(dir, "no-transport.yaml")); err == nil {
		t.Fatal("a declaration with no command and no url was accepted")
	}
}

// A bundle's files cannot be edited after install, so the console's switch is the only off button a
// pack unit has. If that decision lives only in memory, the next start-up rebuilds the table from
// the pack and the role the operator disabled is serving again - with nothing in the UI to explain
// why it came back.
func TestPluginUnitSwitchIsRemembered(t *testing.T) {
	env := newPluginTestEnv(t, true)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	rec := env.do(t, http.MethodPost, "/api/plugins/units/role/报告撰写/enabled", `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if state["switch_persisted"] != true {
		t.Fatalf("the switch was not recorded: %v", state)
	}
	if got, ok := env.switches.recorded["role/报告撰写"]; !ok || got {
		t.Fatalf("recorded decisions=%v, want role/报告撰写=false", env.switches.recorded)
	}
}

// The MCP one is the exception, and it has to be said out loud: start-up re-declares those servers
// disabled whatever this row says, so claiming the switch was persisted would be a promise the
// boot rule does not keep.
func TestPluginMCPSwitchStatesTheBootRule(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeMCPPack(t, env)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	rec := env.do(t, http.MethodPost, "/api/plugins/units/mcp/lab-server/enabled", `{"enabled":true}`)
	state := decodeState(t, rec)
	if state["switch_persisted"] != false {
		t.Fatalf("an MCP switch claimed to be durable: %v", state)
	}
	msg, _ := state["switch_message"].(string)
	if !strings.Contains(msg, "config.yaml") {
		t.Fatalf("the response does not point at the durable route: %q", msg)
	}
	if len(env.switches.recorded) != 0 {
		t.Fatalf("the MCP decision was written anyway: %v", env.switches.recorded)
	}
}

// Unplug and detach are the two ways an identity leaves the table on purpose. A saved row nobody
// forgets comes back as an unexplained "disabled" the next time somebody ships a unit under that
// name.
func TestPluginRemovalForgetsSavedSwitches(t *testing.T) {
	env := newPluginTestEnv(t, true)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	env.do(t, http.MethodPost, "/api/plugins/units/role/报告撰写/enabled", `{"enabled":false}`)
	if rec := env.do(t, http.MethodDelete, "/api/plugins/bundles/reporting-pack", ""); rec.Code != http.StatusOK {
		t.Fatalf("uninstall: %d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"role/报告撰写", "skill/finding-writeup", "tool/pandoc", "agent/report-analyst"} {
		found := false
		for _, got := range env.switches.forgot {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("unplug forgot %v, want every unit identity of the pack including %q", env.switches.forgot, want)
		}
	}
}

// Both halves of the durability promise are things the response has to state, because "已更新" is
// true either way and the operator cannot see that nothing will be there after a restart.
func TestPluginSwitchReportsAMissingOrFailingSwitchStore(t *testing.T) {
	env := newPluginTestEnv(t, true)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	env.plugins.switches = nil
	rec := env.do(t, http.MethodPost, "/api/plugins/units/role/报告撰写/enabled", `{"enabled":false}`)
	state := decodeState(t, rec)
	if state["switch_persisted"] != false {
		t.Fatalf("a missing store reported a durable switch: %v", state)
	}
	if msg, _ := state["switch_message"].(string); !strings.Contains(msg, "未落库") {
		t.Fatalf("the response does not say the switch is session-only: %q", msg)
	}
	// The unit itself still flipped: the failure is durability, not the request.
	if unit, _ := env.table.Unit("role/报告撰写"); unit.Enabled {
		t.Fatal("a failed record left the unit enabled while the response said 已更新")
	}

	env2 := newPluginTestEnv(t, true)
	if rec := env2.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	env2.switches.err = errors.New("database is locked")
	rec = env2.do(t, http.MethodPost, "/api/plugins/units/role/报告撰写/enabled", `{"enabled":false}`)
	state = decodeState(t, rec)
	if state["switch_persisted"] != false {
		t.Fatalf("a failed write reported a durable switch: %v", state)
	}
	if msg, _ := state["switch_message"].(string); !strings.Contains(msg, "database is locked") {
		t.Fatalf("the write failure was swallowed: %q", msg)
	}
}

// An install the store cannot record is a session install, and the response has to say so:
// "能力包已安装并生效" is true in-process either way, and without the flag the pack's
// disappearance at the next restart would look like a random uninstall.
func TestPluginInstallReportsAMissingOrFailingInstallStore(t *testing.T) {
	env := newPluginTestEnv(t, true)
	env.plugins.installs = nil
	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if state["install_recorded"] != false {
		t.Fatalf("a missing store reported a recorded install: %v", state)
	}
	if msg, _ := state["install_message"].(string); !strings.Contains(msg, "未落库") {
		t.Fatalf("the response does not say the install is session-only: %q", msg)
	}
	// The install itself still happened: the failure is durability, not the request.
	if _, ok := env.table.Unit("role/报告撰写"); !ok {
		t.Fatal("a failed record left the pack out of the table while the response said 安装并生效")
	}

	env2 := newPluginTestEnv(t, true)
	env2.installs.err = errors.New("database is locked")
	rec = env2.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack"}`)
	state = decodeState(t, rec)
	if state["install_recorded"] != false {
		t.Fatalf("a failed write reported a recorded install: %v", state)
	}
	if msg, _ := state["install_message"].(string); !strings.Contains(msg, "database is locked") {
		t.Fatalf("the write failure was swallowed: %q", msg)
	}

	// And the same store failing on the way out has to name what a stale row will do: re-install
	// the pack at the next boot.
	rec = env2.do(t, http.MethodDelete, "/api/plugins/bundles/reporting-pack", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall: %d %s", rec.Code, rec.Body.String())
	}
	state = decodeState(t, rec)
	if state["install_forgotten"] != false {
		t.Fatalf("a failed forget reported the record taken back: %v", state)
	}
	if msg, _ := state["install_message"].(string); !strings.Contains(msg, "重新装入") {
		t.Fatalf("the response does not name the consequence of a stale row: %q", msg)
	}
}

// The install endpoint's units field: pick what you want out of a pack. The pack stays installed
// and keeps its full manifest, the table holds only the chosen units, and the click is recorded with
// the selection so a restart replays the same subset.
func TestPluginInstallInstallsAChosenSubsetOfAPack(t *testing.T) {
	env := newPluginTestEnv(t, true)
	rec := env.do(t, http.MethodPost, "/api/plugins/install",
		`{"bundle":"reporting-pack","units":["skill/finding-writeup","role/报告撰写"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if state["units_installed"] != float64(2) || state["units_total"] != float64(4) {
		t.Fatalf("the response does not state what went in of what is offered: %v", state)
	}
	for _, id := range []string{"skill/finding-writeup", "role/报告撰写"} {
		if _, ok := env.table.Unit(id); !ok {
			t.Fatalf("%s was selected and is not installed", id)
		}
	}
	for _, id := range []string{"agent/report-analyst", "tool/pandoc"} {
		if _, ok := env.table.Unit(id); ok {
			t.Fatalf("%s was not selected and is installed", id)
		}
	}
	if _, ok := env.table.Bundle("reporting-pack"); !ok {
		t.Fatalf("the pack is not installed")
	}
	sel := env.installs.selections["reporting-pack"]
	if len(sel) != 2 {
		t.Fatalf("recorded selection = %v, want the two chosen units", sel)
	}
	for _, id := range sel {
		if id != "skill/finding-writeup" && id != "role/报告撰写" {
			t.Fatalf("recorded selection = %v", sel)
		}
	}
	// The tool unit stayed out, so the tool surface must not have been rebuilt for this install.
	if env.tools.calls != 0 {
		t.Fatalf("a selection without tool units rebuilt the tool surface %d times", env.tools.calls)
	}
	// The catalogue marks the two states apart: chosen units are installed, the others are not.
	catalog := availablePack(t, env, "reporting-pack")
	units := catalog["units"].([]interface{})
	byID := map[string]map[string]interface{}{}
	for _, raw := range units {
		u := raw.(map[string]interface{})
		byID[u["id"].(string)] = u
	}
	for _, id := range []string{"skill/finding-writeup", "role/报告撰写"} {
		if byID[id]["installed"] != true {
			t.Fatalf("%s reads as not installed: %v", id, byID[id])
		}
	}
	for _, id := range []string{"agent/report-analyst", "tool/pandoc"} {
		if byID[id]["installed"] != false {
			t.Fatalf("%s reads as installed although it was never chosen: %v", id, byID[id])
		}
	}
}

// availablePack reads the catalogue entry for one pack.
func availablePack(t *testing.T, env *pluginTestEnv, id string) map[string]interface{} {
	t.Helper()
	rec := env.do(t, http.MethodGet, "/api/plugins/available", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("available: %d %s", rec.Code, rec.Body.String())
	}
	catalog := decodeState(t, rec)
	for _, raw := range catalog["bundles"].([]interface{}) {
		pack := raw.(map[string]interface{})
		if pack["id"] == id {
			return pack
		}
	}
	t.Fatalf("pack %s is not in the catalogue: %v", id, catalog)
	return nil
}

func TestPluginInstallRefusesUnitsThePackDoesNotDeclare(t *testing.T) {
	env := newPluginTestEnv(t, true)
	rec := env.do(t, http.MethodPost, "/api/plugins/install",
		`{"bundle":"reporting-pack","units":["role/报告撰写","skill/不存在的技能"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an undeclared unit id returned %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "skill/不存在的技能") {
		t.Fatalf("the refusal does not name the unit: %s", rec.Body.String())
	}
	// Nothing was installed, nothing was recorded: a partial install must not read as a full one.
	if _, ok := env.table.Bundle("reporting-pack"); ok {
		t.Fatalf("a refused selection installed the pack")
	}
	if _, ok := env.installs.recorded["reporting-pack"]; ok {
		t.Fatalf("a refused selection was recorded for the next boot to replay")
	}
}

func TestPluginInstallRejectsAnExplicitlyEmptySelection(t *testing.T) {
	env := newPluginTestEnv(t, true)
	rec := env.do(t, http.MethodPost, "/api/plugins/install",
		`{"bundle":"reporting-pack","units":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an empty selection returned %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if _, ok := env.table.Bundle("reporting-pack"); ok {
		t.Fatalf("an empty selection installed something")
	}
}

// Choosing every unit is the whole-pack decision spelled in full, and it is recorded that way on
// purpose: a later version that adds a unit should install it, exactly as the install button does.
func TestPluginInstallOfEveryUnitIsRecordedAsTheWholePack(t *testing.T) {
	env := newPluginTestEnv(t, true)
	rec := env.do(t, http.MethodPost, "/api/plugins/install",
		`{"bundle":"reporting-pack","units":["role/报告撰写","agent/report-analyst","skill/finding-writeup","tool/pandoc"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeState(t, rec)["units_installed"]; got != float64(4) {
		t.Fatalf("units_installed = %v, want 4", got)
	}
	if sel, recorded := env.installs.selections["reporting-pack"]; !recorded || sel != nil {
		t.Fatalf("a full selection was recorded as %v; it has to read as the whole pack", sel)
	}
}

func TestPluginInstallNarrowsAnExistingInstallAndUnwindsWhatLeft(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeMCPPack2(t, env)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-role-pack"}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	if len(env.mcp.adds) != 1 {
		t.Fatalf("the declaration was not written: %v", env.mcp.adds)
	}
	// The operator starts the server: that is their decision, and a later reconcile of the *same*
	// pack must not take it back by re-declaring the unit disabled.
	if rec := env.do(t, http.MethodPost, "/api/plugins/units/mcp/lab-server/enabled", `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-role-pack","units":["role/持有角色"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("narrowing install: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if removed, _ := state["units_removed"].([]interface{}); len(removed) != 1 || removed[0] != "mcp/lab-server" {
		t.Fatalf("the response does not name the unit that left: %v", state)
	}
	if _, ok := env.table.Unit("mcp/lab-server"); ok {
		t.Fatalf("the narrowed-out unit is still in the table")
	}
	if _, ok := env.mcp.configs["lab-server"]; ok {
		t.Fatalf("the server declaration survived the unit leaving the selection")
	}
	if len(env.mcp.removes) != 1 || env.mcp.removes[0] != "lab-server" {
		t.Fatalf("the leaving unit's declaration was not withdrawn: %v", env.mcp.removes)
	}
	if sel := env.installs.selections["mcp-role-pack"]; len(sel) != 1 || sel[0] != "role/持有角色" {
		t.Fatalf("recorded selection = %v", sel)
	}
	// A role-only pack must not pay for a tool-surface rebuild, in either direction.
	if env.tools.calls != 0 {
		t.Fatalf("a role-only reconcile rebuilt the tool surface %d times", env.tools.calls)
	}
}

// writeMCPPack2 is a pack that carries a declaration and content, so a reconcile can leave one
// behind - the case where the old uninstall path would try to remove a server that was never in the
// table.
func writeMCPPack2(t *testing.T, env *pluginTestEnv) {
	t.Helper()
	writeTestFile(t, filepath.Join(env.bundles, "mcp-role-pack", "mcp", "lab-server.yaml"),
		"type: stdio\ncommand: python3\nargs: [\"-c\", \"pass\"]\n")
	writeTestFile(t, filepath.Join(env.bundles, "mcp-role-pack", "roles", "持有角色.yaml"),
		"name: 持有角色\nuser_prompt: 只有角色\nenabled: true\n")
	writeTestFile(t, filepath.Join(env.bundles, "mcp-role-pack", plugin.ManifestFileName),
		"id: mcp-role-pack\nname: 声明包\nversion: 1.0.0\nunits:\n"+
			"  - kind: mcp\n    path: mcp/lab-server.yaml\n"+
			"  - kind: role\n    path: roles/持有角色.yaml\n")
}

// A partial install narrows what "uninstall" has to unwind: the units that were never installed
// must not be reported (or attempted) as removals.
func TestPluginUninstallAfterAPartialInstallOnlyUnwindsWhatWasInstalled(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeMCPPack2(t, env)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-role-pack","units":["role/持有角色"]}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	if len(env.mcp.adds) != 0 {
		t.Fatalf("an unselected MCP unit was declared: %v", env.mcp.adds)
	}
	rec := env.do(t, http.MethodDelete, "/api/plugins/bundles/mcp-role-pack", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if state["mcp_removed"] != nil && state["mcp_removed"] != float64(0) {
		t.Fatalf("uninstall removed a server that was never declared: %v", state)
	}
	if msg, _ := state["mcp_message"].(string); msg != "" {
		t.Fatalf("uninstall reported a phantom removal: %v", state)
	}
	if len(env.mcp.removes) != 0 {
		t.Fatalf("the manager was asked to remove %v", env.mcp.removes)
	}
	if _, ok := env.table.Unit("role/持有角色"); ok {
		t.Fatalf("the role survived the uninstall")
	}
}

// Two packs, one identity: the catalogue says who holds it, and the install of that unit is refused
// by name instead of half-succeeding.
func TestAvailableCatalogueNamesAConflictingHolder(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeMCPPack2(t, env)
	squatter, err := plugin.NewUnit(plugin.KindRole, "持有角色", filepath.Join(env.bundles, "shipped.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.table.PutLocal(squatter); err != nil {
		t.Fatalf("PutLocal: %v", err)
	}
	pack := availablePack(t, env, "mcp-role-pack")
	units := pack["units"].([]interface{})
	found := false
	for _, raw := range units {
		u := raw.(map[string]interface{})
		if u["id"] != "role/持有角色" {
			continue
		}
		found = true
		if u["installed"] != false {
			t.Fatalf("a unit held by the built-in scan reads as installed: %v", u)
		}
		if msg, _ := u["conflict"].(string); !strings.Contains(msg, "内置目录") {
			t.Fatalf("the catalogue does not name the holder: %v", u)
		}
	}
	if !found {
		t.Fatalf("the conflicting unit is missing from the catalogue: %v", units)
	}
	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-role-pack","units":["role/持有角色"]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("installing over the built-in identity returned %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// Re-touching a pack must not stop a server the operator had started: the declare step covers only
// the units arriving in this call, so an already-installed MCP unit keeps whatever state its switch
// was left in.
func TestPluginReconcileKeepsAnAlreadyInstalledServerDeclared(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeMCPPack2(t, env)
	if rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-role-pack","units":["mcp/lab-server"]}`); rec.Code != http.StatusOK {
		t.Fatalf("install: %s", rec.Body.String())
	}
	if rec := env.do(t, http.MethodPost, "/api/plugins/units/mcp/lab-server/enabled", `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	addsAfterEnable := len(env.mcp.adds)
	if cfg := env.mcp.configs["lab-server"]; !cfg.ExternalMCPEnable {
		t.Fatalf("the switch did not enable the server: %+v", cfg)
	}

	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"mcp-role-pack","units":["mcp/lab-server","role/持有角色"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reconcile: %d %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	if state["mcp_declared"] != nil && state["mcp_declared"] != float64(0) {
		t.Fatalf("the reconcile re-declared the server while adding a role: %v", state)
	}
	if len(env.mcp.adds) != addsAfterEnable {
		t.Fatalf("a reconcile re-declared an already-installed server (adds=%v), which would stop it", env.mcp.adds)
	}
	if cfg := env.mcp.configs["lab-server"]; !cfg.ExternalMCPEnable || cfg.Disabled {
		t.Fatalf("the running server was switched off by an unrelated selection change: %+v", cfg)
	}
	if _, ok := env.table.Unit("role/持有角色"); !ok {
		t.Fatalf("the newly chosen role did not arrive")
	}
}
