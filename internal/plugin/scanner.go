package plugin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Namer decides what a file on disk is *called* as a capability. It is a parameter rather
// than a rule inside this package because the shipped answer differs per kind: a role is
// keyed by the `name:` field inside the file and only falls back to the file name, and a
// skill is keyed by its directory. If the scanner guessed, the identity a bundle installs
// under would not be the identity the existing loader serves, and an upgrade would leave an
// orphan behind.
type Namer func(kind Kind, path string) (string, error)

// BaseNameNamer is the file-name rule: extension-less base for a file, directory name for a
// skill. It is the default, and it is also what a bundle manifest falls back to.
func BaseNameNamer(kind Kind, path string) (string, error) {
	_ = kind
	return baseName(path), nil
}

// baseName is the extension-less file name, or the directory name for a directory.
func baseName(path string) string {
	base := filepath.Base(filepath.ToSlash(path))
	if dot := strings.LastIndex(base, "."); dot > 0 {
		return base[:dot]
	}
	return base
}

// ScanDir turns one built-in capability directory into units, so the capabilities that ship
// in the repository live in the same table as the ones somebody installs later. That is the
// difference between "everything is a plug-in" and a registry that only knows about
// plug-ins.
//
// A directory that does not exist yields no units and no error, matching how the existing
// loaders treat an optional directory (roles/ may be absent in a trimmed install).
func ScanDir(kind Kind, dir string, name Namer) ([]Unit, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("unknown plugin kind %q (known: %s)", kind, kindsText())
	}
	if name == nil {
		name = BaseNameNamer
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s directory %s: %w", kind, dir, err)
	}
	var out []Unit
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		keep, err := matches(kind, path, entry.IsDir())
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}
		unitName, err := name(kind, path)
		if err != nil {
			return nil, fmt.Errorf("%s unit %s: %w", kind, entry.Name(), err)
		}
		if strings.TrimSpace(unitName) == "" {
			unitName = baseName(path)
		}
		u, err := NewUnit(kind, unitName, path)
		if err != nil {
			return nil, err
		}
		digest, err := Digest(path)
		if err != nil {
			return nil, fmt.Errorf("digest %s: %w", u.ID, err)
		}
		u.Digest = digest
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// matches is the per-kind shape rule: which entries of a directory actually provide a unit
// of this kind. README.md is skipped because the shipped directories carry one, and a
// hidden file is skipped because editors leave them behind.
func matches(kind Kind, path string, isDir bool) (bool, error) {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		return false, nil
	}
	switch kind {
	case KindRole, KindTool, KindMCP, KindPlugin:
		if isDir {
			return false, nil
		}
		switch filepath.Ext(base) {
		case ".yaml", ".yml":
			return true, nil
		}
		return false, nil
	case KindAgent:
		if isDir || base == "README.md" {
			return false, nil
		}
		return strings.EqualFold(filepath.Ext(base), ".md"), nil
	case KindSkill:
		if !isDir {
			return false, nil
		}
		md := filepath.Join(path, "SKILL.md")
		info, err := os.Stat(md)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// A directory without SKILL.md is not a skill. Skipping rather than
				// failing matches the listing behaviour the skills API has today, where a
				// half-written skill directory shows up as absent, not as a 500.
				return false, nil
			}
			return false, err
		}
		return !info.IsDir(), nil
	default:
		return false, fmt.Errorf("scanner does not handle kind %q", kind)
	}
}
