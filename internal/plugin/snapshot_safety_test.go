package plugin

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func addUncopyableSocket(t *testing.T, dir string) {
	t.Helper()
	// macOS limits Unix socket paths to 104 bytes, so create it in a short temp directory and
	// rename the entry into the fixture after binding.
	short, err := os.MkdirTemp("", "sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	path := filepath.Join(short, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("Unix socket unavailable: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	if err := os.Rename(path, filepath.Join(dir, "z-socket")); err != nil {
		t.Fatal(err)
	}
}

func TestFailedResnapshotPreservesPreviousVersion(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "pack")
	writePackFile(t, filepath.Join(pack, "a.txt"), "original", 0o644)
	snap, err := SnapshotBundle(root, "pack", "1", pack)
	if err != nil {
		t.Fatal(err)
	}
	writePackFile(t, filepath.Join(pack, "a.txt"), "replacement", 0o644)
	addUncopyableSocket(t, pack)
	if _, err := SnapshotBundle(root, "pack", "1", pack); err == nil {
		t.Fatal("unsupported socket accepted")
	}
	if body, err := os.ReadFile(filepath.Join(snap, "a.txt")); err != nil || string(body) != "original" {
		t.Fatalf("failed replacement destroyed old snapshot: %q %v", body, err)
	}
	if versions := PreviousVersions(root, "pack"); !reflect.DeepEqual(versions, []string{"1"}) {
		t.Fatalf("incomplete snapshot advertised: %v", versions)
	}
}

func TestFailedRestorePreservesAllLiveFiles(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "pack")
	writePackFile(t, filepath.Join(pack, "a.txt"), "old", 0o644)
	snap, err := SnapshotBundle(root, "pack", "1", pack)
	if err != nil {
		t.Fatal(err)
	}
	writePackFile(t, filepath.Join(pack, "a.txt"), "live", 0o644)
	writePackFile(t, filepath.Join(pack, "scratch.txt"), "mine", 0o644)
	addUncopyableSocket(t, snap)
	if _, err := RestoreBundle(root, "pack", "1", pack); err == nil {
		t.Fatal("unsupported snapshot accepted")
	}
	for file, want := range map[string]string{"a.txt": "live", "scratch.txt": "mine"} {
		if body, err := os.ReadFile(filepath.Join(pack, file)); err != nil || string(body) != want {
			t.Fatalf("failed restore changed %s: %q %v", file, body, err)
		}
	}
}

func TestRestoreRefusesLiveSymlinkOutsidePack(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "pack")
	writePackFile(t, filepath.Join(pack, "a.txt"), "old", 0o644)
	if _, err := SnapshotBundle(root, "pack", "1", pack); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "a.txt")
	writePackFile(t, outside, "external", 0o644)
	if err := os.Remove(filepath.Join(pack, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(pack, "a.txt")); err != nil {
		t.Skip(err)
	}
	if _, err := RestoreBundle(root, "pack", "1", pack); err == nil {
		t.Fatal("restore accepted escaping destination symlink")
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "external" {
		t.Fatalf("outside file overwritten: %q %v", body, err)
	}
}

func TestSnapshotRefusesEscapingLayoutAndSourceLinks(t *testing.T) {
	for _, layout := range []bool{false, true} {
		t.Run(map[bool]string{false: "source", true: "layout"}[layout], func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			pack := filepath.Join(root, "pack")
			writePackFile(t, filepath.Join(pack, "a.txt"), "pack", 0o644)
			writePackFile(t, filepath.Join(outside, "a.txt"), "external", 0o644)
			link, target := filepath.Join(pack, "escape"), outside
			if layout {
				link = filepath.Join(root, PreviousDirName)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skip(err)
			}
			if _, err := SnapshotBundle(root, "pack", "1", pack); err == nil {
				t.Fatal("escaping link accepted")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatalf("snapshot wrote outside bundles root: %v %v", entries, err)
			}
		})
	}
}

func TestSnapshotRelocatesInternalAbsoluteSymlinks(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "pack")
	writePackFile(t, filepath.Join(pack, "a.txt"), "original", 0o644)
	if err := os.Symlink(filepath.Join(pack, "a.txt"), filepath.Join(pack, "alias")); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink("alias", filepath.Join(pack, "chain")); err != nil {
		t.Skip(err)
	}
	snap, err := SnapshotBundle(root, "pack", "1", pack)
	if err != nil {
		t.Fatal(err)
	}
	writePackFile(t, filepath.Join(pack, "a.txt"), "live", 0o644)
	if body, err := os.ReadFile(filepath.Join(snap, "alias")); err != nil || string(body) != "original" {
		t.Fatalf("snapshot link still references live pack: %q %v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(snap, "chain")); err != nil || string(body) != "original" {
		t.Fatalf("snapshot link chain broken: %q %v", body, err)
	}
	if _, err := RestoreBundle(root, "pack", "1", pack); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(pack, "alias")); err != nil || string(body) != "original" {
		t.Fatalf("restored link broken: %q %v", body, err)
	}
}

func TestSnapshotRefusesTraversalThroughInternalDirectoryLink(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "pack")
	writePackFile(t, filepath.Join(pack, "file"), "inside", 0o644)
	writePackFile(t, filepath.Join(root, "file"), "outside", 0o644)
	if err := os.Symlink(".", filepath.Join(pack, "alias")); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink("alias/../file", filepath.Join(pack, "escape")); err != nil {
		t.Skip(err)
	}
	if _, err := SnapshotBundle(root, "pack", "1", pack); err == nil {
		t.Fatal("cleaning .. concealed an escaping link")
	}
}

func TestSnapshotStorageRefusesInternalDirectoryAliases(t *testing.T) {
	for _, segment := range []string{PreviousDirName, filepath.Join(PreviousDirName, "pack"), filepath.Join(PreviousDirName, "pack", "1")} {
		t.Run(segment, func(t *testing.T) {
			root := t.TempDir()
			pack, other := filepath.Join(root, "pack"), filepath.Join(root, "other")
			writePackFile(t, filepath.Join(pack, "file"), "pack", 0o644)
			writePackFile(t, filepath.Join(other, "file"), "other", 0o644)
			alias := filepath.Join(root, segment)
			if err := os.MkdirAll(filepath.Dir(alias), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other, alias); err != nil {
				t.Skip(err)
			}
			if _, err := SnapshotBundle(root, "pack", "1", pack); err == nil {
				t.Fatal("snapshot storage redirected into a live pack")
			}
			if _, err := RestoreBundle(root, "pack", "1", pack); err == nil {
				t.Fatal("restore accepted aliased snapshot storage")
			}
			if body, err := os.ReadFile(filepath.Join(other, "file")); err != nil || string(body) != "other" {
				t.Fatalf("aliased live pack changed: %q %v", body, err)
			}
		})
	}
}

func TestDirectoryPublicationFailureRestoresBackup(t *testing.T) {
	dir := t.TempDir()
	writePackFile(t, filepath.Join(dir, "live", "file"), "original", 0o644)
	parent, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := replaceDirectory(parent, "missing-stage", "live"); err == nil {
		t.Fatal("publication of missing stage succeeded")
	}
	if body, err := os.ReadFile(filepath.Join(dir, "live", "file")); err != nil || string(body) != "original" {
		t.Fatalf("failed rename did not restore backup: %q %v", body, err)
	}
}
