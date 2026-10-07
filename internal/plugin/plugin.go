// Package plugin is the in-process plug-in layer: one identity model and one live
// table for every kind of capability the server can be extended with.
//
// Before this package, four kinds of capability each had its own lifecycle and none of
// them could change without a restart:
//
//	roles/*.yaml      parsed inside config.Load, so only at process start - and then
//	                  mutated in place by the role API with no lock at all
//	agents/*.md       re-scanned from a directory path captured once at startup
//	skills/<dir>/     re-scanned per agent run, no registry
//	tools/*.yaml      reloaded only by POST /config/apply
//
// The table below replaces that: a unit is one named thing of one kind pointing at one
// path, a bundle is a set of units installed together under one identity, and readers see
// an immutable snapshot swapped in with an atomic pointer, so an install or an unplug
// cannot tear a concurrent read and needs no restart to take effect.
//
// Distinct from internal/pluginhost, which runs out-of-process plugin binaries. A bundle
// here is a *packaging and lifecycle* concept; a pluginhost process is an *execution* one.
// They meet at the capability registry: installing a unit that exposes callable tools
// registers specs into internal/capability, so authorization follows the unit in and out
// with it rather than being edited separately.
package plugin

import (
	"fmt"
	"strings"
)

// Kind is the class of capability a unit delivers. The set is closed on purpose: a new
// kind means a new consumer needs to learn to read the table, and that is a deliberate
// change, not a string someone typed into a manifest.
type Kind string

const (
	KindRole  Kind = "role"  // roles/<name>.yaml -> config.RoleConfig
	KindAgent Kind = "agent" // agents/<name>.md -> markdown agent definition
	KindSkill Kind = "skill" // skills/<name>/SKILL.md
	KindTool  Kind = "tool"  // tools/<name>.yaml -> security tool recipe
	KindMCP   Kind = "mcp"   // an external MCP server declaration
	// KindPlugin is the only kind that ships executable code: a plugin binary inside the pack plus
	// the reviewed list of the entry points it provides. The other five ship content.
	KindPlugin Kind = "plugin"
)

// Kinds is every accepted value, in the order a bundle should report them.
var Kinds = []Kind{KindRole, KindAgent, KindSkill, KindTool, KindMCP, KindPlugin}

func (k Kind) Valid() bool {
	for _, want := range Kinds {
		if k == want {
			return true
		}
	}
	return false
}

// UnitPathPrefix is the reserved manifest key a bundle uses to say "the whole directory is
// one unit of this kind", used for the directory-shaped kinds (skill).
const UnitPathPrefix = "./"

// Unit is one installed capability.
//
// Name is the identity inside the kind ("Web应用扫描" for a role, the skill directory name
// for a skill) and is what callers look up; ID is the global key, always "<kind>/<name>",
// derived rather than typed so two kinds can never collide on one id.
//
// Path is absolute by the time a unit is in the table. Digest is the content fingerprint
// taken at install time: it is what makes an unplug safe, because a file the operator has
// edited since is not silently deleted.
type Unit struct {
	ID      string `yaml:"-" json:"id"`
	Kind    Kind   `yaml:"kind" json:"kind"`
	Name    string `yaml:"name,omitempty" json:"name"`
	Path    string `yaml:"path" json:"path"`
	Bundle  string `yaml:"-" json:"bundle"`
	Digest  string `yaml:"-" json:"digest"`
	Enabled bool   `yaml:"enabled,omitempty" json:"enabled"`
}

// NewUnit derives the identity from kind and name. Every construction goes through this so
// a malformed id cannot enter the table.
func NewUnit(kind Kind, name, path string) (Unit, error) {
	name = strings.TrimSpace(name)
	if !kind.Valid() {
		return Unit{}, fmt.Errorf("unknown plugin kind %q (known: %s)", kind, kindsText())
	}
	if name == "" {
		return Unit{}, fmt.Errorf("%s unit has no name", kind)
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return Unit{}, fmt.Errorf("%s unit name %q must not contain a path separator", kind, name)
	}
	if strings.TrimSpace(path) == "" {
		return Unit{}, fmt.Errorf("%s unit %q has no source path", kind, name)
	}
	return Unit{ID: string(kind) + "/" + name, Kind: kind, Name: name, Path: path, Enabled: true}, nil
}

func (u *Unit) Validate() error {
	if u.ID == "" {
		return fmt.Errorf("unit has no identity")
	}
	kind, name, ok := strings.Cut(u.ID, "/")
	if !ok || Kind(kind) != u.Kind || name != u.Name {
		return fmt.Errorf("unit identity %q does not match kind/name %q/%q", u.ID, u.Kind, u.Name)
	}
	if !u.Kind.Valid() {
		return fmt.Errorf("unit %s has unknown kind %q", u.ID, u.Kind)
	}
	if strings.TrimSpace(u.Path) == "" {
		return fmt.Errorf("unit %s has no source path", u.ID)
	}
	return nil
}

// Bundle is a set of units installed and removed as one thing.
//
// Dir is the absolute directory the manifest lives in; every unit path is resolved inside
// it, so a bundle cannot deliver a file by pointing at someone else's config.
//
// The descriptive fields (categories, author, homepage, license, compatibility, changelog)
// exist for the console and any future catalogue: they are display metadata, never consulted
// by install, conflict or execution rules, so a missing one degrades a card and nothing else.
type Bundle struct {
	ID            string   `yaml:"id" json:"id"`
	Name          string   `yaml:"name" json:"name"`
	Version       string   `yaml:"version" json:"version"`
	Description   string   `yaml:"description,omitempty" json:"description"`
	Categories    []string `yaml:"categories,omitempty" json:"categories,omitempty"`
	Author        string   `yaml:"author,omitempty" json:"author,omitempty"`
	Homepage      string   `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	License       string   `yaml:"license,omitempty" json:"license,omitempty"`
	Compatibility string   `yaml:"compatibility,omitempty" json:"compatibility,omitempty"`
	Changelog     string   `yaml:"changelog,omitempty" json:"changelog,omitempty"`
	Dir           string   `yaml:"-" json:"dir"`
	ManifestPath  string   `yaml:"-" json:"manifestPath"`
	Units         []Unit   `yaml:"units" json:"units"`
}

func (b *Bundle) Validate() error {
	if strings.TrimSpace(b.ID) == "" {
		return fmt.Errorf("bundle has no id")
	}
	if strings.ContainsAny(b.ID, "/\\\x00") {
		return fmt.Errorf("bundle id %q must not contain a path separator", b.ID)
	}
	if strings.TrimSpace(b.Dir) == "" {
		return fmt.Errorf("bundle %q has no directory", b.ID)
	}
	if len(b.Units) == 0 {
		return fmt.Errorf("bundle %q declares no units", b.ID)
	}
	seen := map[string]bool{}
	for i := range b.Units {
		u := &b.Units[i]
		if u.Bundle == "" {
			u.Bundle = b.ID
		} else if u.Bundle != b.ID {
			return fmt.Errorf("bundle %q claims unit %s owned by %q", b.ID, u.ID, u.Bundle)
		}
		if err := u.Validate(); err != nil {
			return err
		}
		if seen[u.ID] {
			return fmt.Errorf("bundle %q declares unit %s twice", b.ID, u.ID)
		}
		seen[u.ID] = true
	}
	return nil
}

func kindsText() string {
	parts := make([]string, 0, len(Kinds))
	for _, k := range Kinds {
		parts = append(parts, string(k))
	}
	return strings.Join(parts, ", ")
}
