package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, ManifestFileName)
	writeFile(t, path, body)
	return path
}

// TestResolveConfinesEveryUnitToTheBundleDirectory: a manifest is operator-supplied content,
// so the path escape check is the difference between "install a pack" and "install an
// arbitrary file into an arbitrary place".
func TestResolveConfinesEveryUnitToTheBundleDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "..esc.yaml"), "name: escape\n")
	m, err := LoadManifest(writeManifest(t, dir, `id: sneaky
version: 1.0.0
units:
  - kind: role
    path: ../..esc.yaml
`))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if _, err := m.Resolve(); err == nil {
		t.Fatalf("Resolve accepted a path that escapes the bundle directory")
	} else if !strings.Contains(err.Error(), "bundle sneaky") {
		t.Fatalf("escape error does not name the bundle: %v", err)
	}
}

// TestResolveChecksShapeNotJustExistence: the kinds disagree about file vs directory, and a
// manifest that gets it wrong must fail at install time instead of producing a capability
// that never shows up in a listing.
func TestResolveChecksShapeNotJustExistence(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		setup   func(t *testing.T, dir string)
		wantErr string
	}{
		{
			name: "skill given a file",
			body: "id: b\nversion: 1.0.0\nunits:\n  - kind: skill\n    path: s.md\n",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, "s.md"), "not a skill dir\n")
			},
			wantErr: "must be a directory",
		},
		{
			name: "role given a directory",
			body: "id: b\nversion: 1.0.0\nunits:\n  - kind: role\n    path: r\n",
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "r"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "must be a file",
		},
		{
			name:    "skill directory without SKILL.md",
			body:    "id: b\nversion: 1.0.0\nunits:\n  - kind: skill\n    path: s\n",
			setup:   func(t *testing.T, dir string) { _ = os.MkdirAll(filepath.Join(dir, "s"), 0o755) },
			wantErr: "SKILL.md",
		},
		{
			name:    "unknown kind",
			body:    "id: b\nversion: 1.0.0\nunits:\n  - kind: workflow\n    path: w.yaml\n",
			setup:   func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "w.yaml"), "name: w\n") },
			wantErr: "unknown plugin kind",
		},
		{
			name:    "path that does not exist",
			body:    "id: b\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/gone.yaml\n",
			setup:   func(t *testing.T, dir string) {},
			wantErr: "no such file",
		},
		{
			name:    "no units at all",
			body:    "id: b\nversion: 1.0.0\nunits: []\n",
			setup:   func(t *testing.T, dir string) {},
			wantErr: "no units",
		},
		{
			name:    "no version",
			body:    "id: b\nunits:\n  - kind: role\n    path: r.yaml\n",
			setup:   func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "r.yaml"), "name: r\n") },
			wantErr: "no version",
		},
		{
			name:    "no id",
			body:    "version: 1.0.0\nunits:\n  - kind: role\n    path: r.yaml\n",
			setup:   func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "r.yaml"), "name: r\n") },
			wantErr: "no id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			m, err := LoadManifest(writeManifest(t, dir, tc.body))
			if err != nil {
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("LoadManifest error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			_, err = m.Resolve()
			if err == nil {
				t.Fatalf("Resolve accepted a %s manifest", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestResolveDerivesNamesAndStampsOwnership is what lets a pack of four file kinds land as
// four addressable units without the manifest repeating the name it already used in the path.
func TestResolveDerivesNamesAndStampsOwnership(t *testing.T) {
	b := installSample(t, NewTable(), "webapp", "1.0.0")
	if b.Name != "webapp pack" || b.Version != "1.0.0" {
		t.Fatalf("bundle metadata = %q/%q", b.Name, b.Version)
	}
	wantIDs := []string{"role/webapp-lead", "agent/webapp", "skill/webapp-triage", "tool/webapp-scan", "plugin/webapp"}
	got := make([]string, 0, len(b.Units))
	for _, u := range b.Units {
		got = append(got, u.ID)
		if u.Bundle != "webapp" {
			t.Errorf("unit %s is not stamped with its bundle: %q", u.ID, u.Bundle)
		}
		if !filepath.IsAbs(u.Path) {
			t.Errorf("unit %s has a relative path %q", u.ID, u.Path)
		}
		if len(u.Digest) != 64 {
			t.Errorf("unit %s digest = %q, want sha256 hex", u.ID, u.Digest)
		}
	}
	if strings.Join(got, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("units = %v, want %v (sorted by declaration order)", got, wantIDs)
	}
}

// TestNameWithSeparatorIsRejected: "role" units whose name carries a slash would let an
// identity escape its kind bucket, which is the one thing the id format is for.
func TestNameWithSeparatorIsRejected(t *testing.T) {
	for _, name := range []string{"a/b", `a\b`, "", "   "} {
		if _, err := NewUnit(KindRole, name, "/tmp/x.yaml"); err == nil {
			t.Errorf("NewUnit accepted role name %q", name)
		}
	}
	if _, err := NewUnit(Kind("bogus"), "x", "/tmp/x.yaml"); err == nil {
		t.Errorf("NewUnit accepted an unknown kind")
	}
	if _, err := NewUnit(KindRole, "x", "  "); err == nil {
		t.Errorf("NewUnit accepted an empty path")
	}
}

// TestBundleValidateRejectsForeignAndDuplicateUnits before anything is published.
func TestBundleValidateRejectsForeignAndDuplicateUnits(t *testing.T) {
	u, err := NewUnit(KindRole, "r1", "/tmp/r1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dup := u
	if err := (&Bundle{ID: "b", Dir: "/tmp/b", Units: []Unit{u, dup}}).Validate(); err == nil {
		t.Errorf("Validate accepted a bundle declaring %s twice", u.ID)
	}
	foreign := u
	foreign.Bundle = "someone-else"
	if err := (&Bundle{ID: "b", Dir: "/tmp/b", Units: []Unit{foreign}}).Validate(); err == nil {
		t.Errorf("Validate accepted a unit owned by another bundle")
	}
	if err := (&Bundle{ID: "b", Dir: "/tmp/b"}).Validate(); err == nil {
		t.Errorf("Validate accepted a bundle with no units")
	}
	if err := (&Bundle{Dir: "/tmp/b", Units: []Unit{u}}).Validate(); err == nil {
		t.Errorf("Validate accepted a bundle with no id")
	}
	// The legal case stamps Bundle for the caller.
	b := &Bundle{ID: "b", Dir: "/tmp/b", Units: []Unit{u}}
	if err := b.Validate(); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}
	if b.Units[0].Bundle != "b" {
		t.Fatalf("Bundle not stamped onto its units: %q", b.Units[0].Bundle)
	}
}

// TestUnitIdentityIsDerivedNotTyped guards the one format the whole table is keyed on.
func TestUnitIdentityIsDerivedNotTyped(t *testing.T) {
	u, err := NewUnit(KindSkill, "triage", "/tmp/skills/triage")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != "skill/triage" {
		t.Fatalf("ID = %q", u.ID)
	}
	if err := u.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for name, mutate := range map[string]func(*Unit){
		"id does not match kind": func(u *Unit) { u.Kind = KindRole },
		"id does not match name": func(u *Unit) { u.Name = "other" },
		"empty identity":         func(u *Unit) { u.ID = "" },
		"no path":                func(u *Unit) { u.Path = " " },
		"id without a separator": func(u *Unit) { u.ID = "skilltriage" },
	} {
		bad := u
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("Validate accepted a unit with %s", name)
		}
	}
}

// A pack found through a relative bundles root still has to hand its consumers absolute paths: the
// plug-in host refuses to resolve a binary against a working directory, and the unit path is what
// the console, the digest and the capability registration all open files with.
func TestManifestFromARelativeDirectoryYieldsAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "bundles", "rel-pack")
	if err := os.MkdirAll(filepath.Join(pack, "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pack, "roles", "rel.yaml"), "name: rel\nuser_prompt: x\n")
	writeFile(t, filepath.Join(pack, ManifestFileName),
		"id: rel-pack\nname: 相对路径包\nversion: 1.0.0\nunits:\n  - kind: role\n    path: roles/rel.yaml\n")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Cleanup(func() { _ = os.Chdir(wd) })

	m, err := LoadManifestDir(filepath.Join("bundles", "rel-pack"))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := m.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(bundle.Dir) {
		t.Fatalf("the pack directory is relative: %q", bundle.Dir)
	}
	for _, u := range bundle.Units {
		if !filepath.IsAbs(u.Path) {
			t.Fatalf("unit %s carries a relative path %q: a consumer would open it against whatever "+
				"directory the process happens to run from", u.ID, u.Path)
		}
		if rel, err := filepath.Rel(bundle.Dir, u.Path); err != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("unit %s left the pack directory: %q", u.ID, u.Path)
		}
	}
}
