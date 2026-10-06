package sqltime

import (
	"testing"
	"time"
)

// These literals are the SQL every call site produced before the spellings moved here. Pinning them is
// the whole argument that the consolidation changed nothing: a helper that drifts by one byte fails
// here rather than in a query that silently returns no rows.
func TestSpellingsAreTheOnesTheCallSitesAlreadyUsed(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{Compare("created_at", ">="), "strftime('%s', created_at) >= strftime('%s', ?)"},
		{Compare("start_time", "<"), "strftime('%s', start_time) < strftime('%s', ?)"},
		{Seconds("start_time"), "CAST(strftime('%s', start_time) AS INTEGER)"},
		{SecondsOrNull("created_at"), "COALESCE(CAST(strftime('%s', created_at) AS INTEGER), 0)"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("emitted %q, want the existing spelling %q", c.got, c.want)
		}
	}
}

func TestUTCIsZoneIndependent(t *testing.T) {
	// The stored text must not depend on the machine's zone: rows written on a +08:00 host have to be
	// comparable on a UTC host.
	value := time.Date(2026, 3, 5, 10, 0, 0, 0, time.FixedZone("+0800", 8*3600))
	if got := UTC(value); got != "2026-03-05T02:00:00Z" {
		t.Fatalf("UTC() = %q, want the instant converted out of its own zone", got)
	}
}

// The readers this function replaces each accepted a different subset of these forms: the conversation
// scanner took the space-separated pair plus RFC3339, the asset scanner added RFC3339Nano and a Z07:00
// variant, the batch queue took two, the knowledge item reader seven. A value in any of them has to
// decode to the same instant here, or the consolidation changed an answer.
func TestParseAcceptsEveryFormTheReplacedReadersUsed(t *testing.T) {
	utc := time.Date(2026, 10, 6, 5, 45, 30, 123456789, time.UTC)
	east := utc.In(time.FixedZone("CST", 8*3600))

	samples := []struct {
		text string
		want time.Time
		why  string
	}{
		{utc.Format(time.RFC3339Nano), utc, "what UTC() and the driver write"},
		{utc.Format(time.RFC3339), utc.Truncate(time.Second), "RFC3339 without the fraction"},
		{east.Format("2006-01-02 15:04:05.999999999-07:00"), utc, "space separated with a numeric offset"},
		{east.Format("2006-01-02 15:04:05.999999999Z07:00"), utc, "space separated, Z form of the offset"},
		{east.Format("2006-01-02T15:04:05.999999999-07:00"), utc, "T separated with a numeric offset"},
		{east.Format("2006-01-02T15:04:05.999999999Z07:00"), utc, "T separated, Z form of the offset"},
		{east.Format("2006-01-02 15:04:05-07:00"), east.Truncate(time.Second), "offset, no fraction"},
		{east.Format("2006-01-02T15:04:05-07:00"), east.Truncate(time.Second), "T, offset, no fraction"},
		{utc.Format("2006-01-02 15:04:05.999999999"), utc, "no offset, full fraction (reads as UTC)"},
		{utc.Format("2006-01-02T15:04:05.999999999"), utc, "T, no offset, full fraction"},
		{utc.Format("2006-01-02 15:04:05"), utc.Truncate(time.Second), "what datetime('now') writes"},
		{utc.Format("2006-01-02T15:04:05"), utc.Truncate(time.Second), "T, no offset, no fraction"},
		{"2026-10-06 05:45:30.123", time.Date(2026, 10, 6, 5, 45, 30, 123000000, time.UTC), "three fractional digits"},
		{"2026-10-06 05:45:30.123456", time.Date(2026, 10, 6, 5, 45, 30, 123456000, time.UTC), "six fractional digits"},
		{"2026-10-06 13:45:30+08:00", east.Truncate(time.Second), "offset without a fraction, no T"},
	}
	for _, c := range samples {
		got := Parse(c.text)
		if got.IsZero() {
			t.Fatalf("%s: %q did not parse", c.why, c.text)
		}
		if !got.Equal(c.want) {
			t.Fatalf("%s: %q decoded as %v, want %v", c.why, c.text, got, c.want)
		}
	}
}

// ParseOK exists because two of the replaced callers had to tell "unparseable" from "the zero instant":
// one returned a boolean, the other left a pointer unset.
func TestParseOKSeparatesUnparseableFromZero(t *testing.T) {
	for _, text := range []string{"", "   ", "not a time", "2026-13-45 99:99:99"} {
		if got, ok := ParseOK(text); ok || !got.IsZero() {
			t.Fatalf("%q reported parsed=%v value=%v, want false and the zero time", text, ok, got)
		}
	}
	if got, ok := ParseOK("0001-01-01 00:00:00"); !ok || !got.IsZero() {
		t.Fatalf("a stored zero instant must parse as itself: parsed=%v value=%v", ok, got)
	}
	if got := Parse("  2026-10-06 05:45:30  "); got.IsZero() {
		t.Fatal("surrounding spaces are what an older column value carries; the text is trimmed for a reason")
	}
}
