package handler

import (
	"strings"

	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"
)

// The install preview answers "what would installing this pack change" *before* the operator
// clicks. The data for it already exists - a recipe carries `capability:`, a plugin carries its
// reviewed capability list - but until now the card showed unit counts and the class/permission
// surface only became visible after install, in the capability table.
//
// Two honesty rules, matching the console's served/drift philosophy:
//
//   - A recipe without a capability manifest is marked `declared: false` and counted, because
//     that recipe will fail closed at call time ("缺能力清单就是内容 bug"): the preview must not
//     let it read like an ordinary tool.
//   - Reading a unit's metadata is best-effort per unit. One unreadable declaration gets a row
//     with an error, not a broken list - the install path will refuse the pack by itself
//     (checkPluginUnits / RecipeSpecs), and the preview is not the enforcement point.

// previewCapability is one entry point a unit would register: its class, the permission a rule
// can name it by, and its approval floor.
type previewCapability struct {
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	Class      string `json:"class"`
	Permission string `json:"permission,omitempty"`
	Approval   string `json:"approval,omitempty"`
}

// previewUnit is one unit of a not-yet-installed pack, with the metadata its kind carries.
type previewUnit struct {
	UnitID string `json:"unitId"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	// Source is where the metadata comes from: "recipe" (tools/*.yaml capability manifest),
	// "plugin" (the reviewed declaration), "mcp" (a server declaration), "content" (role/agent/skill).
	Source   string              `json:"source"`
	Declared bool                `json:"declared"`
	Grants   []string            `json:"grants,omitempty"`
	Caps     []previewCapability `json:"capabilities,omitempty"`
	Error    string              `json:"error,omitempty"`
}

// bundlePreview is the whole pack's answer, aggregated so the console can render badges and a
// confirm line without re-deriving anything.
type bundlePreview struct {
	Units []previewUnit `json:"units"`
	// Classes counts declared capabilities per class (readonly / mutating / destructive).
	Classes map[string]int `json:"classes"`
	// LiveCode counts units that will run a process once enabled (plugin, mcp).
	LiveCode int `json:"liveCodeUnits"`
	// Undeclared counts tool recipes without a capability manifest: registered in the table, always
	// refused at call time. The number exists so a pack full of them cannot look clean.
	Undeclared int `json:"undeclaredUnits"`
	// Problems counts units whose metadata could not be read at all.
	Problems int `json:"problemUnits"`
}

// buildBundlePreview reads every unit's metadata off the pack directory. It performs no writes
// and is limited to the pack's own files, so it is safe to call from the read-only catalogue.
func buildBundlePreview(bundle *plugin.Bundle) *bundlePreview {
	out := &bundlePreview{Units: make([]previewUnit, 0, len(bundle.Units)), Classes: map[string]int{}}
	for _, u := range bundle.Units {
		row := previewUnit{UnitID: u.ID, Kind: string(u.Kind), Name: u.Name}
		switch u.Kind {
		case plugin.KindTool:
			row.Source = "recipe"
			tool, err := config.LoadToolFromFile(u.Path)
			if err != nil {
				row.Error = err.Error()
				out.Problems++
				break
			}
			if tool.Capability == nil {
				out.Undeclared++
				break
			}
			row.Declared = true
			row.Grants = append([]string(nil), tool.Capability.Grants...)
			pc := previewCapability{
				ID:         strings.TrimSpace(tool.Capability.ID),
				Class:      normalizeClass(tool.Capability.Class),
				Permission: strings.TrimSpace(tool.Capability.Permission),
				Approval:   strings.TrimSpace(tool.Capability.Approval),
			}
			out.addCapability(pc)
			row.Caps = append(row.Caps, pc)
		case plugin.KindPlugin:
			row.Source = "plugin"
			decl, err := loadPluginDeclarationFrom(u, bundle.Dir)
			if err != nil {
				row.Error = err.Error()
				out.Problems++
				break
			}
			row.Declared = true
			row.Grants = append([]string(nil), decl.Grants...)
			for _, c := range decl.Capability {
				pc := previewCapability{
					ID:         c.ID,
					Title:      c.Title,
					Class:      string(c.Class),
					Permission: c.Permission,
					Approval:   approvalName(c.Approval),
				}
				out.addCapability(pc)
				row.Caps = append(row.Caps, pc)
			}
		case plugin.KindMCP:
			row.Source = "mcp"
			row.Declared = true
		default:
			row.Source = "content"
			row.Declared = true
		}
		if row.Source == "plugin" || row.Source == "mcp" {
			out.LiveCode++
		}
		out.Units = append(out.Units, row)
	}
	return out
}

func (b *bundlePreview) addCapability(c previewCapability) {
	if c.Class == "" {
		return
	}
	b.Classes[c.Class]++
}

// normalizeClass lowercases what the preview shows. The load-time check is the authority on
// validity; the preview only has to render consistently ("Read-Only" and "readonly" are one badge).
func normalizeClass(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// approvalName renders the parsed approval floor back to its declared spelling for the preview.
func approvalName(a capability.Approval) string {
	switch a {
	case capability.ApprovalAlways:
		return "always"
	case capability.ApprovalNever:
		return "never"
	default:
		return "inherited"
	}
}
