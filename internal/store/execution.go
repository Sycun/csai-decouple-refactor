package store

import (
	"cyberstrike-ai/internal/sqltime"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Execution owns the read the notification digest makes over tool executions. It is a
// second domain's table, which is why the query lives here rather than in the notification
// handler that consumes it.
type Execution struct {
	db *sql.DB
}

// NewExecution binds the store to a connection.
func NewExecution(db *sql.DB) *Execution {
	return &Execution{db: db}
}

func (e *Execution) requireDB() error {
	if e == nil || e.db == nil {
		return errors.New("store: execution requires a database")
	}
	return nil
}

// FindNearestToolExecutionArguments returns the arguments for the execution record
// closest to a persisted tool_call detail. Eino can persist a tool_call with empty
// model arguments while the monitor execution row still has the real command/URL.
//
// Every refusal answers sql.ErrNoRows rather than an error: the history renderer treats "no arguments
// found" as "render the frame as stored", and it did so when the connection itself was missing. A
// connectionless handle therefore has to keep answering that way instead of the usual store error.
func (e *Execution) FindNearestToolExecutionArguments(conversationID, toolName string, at time.Time, window time.Duration) (string, map[string]interface{}, error) {
	conversationID = strings.TrimSpace(conversationID)
	toolName = strings.TrimSpace(toolName)
	if e == nil || e.db == nil || conversationID == "" || toolName == "" || at.IsZero() {
		return "", nil, sql.ErrNoRows
	}
	if window <= 0 {
		window = 5 * time.Second
	}
	names := []string{toolName}
	if !strings.Contains(toolName, "::") {
		names = append(names, "eino_fs::"+toolName)
	}
	start := at.Add(-window)
	end := at.Add(window)
	rows, err := e.db.Query(`
SELECT id, arguments
FROM tool_executions
WHERE conversation_id = ?
  AND tool_name IN (?, ?)
  AND julianday(start_time) BETWEEN julianday(?) AND julianday(?)
ORDER BY ABS(julianday(start_time) - julianday(?)) ASC, start_time ASC
LIMIT 1`, conversationID, names[0], names[len(names)-1], start, end, at)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", nil, err
		}
		return "", nil, sql.ErrNoRows
	}
	var id string
	var raw string
	if err := rows.Scan(&id, &raw); err != nil {
		return "", nil, err
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", nil, fmt.Errorf("parse tool execution arguments: %w", err)
	}
	return strings.TrimSpace(id), args, nil
}

// FailedExecution is one tool run that ended in failure after the cursor.
type FailedExecution struct {
	ID       string
	ToolName string
	StartSec int64
}

// FailedSince lists failed executions newer than sinceSec, newest first, capped at limit.
func (e *Execution) FailedSince(sinceSec int64, limit int) ([]FailedExecution, error) {
	if err := e.requireDB(); err != nil {
		return nil, err
	}
	rows, err := e.db.Query(`
		SELECT
			id,
			tool_name,
			`+sqltime.SecondsOrNull("start_time")+`
		FROM tool_executions
		WHERE status = 'failed'
		  AND `+sqltime.Seconds("start_time")+` > ?
		ORDER BY start_time DESC
		LIMIT ?
	`, sinceSec, limit)
	if err != nil {
		return nil, fmt.Errorf("list failed executions: %w", err)
	}
	defer rows.Close()

	out := []FailedExecution{}
	for rows.Next() {
		var f FailedExecution
		if err := rows.Scan(&f.ID, &f.ToolName, &f.StartSec); err != nil {
			return nil, fmt.Errorf("scan failed execution: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate failed executions: %w", err)
	}
	return out, nil
}
