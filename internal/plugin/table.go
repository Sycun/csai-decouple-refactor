package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// snapshot is the immutable view readers load. Nothing here is ever modified after the
// pointer is published - every mutation builds a fresh snapshot and swaps it - which is
// what lets a request that is halfway through listing roles keep working while another
// goroutine unplugs one.
type snapshot struct {
	units   map[string]Unit
	bundles map[string]*Bundle
	gen     uint64
}

// Table is the live set of installed capability units and bundles.
//
// Reads are lock-free (one atomic pointer load). The mutex only serialises writers, so two
// concurrent installs cannot interleave and lose one another's units.
type Table struct {
	cur atomic.Pointer[snapshot]
	mu  sync.Mutex
}

func NewTable() *Table {
	t := &Table{}
	t.cur.Store(&snapshot{
		units:   map[string]Unit{},
		bundles: map[string]*Bundle{},
	})
	return t
}

// Generation counts mutations. A per-run consumer can cache a derived form (a parsed role
// map, a skill index) keyed on it instead of re-reading the filesystem every request, and
// still be correct the moment something is plugged in.
func (t *Table) Generation() uint64 { return t.cur.Load().gen }

// Units returns every unit of one kind, sorted by name, including disabled ones; callers
// that serve a run filter on Enabled.
func (t *Table) Units(kind Kind) []Unit {
	snap := t.cur.Load()
	out := make([]Unit, 0, len(snap.units))
	for _, u := range snap.units {
		if u.Kind == kind {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// EnabledPaths returns the source paths of the enabled units of one kind, in name order.
// This is the accessor a loader replaces a directory scan with.
func (t *Table) EnabledPaths(kind Kind) []string {
	units := t.Units(kind)
	out := make([]string, 0, len(units))
	for _, u := range units {
		if u.Enabled {
			out = append(out, u.Path)
		}
	}
	return out
}

func (t *Table) Unit(id string) (Unit, bool) {
	u, ok := t.cur.Load().units[id]
	return u, ok
}

func (t *Table) ByName(kind Kind, name string) (Unit, bool) {
	for _, u := range t.cur.Load().units {
		if u.Kind == kind && u.Name == name {
			return u, true
		}
	}
	return Unit{}, false
}

func (t *Table) Bundle(id string) (*Bundle, bool) {
	snap := t.cur.Load()
	b, ok := snap.bundles[id]
	if !ok {
		return nil, false
	}
	return resolveBundle(snap, b), true
}

func (t *Table) Bundles() []*Bundle {
	snap := t.cur.Load()
	out := make([]*Bundle, 0, len(snap.bundles))
	for _, b := range snap.bundles {
		out = append(out, resolveBundle(snap, b))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// resolveBundle re-reads a bundle's units from the unit map, which is the only authority for
// their state.
//
// A bundle carries its own copy of its units from install time, so without this step switching a
// unit off - which updates the map - would keep reporting the stale copy through every bundle
// view, and the console would show a unit as enabled while the run path had already stopped
// serving it. The two views of the same object must not be able to disagree.
func resolveBundle(snap *snapshot, b *Bundle) *Bundle {
	out := *b
	out.Units = make([]Unit, 0, len(b.Units))
	for _, u := range b.Units {
		if current, ok := snap.units[u.ID]; ok {
			out.Units = append(out.Units, current)
			continue
		}
		out.Units = append(out.Units, u)
	}
	return &out
}

// ErrConflict says a unit identity is already held by somebody else. It is a distinct
// error type because the HTTP layer has to answer 409 for it rather than 500, and because
// "refuse and name the owner" is the whole point: an install that silently shadows a
// shipped role would be a capability the operator did not ask for.
type ErrConflict struct {
	ID       string
	Owner    string // bundle id, or "" for a unit that came from a scanned directory
	Incoming string
}

func (e *ErrConflict) Error() string {
	holding := "the built-in directory scan"
	if e.Owner != "" {
		holding = "bundle " + e.Owner
	}
	if e.Incoming == "" {
		return fmt.Sprintf("unit %s is already provided by %s", e.ID, holding)
	}
	return fmt.Sprintf("unit %s is already provided by %s, bundle %q refuses to shadow it", e.ID, holding, e.Incoming)
}

// InstallBundle puts a bundle's units in and takes the previous version of the *same bundle
// id* out. It is all-or-nothing: the new snapshot is only published once every unit has
// been checked, so a bundle that conflicts halfway through leaves the running set exactly
// as it was.
func (t *Table) InstallBundle(b *Bundle) error {
	return t.installBundle(b, nil)
}

// InstallBundleSelection installs only the named units of a bundle.
//
// This is the "pick what you want out of the pack" path: a pack is a way to ship units together,
// not a rule that they must go in together, so an operator who wants one skill should not have to
// take a role, three agents and a recipe with it. Ownership does not change - the units still
// belong to the bundle and are still removed with it - only membership does.
//
// The selection must name units the bundle actually declares; anything else is refused before the
// snapshot is touched, because a name that matches nothing would otherwise install "everything
// except what you asked for" while reporting success. An empty selection is refused too: that state
// is an uninstall, and it has its own call.
func (t *Table) InstallBundleSelection(b *Bundle, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("bundle %q: 单元选择为空（要全部装入请用整包安装；要卸载请用卸载）", b.ID)
	}
	return t.installBundle(b, ids)
}

// installBundle is the one writer both entry points go through, so a change to the all-or-nothing
// rule or the same-id replacement cannot apply to one of them and not the other. A nil selection
// means "every unit the manifest declares"; a non-nil one is checked against the manifest first.
func (t *Table) installBundle(b *Bundle, ids []string) error {
	if err := b.Validate(); err != nil {
		return err
	}
	selected := b.Units
	if ids != nil {
		byID := make(map[string]Unit, len(b.Units))
		for _, u := range b.Units {
			byID[u.ID] = u
		}
		var unknown []string
		selected = make([]Unit, 0, len(ids))
		seen := map[string]bool{}
		for _, raw := range ids {
			id := strings.TrimSpace(raw)
			if id == "" || seen[id] {
				continue
			}
			u, ok := byID[id]
			if !ok {
				unknown = append(unknown, id)
				continue
			}
			seen[id] = true
			selected = append(selected, u)
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return fmt.Errorf("bundle %q 不声明这些单元：%s", b.ID, strings.Join(unknown, "、"))
		}
		if len(selected) == 0 {
			return fmt.Errorf("bundle %q: 单元选择为空（要全部装入请用整包安装；要卸载请用卸载）", b.ID)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	cur := t.cur.Load()
	next := cloneSnapshot(cur)

	// Drop this bundle's old units first: an upgrade that renames, removes - or simply no longer
	// selects - a unit must actually remove it, not leave the previous version installed alongside.
	for id, u := range next.units {
		if u.Bundle == b.ID {
			delete(next.units, id)
		}
	}

	for _, u := range selected {
		if prev, ok := next.units[u.ID]; ok && prev.Bundle != b.ID {
			return &ErrConflict{ID: u.ID, Owner: prev.Bundle, Incoming: b.ID}
		}
		// A reconcile is not a reason to take the operator's switch back: the same unit carrying
		// the same content keeps whatever on/off state it had. A unit whose path moved is a
		// different capability under a familiar identity, so it starts at the manifest default -
		// the same line boot holds when it forgets a persisted switch whose path drifted.
		if prev, ok := cur.units[u.ID]; ok && prev.Bundle == b.ID && prev.Path == u.Path {
			u.Enabled = prev.Enabled
		}
		next.units[u.ID] = u
	}

	// The bundle keeps its full manifest even when only part of it went in: the manifest is what the
	// console lists the pack's units from, and a copy that had already forgotten the unselected ones
	// would leave the operator no way to add them later.
	next.bundles[b.ID] = b
	next.gen = cur.gen + 1
	t.cur.Store(next)
	return nil
}

// RecordedUnits lists the units of one bundle that are actually in the table, sorted. Callers that
// have to state what a selection currently is (the install record, the console's "n of m") read it
// from here rather than from the manifest, which also lists what was never installed.
func (t *Table) RecordedUnits(bundleID string) []Unit {
	snap := t.cur.Load()
	var out []Unit
	for _, u := range snap.units {
		if u.Bundle == bundleID {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// UninstallBundle detaches every unit the bundle installed and nothing else.
//
// It deletes no files. Units point at sources inside bundles/<id>/, so unplugging is a
// table operation, and that is also why uninstall can never take an operator's edited file
// with it - the only way to lose a capability here is to delete the bundle directory, which
// is a deliberate filesystem act.
func (t *Table) UninstallBundle(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	cur := t.cur.Load()
	if _, ok := cur.bundles[id]; !ok {
		return fmt.Errorf("bundle %q is not installed", id)
	}
	next := cloneSnapshot(cur)
	for unitID, u := range next.units {
		if u.Bundle == id {
			delete(next.units, unitID)
		}
	}
	delete(next.bundles, id)
	next.gen = cur.gen + 1
	t.cur.Store(next)
	return nil
}

// PutLocal records a unit found by scanning a built-in directory (roles/, agents/, skills/,
// tools/). It refuses to overwrite a unit that a bundle owns - a directory scan must never
// shadow an explicit install - and refuses to replace a local unit whose content changed
// under a different path only after the caller had a chance to notice (see UpdateLocal).
func (t *Table) PutLocal(u Unit) error {
	u.Bundle = ""
	if err := u.Validate(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	cur := t.cur.Load()
	if prev, ok := cur.units[u.ID]; ok && prev.Bundle != "" {
		return &ErrConflict{ID: u.ID, Owner: prev.Bundle}
	}
	next := cloneSnapshot(cur)
	next.units[u.ID] = u
	next.gen = cur.gen + 1
	t.cur.Store(next)
	return nil
}

// RemoveLocal drops a scanned unit, typically because its file was deleted through the
// admin API. A unit owned by a bundle is not the caller's to remove.
func (t *Table) RemoveLocal(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	cur := t.cur.Load()
	u, ok := cur.units[id]
	if !ok {
		return fmt.Errorf("unit %s is not installed", id)
	}
	if u.Bundle != "" {
		return &ErrConflict{ID: id, Owner: u.Bundle}
	}
	next := cloneSnapshot(cur)
	delete(next.units, id)
	next.gen = cur.gen + 1
	t.cur.Store(next)
	return nil
}

// SetEnabled flips one unit without touching its source. Disabling rather than uninstalling
// is what makes the built-in roles switchable from the UI today, so the table has to
// represent the same thing instead of deleting and re-adding a file.
func (t *Table) SetEnabled(id string, on bool) (Unit, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cur := t.cur.Load()
	u, ok := cur.units[id]
	if !ok {
		return Unit{}, fmt.Errorf("unit %s is not installed", id)
	}
	u.Enabled = on
	next := cloneSnapshot(cur)
	next.units[id] = u
	next.gen = cur.gen + 1
	t.cur.Store(next)
	return u, nil
}

func cloneSnapshot(in *snapshot) *snapshot {
	out := &snapshot{
		units:   make(map[string]Unit, len(in.units)+4),
		bundles: make(map[string]*Bundle, len(in.bundles)+1),
	}
	for k, v := range in.units {
		out.units[k] = v
	}
	for k, v := range in.bundles {
		out.bundles[k] = v
	}
	return out
}

// Drifted reports units whose source no longer hashes to what was installed.
//
// This is the difference between "the server is running what the table says" and a claim
// nobody checked: an edited bundle file on disk is live only after a re-install, so without
// this check a hot-plugged capability can silently disagree with its own source. Missing
// files count as drift too, reported with the reason.
func (t *Table) Drifted() []string {
	snap := t.cur.Load()
	var out []string
	for id, u := range snap.units {
		if u.Digest == "" {
			continue
		}
		now, err := digestUnit(u.Kind, u.Path)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: source unreadable (%v)", id, err))
			continue
		}
		if now != u.Digest {
			out = append(out, fmt.Sprintf("%s: source changed since install", id))
		}
	}
	sort.Strings(out)
	return out
}

// Digest fingerprints a unit's source: one file's bytes, or a directory's file names and
// bytes in a stable order. Taken at install time and re-computed by Drifted, so "did
// anybody edit this after it went live?" is answered from disk rather than from memory.
func Digest(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	if !info.IsDir() {
		if err := hashFile(sum, path, filepath.Base(path)); err != nil {
			return "", err
		}
		return hex.EncodeToString(sum.Sum(nil)), nil
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, p := range files {
		rel, relErr := filepath.Rel(path, p)
		if relErr != nil {
			return "", relErr
		}
		if err := hashFile(sum, p, filepath.ToSlash(rel)); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func hashFile(w io.Writer, path, nameIn string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	// The name is part of the digest: renaming a file inside a skill directory is a change
	// a consumer can observe, so it must be a change the digest reports.
	_, _ = fmt.Fprintf(w, "%s\x00", nameIn)
	if _, err := io.Copy(w, f); err != nil {
		return err
	}
	_, _ = io.WriteString(w, "\x00")
	return nil
}

// UnitIDFor builds the identity for a (kind, name) pair, for callers that only have the
// name and must look the unit up.
func UnitIDFor(kind Kind, name string) string {
	return string(kind) + "/" + strings.TrimSpace(name)
}
