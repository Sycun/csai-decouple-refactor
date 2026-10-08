package einoskill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/plugin"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	einoskillpkg "github.com/cloudwego/eino/adk/middlewares/skill"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test working directory")
		}
		dir = parent
	}
}

func scannedSkillTable(t *testing.T, skillsDir string) *plugin.Table {
	t.Helper()
	table := plugin.NewTable()
	units, err := plugin.ScanDir(plugin.KindSkill, skillsDir, nil)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	if len(units) < 5 {
		t.Fatalf("only %d skills scanned in %s (the factory tree keeps 5: the discipline skills plus the "+
			"format demo; the professional methods ship in bundles and are installed on request): a "+
			"broken scan would make the parity assertions below vacuous", len(units), skillsDir)
	}
	for _, u := range units {
		if err := table.PutLocal(u); err != nil {
			t.Fatalf("PutLocal %s: %v", u.ID, err)
		}
	}
	return table
}

// TestBackendMatchesEinoBackend is the drift guard for replacing a vendor implementation:
// the same skills/ directory must produce the same front matter, the same body and the same
// base directory through the table backend as through Eino's own filesystem backend.
// Eino's backend is the truth source here, so this compares against the vendor, not against a
// list someoneMaintained by hand.
func TestBackendMatchesEinoBackend(t *testing.T) {
	root := moduleRoot(t)
	skillsDir := filepath.Join(root, "skills")
	ctx := context.Background()

	loc, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		t.Fatalf("eino local backend: %v", err)
	}
	vendor, err := einoskillpkg.NewBackendFromFilesystem(ctx, &einoskillpkg.BackendFromFilesystemConfig{
		Backend: loc,
		BaseDir: skillsDir,
	})
	if err != nil {
		t.Fatalf("eino filesystem backend: %v", err)
	}
	mine, err := NewBackend(scannedSkillTable(t, skillsDir))
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}

	wantFM, err := vendor.List(ctx)
	if err != nil {
		t.Fatalf("vendor List: %v", err)
	}
	gotFM, err := mine.List(ctx)
	if err != nil {
		t.Fatalf("table List: %v", err)
	}
	if len(wantFM) < 5 {
		t.Fatalf("the vendor backend only found %d skills (the factory tree keeps 5); the comparison "+
			"would be meaningless", len(wantFM))
	}
	if len(gotFM) != len(wantFM) {
		t.Fatalf("List cardinality differs: table %d vs eino %d", len(gotFM), len(wantFM))
	}
	want := map[string]einoskillpkg.FrontMatter{}
	for _, fm := range wantFM {
		want[fm.Name] = fm
	}
	for _, fm := range gotFM {
		w, ok := want[fm.Name]
		if !ok {
			t.Errorf("table serves skill %q that the eino backend does not", fm.Name)
			continue
		}
		if w.Description != fm.Description {
			t.Errorf("skill %q description differs:\n  eino: %q\n  table: %q", fm.Name, w.Description, fm.Description)
		}
		if w.Context != fm.Context || w.Agent != fm.Agent || w.Model != fm.Model {
			t.Errorf("skill %q front matter differs: eino %+v table %+v", fm.Name, w, fm)
		}
	}

	// Body and base directory, per skill: this is what the model actually reads.
	for name := range want {
		wantSkill, err := vendor.Get(ctx, name)
		if err != nil {
			t.Fatalf("vendor Get %s: %v", name, err)
		}
		gotSkill, err := mine.Get(ctx, name)
		if err != nil {
			t.Fatalf("table Get %s: %v", name, err)
		}
		if gotSkill.Content != wantSkill.Content {
			t.Errorf("skill %q body differs (eino %d bytes, table %d bytes)", name, len(wantSkill.Content), len(gotSkill.Content))
		}
		if filepath.Base(gotSkill.BaseDirectory) != filepath.Base(wantSkill.BaseDirectory) {
			t.Errorf("skill %q base directory differs: %q vs %q", name, wantSkill.BaseDirectory, gotSkill.BaseDirectory)
		}
	}
	t.Logf("parity confirmed against the vendor backend over %d skills", len(wantFM))
}

// TestBundledSkillIsServedWithoutARestart is the point of the adapter: a skill that lives inside
// a bundle directory - not under skills/ - reaches the middleware the moment it is installed.
// Eino's single-BaseDir backend cannot express this at all.
func TestBundledSkillIsServedWithoutARestart(t *testing.T) {
	root := moduleRoot(t)
	table := scannedSkillTable(t, filepath.Join(root, "skills"))
	b, err := NewBackend(table)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before, err := b.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "phishing-triage")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: phishing-triage\ndescription: initial access triage\n---\n\n## Steps\n\n1. enumerate\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bundle.yaml"), []byte("id: initial-access\nversion: 1.0.0\nunits:\n  - kind: skill\n    path: skills/phishing-triage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := plugin.LoadManifestDir(dir)
	if err != nil {
		t.Fatalf("LoadManifestDir: %v", err)
	}
	bundle, err := m.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := table.InstallBundle(bundle); err != nil {
		t.Fatalf("InstallBundle: %v", err)
	}

	after, err := b.List(ctx)
	if err != nil {
		t.Fatalf("List after install: %v", err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("installed skill not listed: %d -> %d", len(before), len(after))
	}
	sk, err := b.Get(ctx, "phishing-triage")
	if err != nil {
		t.Fatalf("Get installed skill: %v", err)
	}
	if sk.Description != "initial access triage" {
		t.Errorf("description = %q", sk.Description)
	}
	if !strings.Contains(sk.Content, "## Steps") {
		t.Errorf("body not served: %q", sk.Content)
	}
	if sk.BaseDirectory != skillDir {
		t.Errorf("BaseDirectory = %q, want the bundle's own directory %q", sk.BaseDirectory, skillDir)
	}

	if err := table.UninstallBundle("initial-access"); err != nil {
		t.Fatalf("UninstallBundle: %v", err)
	}
	list, err := b.List(ctx)
	if err != nil {
		t.Fatalf("List after unplug: %v", err)
	}
	if len(list) != len(before) {
		t.Fatalf("unplugged skill still listed: %d vs %d", len(list), len(before))
	}
	if _, err := b.Get(ctx, "phishing-triage"); err == nil {
		t.Fatalf("Get still resolves an unplugged skill")
	}
}

// TestTabInBodyIsNotStripped: Eino's backend strips "N\t" line-number prefixes because its local
// filesystem backend adds them. Reading the file directly must not repeat that step, or every
// skill body containing a real tab loses its leading text.
func TestTabInBodyIsNotStripped(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "tabbed")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: tabbed\ndescription: d\n---\n\npath:\ttarget\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	table := plugin.NewTable()
	units, err := plugin.ScanDir(plugin.KindSkill, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("scanned %d units, want 1", len(units))
	}
	if err := table.PutLocal(units[0]); err != nil {
		t.Fatal(err)
	}
	b, err := NewBackend(table)
	if err != nil {
		t.Fatal(err)
	}
	sk, err := b.Get(context.Background(), "tabbed")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(sk.Content, "path:\ttarget") {
		t.Fatalf("tab-separated body was mangled: %q", sk.Content)
	}
}

// TestOneBadSkillFailsTheWholeList mirrors the vendor's fail-closed behaviour: skipping a broken
// skill would silently remove a capability from what the model is told about.
func TestOneBadSkillFailsTheWholeList(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	bad := filepath.Join(dir, "bad")
	for _, d := range []string{good, bad} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(good, "SKILL.md"), []byte("---\nname: good\ndescription: d\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("no front matter delimiter here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	table := plugin.NewTable()
	units, err := plugin.ScanDir(plugin.KindSkill, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		if err := table.PutLocal(u); err != nil {
			t.Fatal(err)
		}
	}
	b, err := NewBackend(table)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.List(context.Background())
	if err == nil {
		t.Fatalf("List tolerated a malformed SKILL.md")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Fatalf("error does not name the offending skill: %v", err)
	}
}

func TestDisabledSkillIsNotServedAndNilTableIsRefused(t *testing.T) {
	table := scannedSkillTable(t, filepath.Join(moduleRoot(t), "skills"))
	b, err := NewBackend(table)
	if err != nil {
		t.Fatal(err)
	}
	all, err := b.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	victim := all[0].Name
	if _, err := table.SetEnabled(plugin.UnitIDFor(plugin.KindSkill, table.Units(plugin.KindSkill)[0].Name), false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	after, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(after) != len(all)-1 {
		t.Fatalf("disabling a skill left %d of %d listed", len(after), len(all))
	}
	if _, err := b.Get(context.Background(), victim); err == nil {
		t.Fatalf("a disabled skill is still fetchable")
	}
	if _, err := NewBackend(nil); err == nil {
		t.Fatalf("NewBackend accepted a nil table")
	}
}
