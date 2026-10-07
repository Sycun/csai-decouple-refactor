package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// The snapshot layer is what "回滚" reads from, so its copies have to be faithful: same bytes,
// same executable bit, same symlinks. A rollback that restores a different pack than the one
// that was installed is worse than no rollback at all.

func writePackFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSnapshotBundleCopiesTreeAndPreservesMode(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "packs", "acme-pack")
	writePackFile(t, filepath.Join(src, ManifestFileName), "id: acme-pack\nversion: 1.0.0\nunits: []\n", 0o644)
	writePackFile(t, filepath.Join(src, "bin", "acme-scan"), "#!/bin/sh\necho hi\n", 0o755)
	if err := os.Symlink("acme-scan", filepath.Join(src, "bin", "latest")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dest, err := SnapshotBundle(root, "acme-pack", "1.0.0", src)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, PreviousDirName, "acme-pack", "1.0.0"); dest != want {
		t.Fatalf("snapshot landed at %q, want %q", dest, want)
	}
	body, err := os.ReadFile(filepath.Join(dest, "bin", "acme-scan"))
	if err != nil || string(body) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("binary not copied faithfully: %v / %q", err, body)
	}
	info, err := os.Stat(filepath.Join(dest, "bin", "acme-scan"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("executable bit lost in snapshot: %v", info.Mode())
	}
	link, err := os.Readlink(filepath.Join(dest, "bin", "latest"))
	if err != nil || link != "acme-scan" {
		t.Fatalf("symlink not preserved: %v / %q", err, link)
	}
	versions := PreviousVersions(root, "acme-pack")
	if len(versions) != 1 || versions[0] != "1.0.0" {
		t.Fatalf("PreviousVersions = %v, want [1.0.0]", versions)
	}
}

func TestSnapshotBundleRefusesUnusableVersions(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "pack")
	writePackFile(t, filepath.Join(src, "f.txt"), "x", 0o644)
	for _, version := range []string{"", "..", "../escape", ".hidden", "a/b", `a\b`} {
		if _, err := SnapshotBundle(root, "pack", version, src); err == nil {
			t.Fatalf("version %q was accepted; a manifest field must not decide where snapshots are written", version)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); err == nil {
		t.Fatal("a traversal version created a directory outside .previous")
	}
}

func TestSnapshotBundleReplacesStaleSameVersion(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "pack")
	writePackFile(t, filepath.Join(src, "old.txt"), "old", 0o644)
	if _, err := SnapshotBundle(root, "pack", "1.0.0", src); err != nil {
		t.Fatal(err)
	}
	// The directory was re-packed in place: one file removed, one changed.
	if err := os.Remove(filepath.Join(src, "old.txt")); err != nil {
		t.Fatal(err)
	}
	writePackFile(t, filepath.Join(src, "new.txt"), "new", 0o644)
	if _, err := SnapshotBundle(root, "pack", "1.0.0", src); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, PreviousDirName, "pack", "1.0.0")
	if _, err := os.Stat(filepath.Join(dest, "old.txt")); err == nil {
		t.Fatal("a stale file survived the re-snapshot; a rollback would restore content that was never installed")
	}
	if body, err := os.ReadFile(filepath.Join(dest, "new.txt")); err != nil || string(body) != "new" {
		t.Fatalf("re-snapshot did not pick up the new content: %v / %q", err, body)
	}
}

func TestSnapshotBundleRefusesToSnapshotItself(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "pack")
	writePackFile(t, filepath.Join(src, "f.txt"), "x", 0o644)
	dest, err := SnapshotBundle(root, "pack", "1.0.0", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotBundle(root, "pack", "1.0.1", dest); err == nil {
		t.Fatal("snapshotting a snapshot was accepted; the recursion has no meaning")
	}
}

func TestRestoreBundleOverwritesButKeepsExtraFiles(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "pack")
	writePackFile(t, filepath.Join(src, "bundle.yaml"), "id: pack\nversion: 1.0.0\n", 0o644)
	writePackFile(t, filepath.Join(src, "role.yaml"), "name: v1\n", 0o644)
	if _, err := SnapshotBundle(root, "pack", "1.0.0", src); err != nil {
		t.Fatal(err)
	}
	// v2 arrives in the live directory, and the operator also keeps a scratch file there.
	writePackFile(t, filepath.Join(src, "bundle.yaml"), "id: pack\nversion: 2.0.0\n", 0o644)
	writePackFile(t, filepath.Join(src, "role.yaml"), "name: v2\n", 0o644)
	writePackFile(t, filepath.Join(src, "scratch.txt"), "mine", 0o644)

	files, err := RestoreBundle(root, "pack", "1.0.0", src)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 {
		t.Fatalf("restore copied %d files, want 2", files)
	}
	if body, err := os.ReadFile(filepath.Join(src, "role.yaml")); err != nil || string(body) != "name: v1\n" {
		t.Fatalf("snapshot content was not restored: %v / %q", err, body)
	}
	if body, err := os.ReadFile(filepath.Join(src, "scratch.txt")); err != nil || string(body) != "mine" {
		t.Fatalf("restore deleted a file the snapshot never knew about: %v / %q", err, body)
	}
	if _, err := RestoreBundle(root, "pack", "9.9.9", src); err == nil {
		t.Fatal("restoring a version with no snapshot was accepted")
	}
}
