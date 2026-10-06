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
