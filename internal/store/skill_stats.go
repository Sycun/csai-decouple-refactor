package store

import (
	"database/sql"
	"errors"
	"time"
)

// SkillStats owns the skill_stats table: one row per skill name, counting calls.
//
// It used to be five methods on the data layer's connection wrapper, reached from the skills page
// and from the agent run loop. A table with two callers and no owner cannot be changed, tested, or
// reasoned about as one thing, so the SQL - and the schema - moved here together.
type SkillStats struct {
	db *sql.DB
}

// NewSkillStats binds the store to a connection.
func NewSkillStats(db *sql.DB) *SkillStats {
	return &SkillStats{db: db}
}

func (s *SkillStats) requireDB() error {
	if s == nil || s.db == nil {
		return errors.New("store: skill stats requires a database")
	}
	return nil
}

// SkillCallStats is one skill's counters. LastCallTime stays a pointer: the column is nullable, and
// the page distinguishes "never called" from "called at a time".
type SkillCallStats struct {
	SkillName    string
	TotalCalls   int
	SuccessCalls int
	FailedCalls  int
	LastCallTime *time.Time
}

const createSkillStatsTable = `
	CREATE TABLE IF NOT EXISTS skill_stats (
		skill_name TEXT PRIMARY KEY,
		total_calls INTEGER NOT NULL DEFAULT 0,
		success_calls INTEGER NOT NULL DEFAULT 0,
		failed_calls INTEGER NOT NULL DEFAULT 0,
		last_call_time DATETIME,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`

// EnsureSchema creates the table. Start-up calls it, the same way it calls every other domain's
// schema; the statement is idempotent.
func (s *SkillStats) EnsureSchema() error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(createSkillStatsTable)
	return err
}

const loadSkillStatsSQL = `
		SELECT skill_name, total_calls, success_calls, failed_calls, last_call_time
		FROM skill_stats
	`

// Load returns every skill's counters, keyed by name.
//
// A row that will not scan used to be logged and skipped, which loses a skill's statistics without
// any caller finding out; it is an error now. The nullable column was already scanned as NullTime,
// so this is not the silent-row-loss class - it is corruption or a schema someone changed under us.
func (s *SkillStats) Load() (map[string]*SkillCallStats, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(loadSkillStatsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make(map[string]*SkillCallStats)
	for rows.Next() {
		var stat SkillCallStats
		var lastCallTime sql.NullTime
		if err := rows.Scan(&stat.SkillName, &stat.TotalCalls, &stat.SuccessCalls, &stat.FailedCalls, &lastCallTime); err != nil {
			return nil, err
		}
		if lastCallTime.Valid {
			stat.LastCallTime = &lastCallTime.Time
		}
		stats[stat.SkillName] = &stat
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

const addSkillStatsSQL = `
		INSERT INTO skill_stats (skill_name, total_calls, success_calls, failed_calls, last_call_time, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(skill_name) DO UPDATE SET
			total_calls = total_calls + ?,
			success_calls = success_calls + ?,
			failed_calls = failed_calls + ?,
			last_call_time = COALESCE(?, last_call_time),
			updated_at = ?
	`

// Add accumulates one call into a skill's counters. A nil lastCallTime does not erase the recorded
// one: the column is COALESCEd, because the run loop reports "this happened" without always knowing
// when the previous call was.
func (s *SkillStats) Add(skillName string, totalCalls, successCalls, failedCalls int, lastCallTime *time.Time) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	var last sql.NullTime
	if lastCallTime != nil {
		last = sql.NullTime{Time: *lastCallTime, Valid: true}
	}
	now := time.Now()
	_, err := s.db.Exec(addSkillStatsSQL,
		skillName, totalCalls, successCalls, failedCalls, last, now,
		totalCalls, successCalls, failedCalls, last, now,
	)
	return err
}

// Clear drops every counter.
func (s *SkillStats) Clear() error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM skill_stats`)
	return err
}

// ClearSkill drops one skill's counters, leaving the others.
func (s *SkillStats) ClearSkill(skillName string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM skill_stats WHERE skill_name = ?`, skillName)
	return err
}
