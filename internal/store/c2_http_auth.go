package store

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

// BindC2HTTPIdentity binds a new HTTP implant identity without permitting an
// existing session to be claimed by a client that only knows its UUID.
func (c *C2) BindC2HTTPIdentity(listenerID, implantUUID, token string) (bool, error) {
	if c == nil || c.db == nil {
		return false, errors.New("store: c2 requires a database")
	}
	if strings.TrimSpace(listenerID) == "" || strings.TrimSpace(implantUUID) == "" || len(token) < 32 || len(token) > 256 {
		return false, nil
	}
	hash := sha256.Sum256([]byte(token))
	encoded := hex.EncodeToString(hash[:])
	_, err := c.db.Exec(`INSERT INTO c2_http_session_auth(implant_uuid,listener_id,token_hash)
 SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM c2_sessions WHERE implant_uuid=?)
 ON CONFLICT(implant_uuid) DO NOTHING`, implantUUID, listenerID, encoded, implantUUID)
	if err != nil {
		return false, err
	}
	return c.VerifyC2HTTPIdentity(listenerID, implantUUID, token)
}

func (c *C2) VerifyC2HTTPIdentity(listenerID, implantUUID, token string) (bool, error) {
	if c == nil || c.db == nil {
		return false, errors.New("store: c2 requires a database")
	}
	if len(token) < 32 || len(token) > 256 {
		return false, nil
	}
	var owner, stored string
	err := c.db.QueryRow(`SELECT listener_id,token_hash FROM c2_http_session_auth WHERE implant_uuid=?`, implantUUID).Scan(&owner, &stored)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	hash := sha256.Sum256([]byte(token))
	encoded := hex.EncodeToString(hash[:])
	return owner == listenerID && subtle.ConstantTimeCompare([]byte(stored), []byte(encoded)) == 1, nil
}

// C2HTTPFileIdentities resolves downstream file IDs through task payloads.
func (c *C2) C2HTTPFileIdentities(listenerID, fileID string) ([]string, error) {
	if c == nil || c.db == nil {
		return nil, errors.New("store: c2 requires a database")
	}
	rows, err := c.db.Query(`SELECT DISTINCT s.implant_uuid FROM c2_tasks t JOIN c2_sessions s ON s.id=t.session_id WHERE s.listener_id=? AND json_valid(t.payload_json) AND json_extract(t.payload_json,'$.file_id')=?`, listenerID, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var identities []string
	for rows.Next() {
		var identity string
		if err := rows.Scan(&identity); err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}
