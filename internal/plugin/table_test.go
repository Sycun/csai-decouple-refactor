package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// sampleBundleDir builds a bundle directory on disk and returns its path:
// one role, one agent markdown, one skill directory, one tool recipe.
func sampleBundleDir(t *testing.T, id, version string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	writeFile(t, filepath.Join(dir, "roles", id+"-lead.yaml"), "name: "+id+"-lead\nenabled: true\n")
	writeFile(t, filepath.Join(dir, "agents", id+".md"), "---\nname: "+id+"\n---\nbody\n")
	writeFile(t, filepath.Join(dir, "skills", id+"-triage", "SKILL.md"), "---\nname: "+id+"-triage\n---\nsteps\n")
	writeFile(t, filepath.Join(dir, "tools", id+"-scan.yaml"), "name: "+id+"-scan\ncommand: /bin/true\n")
	writeFile(t, filepath.Join(dir, "bin", id+"-plugin"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "bin", id+"-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The binary is resolved against the pack directory (not the declaration's own folder), which is
	// what makes "must stay inside the pack" a single rule a reader can check.
	writeFile(t, filepath.Join(dir, "plugins", id+".yaml"), "pluginId: "+id+"\nbinary: bin/"+id+"-plugin\ncapabilities:\n  - id: "+id+".scan\n    class: readonly\n")
	writeFile(t, filepath.Join(dir, ManifestFileName), fmt.Sprintf(`id: %s
name: %s pack
version: %s
description: role pack used by the plugin tests
units:
  - kind: role
    path: roles/%s-lead.yaml
  - kind: agent
    path: agents/%s.md
  - kind: skill
    path: skills/%s-triage
  - kind: tool
    path: tools/%s-scan.yaml
  - kind: plugin
    path: plugins/%s.yaml
`, id, id, version, id, id, id, id, id))
	return dir
}

func installSample(t *testing.T, table *Table, id, version string) *Bundle {
	t.Helper()
	m, err := LoadManifestDir(sampleBundleDir(t, id, version))
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
	return b
}

// TestInstallTakesEffectWithoutAProcessRestart is the whole point of the package: four
// different kinds of capability become readable from one table the moment a bundle lands.
func TestInstallTakesEffectWithoutAProcessRestart(t *testing.T) {
	table := NewTable()
	if got := table.Units(KindRole); len(got) != 0 {
		t.Fatalf("fresh table has %d roles", len(got))
	}
	start := table.Generation()

	b := installSample(t, table, "webapp", "1.0.0")
	if table.Generation() <= start {
		t.Fatalf("generation did not move: %d -> %d", start, table.Generation())
	}

	for _, kind := range Kinds {
		want := 0
		if kind != KindMCP {
			want = 1
		}
		if got := len(table.Units(kind)); got != want {
			t.Errorf("%s units = %d, want %d", kind, got, want)
		}
	}
	if got := table.EnabledPaths(KindRole); len(got) != 1 || !strings.HasSuffix(got[0], "webapp-lead.yaml") {
		t.Fatalf("role paths = %v", got)
	}
	// Names come from the file, matching how the shipped roles/ directory keys them today.
	if _, ok := table.ByName(KindRole, "webapp-lead"); !ok {
		t.Fatalf("role by name missed: %#v", b)
	}
	if got := table.Drifted(); len(got) != 0 {
		t.Fatalf("fresh install reports drift: %v", got)
	}
}

// TestConcurrentReadersNeverTear is the regression this package exists to prevent: the role
// API used to write map[string]RoleConfig in place while run paths read it unguarded, so a
// hot swap could be observed half-applied - and an in-place map write racing a read makes
// the runtime throw "concurrent map read and map write". Run this with -race.
//
// Every t.TempDir/t.Fatalf call stays in the main goroutine: a fatal from a worker would
// call Goexit and the race being tested for would never be observed.
func TestConcurrentReadersNeverTear(t *testing.T) {
	table := NewTable()
	installSample(t, table, "baseline", "1.0.0")

	bundles := make([]*Bundle, 0, 8)
	for i := 0; i < 8; i++ {
		m, err := LoadManifestDir(sampleBundleDir(t, fmt.Sprintf("pack%d", i), "1.0.0"))
		if err != nil {
			t.Fatalf("manifest: %v", err)
		}
		b, err := m.Resolve()
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		bundles = append(bundles, b)
	}

	const rounds = 300
	var wg sync.WaitGroup
	var writeErr error
	stop := make(chan struct{})

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for n := 0; n < rounds; n++ {
				roles := table.Units(KindRole)
				paths := table.EnabledPaths(KindRole)
				// A snapshot is either the old set or the new one, never a mixture: the
				// baseline role is present in every state this table can hold, so seeing a
				// list without it means a torn read.
				var haveBaseline bool
				for _, u := range roles {
					if u.Name == "baseline-lead" {
						haveBaseline = true
					}
				}
				if !haveBaseline {
					t.Errorf("worker %d observed a role set without the baseline unit: %v", worker, names(roles))
					return
				}
				if len(paths) == 0 {
					t.Errorf("worker %d observed zero enabled roles while baseline is installed", worker)
					return
				}
			}
		}(i)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < rounds; n++ {
			if err := table.InstallBundle(bundles[n%len(bundles)]); err != nil {
				writeErr = err
				return
			}
		}
		close(stop)
	}()

	wg.Wait()
	if writeErr != nil {
		t.Fatalf("writer: %v", writeErr)
	}
	if got := len(table.Bundles()); got != 9 {
		t.Fatalf("bundles = %d, want 9 (baseline + 8 swapped in)", got)
	}
}

func names(units []Unit) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		out = append(out, u.Name)
	}
	return out
}

// TestTwoWritesCannotLoseOneAnother: the mutex only serialises writers, so two different
// bundles installed at the same time must both be present afterwards.
func TestTwoWritesCannotLoseOneAnother(t *testing.T) {
	table := NewTable()
	dirs := make([]string, 6)
	for i := range dirs {
		dirs[i] = sampleBundleDir(t, fmt.Sprintf("p%d", i), "1.0.0")
	}

	var wg sync.WaitGroup
	errs := make([]error, len(dirs))
	for i, dir := range dirs {
		wg.Add(1)
		go func(i int, dir string) {
			defer wg.Done()
			m, err := LoadManifestDir(dir)
			if err != nil {
				errs[i] = err
				return
			}
			b, err := m.Resolve()
			if err != nil {
				errs[i] = err
				return
			}
			errs[i] = table.InstallBundle(b)
		}(i, dir)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent install %d: %v", i, err)
		}
	}
	if got := len(table.Bundles()); got != 6 {
		t.Fatalf("bundles = %d, want 6", got)
	}
	if got := len(table.Units(KindRole)); got != 6 {
		t.Fatalf("roles = %d, want 6", got)
	}
}

// TestInstallRefusesToShadowAnExistingUnit is the safety property: a bundle may not replace
// a built-in role, and a directory scan may not replace an installed one. Either direction
// has to leave the table exactly as it was, because "half-applied" is worse than refused.
func TestInstallRefusesToShadowAnExistingUnit(t *testing.T) {
	t.Run("bundle cannot shadow a scanned role", func(t *testing.T) {
		table := NewTable()
		local, err := NewUnit(KindRole, "webapp-lead", "/etc/passwd")
		if err != nil {
			t.Fatal(err)
		}
		if err := table.PutLocal(local); err != nil {
			t.Fatalf("PutLocal: %v", err)
		}
		before := table.Generation()

		m, err := LoadManifestDir(sampleBundleDir(t, "webapp", "1.0.0"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := m.Resolve()
		if err != nil {
			t.Fatal(err)
		}
		conflict := table.InstallBundle(b)
		var ce *ErrConflict
		if conflict == nil {
			t.Fatalf("install shadowed a local role: %v", conflict)
		}
		if !asConflict(conflict, &ce) {
			t.Fatalf("error is not an *ErrConflict: %#v", conflict)
		}
		if ce.ID != "role/webapp-lead" || ce.Owner != "" {
			t.Errorf("conflict = %+v, want role/webapp-lead owned by the scan", ce)
		}
		if table.Generation() != before {
			t.Errorf("a refused install moved the generation")
		}
		if u, ok := table.Unit("role/webapp-lead"); !ok || u.Path != "/etc/passwd" {
			t.Errorf("a refused install changed the held unit: %#v", u)
		}
		// The other three units of the refused bundle must not have landed either.
		if got := len(table.Units(KindSkill)); got != 0 {
			t.Errorf("refused bundle installed %d skills", got)
		}
	})

	t.Run("scan cannot shadow an installed bundle", func(t *testing.T) {
		table := NewTable()
		installSample(t, table, "webapp", "1.0.0")
		before := table.Generation()

		scanned, err := NewUnit(KindRole, "webapp-lead", "/somewhere/else.yaml")
		if err != nil {
			t.Fatal(err)
		}
		err = table.PutLocal(scanned)
		var ce *ErrConflict
		if err == nil || !asConflict(err, &ce) {
			t.Fatalf("PutLocal returned %v, want an *ErrConflict naming webapp", err)
		}
		if ce.Owner != "webapp" {
			t.Errorf("conflict owner = %q, want webapp", ce.Owner)
		}
		if table.Generation() != before {
			t.Errorf("a refused scan moved the generation")
		}
	})
}

func asConflict(err error, target **ErrConflict) bool {
	ce, ok := err.(*ErrConflict)
	if ok {
		*target = ce
	}
	return ok
}

// TestUpgradeReplacesOnlyItsOwnUnits: reinstalling the same bundle id is an upgrade - units
// the new manifest dropped must disappear, and another bundle's units must survive.
func TestUpgradeReplacesOnlyItsOwnUnits(t *testing.T) {
	table := NewTable()
	installSample(t, table, "webapp", "1.0.0")
	installSample(t, table, "cloud", "1.0.0")

	// Same bundle, new manifest: the role is renamed, so the old one has to go.
	replacement := sampleBundleDir(t, "webapp", "2.0.0")
	writeFile(t, filepath.Join(replacement, "roles", "webapp-recon.yaml"), "name: webapp-recon\nenabled: true\n")
	writeFile(t, filepath.Join(replacement, ManifestFileName), `id: webapp
name: webapp pack
version: 2.0.0
units:
  - kind: role
    path: roles/webapp-recon.yaml
`)
	m, err := LoadManifestDir(replacement)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := table.InstallBundle(b); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if _, ok := table.Unit("role/webapp-lead"); ok {
		t.Errorf("upgraded bundle kept the role its manifest no longer declares")
	}
	if _, ok := table.Unit("skill/webapp-triage"); ok {
		t.Errorf("upgraded bundle kept the skill its manifest no longer declares")
	}
	if _, ok := table.Unit("role/webapp-recon"); !ok {
		t.Errorf("upgraded bundle did not install its new role")
	}
	// The unrelated bundle is untouched.
	if _, ok := table.Unit("role/cloud-lead"); !ok {
		t.Errorf("upgrade evicted another bundle's unit")
	}
	if got := len(table.Bundles()); got != 2 {
		t.Fatalf("bundles = %d, want 2", got)
	}
}

// TestUninstallDetachesOnlyThatBundle and leaves the other kinds' units in place.
func TestUninstallDetachesOnlyThatBundle(t *testing.T) {
	table := NewTable()
	installSample(t, table, "webapp", "1.0.0")
	installSample(t, table, "cloud", "1.0.0")
	local, err := NewUnit(KindRole, "custom", filepath.Join(t.TempDir(), "custom.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := table.PutLocal(local); err != nil {
		t.Fatalf("PutLocal: %v", err)
	}

	if err := table.UninstallBundle("webapp"); err != nil {
		t.Fatalf("UninstallBundle: %v", err)
	}
	if _, ok := table.Bundle("webapp"); ok {
		t.Fatalf("bundle still listed")
	}
	for _, id := range []string{"role/webapp-lead", "agent/webapp", "skill/webapp-triage", "tool/webapp-scan"} {
		if _, ok := table.Unit(id); ok {
			t.Errorf("uninstalled bundle still holds %s", id)
		}
	}
	if _, ok := table.Unit("role/cloud-lead"); !ok {
		t.Errorf("uninstall took another bundle's unit")
	}
	if _, ok := table.Unit("role/custom"); !ok {
		t.Errorf("uninstall took a scanned unit")
	}
	if err := table.UninstallBundle("nope"); err == nil {
		t.Errorf("uninstall of an absent bundle returned nil")
	}
}

// TestUninstallLeavesSourcesReadableFromDisk: because no file is deleted, re-installing the
// same directory is one call, and the operator's copy of the pack is intact.
func TestReinstallAfterUninstallWorksFromTheSameDirectory(t *testing.T) {
	table := NewTable()
	dir := sampleBundleDir(t, "webapp", "1.0.0")
	load := func() *Bundle {
		m, err := LoadManifestDir(dir)
		if err != nil {
			t.Fatalf("LoadManifestDir: %v", err)
		}
		b, err := m.Resolve()
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		return b
	}
	if err := table.InstallBundle(load()); err != nil {
		t.Fatal(err)
	}
	if err := table.UninstallBundle("webapp"); err != nil {
		t.Fatal(err)
	}
	if err := table.InstallBundle(load()); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	if _, ok := table.Unit("role/webapp-lead"); !ok {
		t.Fatalf("re-install did not bring the role back")
	}
}

// TestScanCannotRemoveABundleUnit: RemoveLocal must refuse a unit somebody else owns.
func TestScanCannotRemoveABundleUnit(t *testing.T) {
	table := NewTable()
	installSample(t, table, "webapp", "1.0.0")
	if err := table.RemoveLocal("role/webapp-lead"); err == nil {
		t.Fatalf("RemoveLocal deleted a bundle-owned unit")
	}
	if _, ok := table.Unit("role/webapp-lead"); !ok {
		t.Fatalf("a refused removal still took the unit")
	}
}

// TestSetEnabledDoesNotMoveTheSource backs the UI's enable/disable switch: the run path
// filters on Enabled, so flipping it must change what is served without touching files.
func TestSetEnabledFlipsWhatIsServed(t *testing.T) {
	table := NewTable()
	installSample(t, table, "webapp", "1.0.0")
	if _, err := table.SetEnabled("role/webapp-lead", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if got := table.EnabledPaths(KindRole); len(got) != 0 {
		t.Fatalf("disabled role is still served: %v", got)
	}
	if got := table.Units(KindRole); len(got) != 1 {
		t.Fatalf("disabled role vanished from the listing: %v", got)
	}
	if _, err := table.SetEnabled("role/absent", true); err == nil {
		t.Errorf("SetEnabled on an absent unit returned nil")
	}
}

// TestDriftedReportsAnEditedSource: the table must be able to say "what is running is not
// what is on disk", or hot-plug is just a cache with extra steps.
func TestDriftedReportsAnEditedSource(t *testing.T) {
	table := NewTable()
	b := installSample(t, table, "webapp", "1.0.0")
	rolePath := ""
	for _, u := range b.Units {
		if u.Kind == KindRole {
			rolePath = u.Path
		}
	}
	if rolePath == "" {
		t.Fatalf("no role unit in %#v", b)
	}
	if got := table.Drifted(); len(got) != 0 {
		t.Fatalf("clean install drifts: %v", got)
	}

	writeFile(t, rolePath, "name: webapp-lead\nenabled: true\ndescription: edited after install\n")
	got := table.Drifted()
	if len(got) != 1 || !strings.Contains(got[0], "role/webapp-lead") {
		t.Fatalf("edited role not reported as drift: %v", got)
	}

	if err := os.Remove(rolePath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got = table.Drifted()
	if len(got) != 1 || !strings.Contains(got[0], "unreadable") {
		t.Fatalf("deleted role not reported as drift: %v", got)
	}
}
