package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// tool_executions is another domain's table, which is exactly why this read lives here: the history
// renderer asks one question of it and must not be able to reach anything else. The rows below are
// written with a minimal joined schema - the columns this query names, nothing more.

const toolExecutionsJoinedSchema = `
CREATE TABLE tool_executions (
	id TEXT PRIMARY KEY,
	conversation_id TEXT,
	tool_name TEXT,
	arguments TEXT,
	start_time DATETIME
);`

func newExecutionStore(t *testing.T) (*Execution, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "executions.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(toolExecutionsJoinedSchema); err != nil {
		t.Fatalf("create the joined table: %v", err)
	}
	return NewExecution(db), db
}

func insertExecution(t *testing.T, db *sql.DB, id, conversationID, toolName, arguments string, start time.Time) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO tool_executions (id, conversation_id, tool_name, arguments, start_time)
		VALUES (?, ?, ?, ?, ?)`, id, conversationID, toolName, arguments, start); err != nil {
		t.Fatalf("insert execution %s: %v", id, err)
	}
}

// TestNearestToolExecutionArgumentsPicksTheClosestRow pins the ordering the whole method exists for:
// the execution nearest in time - which is neither the newest nor the oldest, so a rewritten
// ORDER BY cannot pass it by accident.
func TestNearestToolExecutionArgumentsPicksTheClosestRow(t *testing.T) {
	e, db := newExecutionStore(t)
	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	insertExecution(t, db, "far-before", "c1", "web_fetch", `{"url":"before"}`, base.Add(-4*time.Second))
	insertExecution(t, db, "near", "c1", "web_fetch", `{"url":"near"}`, base.Add(-400*time.Millisecond))
	insertExecution(t, db, "far-after", "c1", "web_fetch", `{"url":"after"}`, base.Add(4*time.Second))

	id, args, err := e.FindNearestToolExecutionArguments("c1", "web_fetch", base, 5*time.Second)
	if err != nil {
		t.Fatalf("nearest lookup: %v", err)
	}
	if id != "near" {
		t.Fatalf("matched execution = %q, want the one 400ms away, not the oldest or the newest row", id)
	}
	if args["url"] != "near" {
		t.Fatalf("arguments = %v, want the decoded object of the matched row", args)
	}
}

// TestNearestToolExecutionArgumentsBreaksTiesOnEarliestTime pins the second key: two rows the same
// distance from the detail, one before and one after, resolve to the earlier start_time.
func TestNearestToolExecutionArgumentsBreaksTiesOnEarliestTime(t *testing.T) {
	e, db := newExecutionStore(t)
	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	insertExecution(t, db, "after", "c1", "web_fetch", `{"url":"after"}`, base.Add(2*time.Second))
	insertExecution(t, db, "before", "c1", "web_fetch", `{"url":"before"}`, base.Add(-2*time.Second))

	id, _, err := e.FindNearestToolExecutionArguments("c1", "web_fetch", base, 5*time.Second)
	if err != nil {
		t.Fatalf("tie lookup: %v", err)
	}
	if id != "before" {
		t.Fatalf("tie answered %q, want the earlier start_time", id)
	}
}

// TestNearestToolExecutionArgumentsMatchesTheEinoAlias records the second half of the reason this
// query exists: Eino persists the tool_call under eino_fs::<name> while the detail names the bare tool.
func TestNearestToolExecutionArgumentsMatchesTheEinoAlias(t *testing.T) {
	e, db := newExecutionStore(t)
	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	insertExecution(t, db, "aliased", "c1", "eino_fs::read_file", `{"path":"/etc/passwd"}`, base.Add(-2*time.Second))

	id, args, err := e.FindNearestToolExecutionArguments("c1", "read_file", base, 5*time.Second)
	if err != nil {
		t.Fatalf("alias lookup: %v", err)
	}
	if id != "aliased" || args["path"] != "/etc/passwd" {
		t.Fatalf("alias match = %q %v", id, args)
	}

	// A caller that already names the prefixed tool must not be widened to a second candidate.
	if _, _, err := e.FindNearestToolExecutionArguments("c1", "eino_fs::read_file", base, 5*time.Second); err != nil {
		t.Fatalf("already-prefixed name refused: %v", err)
	}
}

// TestNearestToolExecutionArgumentsRefusesSoftly pins the answers that let the renderer fall back to
// the frame as stored: everything it cannot answer is sql.ErrNoRows, never a panic and never a
// generic error the caller would have to distinguish.
func TestNearestToolExecutionArgumentsRefusesSoftly(t *testing.T) {
	e, db := newExecutionStore(t)
	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	insertExecution(t, db, "outside", "c1", "web_fetch", `{"url":"x"}`, base.Add(-30*time.Second))

	for _, tc := range []struct {
		name                     string
		conversationID, toolName string
		at                       time.Time
	}{
		{"outside the window", "c1", "web_fetch", base},
		{"other conversation", "c2", "web_fetch", base},
		{"unknown tool", "c1", "ls", base},
		{"blank conversation", "  ", "web_fetch", base},
		{"blank tool", "c1", "", base},
		{"zero timestamp", "c1", "web_fetch", time.Time{}},
	} {
		_, _, err := e.FindNearestToolExecutionArguments(tc.conversationID, tc.toolName, tc.at, 5*time.Second)
		if err != sql.ErrNoRows {
			t.Fatalf("%s: err = %v, want sql.ErrNoRows", tc.name, err)
		}
	}

	// A window of zero or less is not "no window": it falls back to five seconds, the way the renderer
	// has always called it.
	insertExecution(t, db, "inside-default", "c3", "web_fetch", `{"url":"y"}`, base.Add(-3*time.Second))
	id, _, err := e.FindNearestToolExecutionArguments("c3", "web_fetch", base, 0)
	if err != nil || id != "inside-default" {
		t.Fatalf("window=0 answered %q (%v), want the three-seconds-earlier row", id, err)
	}
}

// TestToolExecutionArgumentsKeepsAParseFailureVisible separates the two refusals: a row whose
// arguments column is not JSON is a real error (the caller logs it at debug), while "nothing matched"
// is ErrNoRows. Collapsing them would make a corrupt row look like an absent one.
func TestToolExecutionArgumentsKeepsAParseFailureVisible(t *testing.T) {
	e, db := newExecutionStore(t)
	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	insertExecution(t, db, "broken", "c1", "web_fetch", "not json at all", base.Add(-time.Second))

	_, _, err := e.FindNearestToolExecutionArguments("c1", "web_fetch", base, 5*time.Second)
	if err == nil {
		t.Fatal("a row whose arguments are not JSON answered no error")
	}
	if err == sql.ErrNoRows {
		t.Fatal("a corrupt arguments column was reported as \"nothing matched\"")
	}

	// And a connectionless handle stays in the soft-refusal family rather than crashing.
	if _, _, err := NewExecution(nil).FindNearestToolExecutionArguments("c1", "web_fetch", base, 5*time.Second); err != sql.ErrNoRows {
		t.Fatalf("connectionless handle = %v, want sql.ErrNoRows", err)
	}
}
