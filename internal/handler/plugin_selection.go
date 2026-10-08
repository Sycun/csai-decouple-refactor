package handler

import (
	"fmt"
	"strings"

	"cyberstrike-ai/internal/plugin"

	"github.com/gin-gonic/gin"
)

// The selection half of the install endpoint: "which units of this pack do I actually want".
//
// A pack is a way to ship units together, not a rule that they must be installed together. Before
// this file existed the only granularity was the whole manifest, so wanting one skill meant taking
// a role, an agent and a recipe with it - and the console could not say which units of a pack were
// actually in the table, because the two states ("declared in the manifest" and "installed") were
// spelled the same way.
//
// Three things stay exactly as they were, because they are what makes a partial install safe:
//
//   - Ownership does not move. The units still belong to the pack (unit.Bundle == pack id), are
//     still refused if they shadow a shipped capability, and still leave with the pack.
//   - Installing declares, never starts. An MCP server or plugin binary that arrives in this call
//     is written into the live layers disabled, same as a whole-pack install.
//   - The install record still describes the whole decision. It now also carries the selection, so
//     "I only took the skill" survives a restart instead of quietly becoming the whole pack.

// unitSelection is one resolved "which units of this pack" decision.
type unitSelection struct {
	bundle *plugin.Bundle
	units  []plugin.Unit // manifest order, the units this install puts in the table
	// whole is true when the request did not narrow anything (no units field, or every unit
	// checked). A whole-pack decision keeps following the directory - a later version that adds a
	// unit installs it - while an explicit selection means exactly the named units and nothing else.
	whole bool
}

// resolveUnitSelection turns a request's units list into a selection against the manifest, naming
// every id the pack does not declare. Refusing (rather than ignoring) an unknown id matters: the
// alternative installs a subset of what the operator asked for while reporting success.
//
// A nil list means "the whole pack" because that is what an absent field means. An explicitly empty
// one is refused rather than read as the same thing: `"units": []` is the one spelling that could
// mean "install nothing", and the endpoint that means that is the uninstall.
func resolveUnitSelection(bundle *plugin.Bundle, raw []string) (unitSelection, error) {
	if raw == nil {
		return unitSelection{bundle: bundle, units: bundle.Units, whole: true}, nil
	}
	if len(raw) == 0 {
		return unitSelection{}, fmt.Errorf("单元选择为空：至少要选一个单元；要卸载请用卸载接口")
	}
	byID := make(map[string]plugin.Unit, len(bundle.Units))
	for _, u := range bundle.Units {
		byID[u.ID] = u
	}
	sel := unitSelection{bundle: bundle, units: make([]plugin.Unit, 0, len(raw))}
	seen := map[string]bool{}
	var unknown []string
	for _, item := range raw {
		id := strings.TrimSpace(item)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		u, ok := byID[id]
		if !ok {
			unknown = append(unknown, id)
			continue
		}
		sel.units = append(sel.units, u)
	}
	if len(unknown) > 0 {
		return unitSelection{}, fmt.Errorf("能力包 %q 不声明这些单元：%s", bundle.ID, strings.Join(unknown, "、"))
	}
	if len(sel.units) == 0 {
		return unitSelection{}, fmt.Errorf("单元选择为空：至少要选一个单元；要卸载请用卸载接口")
	}
	sel.whole = len(sel.units) == len(bundle.Units)
	return sel, nil
}

// withUnits narrows a bundle copy to a subset of its units, keeping the directory and metadata.
//
// The pre-flight checks and the declare step take a *plugin.Bundle, and they must see only what is
// being installed right now: checking (or declaring) a unit that is staying behind in the manifest
// would resurrect the whole-pack semantics this path exists to replace.
func withUnits(b *plugin.Bundle, units []plugin.Unit) *plugin.Bundle {
	narrowed := *b
	narrowed.Units = make([]plugin.Unit, len(units))
	copy(narrowed.Units, units)
	return &narrowed
}

func unitIDs(units []plugin.Unit) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		out = append(out, u.ID)
	}
	return out
}

// unitsMissingFrom returns the units of `have` whose identities are not in `keep`.
func unitsMissingFrom(have, keep []plugin.Unit) []plugin.Unit {
	keepIDs := make(map[string]bool, len(keep))
	for _, u := range keep {
		keepIDs[u.ID] = true
	}
	var out []plugin.Unit
	for _, u := range have {
		if !keepIDs[u.ID] {
			out = append(out, u)
		}
	}
	return out
}

func unitsHaveKind(units []plugin.Unit, kind plugin.Kind) bool {
	for _, u := range units {
		if u.Kind == kind {
			return true
		}
	}
	return false
}

// mutationOutcome is what one table mutation actually did, in the terms the response has to state
// it: which units are in the table now, which left, what the install record says, and how far the
// change reached into the live layers. Both the selection path and the uninstall path fill it, so
// neither can report a step the other performs without doing it.
type mutationOutcome struct {
	// installed is the pack's units as they stand in the table after the mutation - the answer to
	// "what did this click leave installed", not "what does the manifest declare".
	installed []plugin.Unit
	removed   []plugin.Unit
	// selection is what the install record now holds: nil means "the whole pack".
	selection    []string
	recorded     bool
	recordMsg    string
	forgotten    bool
	forgetMsg    string
	snapshot     string
	snapshotMsg  string
	mcpDeclared  int
	mcpRemoved   int
	mcpMessage   string
	plugDeclared int
	plugRemoved  int
	plugMessage  string
	switchMsg    string
	roles        int
	refreshed    bool
	tools        bool
	toolMessage  string
}

// applySelection makes the table, the record and the live layers match one selection.
//
// The order is the whole-pack install's order with two additions: what leaves is unwound (MCP
// declaration dropped, plugin trust domain dropped, saved switch forgotten) and only what arrives
// is declared. Declaring the arriving units - rather than every selected one - is what keeps a
// reconcile from stopping a server the operator had explicitly started: a switch is their decision,
// and adding a skill to a pack is not a reason to take it back.
func (h *PluginHandler) applySelection(c *gin.Context, sel unitSelection) (mutationOutcome, error) {
	var out mutationOutcome
	before := h.table.RecordedUnits(sel.bundle.ID)
	beforeIDs := make(map[string]bool, len(before))
	for _, u := range before {
		beforeIDs[u.ID] = true
	}
	out.removed = unitsMissingFrom(before, sel.units)
	var arriving []plugin.Unit
	for _, u := range sel.units {
		if !beforeIDs[u.ID] {
			arriving = append(arriving, u)
		}
	}

	if sel.whole {
		if err := h.table.InstallBundle(sel.bundle); err != nil {
			return out, err
		}
	} else if err := h.table.InstallBundleSelection(sel.bundle, unitIDs(sel.units)); err != nil {
		return out, err
	}
	out.installed = h.table.RecordedUnits(sel.bundle.ID)

	// The record describes the decision, and the decision is now "this selection". A whole-pack
	// install records nil (the row means "follow the pack"), an explicit one records its ids.
	if !sel.whole {
		out.selection = unitIDs(sel.units)
	}
	out.recorded, out.recordMsg = h.rememberInstall(sel.bundle.ID, sel.bundle.Version, out.selection)
	// The version just installed becomes its own rollback target. Taken after the table accepted
	// the bundle, so the copy describes what is live rather than what was merely on disk.
	out.snapshot, out.snapshotMsg = h.snapshotInstalled(sel.bundle)

	if len(out.removed) > 0 {
		out.mcpRemoved, out.mcpMessage = h.dropMCP(out.removed)
		out.plugRemoved, out.plugMessage = h.dropPluginUnits(out.removed)
		if msg := h.forgetSwitches(out.removed); msg != "" {
			out.switchMsg = msg
		}
	}
	if len(arriving) > 0 {
		narrowed := withUnits(sel.bundle, arriving)
		if n, msg := h.installMCPDeclarations(narrowed); n > 0 || msg != "" {
			out.mcpDeclared, out.mcpMessage = n, msg
		}
		if n, msg := h.declarePluginUnits(narrowed); n > 0 || msg != "" {
			out.plugDeclared, out.plugMessage = n, msg
		}
	}

	// A pack that brings a recipe or a plugin binary changes what the tool surface should hold, so
	// both rebuild it. Only the units that moved decide that: a role-only selection must not pay for
	// ClearTools.
	moved := append(append([]plugin.Unit{}, arriving...), out.removed...)
	report := h.republishCatalog(c, unitsHaveKind(moved, plugin.KindTool) || unitsHaveKind(moved, plugin.KindPlugin))
	out.roles, out.refreshed, out.tools, out.toolMessage = report.roles, report.refreshed, report.tools, report.toolMessage
	return out, nil
}

// applyUninstall is the whole-pack removal as a shared sequence, so the console's unplug button and
// the "remove this last unit" path perform the same steps in the same order.
func (h *PluginHandler) applyUninstall(c *gin.Context, id string) (mutationOutcome, error) {
	var out mutationOutcome
	if _, ok := h.table.Bundle(id); !ok {
		return out, fmt.Errorf("bundle %q is not installed", id)
	}
	// The manifest lists every unit the pack could deliver; only the ones in the table are this
	// uninstall's to unwind. Reading the manifest would mean dropping declarations for units that
	// were never installed, and reporting removals that never happened.
	installed := h.table.RecordedUnits(id)
	wantTools := unitsHaveKind(installed, plugin.KindTool) || unitsHaveKind(installed, plugin.KindPlugin)
	if err := h.table.UninstallBundle(id); err != nil {
		return out, err
	}
	out.removed = installed
	// The record goes with the pack: a row left behind would re-install it at the next boot, and
	// the removal would look like it lasted one session.
	out.forgotten, out.forgetMsg = h.forgetInstall(id)
	out.mcpRemoved, out.mcpMessage = h.dropMCP(installed)
	out.plugRemoved, out.plugMessage = h.dropPluginUnits(installed)
	if msg := h.forgetSwitches(installed); msg != "" {
		out.switchMsg = msg
	}
	report := h.republishCatalog(c, wantTools)
	out.roles, out.refreshed, out.tools, out.toolMessage = report.roles, report.refreshed, report.tools, report.toolMessage
	return out, nil
}

// applyUnitRemoval takes one unit out of an installed pack by narrowing the recorded selection - the
// per-unit counterpart of unplugging. Removing the pack's last unit is not a special case to be
// refused: with nothing left in the table, the honest state is the uninstall, record and all.
func (h *PluginHandler) applyUnitRemoval(c *gin.Context, victim plugin.Unit) (mutationOutcome, bool, error) {
	remaining := unitsMissingFrom(h.table.RecordedUnits(victim.Bundle), []plugin.Unit{victim})
	if len(remaining) == 0 {
		out, err := h.applyUninstall(c, victim.Bundle)
		return out, true, err
	}
	bundle, ok := h.table.Bundle(victim.Bundle)
	if !ok {
		return mutationOutcome{}, false, fmt.Errorf("插件单元 %s 的能力包 %q 已不在表里", victim.ID, victim.Bundle)
	}
	// The in-table manifest, not a fresh read of the directory: "remove this one unit" should not
	// silently upgrade the other eleven to whatever the directory holds now. The table's copy
	// declares every unit it holds, so the remaining ids resolve against it by construction.
	sel, err := resolveUnitSelection(bundle, unitIDs(remaining))
	if err != nil {
		return mutationOutcome{}, false, err
	}
	out, err := h.applySelection(c, sel)
	return out, false, err
}
