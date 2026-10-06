package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// The monitor ledger - tool_executions, tool_stats, their schema and the aggregations the console
// reads - moved out of the connection wrapper. These cases run against a real database: the schema
// guard proves a fresh install builds every object and that the late columns come back on a legacy
// table, and the roundtrips pin the SQL that moved verbatim.

func testMonitorStore(t *testing.T, access func(userID, scope, resourceType, resourceID string) bool) (*Monitor, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m := NewMonitor(db, access)
	if err := m.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := m.MigrateLateColumns(); err != nil {
		t.Fatalf("MigrateLateColumns: %v", err)
	}
	if err := m.EnsureIndexes(); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	return m, db
}

func TestMonitorEnsureSchemaIsIdempotentAndBuildsEveryObject(t *testing.T) {
	m, db := testMonitorStore(t, nil)
	if err := m.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	if err := m.EnsureIndexes(); err != nil {
		t.Fatalf("second EnsureIndexes: %v", err)
	}
	for _, object := range []struct{ kind, name string }{
		{"table", "tool_executions"}, {"table", "tool_stats"},
		{"index", "idx_tool_executions_tool_name"}, {"index", "idx_tool_executions_start_time"},
		{"index", "idx_tool_executions_status"}, {"index", "idx_tool_executions_owner"},
		{"index", "idx_tool_executions_conversation"},
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`,
			object.kind, object.name).Scan(&count); err != nil {
			t.Fatalf("look up %s %s: %v", object.kind, object.name, err)
		}
		if count != 1 {
			t.Fatalf("%s %s present %d times, want 1", object.kind, object.name, count)
		}
	}
}

func TestMonitorMigrateLateColumnsBackfillsALegacyTable(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m := NewMonitor(db, nil)
	// A database written by the first release: no partial-output, owner or conversation columns.
	if _, err := db.Exec(`CREATE TABLE tool_executions (
		id TEXT PRIMARY KEY, tool_name TEXT NOT NULL, arguments TEXT NOT NULL, status TEXT NOT NULL,
		start_time DATETIME NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed legacy table: %v", err)
	}
	if err := m.MigrateLateColumns(); err != nil {
		t.Fatalf("MigrateLateColumns: %v", err)
	}
	if err := m.MigrateLateColumns(); err != nil {
		t.Fatalf("second MigrateLateColumns: %v", err)
	}
	for _, col := range []string{"partial_output", "partial_output_bytes", "partial_output_truncated",
		"partial_output_updated_at", "owner_user_id", "conversation_id"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('tool_executions') WHERE name = ?`, col).Scan(&count); err != nil {
			t.Fatalf("read pragma for %s: %v", col, err)
		}
		if count != 1 {
			t.Fatalf("column %s present %d times after the backfill, want 1", col, count)
		}
	}
}

func TestMonitorExecutionRoundTripsItsJSONColumns(t *testing.T) {
	m, _ := testMonitorStore(t, nil)
	now := time.Now().Truncate(time.Millisecond)
	end := now.Add(2 * time.Second)
	started := &ToolExecution{
		ID:       "exec-1",
		ToolName: "test::tool",
		Arguments: map[string]interface{}{
			"command": "echo hi",
		},
		Status:                 ToolExecutionStatusCompleted,
		Result:                 &ToolResult{Content: []Content{{Type: "text", Text: "hi"}}},
		StartTime:              now,
		EndTime:                &end,
		Duration:               2 * time.Second,
		PartialOutput:          "hi",
		PartialOutputBytes:     2,
		PartialOutputTruncated: true,
		PartialOutputUpdatedAt: &now,
		OwnerUserID:            "u1",
		ConversationID:         "c1",
	}
	if err := m.SaveToolExecution(started); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := m.GetToolExecution("exec-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("execution vanished")
	}
	if got.Arguments["command"] != "echo hi" || got.Result == nil || len(got.Result.Content) != 1 || got.Result.Content[0].Text != "hi" {
		t.Fatalf("JSON columns round-tripped as %#v / %#v", got.Arguments, got.Result)
	}
	if !got.PartialOutputTruncated || got.PartialOutputBytes != 2 || got.PartialOutput != "hi" {
		t.Fatalf("partial output columns came back as %#v", got)
	}
	if got.Duration != 2*time.Second || got.EndTime == nil || !got.EndTime.Equal(end) {
		t.Fatalf("duration/end time came back as %v / %v", got.Duration, got.EndTime)
	}
	if got.OwnerUserID != "u1" || got.ConversationID != "c1" {
		t.Fatalf("ownership columns came back as %q / %q", got.OwnerUserID, got.ConversationID)
	}
	list, err := m.LoadToolExecutionsWithPagination(0, 10, ToolExecutionStatusCompleted, "test")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "exec-1" {
		t.Fatalf("list = %#v, want the saved execution", list)
	}
}

func TestMonitorStatsAccumulateAndDecayToZero(t *testing.T) {
	m, _ := testMonitorStore(t, nil)
	now := time.Now()
	if err := m.UpdateToolStats("t1", 3, 2, 1, &now); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := m.UpdateToolStats("t1", 2, 0, 2, nil); err != nil {
		t.Fatalf("second update: %v", err)
	}
	stats, err := m.LoadToolStats()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stats["t1"] == nil || stats["t1"].TotalCalls != 5 || stats["t1"].SuccessCalls != 2 || stats["t1"].FailedCalls != 3 {
		t.Fatalf("stats = %#v, want accumulated totals", stats["t1"])
	}
	if err := m.DecreaseToolStats("t1", 5, 2, 3); err != nil {
		t.Fatalf("decrease: %v", err)
	}
	stats, err = m.LoadToolStats()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, present := stats["t1"]; present {
		t.Fatalf("stats = %#v, want the zero row deleted", stats["t1"])
	}
}

func TestMonitorSummaryAndPurgeAgreeWithTheRows(t *testing.T) {
	m, _ := testMonitorStore(t, nil)
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	save := func(id, tool, status string, at time.Time) {
		t.Helper()
		if err := m.SaveToolExecution(&ToolExecution{ID: id, ToolName: tool, Status: status,
			StartTime: at, Arguments: map[string]interface{}{}}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	save("e1", "t1", ToolExecutionStatusCompleted, now)
	save("e2", "t1", ToolExecutionStatusFailed, now)
	save("e3", "t2", ToolExecutionStatusBlocked, now)
	save("e4", "t2", ToolExecutionStatusCompleted, old)
	if err := m.UpdateToolStats("t1", 2, 1, 1, &now); err != nil {
		t.Fatalf("stats: %v", err)
	}
	if err := m.UpdateToolStats("t2", 2, 1, 0, &now); err != nil {
		t.Fatalf("stats: %v", err)
	}
	summary, err := m.LoadToolStatsSummary(6)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Summary.TotalCalls != 4 || summary.Summary.SuccessCalls != 2 || summary.Summary.FailedCalls != 1 || summary.Summary.BlockedCalls != 1 {
		t.Fatalf("summary = %#v, want 4/2/1/1", summary.Summary)
	}
	if len(summary.TopTools) != 2 || summary.TopTools[0].ToolName != "t1" {
		t.Fatalf("top tools = %#v", summary.TopTools)
	}
	deleted, err := m.PurgeToolExecutionsBefore(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("purged %d rows, want the single 48h-old one", deleted)
	}
	stats, err := m.LoadToolStats()
	if err != nil {
		t.Fatalf("load stats: %v", err)
	}
	if stats["t2"] == nil || stats["t2"].TotalCalls != 1 || stats["t2"].SuccessCalls != 0 {
		t.Fatalf("t2 stats after purge = %#v, want the deleted row's counts subtracted", stats["t2"])
	}
}

func TestMonitorAccessUsesTheInjectedRuleOnlyForTheConversation(t *testing.T) {
	var asked []string
	m, _ := testMonitorStore(t, func(userID, scope, resourceType, resourceID string) bool {
		asked = append(asked, resourceType+":"+resourceID)
		return resourceID == "c-visible"
	})
	if err := m.SaveToolExecution(&ToolExecution{ID: "owned", ToolName: "t", Status: ToolExecutionStatusCompleted,
		StartTime: time.Now(), Arguments: map[string]interface{}{}, OwnerUserID: "u1"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := m.SaveToolExecution(&ToolExecution{ID: "shared", ToolName: "t", Status: ToolExecutionStatusCompleted,
		StartTime: time.Now(), Arguments: map[string]interface{}{}, ConversationID: "c-visible"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !m.UserCanAccessToolExecution("u1", ScopeOwn, "owned") {
		t.Fatal("the owner must reach their own execution without asking the rule")
	}
	if len(asked) != 0 {
		t.Fatalf("the owner branch asked the injected rule anyway: %v", asked)
	}
	if !m.UserCanAccessToolExecution("u2", ScopeAssigned, "shared") {
		t.Fatalf("the conversation branch must consult the injected rule (asked=%v)", asked)
	}
	if m.UserCanAccessToolExecution("u2", ScopeAssigned, "owned") {
		t.Fatal("a non-owner must not reach an unshared execution")
	}
	if m.UserCanAccessToolExecution("", ScopeOwn, "owned") {
		t.Fatal("an empty user must not reach anything")
	}
}
