// Package sqltime owns how a Go instant is written to, and compared inside, a SQLite DATETIME column.
//
// The expressions below used to exist in two dialects at once: internal/database compared a column with
// strftime('%s', col) <op> strftime('%s', ?) against a formatted string, while the store packages
// compared CAST(strftime('%s', col) AS INTEGER) > ? against Unix seconds. Same question, two spellings,
// two parameter encodings - which is how a timezone rule ends up enforced in one family and missed in
// the other. The spellings live here now; which encoding a call site passes stays with that call site,
// because it is determined by what that site already binds.
package sqltime

import "time"

// UTC is the canonical text form of an instant: SQLite reads these back the same way regardless of the
// machine's local zone.
func UTC(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Compare renders "column <op> <param>" as a comparison of two instants by Unix seconds. The bound
// parameter is a UTC text.
func Compare(column, op string) string {
	return "strftime('%s', " + column + ") " + op + " strftime('%s', ?)"
}

// Seconds renders a column as its Unix seconds, for comparison against a parameter that is already an
// integer - which is what every store query taking a cutoff in seconds has always bound.
func Seconds(column string) string {
	return "CAST(strftime('%s', " + column + ") AS INTEGER)"
}

// SecondsOrNull is Seconds for a column that may be NULL, where "unknown" has to arrive as 0 rather
// than as a NULL the scan target cannot hold.
func SecondsOrNull(column string) string {
	return "COALESCE(" + Seconds(column) + ", 0)"
}
