package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The plugin kind's fingerprint has to cover the executable, or Drifted() answers "nobody edited
// this pack" about a unit whose binary was replaced after install.

func TestPluginUnitDigestCoversTheBinary(t *testing.T) {
	table := NewTable()
	bundle := installSample(t, table, "webapp", "1.0.0")

	var unit Unit
	for _, u := range bundle.Units {
		if u.Kind == KindPlugin {
			unit = u
		}
	}
	if unit.ID != "plugin/webapp" {
		t.Fatalf("sample bundle has no plugin unit: %#v", bundle.Units)
	}
	if len(unit.Digest) != 64 {
		t.Fatalf("digest = %q, want sha256 hex", unit.Digest)
	}
	if got := table.Drifted(); len(got) != 0 {
		t.Fatalf("a fresh install reports drift: %v", got)
	}

	// Replacing the executable - not the declaration - must be visible. This is the whole
	// difference between "we digest what may be called" and "we digest what will run".
	binary, err := PluginBinaryPath(unit.Path)
	if err != nil {
		t.Fatalf("companion: %v", err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := table.Drifted()
	if len(got) != 1 || !strings.Contains(got[0], "plugin/webapp") {
		t.Fatalf("an edited plugin binary reported drift %v, want one plugin/webapp entry", got)
	}

	// And the declaration still counts on its own: the pair is one fingerprint, not the last write.
	if err := os.WriteFile(unit.Path, []byte("pluginId: webapp\nbinary: bin/webapp-plugin\ncapabilities: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if again := table.Drifted(); len(again) != 1 {
		t.Fatalf("after editing both files the drift list should still name the unit once, got %v", again)
	}
}

func TestPluginCompanionResolutionRefusesAnUncontainedBinary(t *testing.T) {
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "..", "outside-binary")
	if err := os.WriteFile(outside, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(plugins, "..", "bin")
	if err := os.MkdirAll(link, 0o755); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(dir, "bin")
	if err := os.MkdirAll(escape, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{
		"binary: /bin/sh\n",
		"binary: ../outside-binary\n",
		"binary: bin/missing\n",
		"binary: bin\n",
		"pluginId: x\n",
	} {
		path := filepath.Join(plugins, "case.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := PluginBinaryPath(path); err == nil {
			t.Errorf("accepted declaration %q", body)
		}
	}

	// A declaration outside plugins/ cannot tell which pack owns it, so it is refused rather than
	// guessed at by walking up the tree.
	stray := filepath.Join(dir, "loose.yaml")
	if err := os.WriteFile(stray, []byte("binary: bin/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PluginBinaryPath(stray); err == nil || !strings.Contains(err.Error(), "plugins/") {
		t.Fatalf("a declaration outside plugins/ was resolved anyway: %v", err)
	}

	// A symbolic link placed inside the pack but pointing out of it is the same escape.
	writeTestFileForCompanion(t, filepath.Join(dir, "bin", "real"), "target")
	target := filepath.Join(dir, "..", "escape-target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "bin", "via-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(plugins, "link.yaml")
	if err := os.WriteFile(path, []byte("binary: bin/via-link\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PluginBinaryPath(path); err == nil || !strings.Contains(err.Error(), "outside the pack") {
		t.Fatalf("a symlinked binary outside the pack was accepted: %v", err)
	}
}

func writeTestFileForCompanion(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDigestPathsIsOrderAndNameSensitive(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	renamed := filepath.Join(dir, "c")
	for _, p := range []string{a, b, renamed} {
		if err := os.WriteFile(p, []byte("same bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := DigestPaths(a, b)
	if err != nil {
		t.Fatal(err)
	}
	swapped, err := DigestPaths(b, a)
	if err != nil {
		t.Fatal(err)
	}
	if first == swapped {
		t.Fatal("the digest ignores which file comes first, so swapping roles is invisible")
	}
	single, err := Digest(a)
	if err != nil {
		t.Fatal(err)
	}
	if single == first {
		t.Fatal("a two-file digest must not equal the digest of one of its files")
	}
	if _, err := DigestPaths(dir); err == nil {
		t.Fatal("a directory was accepted as one member of a multi-file digest")
	}
	if _, err := DigestPaths(); err == nil {
		t.Fatal("an empty path list produced a digest")
	}
}
