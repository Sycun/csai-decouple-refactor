package store

import (
	"cyberstrike-ai/internal/sqltime"
	"database/sql"
	"errors"
	"fmt"
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
