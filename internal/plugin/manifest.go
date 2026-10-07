package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cyberstrike-ai/internal/skillpackage"

	"gopkg.in/yaml.v3"
)

// ManifestFileName is the file that makes a directory a bundle.
const ManifestFileName = "bundle.yaml"

// ManifestUnit is one line of a bundle manifest: a kind plus a path *inside* the bundle.
//
// There is deliberately no destination path. Where a capability ends up is decided by the
// kind (a role unit is read by the role loader, whoever wrote the manifest), so a bundle
// cannot install itself over an unrelated file by naming a destination.
type ManifestUnit struct {
	Kind string `yaml:"kind"`
	Name string `yaml:"name,omitempty"`
	Path string `yaml:"path"`
}

// Manifest is the on-disk form of a bundle.
//
// Everything below Description is optional catalogue metadata: the console renders it, and no
// install-time rule reads it. Keeping it optional is what lets the four shipped example packs
// stay valid without touching them.
type Manifest struct {
	ID            string         `yaml:"id"`
	Name          string         `yaml:"name,omitempty"`
	Version       string         `yaml:"version"`
	Description   string         `yaml:"description,omitempty"`
	Categories    []string       `yaml:"categories,omitempty"`
	Author        string         `yaml:"author,omitempty"`
	Homepage      string         `yaml:"homepage,omitempty"`
	License       string         `yaml:"license,omitempty"`
	Compatibility string         `yaml:"compatibility,omitempty"`
	Changelog     string         `yaml:"changelog,omitempty"`
	Units         []ManifestUnit `yaml:"units"`

	dir          string
	manifestPath string
}

// LoadManifestDir reads <dir>/bundle.yaml.
func LoadManifestDir(dir string) (*Manifest, error) {
	return LoadManifest(filepath.Join(dir, ManifestFileName))
}

func LoadManifest(path string) (*Manifest, error) {
	// How a pack was found depends on how the server was started: `-config ./config.yaml` makes the
	// bundles root relative, and a unit's path is the handle every consumer opens the file with. The
	// plug-in host deliberately refuses a binary path it would have to guess a working directory for,
	// so the pack's directory is made absolute here, once, instead of each reader re-deciding what
	// directory a path was typed against.
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve bundle manifest %s: %w", path, err)
	}
	path = abs
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read bundle manifest: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse bundle manifest %s: %w", path, err)
	}
	m.dir = filepath.Dir(path)
	m.manifestPath = path
	if err := m.check(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) check() error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("bundle manifest %s has no id", m.manifestPath)
	}
	if strings.TrimSpace(m.Version) == "" {
		// Version is required because "one click to extend" has to be reversible to a
		// known-good state, and because the UI needs something to show next to an
		// installed bundle. A bundle without it cannot be upgraded or rolled back.
		return fmt.Errorf("bundle %s has no version", m.ID)
	}
	if len(m.Units) == 0 {
		return fmt.Errorf("bundle %s declares no units", m.ID)
	}
	return nil
}

// Resolve turns declared paths into units: every path is confined to the bundle directory,
// must exist, and must have the shape its kind requires. It digests each source on the way,
// which is what later proves the file on disk is still the one that was installed.
func (m *Manifest) Resolve() (*Bundle, error) {
	if err := m.check(); err != nil {
		return nil, err
	}
	b := &Bundle{
		ID:            strings.TrimSpace(m.ID),
		Name:          strings.TrimSpace(m.Name),
		Version:       strings.TrimSpace(m.Version),
		Description:   strings.TrimSpace(m.Description),
		Categories:    normalizeCategories(m.Categories),
		Author:        strings.TrimSpace(m.Author),
		Homepage:      strings.TrimSpace(m.Homepage),
		License:       strings.TrimSpace(m.License),
		Compatibility: strings.TrimSpace(m.Compatibility),
		Changelog:     strings.TrimSpace(m.Changelog),
		Dir:           m.dir,
		ManifestPath:  m.manifestPath,
	}
	if b.Name == "" {
		b.Name = b.ID
	}
	for _, du := range m.Units {
		kind := Kind(strings.TrimSpace(du.Kind))
		if !kind.Valid() {
			return nil, fmt.Errorf("bundle %s: %w", b.ID, fmt.Errorf("unknown plugin kind %q (known: %s)", du.Kind, kindsText()))
		}
		abs, err := skillpackage.SafeRelPath(m.dir, du.Path)
		if err != nil {
			return nil, fmt.Errorf("bundle %s: unit path %q is not usable: %w", b.ID, du.Path, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("bundle %s: unit %s path %q: %w", b.ID, kind, du.Path, err)
		}
		// A skill is a directory holding SKILL.md; the other kinds are one file each.
		// Enforcing the shape here turns a mis-written manifest into an install-time
		// error instead of a capability that silently never appears.
		if kind == KindSkill && !info.IsDir() {
			return nil, fmt.Errorf("bundle %s: skill unit %q must be a directory containing SKILL.md", b.ID, du.Path)
		}
		if kind != KindSkill && info.IsDir() {
			return nil, fmt.Errorf("bundle %s: %s unit %q must be a file, found a directory", b.ID, kind, du.Path)
		}
		if kind == KindSkill {
			if _, err := skillpackage.ResolveSKILLPath(abs); err != nil {
				return nil, fmt.Errorf("bundle %s: skill unit %q: %w", b.ID, du.Path, err)
			}
		}
		name := strings.TrimSpace(du.Name)
		if name == "" {
			name = nameFromPath(abs, kind)
		}
		u, err := NewUnit(kind, name, abs)
		if err != nil {
			return nil, fmt.Errorf("bundle %s: %w", b.ID, err)
		}
		u.Bundle = b.ID
		u.Enabled = true
		// For a kind with companions (plugin: declaration + the binary it names) this covers every
		// file that defines the unit, and Drifted re-computes it the same way - so replacing a
		// pack's executable after install is reported instead of executed silently.
		digest, err := digestUnit(kind, abs)
		if err != nil {
			return nil, fmt.Errorf("bundle %s: digest %s: %w", b.ID, u.ID, err)
		}
		u.Digest = digest
		b.Units = append(b.Units, u)
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}

// nameFromPath derives the capability name from its source file, the same rule the shipped
// directories use: the extension-less base for a file (so roles/Web应用扫描.yaml is role
// "Web应用扫描"), the directory base for a skill.
func nameFromPath(abs string, kind Kind) string {
	return baseName(abs)
}

// normalizeCategories trims, drops empties and de-duplicates while keeping the author's order:
// the list is rendered, so "security,  security" must not become two chips.
func normalizeCategories(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		c := strings.TrimSpace(raw)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
