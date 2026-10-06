package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// C2PayloadArtifacts owns c2_payload_artifacts: which generated payload file belongs to which
// listener and which user asked for it. The table exists because a beacon file is downloadable by
// name, and a name alone says nothing about who may fetch it.
//
// It answers "who does this file belong to". Whether a caller may reach a listener at all is the
// RBAC layer's question, and the callers compose the two - the same split as the robot bindings,
// so the artifact table never starts deciding permissions.
type C2PayloadArtifacts struct {
	db *sql.DB
}

// NewC2PayloadArtifacts binds the store to a connection.
func NewC2PayloadArtifacts(db *sql.DB) *C2PayloadArtifacts {
	return &C2PayloadArtifacts{db: db}
}

func (c *C2PayloadArtifacts) requireDB() error {
	if c == nil || c.db == nil {
		return errors.New("store: c2 payload artifacts requires a database")
	}
	return nil
}

// Artifact is one generated file's ownership record.
type Artifact struct {
	Filename    string
	PayloadID   string
	ListenerID  string
	OwnerUserID string
	CreatedAt   time.Time
}

const c2PayloadArtifactsSchema = `
	CREATE TABLE IF NOT EXISTS c2_payload_artifacts (
		filename TEXT PRIMARY KEY,
		payload_id TEXT NOT NULL,
		listener_id TEXT NOT NULL,
		owner_user_id TEXT NOT NULL,
		created_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_c2_payload_artifacts_listener ON c2_payload_artifacts(listener_id);
`

// EnsureSchema creates the table and its listener index. Idempotent.
func (c *C2PayloadArtifacts) EnsureSchema() error {
	if err := c.requireDB(); err != nil {
		return err
	}
	if _, err := c.db.Exec(c2PayloadArtifactsSchema); err != nil {
		return fmt.Errorf("创建c2_payload_artifacts表失败: %w", err)
	}
	return nil
}

// Record writes or rewrites one artifact.
//
// A missing filename, listener or owner is refused as **silently as the write path always has**:
// the callers fire this after a successful build and ignore the error, so a record nobody can be
// held against them must not turn into a failed download later. Rebuilding the same file name
// replaces the record rather than keeping a stale owner - the file on disk is the new one.
func (c *C2PayloadArtifacts) Record(filename, payloadID, listenerID, ownerUserID string) error {
	if err := c.requireDB(); err != nil {
		return err
	}
	filename = strings.TrimSpace(filename)
	if filename == "" || strings.TrimSpace(listenerID) == "" || strings.TrimSpace(ownerUserID) == "" {
		return nil
	}
	_, err := c.db.Exec(`
		INSERT INTO c2_payload_artifacts(filename, payload_id, listener_id, owner_user_id, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(filename) DO UPDATE SET payload_id=excluded.payload_id, listener_id=excluded.listener_id, owner_user_id=excluded.owner_user_id, created_at=excluded.created_at
	`, filename, payloadID, listenerID, ownerUserID, time.Now())
	return err
}

// Lookup finds the ownership record for one file name, and reports false for a payload that was
// never recorded. The download path treats "no record" as a denial, which is the behaviour it has
// always had: a file the base knows nothing about belongs to nobody, and the safe answer is no.
func (c *C2PayloadArtifacts) Lookup(filename string) (Artifact, bool, error) {
	var a Artifact
	if err := c.requireDB(); err != nil {
		return a, false, err
	}
	err := c.db.QueryRow(`
		SELECT filename, payload_id, listener_id, owner_user_id, created_at
		FROM c2_payload_artifacts WHERE filename = ?`, strings.TrimSpace(filename)).
		Scan(&a.Filename, &a.PayloadID, &a.ListenerID, &a.OwnerUserID, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, false, nil
	}
	if err != nil {
		return Artifact{}, false, err
	}
	return a, true, nil
}
