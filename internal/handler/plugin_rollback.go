package handler

import (
	"fmt"
	"path/filepath"
	"strings"

	"cyberstrike-ai/internal/plugin"
)

// Rolling back is the same install the console already performs, from a different source: the
// snapshot copy taken when the wanted version was installed. It shares the install endpoint on
// purpose - the record, the declarations, the catalog publish and the tool-layer rebuild after it
// are the sequence every install must run, and a second route would be a second place for one of
// them to be forgotten.

// rollbackSnapshot reads and validates the named version's snapshot: it must describe the pack
// asked for, under the id its snapshots were filed under. A rollback that silently swapped bytes
// from another pack would be indistinguishable from a supply-chain substitution, so the checks
// run before anything is copied - and before the install endpoint decides what selection the
// rollback keeps.
func (h *PluginHandler) rollbackSnapshot(dir, from string) (*plugin.Bundle, error) {
	// The console names packs the way the catalogue does - by directory - so that is the first
	// candidate. The live manifest, when it is readable, is the authority and corrects it: a pack
	// whose directory and id disagree must roll back under the id its snapshots were filed under.
	id := filepath.Base(dir)
	if live, err := loadBundle(dir); err == nil && strings.TrimSpace(live.ID) != "" {
		id = live.ID
	}
	snapDir, err := plugin.PreviousVersionDir(h.bundles, id, from)
	if err != nil {
		return nil, err
	}
	snapBundle, err := loadBundle(snapDir)
	if err != nil {
		return nil, fmt.Errorf("能力包 %s 的版本 %s 快照不可用: %w", id, from, err)
	}
	if snapBundle.ID != id {
		return nil, fmt.Errorf("快照 %s 声明为 %q，与能力包 %s 不一致", from, snapBundle.ID, id)
	}
	if strings.TrimSpace(snapBundle.Version) != strings.TrimSpace(from) {
		return nil, fmt.Errorf("快照目录名 %s 与清单版本 %q 不一致", from, snapBundle.Version)
	}
	return snapBundle, nil
}

// restoreRollback copies the named version's snapshot over the pack directory.
func (h *PluginHandler) restoreRollback(dir, from string) error {
	snapBundle, err := h.rollbackSnapshot(dir, from)
	if err != nil {
		return err
	}
	_, err = plugin.RestoreBundle(h.bundles, snapBundle.ID, from, dir)
	return err
}

// snapshotInstalled keeps a copy of the version just installed, so a later upgrade has a rollback
// target and a later rollback has a rule to read it back with.
//
// A failure is reported in the response, not raised: the install is already live and the table
// already holds it, so turning a successful install into a failed request would be the worse lie.
// "没有回滚点是" is something the operator can see and act on; "安装失败" over a live pack is not.
func (h *PluginHandler) snapshotInstalled(bundle *plugin.Bundle) (string, string) {
	if strings.TrimSpace(h.bundles) == "" {
		return "", "未配置能力包根目录，本次安装没有保留回滚快照"
	}
	if _, err := plugin.SnapshotBundle(h.bundles, bundle.ID, bundle.Version, bundle.Dir); err != nil {
		return "", err.Error()
	}
	return bundle.Version, ""
}

// rollbackVersions lists the snapshot versions the console may offer. Non-nil even when empty so
// the field reads as "no rollback targets" rather than as a disabled feature.
func (h *PluginHandler) rollbackVersions(bundleID string) []string {
	versions := plugin.PreviousVersions(h.bundles, bundleID)
	if versions == nil {
		return []string{}
	}
	return versions
}
