package database

import (
	"errors"
	"strings"
	"time"

	"cyberstrike-ai/internal/sqltime"
)

// formatSQLiteUTC and sqliteEpochGE are one-line delegations: the only spellings of these two
// expressions live in internal/sqltime, and every call site in this package keeps its own parameter
// encoding.
func formatSQLiteUTC(t time.Time) string { return sqltime.UTC(t) }

func sqliteEpochGE(column, op string) string { return sqltime.Compare(column, op) }

// ParseRFC3339Time parses API/query timestamps (RFC3339 or RFC3339Nano).
func ParseRFC3339Time(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("empty time value")
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
