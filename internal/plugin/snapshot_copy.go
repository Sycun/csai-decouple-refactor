package plugin

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func openSnapshotParent(root, bundleID string, create bool) (*os.Root, error) {
	if _, err := PreviousDirFor(root, bundleID); err != nil {
		return nil, err
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	rel := filepath.Join(PreviousDirName, strings.TrimSpace(bundleID))
	if err := rejectSnapshotLayoutLinks(base, rel); err != nil {
		return nil, err
	}
	if create {
		if err := base.MkdirAll(rel, 0o750); err != nil {
			return nil, err
		}
	}
	return base.OpenRoot(rel)
}

// Snapshot storage is owned by this package. Even an internal alias must not redirect a
// version slot into a live pack or another bundle's snapshots.
func rejectSnapshotLayoutLinks(base *os.Root, rel string) error {
	prefix := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		prefix = filepath.Join(prefix, part)
		info, err := base.Lstat(prefix)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("快照目录 %s 必须是实际目录，不能使用符号链接", prefix)
		}
	}
	return nil
}

// Restrict both snapshot sources and restore targets to live packs beneath the bundles root.
func livePackRel(root, dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("能力包目录为空")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absDir)
	if err != nil || !filepath.IsLocal(rel) || rel == "." || rel == PreviousDirName || strings.HasPrefix(rel, PreviousDirName+string(filepath.Separator)) {
		return "", fmt.Errorf("能力包目录 %s 必须在根目录内，且不能是快照目录", dir)
	}
	// Catch aliases into .previous as well as lexical paths. Subsequent IO uses os.Root so an
	// external symlink swapped after this check cannot redirect reads or writes outside root.
	if real, err := filepath.EvalSymlinks(absDir); err == nil {
		realRoot, err := filepath.EvalSymlinks(absRoot)
		if err != nil {
			return "", err
		}
		if r, err := filepath.Rel(realRoot, real); err != nil || !filepath.IsLocal(r) || r == "." || r == PreviousDirName || strings.HasPrefix(r, PreviousDirName+string(filepath.Separator)) {
			return "", fmt.Errorf("能力包目录 %s 指向根目录外或快照目录", dir)
		}
	}
	return rel, nil
}

func tempDirectory(parent *os.Root, prefix string) (string, error) {
	for range 10 {
		name := fmt.Sprintf("%s%x", prefix, randomDirectorySuffix())
		if err := parent.Mkdir(name, 0o750); err == nil {
			return name, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("无法创建临时能力包目录")
}

func randomDirectorySuffix() []byte {
	value := make([]byte, 16)
	_, _ = rand.Read(value) // crypto/rand.Read is guaranteed to fill the buffer or terminate.
	return value
}

// Portable rename cannot atomically exchange two non-empty directories. Retain the old one
// until the prepared tree is in place and restore it if publication fails.
func replaceDirectory(parent *os.Root, stage, name string) error {
	backup := ""
	if _, err := parent.Lstat(name); err == nil {
		backup, err = tempDirectory(parent, ".backup-")
		if err != nil {
			return err
		}
		if err := parent.Remove(backup); err != nil {
			return err
		}
		if err := parent.Rename(name, backup); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := parent.Rename(stage, name); err != nil {
		if backup != "" {
			if restoreErr := parent.Rename(backup, name); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("恢复旧目录失败，旧文件保留在 %s: %w", backup, restoreErr))
			}
		}
		return err
	}
	if backup != "" {
		return parent.RemoveAll(backup)
	}
	return nil
}

// copyTree confines every read and write, including symlink traversal, to the opened roots.
// Only regular files, directories and internal links can be faithfully snapshotted.
func copyTree(source, target *os.Root) (int, error) {
	files := 0
	err := fs.WalkDir(source.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := source.Readlink(path)
			if err != nil {
				return err
			}
			if filepath.IsAbs(link) {
				// Internal absolute links must become relative for a snapshot to be relocatable.
				absSource, err := filepath.EvalSymlinks(source.Name())
				if err != nil {
					return err
				}
				realLink, err := filepath.EvalSymlinks(link)
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(absSource, realLink)
				if err != nil || !filepath.IsLocal(rel) {
					return fmt.Errorf("符号链接 %s 指向包目录外", path)
				}
				link, err = filepath.Rel(filepath.Dir(path), rel)
				if err != nil {
					return err
				}
			}
			resolved := filepath.Dir(path) + string(filepath.Separator) + link
			if !filepath.IsLocal(resolved) {
				return fmt.Errorf("符号链接 %s 指向包目录外", path)
			}
			if err := target.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return target.Symlink(link, path)
		case info.IsDir():
			return target.MkdirAll(path, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			if err := copyFile(source, target, path, info.Mode().Perm()); err != nil {
				return err
			}
			files++
			return nil
		default:
			return fmt.Errorf("不支持的条目类型: %s", path)
		}
	})
	if err == nil {
		// Validate after every link has been relocated, allowing link chains whose internal
		// absolute components have become relative. os.Root resolves the actual chain before
		// traversing .., so lexical cleaning cannot conceal a traversal outside the pack.
		err = fs.WalkDir(target.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&fs.ModeSymlink != 0 {
				if _, err := target.Stat(path); err != nil {
					return fmt.Errorf("符号链接 %s 的目标不可用: %w", path, err)
				}
			}
			return nil
		})
	}
	return files, err
}

func copyFile(source, target *os.Root, path string, mode os.FileMode) error {
	in, err := source.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	// Replace the entry itself, so overlaying a file never truncates another file through a link.
	if err := target.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	out, err := target.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode|0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	modeErr := out.Chmod(mode)
	return errors.Join(copyErr, modeErr, out.Close())
}
