package skillpackage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPackageFileSymlinksStayWithinSkill(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "local")
	outside := t.TempDir()
	if err := os.MkdirAll(skill, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# local"), 0644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("unchanged"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(skill, "escape")); err != nil {
		t.Skip(err)
	}
	if _, err := ReadPackageFile(root, "local", "escape/secret", 100); err == nil {
		t.Fatal("read escaped skill")
	}
	for _, path := range []string{"escape/secret", "escape/new/nested.txt"} {
		if err := WritePackageFile(root, "local", path, []byte("changed")); err == nil {
			t.Fatalf("write escaped: %s", path)
		}
	}
	data, err := os.ReadFile(secret)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("outside file changed")
	}
	if err := WritePackageFile(root, "local", "resources/new/nested.txt", []byte("inside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("resources/new/nested.txt", filepath.Join(skill, "alias")); err != nil {
		t.Skip(err)
	}
	data, err = ReadPackageFile(root, "local", "alias", 100)
	if err != nil || string(data) != "inside" {
		t.Fatalf("internal link: %q %v", data, err)
	}
	if err := os.Symlink(filepath.Join(skill, "resources", "new", "nested.txt"), filepath.Join(skill, "absolute-alias")); err != nil {
		t.Skip(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeRoot, err := filepath.Rel(cwd, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackageFile(relativeRoot, "local", "absolute-alias", 100); err != nil {
		t.Fatalf("relative root with internal absolute link: %v", err)
	}
}
