package store

import (
	"database/sql"
	"errors"
	"fmt"
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

// InstalledBundles is the store for that table.
type InstalledBundles struct {
	db *sql.DB
}

// NewInstalledBundles binds the store to a connection.
func NewInstalledBundles(db *sql.DB) *InstalledBundles {
	return &InstalledBundles{db: db}
}

// EnsureSchema creates the table.
func (s *InstalledBundles) EnsureSchema() error {
	if s == nil || s.db == nil {
		return errors.New("store: installed bundles requires a database")
	}
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS installed_bundles (
			bundle_id TEXT PRIMARY KEY,
			version TEXT NOT NULL,
			installed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	return err
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
type InstalledBundle struct {
	ID      string
	Version string
}

// Record stores the operator's install decision for one pack. Re-recording the same id is an
// upgrade: the row keeps one entry per pack and the newest version.
func (s *InstalledBundles) Record(bundleID, version string) error {
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
	_, err := s.db.Exec(`
		INSERT INTO installed_bundles (bundle_id, version, installed_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(bundle_id) DO UPDATE SET
			version = excluded.version,
			installed_at = CURRENT_TIMESTAMP;
	`, id, version)
	return err
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
	rows, err := s.db.Query(`SELECT bundle_id, version FROM installed_bundles ORDER BY bundle_id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstalledBundle
	for rows.Next() {
		var row InstalledBundle
		if err := rows.Scan(&row.ID, &row.Version); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
