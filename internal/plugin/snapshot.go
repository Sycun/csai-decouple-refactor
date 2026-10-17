package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// PreviousDirName is the directory under the bundles root where the install path keeps one copy
// per installed version of a pack: <root>/.previous/<bundle id>/<version>/.
//
// The copy exists so "回滚" is a filesystem operation the operator can inspect, not a claim: the
// install endpoint takes a version from here and restores it. The name starts with a dot because
// every directory scanner in this package skips hidden entries, so a snapshot can never be read
// as a shippable pack (the catalogue, the boot scan and the install endpoint all stay away).
const PreviousDirName = ".previous"

// Serialise replacement and listing of version slots and live pack directories.
var snapshotMu sync.RWMutex

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
	dest := filepath.Join(dir, version)
	base, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer base.Close()
	if err := rejectSnapshotLayoutLinks(base, filepath.Join(PreviousDirName, strings.TrimSpace(bundleID), version)); err != nil {
		return "", err
	}
	_, err = base.Stat(filepath.Join(PreviousDirName, strings.TrimSpace(bundleID), version))
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("快照路径不安全: %w", err)
	}
	return dest, nil
}

// PreviousVersions lists the snapshot versions available for one bundle, sorted so the console
// renders the same list twice.
func PreviousVersions(root, bundleID string) []string {
	snapshotMu.RLock()
	defer snapshotMu.RUnlock()
	parent, err := openSnapshotParent(root, bundleID, false)
	if err != nil {
		return nil
	}
	defer parent.Close()
	f, err := parent.Open(".")
	if err != nil {
		return nil
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
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
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	dest, err := PreviousVersionDir(root, bundleID, version)
	if err != nil {
		return "", err
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer base.Close()
	rel, err := livePackRel(root, srcDir)
	if err != nil {
		return "", err
	}
	source, err := base.OpenRoot(rel)
	if err != nil {
		return "", fmt.Errorf("能力包目录不可用: %w", err)
	}
	defer source.Close()
	parent, err := openSnapshotParent(root, bundleID, true)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	stage, err := tempDirectory(parent, ".snapshot-")
	if err != nil {
		return "", err
	}
	defer parent.RemoveAll(stage)
	target, err := parent.OpenRoot(stage)
	if err != nil {
		return "", err
	}
	_, copyErr := copyTree(source, target)
	target.Close()
	if copyErr != nil {
		return "", fmt.Errorf("快照能力包失败: %w", copyErr)
	}
	if err := replaceDirectory(parent, stage, strings.TrimSpace(version)); err != nil {
		return "", err
	}
	return dest, nil
}

// RestoreBundle prepares a complete replacement, preserving operator files absent from the
// snapshot. Copy failures leave the live directory intact; rename failures restore its backup.
func RestoreBundle(root, bundleID, version, destDir string) (int, error) {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	if _, err := PreviousVersionDir(root, bundleID, version); err != nil {
		return 0, err
	}
	snapParent, err := openSnapshotParent(root, bundleID, false)
	if err != nil {
		return 0, err
	}
	defer snapParent.Close()
	source, err := snapParent.OpenRoot(strings.TrimSpace(version))
	if err != nil {
		return 0, fmt.Errorf("能力包 %s 没有版本 %s 的可用快照: %w", bundleID, version, err)
	}
	defer source.Close()
	rel, err := livePackRel(root, destDir)
	if err != nil {
		return 0, err
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return 0, err
	}
	defer base.Close()
	if err := base.MkdirAll(filepath.Dir(rel), 0o750); err != nil {
		return 0, err
	}
	parent, err := base.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return 0, err
	}
	defer parent.Close()
	name := filepath.Base(rel)
	stage, err := tempDirectory(parent, ".restore-")
	if err != nil {
		return 0, err
	}
	defer parent.RemoveAll(stage)
	target, err := parent.OpenRoot(stage)
	if err != nil {
		return 0, err
	}
	defer target.Close()
	if live, err := parent.OpenRoot(name); err == nil {
		_, copyErr := copyTree(live, target)
		live.Close()
		if copyErr != nil {
			return 0, fmt.Errorf("保留现有能力包文件失败: %w", copyErr)
		}
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	files, err := copyTree(source, target)
	if err != nil {
		return 0, fmt.Errorf("准备回滚文件失败: %w", err)
	}
	if err := replaceDirectory(parent, stage, name); err != nil {
		return 0, err
	}
	return files, nil
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
