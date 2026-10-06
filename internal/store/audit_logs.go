package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cyberstrike-ai/internal/sqltime"
)

// AuditLogs owns audit_logs: the platform's own operation record.
//
// Its five queries used to sit on the data layer's connection wrapper while the table's schema was
// created in the middle of the start-up sweep for everything else, and the interface that named them
// - AuditStore - also carried eight lookups over seven other tables, because the audit reader hands
// its storage to the resource-availability check. Those eight now have their own surface; what is
// declared here is only what this table answers.
type AuditLogs struct {
	db *sql.DB
}

// NewAuditLogs binds the store to a connection.
func NewAuditLogs(db *sql.DB) *AuditLogs {
	return &AuditLogs{db: db}
}

func (a *AuditLogs) requireDB() error {
	if a == nil || a.db == nil {
		return errors.New("store: audit logs requires a database")
	}
	return nil
}

// AuditLog is one operation record. ResourceAvailable is not a column: it is filled in at read time
// when the row points at a resource that can still be looked up, and the console distinguishes
// "checked and missing" from "not checkable" by the pointer being nil.
type AuditLog struct {
	ID                string                 `json:"id"`
	CreatedAt         time.Time              `json:"createdAt"`
	Level             string                 `json:"level"`
	Category          string                 `json:"category"`
	Action            string                 `json:"action"`
	Result            string                 `json:"result"`
	Actor             string                 `json:"actor"`
	SessionHint       string                 `json:"sessionHint,omitempty"`
	ClientIP          string                 `json:"clientIp,omitempty"`
	UserAgent         string                 `json:"userAgent,omitempty"`
	ResourceType      string                 `json:"resourceType,omitempty"`
	ResourceID        string                 `json:"resourceId,omitempty"`
	ResourceAvailable *bool                  `json:"resourceAvailable,omitempty"`
	Message           string                 `json:"message"`
	Detail            map[string]interface{} `json:"detail,omitempty"`
}

// AuditListFilter is the query surface of the audit page. An empty field is "don't filter", and
// RelatedUserID reaches into the detail JSON as well as the resource id - both spellings of the key
// are matched because rows written before the field was normalized carry the other one.
type AuditListFilter struct {
	Actor         string
	Level         string
	Category      string
	Action        string
	Result        string
	Query         string
	ResourceType  string
	ResourceID    string
	RelatedUserID string
	Since         *time.Time
	Until         *time.Time
	Limit         int
	Offset        int
}

const auditLogsSchema = `
	CREATE TABLE IF NOT EXISTS audit_logs (
		id TEXT PRIMARY KEY,
		created_at DATETIME NOT NULL,
		level TEXT NOT NULL DEFAULT 'info',
		category TEXT NOT NULL,
		action TEXT NOT NULL,
		result TEXT NOT NULL,
		actor TEXT NOT NULL DEFAULT 'admin',
		session_hint TEXT,
		client_ip TEXT,
		user_agent TEXT,
		resource_type TEXT,
		resource_id TEXT,
		message TEXT NOT NULL,
		detail_json TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at);
	CREATE INDEX IF NOT EXISTS idx_audit_logs_category ON audit_logs(category);
	CREATE INDEX IF NOT EXISTS idx_audit_logs_action ON audit_logs(action);
	CREATE INDEX IF NOT EXISTS idx_audit_logs_result ON audit_logs(result);
`

// EnsureSchema creates the table and its four indexes. Idempotent.
func (a *AuditLogs) EnsureSchema() error {
	if err := a.requireDB(); err != nil {
		return err
	}
	_, err := a.db.Exec(auditLogsSchema)
	return err
}

const auditLogColumns = `id, created_at, level, category, action, result, actor,
			COALESCE(session_hint, ''), COALESCE(client_ip, ''), COALESCE(user_agent, ''),
			COALESCE(resource_type, ''), COALESCE(resource_id, ''), message, COALESCE(detail_json, '')`

func scanAuditLog(row interface{ Scan(...interface{}) error }) (AuditLog, string, error) {
	var log AuditLog
	var detailJSON string
	err := row.Scan(
		&log.ID, &log.CreatedAt, &log.Level, &log.Category, &log.Action, &log.Result, &log.Actor,
		&log.SessionHint, &log.ClientIP, &log.UserAgent,
		&log.ResourceType, &log.ResourceID, &log.Message, &detailJSON,
	)
	return log, detailJSON, err
}

// Append inserts one record. The instant is stored through sqltime so the row reads back the same on
// a host in another zone.
func (a *AuditLogs) Append(log *AuditLog) error {
	if err := a.requireDB(); err != nil {
		return err
	}
	if log == nil {
		return errors.New("audit log is nil")
	}
	if strings.TrimSpace(log.ID) == "" {
		return errors.New("audit id is required")
	}
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now().UTC()
	} else {
		log.CreatedAt = log.CreatedAt.UTC()
	}
	if strings.TrimSpace(log.Level) == "" {
		log.Level = "info"
	}
	detailJSON := ""
	if len(log.Detail) > 0 {
		if b, err := json.Marshal(log.Detail); err == nil {
			detailJSON = string(b)
		}
	}
	_, err := a.db.Exec(`
		INSERT INTO audit_logs (
			id, created_at, level, category, action, result, actor, session_hint,
			client_ip, user_agent, resource_type, resource_id, message, detail_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, log.ID, sqltime.UTC(log.CreatedAt), log.Level, log.Category, log.Action, log.Result,
		log.Actor, log.SessionHint, log.ClientIP, log.UserAgent,
		log.ResourceType, log.ResourceID, log.Message, detailJSON,
	)
	return err
}

// GetByID returns one record.
func (a *AuditLogs) GetByID(id string) (*AuditLog, error) {
	if err := a.requireDB(); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("id is required")
	}
	var row AuditLog
	var detailJSON string
	err := a.db.QueryRow(`
		SELECT `+auditLogColumns+`
		FROM audit_logs WHERE id = ?
	`, id).Scan(
		&row.ID, &row.CreatedAt, &row.Level, &row.Category, &row.Action, &row.Result, &row.Actor,
		&row.SessionHint, &row.ClientIP, &row.UserAgent,
		&row.ResourceType, &row.ResourceID, &row.Message, &detailJSON,
	)
	if err != nil {
		return nil, err
	}
	if detailJSON != "" {
		_ = json.Unmarshal([]byte(detailJSON), &row.Detail)
	}
	return &row, nil
}

// auditWhere builds the filter. The condition list starts at 1=1 so every clause below is optional.
func auditWhere(filter AuditListFilter) (string, []interface{}) {
	conditions := []string{"1=1"}
	args := []interface{}{}
	appendEq := func(column, value string) {
		if strings.TrimSpace(value) != "" {
			conditions = append(conditions, column+" = ?")
			args = append(args, value)
		}
	}
	appendEq("actor", filter.Actor)
	appendEq("level", filter.Level)
	appendEq("category", filter.Category)
	appendEq("action", filter.Action)
	appendEq("result", filter.Result)
	appendEq("resource_type", filter.ResourceType)
	appendEq("resource_id", filter.ResourceID)
	if relatedUserID := strings.TrimSpace(filter.RelatedUserID); relatedUserID != "" {
		conditions = append(conditions, `(resource_id = ? OR detail_json LIKE ? OR detail_json LIKE ?)`)
		args = append(args, relatedUserID, `%"user_id":"`+relatedUserID+`"%`, `%"userId":"`+relatedUserID+`"%`)
	}
	if filter.Since != nil {
		conditions = append(conditions, sqltime.Compare("created_at", ">="))
		args = append(args, sqltime.UTC(*filter.Since))
	}
	if filter.Until != nil {
		conditions = append(conditions, sqltime.Compare("created_at", "<="))
		args = append(args, sqltime.UTC(*filter.Until))
	}
	if q := strings.TrimSpace(filter.Query); q != "" {
		like := "%" + q + "%"
		conditions = append(conditions, "(message LIKE ? OR resource_id LIKE ? OR action LIKE ? OR category LIKE ? OR detail_json LIKE ?)")
		args = append(args, like, like, like, like, like)
	}
	return strings.Join(conditions, " AND "), args
}

// Count reports how many records match, independent of the page window.
func (a *AuditLogs) Count(filter AuditListFilter) (int64, error) {
	if err := a.requireDB(); err != nil {
		return 0, err
	}
	where, args := auditWhere(filter)
	var n int64
	err := a.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE `+where, args...).Scan(&n)
	return n, err
}

// List returns the matching records newest first. The limit is clamped rather than trusted: a caller
// that forgets to set one gets the default page, not the table.
func (a *AuditLogs) List(filter AuditListFilter) ([]*AuditLog, error) {
	if err := a.requireDB(); err != nil {
		return nil, err
	}
	where, args := auditWhere(filter)
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := a.db.Query(`
		SELECT `+auditLogColumns+`
		FROM audit_logs
		WHERE `+where+`
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*AuditLog
	for rows.Next() {
		log, detailJSON, err := scanAuditLog(rows)
		if err != nil {
			continue
		}
		if detailJSON != "" {
			_ = json.Unmarshal([]byte(detailJSON), &log.Detail)
		}
		row := log
		list = append(list, &row)
	}
	return list, rows.Err()
}

// DeleteBefore applies the retention window and reports how many rows left.
func (a *AuditLogs) DeleteBefore(cutoff time.Time) (int64, error) {
	if err := a.requireDB(); err != nil {
		return 0, err
	}
	res, err := a.db.Exec(`DELETE FROM audit_logs WHERE `+sqltime.Compare("created_at", "<"), sqltime.UTC(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
