package handler

import (
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/plugin"

	"go.uber.org/zap"
)

// The market-facing half of the console: what installing a pack would register (before anyone
// clicks), where installed executable code came from and whether it is revoked, and the
// snapshot/upgrade/rollback loop. These are the reads and writes the console renders; each one
// exists so an operator can act on what is actually there instead of on what a button implies.

// fakeTrust is the console-side revocation view under test control.
type fakeTrust struct{ view RevocationView }

func (f *fakeTrust) Revocations() RevocationView { return f.view }

func findBundle(t *testing.T, state map[string]interface{}, id string) map[string]interface{} {
	t.Helper()
	bundles, _ := state["bundles"].([]interface{})
	for _, raw := range bundles {
		b, _ := raw.(map[string]interface{})
		if b != nil && b["id"] == id {
			return b
		}
	}
	t.Fatalf("bundle %q not found in state", id)
	return nil
}

func findPreviewUnit(t *testing.T, pack map[string]interface{}, unitID string) map[string]interface{} {
	t.Helper()
	preview, _ := pack["preview"].(map[string]interface{})
	units, _ := preview["units"].([]interface{})
	for _, raw := range units {
		u, _ := raw.(map[string]interface{})
		if u != nil && u["unitId"] == unitID {
			return u
		}
	}
	t.Fatalf("preview unit %q not found", unitID)
	return nil
}

func writePreviewPack(t *testing.T, env *pluginTestEnv) {
	t.Helper()
	dir := filepath.Join(env.bundles, "preview-pack")
	writeTestFile(t, filepath.Join(dir, "tools", "payload.yaml"), `name: payload
command: /bin/true
capability:
  id: community.payload
  version: 1.0.0
  class: destructive
  permission: agent:destructive-execute
  approval: always
  runtime: recipe:exec
  grants: ["process.exec(/bin/true)"]
`)
	// A recipe with no capability manifest: registered in the table, refused at every call.
	writeTestFile(t, filepath.Join(dir, "tools", "loose.yaml"), "name: loose\ncommand: /bin/true\n")
	writeExecutable(t, filepath.Join(dir, "bin", "acme"))
	writeTestFile(t, filepath.Join(dir, "plugins", "acme.yaml"), `pluginId: acme
binary: bin/acme
grants: ["net.connect(10.0.0.0/8)"]
version: 1.0.0
capabilities:
  - id: acme.wipe
    title: 擦除
    class: destructive
    permission: agent:acme.wipe
    approval: always
`)
	writeTestFile(t, filepath.Join(dir, "roles", "preview-role.yaml"), "name: preview-role\nenabled: true\n")
	writeTestFile(t, filepath.Join(dir, plugin.ManifestFileName), `id: preview-pack
name: 预览示例包
version: 2.0.0
description: 安装预览夹具
categories: [红队, 演示]
author: acme
homepage: https://example.invalid/preview
license: Apache-2.0
compatibility: ">=0.9"
changelog: |
  首版
units:
  - kind: role
    path: roles/preview-role.yaml
  - kind: tool
    path: tools/payload.yaml
  - kind: tool
    path: tools/loose.yaml
  - kind: plugin
    path: plugins/acme.yaml
`)
}

// TestPluginAvailableShowsWhatInstallingWouldRegister: the card's confirm dialog is only honest
// if the catalogue already knows the classes, the live-code flag and the recipes that will fail
// closed at call time. All of it is read from the pack's own files, before any install.
func TestPluginAvailableShowsWhatInstallingWouldRegister(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writePreviewPack(t, env)

	rec := env.do(t, "GET", "/api/plugins/available", "")
	if rec.Code != 200 {
		t.Fatalf("available returned %d: %s", rec.Code, rec.Body.String())
	}
	state := decodeState(t, rec)
	bundles, _ := state["bundles"].([]interface{})
	var pack map[string]interface{}
	for _, raw := range bundles {
		b, _ := raw.(map[string]interface{})
		if b != nil && b["id"] == "preview-pack" {
			pack = b
		}
	}
	if pack == nil {
		t.Fatalf("preview-pack missing from the catalogue: %s", rec.Body.String())
	}

	// Catalogue metadata travels with the entry (P0-4).
	if got := pack["author"]; got != "acme" {
		t.Fatalf("author not carried: %v", got)
	}
	if got, _ := pack["categories"].([]interface{}); len(got) != 2 || got[0] != "红队" {
		t.Fatalf("categories not carried: %v", pack["categories"])
	}
	if pack["license"] != "Apache-2.0" || pack["compatibility"] != ">=0.9" || pack["homepage"] != "https://example.invalid/preview" {
		t.Fatalf("metadata incomplete: %v", pack)
	}

	preview, _ := pack["preview"].(map[string]interface{})
	if preview == nil {
		t.Fatal("no preview on the catalogue entry")
	}
	classes, _ := preview["classes"].(map[string]interface{})
	if classes["destructive"] != float64(2) {
		t.Fatalf("class counts must include both declared destructive capabilities, got %v", classes)
	}
	if preview["liveCodeUnits"] != float64(1) {
		t.Fatalf("liveCodeUnits must count the plugin unit, got %v", preview["liveCodeUnits"])
	}
	if preview["undeclaredUnits"] != float64(1) {
		t.Fatalf("the recipe without a capability manifest must be counted, got %v", preview["undeclaredUnits"])
	}
	if preview["problemUnits"] != float64(0) {
		t.Fatalf("problemUnits should be 0 for a readable pack, got %v", preview["problemUnits"])
	}

	loose := findPreviewUnit(t, pack, "tool/loose")
	if loose["declared"] != false {
		t.Fatalf("a recipe without a capability manifest must read as undeclared: %v", loose)
	}
	payload := findPreviewUnit(t, pack, "tool/payload")
	if payload["declared"] != true || payload["source"] != "recipe" {
		t.Fatalf("payload recipe row wrong: %v", payload)
	}
	pluginRow := findPreviewUnit(t, pack, "plugin/acme")
	if pluginRow["source"] != "plugin" {
		t.Fatalf("plugin row must say where its metadata comes from: %v", pluginRow)
	}
	caps, _ := pluginRow["capabilities"].([]interface{})
	if len(caps) != 1 {
		t.Fatalf("plugin capabilities not listed: %v", pluginRow["capabilities"])
	}
	first, _ := caps[0].(map[string]interface{})
	if first["id"] != "acme.wipe" || first["class"] != "destructive" || first["approval"] != "always" {
		t.Fatalf("plugin capability metadata wrong: %v", first)
	}

	// The upgrade diff compares digests, so the catalogue has to carry them.
	units, _ := pack["units"].([]interface{})
	if len(units) == 0 {
		t.Fatal("no unit rows in the catalogue entry")
	}
	for _, raw := range units {
		u, _ := raw.(map[string]interface{})
		if digest, _ := u["digest"].(string); digest == "" {
			t.Fatalf("unit %v has no digest: an upgrade diff would have nothing to compare", u["id"])
		}
	}
}

// TestPluginConsoleShowsRevocationsAndProvenance: a revoked build that keeps running must be
// visible as revoked on the row itself, next to the publisher and the digest the block list keys
// on - and the panel must name where the list came from.
func TestPluginConsoleShowsRevocationsAndProvenance(t *testing.T) {
	env := newPluginTestEnv(t, false)
	packDir := filepath.Join(env.bundles, "code-pack")
	writeExecutable(t, filepath.Join(packDir, "bin", "acme"))
	writeTestFile(t, filepath.Join(packDir, "plugins", "acme.yaml"), `pluginId: acme
binary: bin/acme
grants: ["net.connect(10.0.0.0/8)"]
version: 1.0.0
capabilities:
  - id: acme.scan
    title: 扫描
    class: mutating
    permission: agent:acme.scan
`)
	writeTestFile(t, filepath.Join(packDir, plugin.ManifestFileName),
		"id: code-pack\nversion: 1.0.0\nunits:\n  - kind: plugin\n    path: plugins/acme.yaml\n")

	trust := &fakeTrust{view: RevocationView{Loaded: false}}
	env.trust = trust
	env.plugins = NewPluginHandler(env.table, env.bundles, env.roles, env.tools, env.mcp, env.pluginsProvisioner, env.switches, env.installs, env.trust, nil, zap.NewNop())

	if rec := env.do(t, "POST", "/api/plugins/install", `{"bundle":"code-pack"}`); rec.Code != 200 {
		t.Fatalf("install returned %d: %s", rec.Code, rec.Body.String())
	}

	// No list installed: the panel says so, and nothing is marked revoked.
	state := decodeState(t, env.do(t, "GET", "/api/plugins", ""))
	rev, _ := state["revocations"].(map[string]interface{})
	if rev == nil || rev["loaded"] != false {
		t.Fatalf("an unloaded block list must read as loaded:false, got %v", state["revocations"])
	}
	bundle := findBundle(t, state, "code-pack")
	units, _ := bundle["units"].([]interface{})
	row, _ := units[0].(map[string]interface{})
	if row["publisher"] != "acme" {
		t.Fatalf("provenance publisher missing on the plugin row: %v", row)
	}
	artifactDigest, _ := row["artifactDigest"].(string)
	if len(artifactDigest) != 64 {
		t.Fatalf("artifact digest must be the binary's sha256, got %q", artifactDigest)
	}
	if row["revoked"] == true {
		t.Fatalf("nothing is revoked yet: %v", row)
	}

	// Publisher revoked: the row says so, and the panel carries the list.
	trust.view = RevocationView{
		Source:     "/etc/csai/revocations.json",
		Loaded:     true,
		Publishers: []string{"acme"},
	}
	state = decodeState(t, env.do(t, "GET", "/api/plugins", ""))
	rev, _ = state["revocations"].(map[string]interface{})
	if rev["loaded"] != true || rev["source"] != "/etc/csai/revocations.json" {
		t.Fatalf("panel must name the loaded list and its source: %v", rev)
	}
	bundle = findBundle(t, state, "code-pack")
	units, _ = bundle["units"].([]interface{})
	row, _ = units[0].(map[string]interface{})
	if row["revoked"] != true {
		t.Fatalf("a publisher-revoked unit must be marked on its row: %v", row)
	}

	// Digest-level revocation matches the same way the execution path does.
	trust.view = RevocationView{Loaded: true, Digests: []string{artifactDigest}}
	state = decodeState(t, env.do(t, "GET", "/api/plugins", ""))
	bundle = findBundle(t, state, "code-pack")
	units, _ = bundle["units"].([]interface{})
	row, _ = units[0].(map[string]interface{})
	if row["revoked"] != true {
		t.Fatalf("a build-revoked unit must be marked on its row: %v", row)
	}
}

// TestPluginInstallSnapshotsUpgradesAndRollsBack walks the whole loop the console offers: install
// keeps a copy, the directory moving forward is an upgrade, and the endpoint restores the copy on
// request - with the install record following along so the next boot replays the rolled-back
// version, not the one that was rolled away from.
func TestPluginInstallSnapshotsUpgradesAndRollsBack(t *testing.T) {
	env := newPluginTestEnv(t, true)

	rec := env.do(t, "POST", "/api/plugins/install", `{"bundle":"reporting-pack"}`)
	if rec.Code != 200 {
		t.Fatalf("install returned %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeState(t, rec)
	if body["snapshot_version"] != "1.0.0" {
		t.Fatalf("install must keep a rollback copy of what it installed: %v", body["snapshot_version"])
	}
	snapManifest := filepath.Join(env.bundles, plugin.PreviousDirName, "reporting-pack", "1.0.0", plugin.ManifestFileName)
	if !fileExists(snapManifest) {
		t.Fatalf("snapshot %s missing after install", snapManifest)
	}

	// The directory moves to v2; re-installing is the upgrade.
	writeTestFile(t, filepath.Join(env.bundles, "reporting-pack", plugin.ManifestFileName),
		"id: reporting-pack\nname: 报告角色包\nversion: 2.0.0\ndescription: role+agent+skill+tool\nunits:\n  - kind: role\n    path: roles/报告撰写.yaml\n  - kind: agent\n    path: agents/report-analyst.md\n  - kind: skill\n    path: skills/finding-writeup\n  - kind: tool\n    path: tools/pandoc.yaml\n")
	rec = env.do(t, "POST", "/api/plugins/install", `{"bundle":"reporting-pack"}`)
	if rec.Code != 200 {
		t.Fatalf("upgrade returned %d: %s", rec.Code, rec.Body.String())
	}
	if got := env.installs.recorded["reporting-pack"]; got != "2.0.0" {
		t.Fatalf("the install record must follow the upgrade, got %q", got)
	}
	state := decodeState(t, env.do(t, "GET", "/api/plugins", ""))
	bundle := findBundle(t, state, "reporting-pack")
	if bundle["version"] != "2.0.0" {
		t.Fatalf("table still holds %v after upgrade", bundle["version"])
	}
	rollbacks, _ := bundle["rollbacks"].([]interface{})
	if len(rollbacks) != 2 || rollbacks[0] != "1.0.0" || rollbacks[1] != "2.0.0" {
		t.Fatalf("rollback targets must list every snapshotted version, got %v", bundle["rollbacks"])
	}

	// Roll back to v1: the directory is restored, the table follows, and the record follows it
	// too - a record left at 2.0.0 would re-install the version the operator just rejected.
	rec = env.do(t, "POST", "/api/plugins/install", `{"bundle":"reporting-pack","from_version":"1.0.0"}`)
	if rec.Code != 200 {
		t.Fatalf("rollback returned %d: %s", rec.Code, rec.Body.String())
	}
	body = decodeState(t, rec)
	if body["rolled_back_to"] != "1.0.0" {
		t.Fatalf("rollback response must name the restored version: %v", body)
	}
	if got := env.installs.recorded["reporting-pack"]; got != "1.0.0" {
		t.Fatalf("the install record must follow the rollback, got %q", got)
	}
	state = decodeState(t, env.do(t, "GET", "/api/plugins", ""))
	bundle = findBundle(t, state, "reporting-pack")
	if bundle["version"] != "1.0.0" {
		t.Fatalf("table must hold the restored version, got %v", bundle["version"])
	}

	// Refusals: a version with no snapshot, and a traversal-shaped version.
	if rec = env.do(t, "POST", "/api/plugins/install", `{"bundle":"reporting-pack","from_version":"9.9.9"}`); rec.Code != 400 {
		t.Fatalf("rolling back to a version with no snapshot must be refused, got %d", rec.Code)
	}
	if rec = env.do(t, "POST", "/api/plugins/install", `{"bundle":"reporting-pack","from_version":"../escape"}`); rec.Code != 400 {
		t.Fatalf("a traversal version must be refused, got %d", rec.Code)
	}
	state = decodeState(t, env.do(t, "GET", "/api/plugins", ""))
	bundle = findBundle(t, state, "reporting-pack")
	if bundle["version"] != "1.0.0" {
		t.Fatalf("a refused rollback must leave the table untouched, got %v", bundle["version"])
	}
}

// TestPluginInstallReportsASnapshotFailure: the install already happened when the copy fails, so
// the response has to carry the failure instead of pretending the rollback target exists.
func TestPluginInstallReportsASnapshotFailure(t *testing.T) {
	env := newPluginTestEnv(t, true)
	writeTestFile(t, filepath.Join(env.bundles, plugin.PreviousDirName), "a file where the snapshot root should be\n")

	rec := env.do(t, "POST", "/api/plugins/install", `{"bundle":"reporting-pack"}`)
	if rec.Code != 200 {
		t.Fatalf("a snapshot failure must not fail a successful install, got %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeState(t, rec)
	if msg, _ := body["snapshot_error"].(string); msg == "" {
		t.Fatalf("snapshot failure must be reported in the response: %v", body)
	}
	state := decodeState(t, env.do(t, "GET", "/api/plugins", ""))
	bundle := findBundle(t, state, "reporting-pack")
	rollbacks, _ := bundle["rollbacks"].([]interface{})
	if len(rollbacks) != 0 {
		t.Fatalf("no snapshot was written, so no rollback target may be offered: %v", bundle["rollbacks"])
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
