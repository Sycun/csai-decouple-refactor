package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// InstalledBundles owns installed_bundles: which capability packs the operator actually installed,
// kept outside the pack directories themselves.
//
// The table exists because a directory under <configDir>/bundles used to mean "installed": every
// start-up re-installed everything it found there. That made the shipped catalogue and the
// installation the same set - a pack sitting next to the app was live before anyone chose it, and
// the console's install button only mattered until the next restart in the other direction (a pack
// somebody removed from the table came straight back).
//
// What is persisted is the operator's decision, and start-up replays exactly those decisions:
// a directory with no row is the catalogue ("can be installed"), a row with a directory is the
// installation ("comes back on every boot"). Uninstalling forgets the row, which is what makes the
// removal durable rather than a one-session overlay.
//
// A row also carries the *unit selection*: the operator may install one skill out of a pack instead
// of the whole pack, and that click has to survive a restart like any other. The selection is the
// set of unit identities ("skill/sink-driven-audit") that went into the table; an empty selection
// means the whole pack, which is both the legacy shape and the meaning of "follow the pack as it
// moves" (a version bump that adds a unit brings it in). A row whose selection shrinks to nothing
// is not stored at all - that state is an uninstall, and it is spelled by forgetting the row.

// InstalledBundles is the store for that table.
type InstalledBundles struct {
	db *sql.DB
}

// NewInstalledBundles binds the store to a connection.
func NewInstalledBundles(db *sql.DB) *InstalledBundles {
	return &InstalledBundles{db: db}
}

// EnsureSchema creates the table, and adds the unit-selection column to a database that predates
// it. The probe-and-ALTER shape is the one every late column in this package uses: a duplicate
// column answer is the normal already-there case, and a failing probe still attempts the ALTER so a
// half-initialised schema is not left with neither.
func (s *InstalledBundles) EnsureSchema() error {
	if s == nil || s.db == nil {
		return errors.New("store: installed bundles requires a database")
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS installed_bundles (
			bundle_id TEXT PRIMARY KEY,
			version TEXT NOT NULL,
			units TEXT,
			installed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`); err != nil {
		return err
	}
	return s.MigrateUnitSelectionColumn()
}

// MigrateUnitSelectionColumn adds installed_bundles.units where the table predates it. Exported so
// the regression test can drive it against a base created the old way.
func (s *InstalledBundles) MigrateUnitSelectionColumn() error {
	if s == nil || s.db == nil {
		return errors.New("store: installed bundles requires a database")
	}
	count, probeErr := schemaColumnCount(s.db, "installed_bundles", "units")
	if probeErr != nil {
		if _, addErr := s.db.Exec("ALTER TABLE installed_bundles ADD COLUMN units TEXT"); addErr != nil && !isDuplicateColumnError(addErr) {
			return fmt.Errorf("store: 添加 installed_bundles.units 失败: %w", addErr)
		}
		return nil
	}
	if count > 0 {
		return nil
	}
	if _, err := s.db.Exec("ALTER TABLE installed_bundles ADD COLUMN units TEXT"); err != nil && !isDuplicateColumnError(err) {
		return fmt.Errorf("store: 添加 installed_bundles.units 失败: %w", err)
	}
	return nil
}

// ValidBundleID reports whether an id is a plain directory name - the only shape this table may
// hold. A stored id is used at start-up to build <bundles root>/<id>, before any manifest has been
// read, so a row that could name something outside that root is refused here rather than joined.
func ValidBundleID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.HasPrefix(id, ".") {
		return false // the catalogue scanner skips hidden directories; so does this table
	}
	if strings.ContainsAny(id, `/\`+"\x00") || strings.Contains(id, "..") {
		return false
	}
	return true
}

// InstalledBundle is one recorded install.
//
// Units is the operator's selection, or nil for "the whole pack". The nil case is not "nothing
// selected": a whole-pack install has to keep following the directory (a version bump that adds a
// unit installs it), while an explicit list is a decision about named units and nothing else.
type InstalledBundle struct {
	ID      string
	Version string
	Units   []string
}

// Record stores the operator's install decision for one pack. Re-recording the same id is an
// upgrade: the row keeps one entry per pack and the newest version.
//
// An empty selection is stored as NULL rather than as an empty list, because the two must not be
// confused: nil is "the whole pack", and a stored empty list would re-play as "install nothing"
// while still counting as an installation.
func (s *InstalledBundles) Record(bundleID, version string, units []string) error {
	id := strings.TrimSpace(bundleID)
	if !ValidBundleID(id) {
		return fmt.Errorf("store: %q is not a usable bundle id", bundleID)
	}
	version = strings.TrimSpace(version)
	if version == "" {
		// Version is what an upgrade is compared against; a row without one could not tell the
		// operator which build is installed, which is the only thing the row is for.
		return fmt.Errorf("store: bundle %s has no version to record", id)
	}
	if s == nil || s.db == nil {
		return errors.New("store: installed bundles requires a database")
	}
	encoded, err := encodeUnitSelection(units)
	if err != nil {
		return fmt.Errorf("store: bundle %s 的单元选择无法编码: %w", id, err)
	}
	var stored any
	if encoded != "" {
		stored = encoded
	}
	_, err = s.db.Exec(`
		INSERT INTO installed_bundles (bundle_id, version, units, installed_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(bundle_id) DO UPDATE SET
			version = excluded.version,
			units = excluded.units,
			installed_at = CURRENT_TIMESTAMP;
	`, id, version, stored)
	return err
}

// encodeUnitSelection renders a selection as stored JSON. Sorted and de-duplicated so the same
// decision writes the same row twice, which is what makes a diff of this table readable.
func encodeUnitSelection(units []string) (string, error) {
	cleaned := make([]string, 0, len(units))
	seen := map[string]bool{}
	for _, raw := range units {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		cleaned = append(cleaned, id)
	}
	if len(cleaned) == 0 {
		return "", nil
	}
	sort.Strings(cleaned)
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// decodeUnitSelection is the read half. A row written before this column existed (NULL) and a row
// that says "the whole pack" are the same thing here.
func decodeUnitSelection(raw sql.NullString) ([]string, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, nil
	}
	var units []string
	if err := json.Unmarshal([]byte(raw.String), &units); err != nil {
		return nil, fmt.Errorf("安装记录里的单元选择不是合法 JSON: %w", err)
	}
	out := make([]string, 0, len(units))
	for _, u := range units {
		if id := strings.TrimSpace(u); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// Forget drops a pack's row. Uninstalling is the one way an install decision is taken back, and
// the row has to go with it - otherwise the next boot would install the pack again.
func (s *InstalledBundles) Forget(bundleID string) error {
	id := strings.TrimSpace(bundleID)
	if !ValidBundleID(id) {
		return fmt.Errorf("store: %q is not a usable bundle id", bundleID)
	}
	if s == nil || s.db == nil {
		return errors.New("store: installed bundles requires a database")
	}
	_, err := s.db.Exec(`DELETE FROM installed_bundles WHERE bundle_id = ?;`, id)
	return err
}

// All returns every recorded install, ordered by id so a boot log reads the same way twice.
func (s *InstalledBundles) All() ([]InstalledBundle, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store: installed bundles requires a database")
	}
	rows, err := s.db.Query(`SELECT bundle_id, version, units FROM installed_bundles ORDER BY bundle_id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstalledBundle
	for rows.Next() {
		var row InstalledBundle
		var units sql.NullString
		if err := rows.Scan(&row.ID, &row.Version, &units); err != nil {
			return nil, err
		}
		decoded, err := decodeUnitSelection(units)
		if err != nil {
			// A hand-edited row must be reported, not silently treated as "whole pack": that would
			// install units the operator never chose. The caller skips the pack and names the row.
			return nil, fmt.Errorf("读取 %s 的安装记录失败: %w", row.ID, err)
		}
		row.Units = decoded
		out = append(out, row)
	}
	return out, rows.Err()
}
