package store

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func openSkillStatsStore(t *testing.T) *SkillStats {
	t.Helper()
	stats := NewSkillStats(openDB(t))
	if err := stats.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	return stats
}

func TestSkillStatsSchemaIsIdempotent(t *testing.T) {
	stats := openSkillStatsStore(t)
	if err := stats.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	empty, err := stats.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("a fresh table is not empty: %v", empty)
	}
}

// The accumulation is the contract the agent run loop depends on: one call adds one, and a report
// without a timestamp must not erase the last call that was recorded.
func TestSkillStatsAddAccumulatesAndCoalescesLastCallTime(t *testing.T) {
	stats := openSkillStatsStore(t)
	first := time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	if err := stats.Add("审计", 1, 1, 0, &first); err != nil {
		t.Fatal(err)
	}
	if err := stats.Add("审计", 1, 0, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := stats.Add("另一个", 1, 1, 0, &first); err != nil {
		t.Fatal(err)
	}

	all, err := stats.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := all["审计"]
	if got == nil {
		t.Fatalf("the skill is missing from %v", all)
	}
	if got.TotalCalls != 2 || got.SuccessCalls != 1 || got.FailedCalls != 1 {
		t.Fatalf("counters after three reports = %+v, want total 2 / success 1 / failed 1", got)
	}
	if got.LastCallTime == nil || !got.LastCallTime.Equal(first) {
		t.Fatalf("a report without a timestamp erased the recorded one: %v", got.LastCallTime)
	}
}

// last_call_time is nullable, and the page shows "never called" from a nil pointer rather than a
// zero time. A row written before any call was timed must survive the round trip as nil.
func TestSkillStatsLoadKeepsNullLastCallTime(t *testing.T) {
	stats := openSkillStatsStore(t)
	if err := stats.Add("没有时间戳", 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	all, err := stats.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := all["没有时间戳"]
	if got == nil {
		t.Fatal("the row disappeared")
	}
	if got.LastCallTime != nil {
		t.Fatalf("a NULL last_call_time came back as %v", *got.LastCallTime)
	}
	if got.TotalCalls != 1 {
		t.Fatalf("counters = %+v", got)
	}
}

func TestSkillStatsClearTargets(t *testing.T) {
	stats := openSkillStatsStore(t)
	now := time.Now()
	for _, name := range []string{"留下", "删掉"} {
		if err := stats.Add(name, 1, 1, 0, &now); err != nil {
			t.Fatal(err)
		}
	}
	if err := stats.ClearSkill("删掉"); err != nil {
		t.Fatal(err)
	}
	all, err := stats.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, still := all["删掉"]; still {
		t.Fatalf("ClearSkill removed the wrong row: %v", all)
	}
	if _, ok := all["留下"]; !ok {
		t.Fatalf("ClearSkill took a row nobody named: %v", all)
	}
	if err := stats.Clear(); err != nil {
		t.Fatal(err)
	}
	all, err = stats.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("Clear left %v", all)
	}
}

// A store built without a connection - which is what a nil *database.DB becomes on the way in -
// answers an error instead of panicking, on both the nil struct and the nil receiver.
func TestSkillStatsWithoutDatabaseIsRefused(t *testing.T) {
	if err := NewSkillStats(nil).EnsureSchema(); err == nil {
		t.Fatal("a connectionless store accepted a schema call")
	}
	if _, err := NewSkillStats(nil).Load(); err == nil {
		t.Fatal("a connectionless store returned statistics")
	}
	if err := NewSkillStats(nil).Add("x", 1, 1, 0, nil); err == nil {
		t.Fatal("a connectionless store wrote a row")
	}
	var missing *SkillStats
	if err := missing.Clear(); err == nil {
		t.Fatal("a nil store answered a clear call")
	}
}
