// Package sqltime owns how a Go instant is written to, and compared inside, a SQLite DATETIME column.
//
// The expressions below used to exist in two dialects at once: internal/database compared a column with
// strftime('%s', col) <op> strftime('%s', ?) against a formatted string, while the store packages
// compared CAST(strftime('%s', col) AS INTEGER) > ? against Unix seconds. Same question, two spellings,
// two parameter encodings - which is how a timezone rule ends up enforced in one family and missed in
// the other. The spellings live here now; which encoding a call site passes stays with that call site,
// because it is determined by what that site already binds.
package sqltime

import (
	"strings"
	"time"
)

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

// layouts are the text forms a SQLite DATETIME column has been written in. UTC and the driver's own
// RFC3339 output come first because that is what the current build writes; the space-separated family
// is what older builds and `datetime('now')` produce, with and without an offset and with a variable
// number of fractional digits.
//
// A `.999999999` fractional layout also parses shorter fractions, and a space in the layout also matches
// the `T` separator - so this list deliberately overlaps: dropping any single entry still parses every
// known stored form (verified, all twelve ways round). A generous reader is the point; a legacy base may
// hold any of them, and the cost of a redundant attempt is one failed comparison.
var layouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05-07:00",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
}

// Parse reads one column value back into an instant, and answers the zero time for anything in none of
// those forms.
//
// Before this function existed the same column was read by a dozen local parsers, each accepting a
// different subset - so whether a row carried a real time or a zero one depended on which code path
// happened to read it. One accepted set means one answer.
func Parse(text string) time.Time {
	t, _ := ParseOK(text)
	return t
}

// ParseOK is Parse for the callers that have to distinguish "no instant" from "the zero instant":
// the asset scanner returns whether a value parsed at all, and a monitor summary leaves a field unset
// rather than setting it to year one.
func ParseOK(text string) (time.Time, bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
