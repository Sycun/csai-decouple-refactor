package handler

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/plugin"

	"go.uber.org/zap"
)

// A plugin unit's declaration is the reviewed artifact: it names the binary, the grants, and the
// exact entry points. These tests cover the rules that hold before anything runs, because after the
// switch is on, this same file is the yardstick the running plugin is measured against.

const pluginDeclaration = `pluginId: ref
binary: ./bin/refplugin
args: []
grants: ["net.connect(10.0.0.0/8)"]
version: 1.0.0
capabilities:
  - id: ref.echo
    title: 回显
    description: 回显一段文本
    class: readonly
    approval: never
    paramsSchema:
      type: object
`

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	writeTestFile(t, path, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// packWith writes a pack directory holding one plugin unit and returns its path.
func packWith(t *testing.T, declaration string) string {
	t.Helper()
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "bin", "refplugin"))
	writeTestFile(t, filepath.Join(dir, "plugins", "ref.yaml"), declaration)
	return dir
}

func loadPack(t *testing.T, dir string) (*PluginUnitDeclaration, error) {
	t.Helper()
	return LoadPluginUnitDeclaration(filepath.Join(dir, "plugins", "ref.yaml"), dir)
}

func TestPluginDeclarationLoadsAReviewedList(t *testing.T) {
	dir := packWith(t, pluginDeclaration)
	decl, err := loadPack(t, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if decl.Publisher != "ref" || len(decl.Capability) != 1 {
		t.Fatalf("decl = %+v", decl)
	}
	c := decl.Capability[0]
	if c.ID != "ref.echo" || c.Class != capability.ClassReadonly || c.Approval != capability.ApprovalNever {
		t.Fatalf("capability = %+v", c)
	}
	resolvedPack, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The loader hands back the resolved path: that is what makes the containment check mean
	// something on a macOS temp dir, which is itself a symlink.
	if decl.Binary != filepath.Join(resolvedPack, "bin", "refplugin") {
		t.Fatalf("binary = %q, want it resolved inside the pack (%s)", decl.Binary, dir)
	}

	// The identity that reaches the table must carry the plugin ABI runtime: that prefix is what
	// routes a call out of process, so a wrong value here would run a pack's code inside the server.
	spec := decl.SpecFor(c)
	if spec.Runtime != capability.RuntimePluginAbi {
		t.Fatalf("runtime = %q", spec.Runtime)
	}
	if !strings.HasPrefix(string(spec.Runtime), "plugin-host:") {
		t.Fatalf("runtime %q would not route out of process", spec.Runtime)
	}
	if spec.Name != "ref.echo" || spec.Publisher != "ref" || spec.Source != "pack-plugin" {
		t.Fatalf("spec = %+v", spec)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("the built spec does not validate: %v", err)
	}
	if len(spec.ParamsSchema) == 0 {
		t.Fatal("a declared paramsSchema must reach the capability, or the form and the LLM schema stay hand-written")
	}
}

func TestPluginDeclarationRefusesABinaryOutsideItsPack(t *testing.T) {
	if _, err := loadPack(t, packWith(t, pluginDeclaration)); err != nil {
		t.Fatalf("the baseline declaration must load: %v", err)
	}
	for name, body := range map[string]string{
		"absolute path":   "pluginId: ref\nbinary: /bin/sh\ncapabilities:\n  - id: ref.echo\n    class: readonly\n",
		"parent escape":   "pluginId: ref\nbinary: ../../escape\ncapabilities:\n  - id: ref.echo\n    class: readonly\n",
		"missing file":    "pluginId: ref\nbinary: ./bin/nope\ncapabilities:\n  - id: ref.echo\n    class: readonly\n",
		"points at a dir": "pluginId: ref\nbinary: ./bin\ncapabilities:\n  - id: ref.echo\n    class: readonly\n",
		"empty binary":    "pluginId: ref\ncapabilities:\n  - id: ref.echo\n    class: readonly\n",
	} {
		if _, err := loadPack(t, packWith(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// A symlink that lives inside the pack and points outside it is the same escape with extra steps.
	dir := packWith(t, "pluginId: ref\nbinary: ./bin/link\ncapabilities:\n  - id: ref.echo\n    class: readonly\n")
	target := filepath.Join(dir, "..", "outside-target")
	writeExecutable(t, target)
	if err := os.Symlink(target, filepath.Join(dir, "bin", "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := loadPack(t, dir); err == nil || !strings.Contains(err.Error(), "之外") {
		t.Fatalf("a pack-internal symlink escaping the pack was accepted: %v", err)
	}
}

func TestPluginDeclarationRefusesUnreviewableCapabilities(t *testing.T) {
	cases := map[string]string{
		"no capabilities":           "pluginId: ref\nbinary: ./bin/refplugin\ncapabilities: []\n",
		"publisher mismatches unit": "pluginId: other\nbinary: ./bin/refplugin\ncapabilities:\n  - id: other.echo\n",
		"foreign capability id":     "pluginId: ref\nbinary: ./bin/refplugin\ncapabilities:\n  - id: victim.echo\n",
		"undotted capability id":    "pluginId: ref\nbinary: ./bin/refplugin\ncapabilities:\n  - id: echo\n",
		"duplicate capability":      "pluginId: ref\nbinary: ./bin/refplugin\ncapabilities:\n  - id: ref.echo\n  - id: ref.echo\n",
		"mutating without perm":     "pluginId: ref\nbinary: ./bin/refplugin\ncapabilities:\n  - id: ref.write\n    class: mutating\n",
		"unknown class":             "pluginId: ref\nbinary: ./bin/refplugin\ncapabilities:\n  - id: ref.echo\n    class: spooky\n",
		"readonly forces approval":  "pluginId: ref\nbinary: ./bin/refplugin\ncapabilities:\n  - id: ref.echo\n    class: readonly\n    approval: always\n",
		"write skips approval":      "pluginId: ref\nbinary: ./bin/refplugin\ncapabilities:\n  - id: ref.write\n    class: mutating\n    permission: agent:write\n    approval: never\n",
		"grant without target":      "pluginId: ref\nbinary: ./bin/refplugin\ngrants: [\"net.connect\"]\ncapabilities:\n  - id: ref.echo\n    class: readonly\n",
	}
	for name, body := range cases {
		dir := packWith(t, body)
		if _, err := loadPack(t, dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// recordingPlugins stands in for the live host plus the capability table so the HTTP contract of
// the switch can be tested without a child process; internal/app covers the real one.
type recordingPlugins struct {
	provisioned map[string][]string
	dropped     []string
	failWith    string
}

func (r *recordingPlugins) ProvisionPackPlugin(unit plugin.Unit, decl *PluginUnitDeclaration) ([]string, error) {
	if r.failWith != "" {
		return nil, fmt.Errorf("%s", r.failWith)
	}
	ids := decl.Names()
	if r.provisioned == nil {
		r.provisioned = map[string][]string{}
	}
	r.provisioned[unit.Name] = ids
	return ids, nil
}

func (r *recordingPlugins) DropPackPlugin(unit plugin.Unit) (int, string) {
	r.dropped = append(r.dropped, unit.Name)
	delete(r.provisioned, unit.Name)
	return 1, ""
}

func (r *recordingPlugins) PackPluginServed(name string) (bool, []string) {
	ids, ok := r.provisioned[name]
	return ok, ids
}

// runtimeRows mirrors what a live host reports, so the state surface can be tested without a
// child process. The pack-declared row is the one an operator needs to see.
func (r *recordingPlugins) PackPluginRuntimes() []PluginRuntimeState {
	var rows []PluginRuntimeState
	for name := range r.provisioned {
		rows = append(rows, PluginRuntimeState{
			Domain: name, Bundle: "code-pack", Running: true,
			Grants: []string{"net.connect(10.0.0.0/8)"}, FromPack: true,
		})
	}
	return rows
}

func envWithPlugins(t *testing.T, recorder PluginProvisioner) *pluginTestEnv {
	t.Helper()
	env := newPluginTestEnv(t, false)
	env.pluginsProvisioner = recorder
	env.plugins = NewPluginHandler(env.table, env.bundles, env.roles, env.tools, env.mcp, recorder, env.switches, nil, zap.NewNop())
	return env
}

// writeCodePack puts a single-plugin pack inside the test environment's bundles root.
func writeCodePack(t *testing.T, env *pluginTestEnv) string {
	t.Helper()
	dir := filepath.Join(env.bundles, "code-pack")
	writeExecutable(t, filepath.Join(dir, "bin", "refplugin"))
	writeTestFile(t, filepath.Join(dir, "plugins", "ref.yaml"), pluginDeclaration)
	writeTestFile(t, filepath.Join(dir, "bundle.yaml"), strings.Join([]string{
		"id: code-pack", "name: 代码能力包", "version: 1.0.0",
		"description: 一个随包交付二进制的例子",
		"units:", "  - kind: plugin", "    path: plugins/ref.yaml",
	}, "\n")+"\n")
	return dir
}

func installPack(t *testing.T, env *pluginTestEnv, name string) (int, map[string]interface{}) {
	t.Helper()
	rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"`+name+`"}`)
	var body map[string]interface{}
	if rec.Code == http.StatusOK {
		body = decodeState(t, rec)
		return rec.Code, body
	}
	return rec.Code, map[string]interface{}{"raw": rec.Body.String()}
}

func TestPluginUnitInstallsDeclaredAndDisabled(t *testing.T) {
	recorder := &recordingPlugins{provisioned: map[string][]string{}}
	env := envWithPlugins(t, recorder)
	writeCodePack(t, env)

	code, body := installPack(t, env, "code-pack")
	if code != http.StatusOK {
		t.Fatalf("install = %d: %v", code, body)
	}
	if body["plugin_declared"] != float64(1) {
		t.Fatalf("install did not report the declared plugin unit: %v", body)
	}
	if body["plugin_started"] != false {
		t.Fatalf("installing a pack must not start its binary: %v", body)
	}
	if len(recorder.provisioned) != 0 {
		t.Fatalf("install provisioned a plugin: %v", recorder.provisioned)
	}

	state := decodeState(t, env.do(t, http.MethodGet, "/api/plugins", ""))
	units, _ := state["bundles"].([]interface{})
	if len(units) != 1 {
		t.Fatalf("the bundle never reached the state: %v", state["bundles"])
	}
	bundle := units[0].(map[string]interface{})
	listed := bundle["units"].([]interface{})[0].(map[string]interface{})
	if listed["id"] != "plugin/ref" || listed["enabled"] != false {
		t.Fatalf("unit view = %v, want plugin/ref disabled after install", listed)
	}
	if listed["served"] != false || !strings.Contains(fmt.Sprint(listed["reason"]), "已声明、未启用") {
		t.Fatalf("the console must say the plugin is declared but not running: %v", listed)
	}
}

func TestPluginSwitchRefusesAndRevertsWhenVerificationFails(t *testing.T) {
	recorder := &recordingPlugins{
		provisioned: map[string][]string{},
		failWith:    "插件 ref 与包里审阅过的清单不一致：缺少 ref.echo",
	}
	env := envWithPlugins(t, recorder)
	writeCodePack(t, env)
	if code, body := installPack(t, env, "code-pack"); code != http.StatusOK {
		t.Fatalf("install: %v", body)
	}

	rec := env.do(t, http.MethodPost, "/api/plugins/units/plugin/ref/enabled", `{"enabled":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a failed verification returned %d instead of a refusal: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "审阅过的清单不一致") {
		t.Fatalf("the refusal lost its reason: %s", rec.Body.String())
	}
	if len(recorder.provisioned) != 0 {
		t.Fatalf("a refused switch left capabilities registered: %v", recorder.provisioned)
	}
	// And the unit is off again: a switch that failed must not leave the console claiming the
	// operator enabled something that cannot be called.
	state := decodeState(t, env.do(t, http.MethodGet, "/api/plugins", ""))
	unit := state["bundles"].([]interface{})[0].(map[string]interface{})["units"].([]interface{})[0].(map[string]interface{})
	if unit["enabled"] != false {
		t.Fatalf("the switch was not reverted: %v", unit)
	}
}

func TestPluginSwitchOnRegistersAndOffAndUnplugDropIt(t *testing.T) {
	recorder := &recordingPlugins{provisioned: map[string][]string{}}
	env := envWithPlugins(t, recorder)
	writeCodePack(t, env)
	if code, body := installPack(t, env, "code-pack"); code != http.StatusOK {
		t.Fatalf("install: %v", body)
	}

	rec := env.do(t, http.MethodPost, "/api/plugins/units/plugin/ref/enabled", `{"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable = %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeState(t, rec)
	if body["plugin_applied"] != true {
		t.Fatalf("enable did not report the provisioning: %v", body)
	}
	caps, _ := body["plugin_capabilities"].([]interface{})
	if len(caps) != 1 || caps[0] != "ref.echo" {
		t.Fatalf("the verified capability set must come back to the console: %v", body["plugin_capabilities"])
	}
	if got := strings.Join(recorder.provisioned["ref"], ","); got != "ref.echo" {
		t.Fatalf("provisioned = %q", got)
	}
	// A running third-party binary has to be visible from the same screen that installed it.
	state := decodeState(t, env.do(t, http.MethodGet, "/api/plugins", ""))
	rows, _ := state["pluginHost"].([]interface{})
	if len(rows) != 1 {
		t.Fatalf("the state must list the live plugin instance, got %v", state["pluginHost"])
	}
	row := rows[0].(map[string]interface{})
	if row["domain"] != "ref" || row["bundle"] != "code-pack" || row["running"] != true || row["fromPack"] != true {
		t.Fatalf("plugin runtime row = %v", row)
	}
	unit := decodeState(t, env.do(t, http.MethodGet, "/api/plugins", ""))["bundles"].([]interface{})[0].(map[string]interface{})["units"].([]interface{})[0].(map[string]interface{})
	if unit["served"] != true {
		t.Fatalf("a provisioned plugin must report served: %v", unit)
	}

	env.do(t, http.MethodPost, "/api/plugins/units/plugin/ref/enabled", `{"enabled":false}`)
	if len(recorder.provisioned) != 0 {
		t.Fatalf("switching off left the plugin provisioned: %v", recorder.provisioned)
	}

	// Unplugging the pack takes the same thing back even while the unit is enabled, so no
	// capability can outlive the pack that shipped the binary providing it.
	writeCodePack(t, env)
	if code, body := installPack(t, env, "code-pack"); code != http.StatusOK {
		t.Fatalf("re-install: %v", body)
	}
	env.do(t, http.MethodPost, "/api/plugins/units/plugin/ref/enabled", `{"enabled":true}`)
	uninstall := decodeState(t, env.do(t, http.MethodDelete, "/api/plugins/bundles/code-pack", ""))
	if uninstall["plugin_removed"] != float64(1) {
		t.Fatalf("uninstall did not report removing the plugin: %v", uninstall)
	}
	if len(recorder.provisioned) != 0 {
		t.Fatalf("uninstall left the plugin callable: %v", recorder.provisioned)
	}
}

func TestInstallRefusesAnUnusablePluginDeclaration(t *testing.T) {
	recorder := &recordingPlugins{provisioned: map[string][]string{}}
	env := envWithPlugins(t, recorder)
	dir := writeCodePack(t, env)
	// Point the declaration at a binary the pack does not contain.
	writeTestFile(t, filepath.Join(dir, "plugins", "ref.yaml"),
		"pluginId: ref\nbinary: /bin/sh\ncapabilities:\n  - id: ref.echo\n    class: readonly\n")

	code, body := installPack(t, env, "code-pack")
	if code != http.StatusBadRequest {
		t.Fatalf("an unusable plugin declaration was accepted: %d %v", code, body)
	}
	if _, present := env.table.Unit("plugin/ref"); present {
		t.Fatal("a refused install still put the unit in the table")
	}
	if len(recorder.provisioned) != 0 {
		t.Fatalf("a refused install touched the live host: %v", recorder.provisioned)
	}
}

func TestStandalonePluginUnitIsRefused(t *testing.T) {
	// Plugin units arrive from packs only: the owning bundle is what lets an unplug remove the
	// trust domain, and there is no built-in plugins directory to scan.
	env := envWithPlugins(t, &recordingPlugins{provisioned: map[string][]string{}})
	dir := packWith(t, pluginDeclaration)
	unit := plugin.Unit{ID: "plugin/ref", Kind: plugin.KindPlugin, Name: "ref", Path: filepath.Join(dir, "plugins", "ref.yaml"), Enabled: true}
	_, message, ok := env.plugins.applyPluginSwitch(unit)
	if ok || !strings.Contains(message, "能力包") {
		t.Fatalf("a standalone plugin unit must be refused with the reason, got ok=%v message=%q", ok, message)
	}
}

func TestPluginSwitchWithoutAProvisionerIsRefused(t *testing.T) {
	env := newPluginTestEnv(t, false)
	writeCodePack(t, env)
	if code, body := installPack(t, env, "code-pack"); code != http.StatusOK {
		t.Fatalf("install with no provisioner wired: %v", body)
	}
	unit := decodeState(t, env.do(t, http.MethodGet, "/api/plugins", ""))["bundles"].([]interface{})[0].(map[string]interface{})["units"].([]interface{})[0].(map[string]interface{})
	if unit["served"] != false || !strings.Contains(fmt.Sprint(unit["reason"]), "未接入插件宿主") {
		t.Fatalf("the state must name the missing wiring: %v", unit)
	}
	rec := env.do(t, http.MethodPost, "/api/plugins/units/plugin/ref/enabled", `{"enabled":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("enabling without a provisioner returned %d: %s", rec.Code, rec.Body.String())
	}
}

// The packaging layer resolves the same `binary:` key to digest the executable, and it does so
// without importing this package's schema knowledge. Two derivations of one path is a split waiting
// to happen - so the two are compared here, on the same declaration, rather than assumed equal.
func TestPluginCompanionAgreesWithTheLoader(t *testing.T) {
	dir := packWith(t, pluginDeclaration)
	decl, err := loadPack(t, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	companion, err := plugin.PluginBinaryPath(filepath.Join(dir, "plugins", "ref.yaml"))
	if err != nil {
		t.Fatalf("companion: %v", err)
	}
	if companion != decl.Binary {
		t.Fatalf("the packaging layer resolved %q and the loader %q", companion, decl.Binary)
	}
	// Revocation addresses a build, so the digest on the identity is the binary's own - not the
	// declaration-plus-binary pair the unit's drift fingerprint uses.
	want, err := plugin.Digest(companion)
	if err != nil {
		t.Fatal(err)
	}
	if decl.BinaryDigest != want {
		t.Fatalf("decl.BinaryDigest = %q, want the binary digest %q", decl.BinaryDigest, want)
	}
	spec := decl.SpecFor(decl.Capability[0])
	if spec.ArtifactDigest != want || spec.Publisher != "ref" {
		t.Fatalf("the registered capability carries no provenance a revocation could name: %+v", spec)
	}

	// Editing only the binary changes the fingerprint the table records, and leaves the
	// declaration's own digest alone.
	if err := os.WriteFile(companion, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	after, err := loadPack(t, dir)
	if err != nil {
		t.Fatalf("reload after the binary changed: %v", err)
	}
	if after.BinaryDigest == want {
		t.Fatal("the binary digest survived the binary changing, so a revoked build stays callable")
	}
}
