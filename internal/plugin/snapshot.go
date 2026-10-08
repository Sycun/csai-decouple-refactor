package plugin

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PreviousDirName is the directory under the bundles root where the install path keeps one copy
// per installed version of a pack: <root>/.previous/<bundle id>/<version>/.
//
// The copy exists so "回滚" is a filesystem operation the operator can inspect, not a claim: the
// install endpoint takes a version from here and restores it. The name starts with a dot because
// every directory scanner in this package skips hidden entries, so a snapshot can never be read
// as a shippable pack (the catalogue, the boot scan and the install endpoint all stay away).
const PreviousDirName = ".previous"

// PreviousDirFor names the per-bundle snapshot parent inside one bundles root.
func PreviousDirFor(root, bundleID string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("能力包根目录为空")
	}
	if !usableLayoutName(bundleID) {
		return "", fmt.Errorf("能力包 ID %q 不能用作快照目录名", bundleID)
	}
	return filepath.Join(root, PreviousDirName, strings.TrimSpace(bundleID)), nil
}

// PreviousVersionDir names one snapshot. The version comes from a manifest, so it is refused
// unless it is a plain single path element: a version like "../../etc" must not decide where a
// snapshot is written or read.
func PreviousVersionDir(root, bundleID, version string) (string, error) {
	dir, err := PreviousDirFor(root, bundleID)
	if err != nil {
		return "", err
	}
	version = strings.TrimSpace(version)
	if !usableLayoutName(version) {
		return "", fmt.Errorf("能力包 %s 的版本 %q 不能用作快照目录名", bundleID, version)
	}
	return filepath.Join(dir, version), nil
}

// PreviousVersions lists the snapshot versions available for one bundle, sorted so the console
// renders the same list twice.
func PreviousVersions(root, bundleID string) []string {
	dir, err := PreviousDirFor(root, bundleID)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !usableLayoutName(entry.Name()) {
			continue
		}
		out = append(out, entry.Name())
	}
	sort.Strings(out)
	return out
}

// SnapshotBundle copies one installed pack directory into its per-version slot, replacing an
// existing snapshot of the same version (a re-install of unchanged content must not accumulate
// copies, and a re-install of changed content must leave the snapshot describing what is now
// installed).
//
// The source must be a pack directory, not one of our own snapshots: copying .previous into
// .previous is refused rather than followed, because the recursion has no meaning and silently
// accepting it would fill the disk with nested copies.
func SnapshotBundle(root, bundleID, version, srcDir string) (string, error) {
	dest, err := PreviousVersionDir(root, bundleID, version)
	if err != nil {
		return "", err
	}
	srcAbs, err := filepath.Abs(srcDir)
	if err != nil {
		return "", fmt.Errorf("解析能力包目录失败: %w", err)
	}
	prevRoot, err := filepath.Abs(filepath.Join(root, PreviousDirName))
	if err != nil {
		return "", fmt.Errorf("解析快照根目录失败: %w", err)
	}
	if rel, relErr := filepath.Rel(prevRoot, srcAbs); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("拒绝把快照目录 %s 再快照一次", srcDir)
	}
	if info, statErr := os.Stat(srcAbs); statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("能力包目录不可用: %s", srcDir)
	}
	// Replace, don't merge: a stale same-version snapshot with files the new one lacks would
	// make a rollback restore something that was never installed.
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("清理旧快照失败: %w", err)
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return "", fmt.Errorf("创建快照目录失败: %w", err)
	}
	if _, err := copyTree(srcAbs, dest); err != nil {
		return "", fmt.Errorf("快照能力包失败: %w", err)
	}
	return dest, nil
}

// RestoreBundle copies a snapshot back over the live pack directory. Files the snapshot contains
// are overwritten; files it does not contain are left alone, the same way unplugging never deletes
// the operator's files - a rollback replaces what the pack ships, not everything in the directory.
func RestoreBundle(root, bundleID, version, destDir string) (int, error) {
	snap, err := PreviousVersionDir(root, bundleID, version)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(snap)
	if err != nil || !info.IsDir() {
		return 0, fmt.Errorf("能力包 %s 没有版本 %s 的快照", bundleID, version)
	}
	if strings.TrimSpace(destDir) == "" {
		return 0, fmt.Errorf("能力包 %s 的目标目录为空", bundleID)
	}
	return copyTree(snap, destDir)
}

// usableLayoutName reports whether a string can be one directory name inside the snapshot
// layout: no separators, no NUL, not the traversal names, and not hidden (a hidden name would be
// skipped by the same listing rules everywhere else, so a snapshot nobody can enumerate is a
// snapshot nobody can roll back to).
func usableLayoutName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false
	}
	if strings.ContainsAny(name, `/\`+"\x00") {
		return false
	}
	return true
}

// copyTree copies regular files, directories and symlinks from src to dst, preserving the file
// mode (a plugin binary's executable bit is part of what makes the pack work). Anything else
// (device, socket) is refused: a pack that ships one is not something a snapshot can faithfully
// reproduce, and pretending otherwise would restore a different thing than was installed.
func copyTree(src, dst string) (int, error) {
	files := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			link, linkErr := os.Readlink(path)
			if linkErr != nil {
				return linkErr
			}
			_ = os.Remove(target)
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			if err := copyFile(path, target, info.Mode().Perm()); err != nil {
				return err
			}
			files++
			return nil
		default:
			return fmt.Errorf("不支持的条目类型: %s", path)
		}
	})
	return files, err
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// OpenFile applies the mode only on create; an overwrite keeps the old mode, so set it again
	// or a rollback could restore a binary without its executable bit.
	return os.Chmod(dst, mode)
}
