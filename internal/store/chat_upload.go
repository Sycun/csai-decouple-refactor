package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ChatUploads owns chat_upload_artifacts: the map from an uploaded file's relative path to the
// conversation it belongs to and the user who uploaded it.
//
// Two things about this table were ownerless before. Its SQL sat on the data layer's connection
// wrapper next to three other domains the handler also reaches for (conversation titles, project
// names, base directories), and its CREATE TABLE sat inside the RBAC initialisation, so the access
// control code was the thing that made the table exist. Both move here.
type ChatUploads struct {
	db *sql.DB
}

// NewChatUploads binds the store to a connection.
func NewChatUploads(db *sql.DB) *ChatUploads {
	return &ChatUploads{db: db}
}

func (c *ChatUploads) requireDB() error {
	if c == nil || c.db == nil {
		return errors.New("store: chat uploads requires a database")
	}
	return nil
}

// The artifact row is the authorization record for one uploaded file: whoever serves or rewrites the
// path asks it who owns the file and which conversation it came from. conversation_id cascades from
// conversations, which is why deleting a conversation takes its attachments' rows with it.
const chatUploadArtifactSchema = `
	CREATE TABLE IF NOT EXISTS chat_upload_artifacts (
		relative_path TEXT PRIMARY KEY,
		conversation_id TEXT NOT NULL,
		owner_user_id TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_chat_upload_artifacts_conversation ON chat_upload_artifacts(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_chat_upload_artifacts_owner ON chat_upload_artifacts(owner_user_id);
`

// EnsureSchema creates the table and its two indexes. It has to run after the conversations table
// exists, because of the foreign key.
func (c *ChatUploads) EnsureSchema() error {
	if err := c.requireDB(); err != nil {
		return err
	}
	_, err := c.db.Exec(chatUploadArtifactSchema)
	return err
}

// Record files the path against a conversation and its owner. An empty path, conversation or owner is
// not an error and writes nothing: the upload endpoints call this best-effort after the file is on
// disk, and a missing owner must not turn a completed upload into a failure.
func (c *ChatUploads) Record(relativePath, conversationID, ownerUserID string) error {
	if err := c.requireDB(); err != nil {
		return err
	}
	relativePath = strings.TrimSpace(relativePath)
	conversationID = strings.TrimSpace(conversationID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if relativePath == "" || conversationID == "" || ownerUserID == "" {
		return nil
	}
	_, err := c.db.Exec(`
		INSERT INTO chat_upload_artifacts(relative_path, conversation_id, owner_user_id, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(relative_path) DO UPDATE SET conversation_id=excluded.conversation_id, owner_user_id=excluded.owner_user_id
	`, relativePath, conversationID, ownerUserID, time.Now())
	return err
}

// OwnerOf reports the conversation and owner recorded for a path. The boolean is the answer callers
// branch on: a path with no row is not an error, it is simply not a chat upload artifact.
func (c *ChatUploads) OwnerOf(relativePath string) (conversationID, ownerUserID string, ok bool) {
	if err := c.requireDB(); err != nil {
		return "", "", false
	}
	err := c.db.QueryRow(`SELECT conversation_id, owner_user_id FROM chat_upload_artifacts WHERE relative_path = ?`, strings.TrimSpace(relativePath)).Scan(&conversationID, &ownerUserID)
	return conversationID, ownerUserID, err == nil
}

// Forget removes the row for a path *and* for everything under it, because an attachment directory is
// addressed by prefix and deleting the directory must not leave children owned by nothing.
func (c *ChatUploads) Forget(relativePath string) error {
	if err := c.requireDB(); err != nil {
		return err
	}
	path := strings.Trim(strings.TrimSpace(relativePath), "/")
	if path == "" {
		return nil
	}
	_, err := c.db.Exec(`DELETE FROM chat_upload_artifacts WHERE relative_path = ? OR relative_path LIKE ? ESCAPE '\'`, path, escapeLikePrefix(path)+"/%")
	return err
}

// Rename moves a path and its subtree, keeping the recorded prefix in step with the files on disk.
func (c *ChatUploads) Rename(oldPath, newPath string) error {
	if err := c.requireDB(); err != nil {
		return err
	}
	oldPath = strings.Trim(strings.TrimSpace(oldPath), "/")
	newPath = strings.Trim(strings.TrimSpace(newPath), "/")
	if oldPath == "" || newPath == "" {
		return nil
	}
	_, err := c.db.Exec(`
		UPDATE chat_upload_artifacts
		SET relative_path = CASE
			WHEN relative_path = ? THEN ?
			ELSE ? || substr(relative_path, length(?) + 1)
		END
		WHERE relative_path = ? OR relative_path LIKE ? ESCAPE '\'
	`, oldPath, newPath, newPath, oldPath, oldPath, escapeLikePrefix(oldPath)+"/%")
	return err
}

// escapeLikePrefix quotes the LIKE metacharacters of a path prefix.
func escapeLikePrefix(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}
