package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// CapabilitySwitches owns capability_unit_switches: the operator's on/off decision for one
// capability unit, kept outside the unit's own source file.
//
// The table exists because a capability pack's files must not be edited after install (the
// installed content has to keep matching the digest recorded at install time), so the plug-in
// console's switch had nowhere to live and only changed the in-process table. A restart rebuilds
// that table from disk, and every unit a bundle owns came back enabled - so a role somebody
// switched off was serving again, and a bundled MCP server they had stopped was declared once
// more.
//
// What is persisted is the *narrowing* decision, and only that is applied: a saved row can hide a
// unit the file enabled, never reveal one the file disabled. That is the same rule the tool layer
// already runs (`file enabled AND table enabled`), and it is why re-enabling is allowed to be
// implicit - a unit with no row is simply in whatever state its source says.

// switchUnitPrefixes are the capability kinds a switch may be recorded for. The list is pinned
// against plugin.Kinds by TestSwitchPrefixesCoverEveryCapabilityKind, so adding a kind without
// deciding whether its switch persists fails there rather than storing an unparseable identity.
// A plugin/ row is accepted for the same reason an mcp/ one is: the kind is real, its identity must
// parse, and a narrowing "off" is always safe to record. Neither is ever replayed as "on" - the
// boot path re-declares both disabled, because running a pack's process or re-trusting a pack's
// binary is the operator's decision every time, not a persisted one.
var switchUnitPrefixes = []string{"role/", "agent/", "skill/", "tool/", "mcp/", "plugin/"}

// CapabilitySwitches is the store for that table.
type CapabilitySwitches struct {
	db *sql.DB
}

// NewCapabilitySwitches binds the store to a connection.
func NewCapabilitySwitches(db *sql.DB) *CapabilitySwitches {
	return &CapabilitySwitches{db: db}
}

// EnsureSchema creates the table.
func (s *CapabilitySwitches) EnsureSchema() error {
	if s == nil || s.db == nil {
		return errors.New("store: capability switches requires a database")
	}
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS capability_unit_switches (
			unit_id TEXT PRIMARY KEY,
			path TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	return err
}

// SwitchKindPrefixes exposes the accepted identity prefixes so a gate can compare them with
// plugin.Kinds. The test that does it lives in internal/app, the one package that already imports
// both: this list drifting behind the kind list is silent otherwise, because a switch for an
// unknown kind is simply refused and the unit's state then reverts at the next restart.
func SwitchKindPrefixes() []string {
	out := make([]string, len(switchUnitPrefixes))
	copy(out, switchUnitPrefixes)
	return out
}

// SwitchPathKey reduces a capability source path to the slot it occupies: its file name plus the
// directory above it (`roles/报告撰写.yaml`).
//
// The full path cannot be the comparison because it is built from wherever config.yaml was found:
// `-config ./config.yaml` and `-config /srv/csai/config.yaml` are the same installation and produce
// `bundles/p/roles/x.yaml` next to `/srv/csai/bundles/p/roles/x.yaml`. Comparing whole paths there
// threw away a decision the operator had just made. The slot still distinguishes a capability that
// moved to a different file, which is what the row has to be able to notice.
func SwitchPathKey(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	dir := filepath.Base(filepath.Dir(path))
	if dir == "." || dir == string(filepath.Separator) || dir == ".." {
		return filepath.Base(path)
	}
	return filepath.ToSlash(filepath.Join(dir, filepath.Base(path)))
}

// SwitchableUnitID validates the identity the console put in a URL path. It is checked here rather
// than trusted because this table is read at start-up, before anything has proved the identity
// came from a real unit.
func SwitchableUnitID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	kind, name, ok := strings.Cut(value, "/")
	if !ok || name == "" || strings.Contains(name, "/") || strings.Contains(value, "..") {
		return "", false
	}
	for _, prefix := range switchUnitPrefixes {
		if kind+"/" == prefix {
			return value, true
		}
	}
	return "", false
}

// Record stores the operator's decision for one unit, together with the source path the decision
// was about. A row whose path no longer matches is treated as stale and skipped at boot, so
// deleting a capability and shipping a different one under the same identity cannot inherit an
// old switch.
func (s *CapabilitySwitches) Record(unitID, path string, enabled bool) error {
	id, ok := SwitchableUnitID(unitID)
	if !ok {
		return fmt.Errorf("store: %q is not a capability unit identity", unitID)
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("store: switch for %s needs the source path it was about", id)
	}
	if s == nil || s.db == nil {
		return errors.New("store: capability switches requires a database")
	}
	_, err := s.db.Exec(`
		INSERT INTO capability_unit_switches (unit_id, path, enabled, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(unit_id) DO UPDATE SET
			path = excluded.path,
			enabled = excluded.enabled,
			updated_at = CURRENT_TIMESTAMP;
	`, id, path, enabledBool(enabled))
	return err
}

// Forget drops saved decisions for units that no longer exist - unplugging a pack and detaching a
// scanned unit are the two ways an identity leaves the table on purpose.
func (s *CapabilitySwitches) Forget(unitIDs ...string) error {
	if s == nil || s.db == nil {
		return errors.New("store: capability switches requires a database")
	}
	for _, raw := range unitIDs {
		id, ok := SwitchableUnitID(raw)
		if !ok {
			continue // never recordable, so nothing to forget
		}
		if _, err := s.db.Exec(`DELETE FROM capability_unit_switches WHERE unit_id = ?;`, id); err != nil {
			return err
		}
	}
	return nil
}

// Switch is one saved decision.
type Switch struct {
	UnitID  string
	Path    string
	Enabled bool
}

// All returns every saved decision, ordered by identity so a boot log reads the same way twice.
// Rows whose enabled is true are returned too: the caller prunes the ones no unit matches, and an
// on-row for a unit whose file says off must not turn it on.
func (s *CapabilitySwitches) All() ([]Switch, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store: capability switches requires a database")
	}
	rows, err := s.db.Query(`SELECT unit_id, path, enabled FROM capability_unit_switches ORDER BY unit_id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Switch
	for rows.Next() {
		var (
			id, path string
			on       int
		)
		if err := rows.Scan(&id, &path, &on); err != nil {
			return nil, err
		}
		out = append(out, Switch{UnitID: id, Path: path, Enabled: on == 1})
	}
	return out, rows.Err()
}

func enabledBool(on bool) int {
	if on {
		return 1
	}
	return 0
}
