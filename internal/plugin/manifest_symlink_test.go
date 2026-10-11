package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestRejectsExternalSymlinkSources(t *testing.T) {
	for _, shape := range []string{"role", "skill", "skill-resource", "manifest"} {
		t.Run(shape, func(t *testing.T) {
			root := t.TempDir()
			pack := filepath.Join(root, "pack")
			outside := filepath.Join(root, "outside")
			if err := os.MkdirAll(outside, 0755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(outside, "SKILL.md"), "# external\n")
			writeFile(t, filepath.Join(outside, "role.yaml"), "name: external\n")
			body := "id: probe\nversion: '1'\nunits:\n  - kind: role\n    path: role.yaml\n"
			if shape == "skill" || shape == "skill-resource" {
				body = "id: probe\nversion: '1'\nunits:\n  - kind: skill\n    path: skill\n"
			}
			writeManifest(t, pack, body)
			link := func(target, path string) {
				t.Helper()
				if err := os.Symlink(target, path); err != nil {
					t.Skip(err)
				}
			}
			switch shape {
			case "role":
				link(filepath.Join(outside, "role.yaml"), filepath.Join(pack, "role.yaml"))
			case "skill":
				link(outside, filepath.Join(pack, "skill"))
			case "skill-resource":
				writeFile(t, filepath.Join(pack, "skill", "SKILL.md"), "# local\n")
				link(outside, filepath.Join(pack, "skill", "resources"))
			case "manifest":
				writeFile(t, filepath.Join(outside, ManifestFileName), body)
				if err := os.Remove(filepath.Join(pack, ManifestFileName)); err != nil {
					t.Fatal(err)
				}
				link(filepath.Join(outside, ManifestFileName), filepath.Join(pack, ManifestFileName))
			}
			m, err := LoadManifestDir(pack)
			if err != nil {
				if shape == "manifest" {
					return
				}
				t.Fatal(err)
			}
			if _, err := m.Resolve(); err == nil {
				t.Fatal("accepted source outside bundle")
			}
		})
	}
}

func TestManifestAllowsInternalFileSymlink(t *testing.T) {
	pack := t.TempDir()
	writeFile(t, filepath.Join(pack, "real.yaml"), "name: local\n")
	if err := os.Symlink("real.yaml", filepath.Join(pack, "role.yaml")); err != nil {
		t.Skip(err)
	}
	writeManifest(t, pack, "id: local\nversion: '1'\nunits:\n  - kind: role\n    path: role.yaml\n")
	m, err := LoadManifestDir(pack)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve(); err != nil {
		t.Fatal(err)
	}
}
