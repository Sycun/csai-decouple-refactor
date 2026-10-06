package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberstrike-ai/internal/sqltime"

	"github.com/google/uuid"
)

// RobotIdentity owns the two tables that turn an account on a chat platform into a local user:
// robot_user_bindings (external identity → RBAC user) and robot_binding_codes (the short-lived,
// single-use secret that creates a binding).
//
// What it deliberately does not do is decide what a bound user may do. ConsumeBindingCode and
// ResolveBoundUser answer "which account", and the caller asks the RBAC layer what that account is
// allowed to see. Keeping that line here means the identity tables never reach into permissions, and
// the authorization decision stays in one place instead of being half-recomputed beside a JOIN.
type RobotIdentity struct {
	db *sql.DB
}

// NewRobotIdentity binds the store to a connection.
func NewRobotIdentity(db *sql.DB) *RobotIdentity {
	return &RobotIdentity{db: db}
}

func (r *RobotIdentity) requireDB() error {
	if r == nil || r.db == nil {
		return errors.New("store: robot identity requires a database")
	}
	return nil
}

// Binding is one external identity pointing at one local account.
type Binding struct {
	ID             string    `json:"id"`
	Platform       string    `json:"platform"`
	ExternalUserID string    `json:"externalUserId"`
	RBACUserID     string    `json:"rbacUserId"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// The two tables and their indexes, moved whole out of the RBAC start-up sweep: the statements are
// byte-identical to what that list held, including the cascade that removes a binding when its
// account is deleted.
const robotIdentitySchema = `
	CREATE TABLE IF NOT EXISTS robot_user_bindings (
		id TEXT PRIMARY KEY,
		platform TEXT NOT NULL,
		external_user_id TEXT NOT NULL,
		rbac_user_id TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (rbac_user_id) REFERENCES rbac_users(id) ON DELETE CASCADE,
		UNIQUE(platform, external_user_id)
	);
	CREATE TABLE IF NOT EXISTS robot_binding_codes (
		code_hash TEXT PRIMARY KEY,
		rbac_user_id TEXT NOT NULL,
		expires_at DATETIME NOT NULL,
		used_at DATETIME,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (rbac_user_id) REFERENCES rbac_users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_robot_user_bindings_user ON robot_user_bindings(rbac_user_id);
	CREATE INDEX IF NOT EXISTS idx_robot_binding_codes_expiry ON robot_binding_codes(expires_at);
`

// EnsureSchema creates both tables and their indexes. Idempotent.
func (r *RobotIdentity) EnsureSchema() error {
	if err := r.requireDB(); err != nil {
		return err
	}
	if _, err := r.db.Exec(robotIdentitySchema); err != nil {
		return fmt.Errorf("创建机器人绑定表失败: %w", err)
	}
	return nil
}

// normalizeIdentity lower-cases the platform and trims both halves. An empty platform or empty
// external id is refused rather than stored, because either would make a binding that matches
// everybody on that platform.
func normalizeIdentity(platform, externalUserID string) (string, string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	externalUserID = strings.TrimSpace(externalUserID)
	if platform == "" || externalUserID == "" {
		return "", "", fmt.Errorf("robot platform and external user identity are required")
	}
	return platform, externalUserID, nil
}

// CreateBindingCode issues one single-use secret for an account.
//
// The prune inside the same transaction is the retention rule: one active code per user, and nothing
// that is expired or already spent is kept. It is in the transaction because pruning separately would
// let a concurrent issue leave two live codes for the same user, which doubles the window in which a
// guessed code works.
func (r *RobotIdentity) CreateBindingCode(userID, codeHash string, expiresAt time.Time) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	userID = strings.TrimSpace(userID)
	codeHash = strings.TrimSpace(codeHash)
	if userID == "" || codeHash == "" || !expiresAt.After(time.Now()) {
		return fmt.Errorf("invalid robot binding code")
	}
	now := time.Now()
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DELETE FROM robot_binding_codes WHERE rbac_user_id = ? OR expires_at <= ? OR used_at IS NOT NULL`, userID, now); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO robot_binding_codes (code_hash, rbac_user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`, codeHash, userID, expiresAt, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ConsumeBindingCode spends a code and points the external identity at the account that issued it.
// It answers which account the code belonged to; what that account may do is the caller's to ask.
//
// Single-use has two checks, and only one of them is what a caller usually sees: the lookup will not
// return a code that already carries used_at, and the update marks it only if it still does not. Under
// the serialised writes of one SQLite connection the lookup refuses the second request, so the
// one-row-affected guard is what covers the interleaving where both lookups ran before either write
// landed - a path this repository's tests cannot reach, and which is cheaper kept than argued about.
//
// An existing binding is deliberately replaced so a user can recover from a stale or wrong association
// with a fresh code.
//
// The join on rbac_users is a predicate, not a reach into permissions: a disabled account's code is
// not consumable.
func (r *RobotIdentity) ConsumeBindingCode(platform, externalUserID, codeHash string) (string, error) {
	if err := r.requireDB(); err != nil {
		return "", err
	}
	platform, externalUserID, err := normalizeIdentity(platform, externalUserID)
	if err != nil {
		return "", err
	}
	codeHash = strings.TrimSpace(codeHash)
	if codeHash == "" {
		return "", fmt.Errorf("binding code is required")
	}
	tx, err := r.db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var userID string
	now := time.Now()
	err = tx.QueryRow(`
		SELECT c.rbac_user_id
		FROM robot_binding_codes c
		JOIN rbac_users u ON u.id = c.rbac_user_id
		WHERE c.code_hash = ? AND c.used_at IS NULL AND c.expires_at > ? AND u.enabled = 1
	`, codeHash, now).Scan(&userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("binding code is invalid or expired")
		}
		return "", err
	}
	result, err := tx.Exec(`UPDATE robot_binding_codes SET used_at = ? WHERE code_hash = ? AND used_at IS NULL`, now, codeHash)
	if err != nil {
		return "", err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return "", fmt.Errorf("binding code has already been used")
	}
	if _, err = tx.Exec(`
		INSERT INTO robot_user_bindings (id, platform, external_user_id, rbac_user_id, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(platform, external_user_id) DO UPDATE SET
			rbac_user_id = excluded.rbac_user_id,
			enabled = 1,
			updated_at = excluded.updated_at
	`, uuid.New().String(), platform, externalUserID, userID, now, now); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return userID, nil
}

// ResolveBoundUser answers which account an external identity is bound to, or false when nothing binds
// it - including when the bound account has been disabled, which is not "unbound" but must not resolve.
func (r *RobotIdentity) ResolveBoundUser(platform, externalUserID string) (string, bool, error) {
	if err := r.requireDB(); err != nil {
		return "", false, err
	}
	platform, externalUserID, err := normalizeIdentity(platform, externalUserID)
	if err != nil {
		return "", false, err
	}
	var userID string
	err = r.db.QueryRow(`
		SELECT b.rbac_user_id
		FROM robot_user_bindings b
		JOIN rbac_users u ON u.id = b.rbac_user_id
		WHERE b.platform = ? AND b.external_user_id = ? AND b.enabled = 1 AND u.enabled = 1
	`, platform, externalUserID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return userID, true, nil
}

// ListBindings is what the account page and the alert subscribers list show: every platform identity
// pointing at this one account, most recently touched first.
func (r *RobotIdentity) ListBindings(userID string) ([]Binding, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(`
		SELECT id, platform, external_user_id, rbac_user_id, enabled, created_at, updated_at
		FROM robot_user_bindings WHERE rbac_user_id = ? ORDER BY updated_at DESC
	`, strings.TrimSpace(userID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Binding
	for rows.Next() {
		var b Binding
		var enabled int
		var createdAt, updatedAt string
		if err := rows.Scan(&b.ID, &b.Platform, &b.ExternalUserID, &b.RBACUserID, &enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		b.Enabled = enabled != 0
		b.CreatedAt = sqltime.Parse(createdAt)
		b.UpdatedAt = sqltime.Parse(updatedAt)
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBindingForUser unbinds by binding id, and only for the account that owns it: the id alone is
// not a capability, so the owner is part of the predicate. Nothing removed is reported as not-found,
// which is what the endpoint turns into its "not yours / not there" answer.
func (r *RobotIdentity) DeleteBindingForUser(bindingID, userID string) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	result, err := r.db.Exec(`DELETE FROM robot_user_bindings WHERE id = ? AND rbac_user_id = ?`, strings.TrimSpace(bindingID), strings.TrimSpace(userID))
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteBindingByIdentity unbinds one external identity, whatever account it points at. It is the
// administrator's path: the caller names the platform account rather than our own row id. An identity
// that was never bound is not an error.
func (r *RobotIdentity) DeleteBindingByIdentity(platform, externalUserID string) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	platform, externalUserID, err := normalizeIdentity(platform, externalUserID)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`DELETE FROM robot_user_bindings WHERE platform = ? AND external_user_id = ?`, platform, externalUserID)
	return err
}
