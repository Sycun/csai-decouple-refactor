package store

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// The usage rows point at the timeline, so a faithful fixture is the four parent tables plus
// process_details, over a connection opened the way the app opens it (foreign keys on).
func newUsageStore(t *testing.T) (*ModelTokenUsage, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "usage.db")+"?_journal_mode=WAL&_foreign_keys=1&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := `
	CREATE TABLE conversations (id TEXT PRIMARY KEY, owner_user_id TEXT, project_id TEXT);
	CREATE TABLE projects (id TEXT PRIMARY KEY, owner_user_id TEXT);
	CREATE TABLE rbac_resource_assignments (user_id TEXT, resource_type TEXT, resource_id TEXT);
	CREATE TABLE messages (id TEXT PRIMARY KEY);
	CREATE TABLE process_details (
		id TEXT PRIMARY KEY, message_id TEXT, conversation_id TEXT, event_type TEXT,
		message TEXT, data TEXT, created_at DATETIME
	);`
	if _, err := db.Exec(fixture); err != nil {
		t.Fatal(err)
	}
	usage := NewModelTokenUsage(db)
	if err := usage.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	return usage, db
}

func usageSeedConversation(t *testing.T, db *sql.DB, id, owner, project string) {
	t.Helper()
	var projectValue any
	if project != "" {
		projectValue = project
	}
	if _, err := db.Exec(`INSERT INTO conversations (id, owner_user_id, project_id) VALUES (?, ?, ?)`, id, owner, projectValue); err != nil {
		t.Fatalf("seed conversation %s: %v", id, err)
	}
}

// writeDetail is the timeline path: one process detail carrying a usage payload, which is exactly how
// the run loop reaches this table.
func writeDetail(t *testing.T, db *sql.DB, usage *ModelTokenUsage, conversationID, messageID, detailID, eventType, payload string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO messages (id) VALUES (?)`, messageID); err != nil {
		t.Fatalf("seed message %s: %v", messageID, err)
	}
	if _, err := db.Exec(`INSERT INTO process_details (id, message_id, conversation_id, event_type, message, data, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, detailID, messageID, conversationID, eventType, "note", payload,
		time.Now().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("seed process detail %s: %v", detailID, err)
	}
	if err := usage.RecordFromProcessDetail(messageID, conversationID, detailID, eventType, payload); err != nil {
		t.Fatalf("RecordFromProcessDetail: %v", err)
	}
}

const usagePayload = `{"source":"agent","orchestration":"react","reason":"final","model":"gpt-test",` +
	`"modelCalls":2,"promptTokens":100,"completionTokens":50,"totalTokens":150,"cachedTokens":7,"reasoningTokens":3}`

func TestModelTokenUsageEnsureSchemaIsIdempotent(t *testing.T) {
	usage, db := newUsageStore(t)
	if err := usage.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	for _, index := range []string{
		"idx_model_token_usage_created_at",
		"idx_model_token_usage_conversation",
		"idx_model_token_usage_project",
		"idx_model_token_usage_model",
	} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name = ?`, index).Scan(&name); err != nil {
			t.Fatalf("index %s was not created: %v", index, err)
		}
	}
}

// The hook is called for every process detail the timeline writes, so anything that is not a usage
// summary has to be ignored rather than stored or rejected.
func TestModelTokenUsageRecordFromProcessDetailOnlyTakesUsageSummaries(t *testing.T) {
	usage, db := newUsageStore(t)
	usageSeedConversation(t, db, "c-1", "u-1", "")

	writeDetail(t, db, usage, "c-1", "m-1", "d-tool", "tool_call", usagePayload)
	if rows := usageCount(t, usage); rows != 0 {
		t.Fatalf("a tool_call detail was recorded as usage: %d rows", rows)
	}

	writeDetail(t, db, usage, "c-1", "m-2", "d-zero", UsageEventType, `{"source":"agent","modelCalls":0,"totalTokens":0}`)
	if rows := usageCount(t, usage); rows != 0 {
		t.Fatalf("a payload with no counters was recorded: %d rows", rows)
	}

	writeDetail(t, db, usage, "c-1", "m-3", "d-real", UsageEventType, usagePayload)
	if rows := usageCount(t, usage); rows != 1 {
		t.Fatalf("a usage summary was not recorded: %d rows", rows)
	}

	// A detail that cannot be tied back to a conversation is not attributable, so it is not usage.
	if err := usage.RecordFromProcessDetail("m-4", "", "d-nosuch", UsageEventType, usagePayload); err != nil {
		t.Fatalf("an unattributable detail should be dropped quietly: %v", err)
	}
	if rows := usageCount(t, usage); rows != 1 {
		t.Fatalf("an unattributable detail was stored: %d rows", rows)
	}
}

func usageCount(t *testing.T, usage *ModelTokenUsage) int {
	t.Helper()
	rows, err := usage.List(TokenUsageFilter{Limit: 500, Access: Access{Scope: ScopeAll}})
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// process_detail_id is UNIQUE on purpose: a replayed timeline event updates the same row instead of
// adding a second one, so a run's totals add up once.
func TestModelTokenUsageUpsertIsIdempotentPerProcessDetail(t *testing.T) {
	usage, db := newUsageStore(t)
	// The DSN matches what the app opens with, because the backfill reads the timeline and writes this
	// table over the same pool: that only stays out of each other's way in WAL mode.
	if _, err := db.Exec(`INSERT INTO projects (id, owner_user_id) VALUES ('p-1', 'u-1')`); err != nil {
		t.Fatal(err)
	}
	usageSeedConversation(t, db, "c-1", "u-1", "p-1")
	writeDetail(t, db, usage, "c-1", "m-1", "d-1", UsageEventType, usagePayload)

	// Same detail, bigger counters: the row is rewritten, not duplicated.
	bigger := strings.Replace(usagePayload, `"totalTokens":150`, `"totalTokens":900`, 1)
	if err := usage.RecordFromProcessDetail("m-1", "c-1", "d-1", UsageEventType, bigger); err != nil {
		t.Fatal(err)
	}
	rows, err := usage.List(TokenUsageFilter{Access: Access{Scope: ScopeAll}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("re-recording one detail made %d rows, want 1", len(rows))
	}
	if rows[0].TotalTokens != 900 {
		t.Fatalf("the rewrite kept total_tokens = %d, want 900", rows[0].TotalTokens)
	}
	if rows[0].ProjectID != "p-1" {
		t.Fatalf("project_id = %q, want the conversation's p-1", rows[0].ProjectID)
	}
}

func TestModelTokenUsageTotalFallsBackToPromptPlusCompletion(t *testing.T) {
	usage, db := newUsageStore(t)
	usageSeedConversation(t, db, "c-1", "u-1", "")
	payload := `{"model":"gpt-test","promptTokens":40,"completionTokens":10}`
	writeDetail(t, db, usage, "c-1", "m-1", "d-1", UsageEventType, payload)
	rows, err := usage.List(TokenUsageFilter{Access: Access{Scope: ScopeAll}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TotalTokens != 50 {
		t.Fatalf("want one row with total 50, got %+v", rows)
	}
	// The conversation has no project, so the column is NULL and the row reads as empty rather than
	// as an error or a missing row.
	if rows[0].ProjectID != "" {
		t.Fatalf("project_id read as %q, want empty", rows[0].ProjectID)
	}
}

// A conversation that is gone makes its usage invisible: the read joins the conversation, which is
// what keeps the dashboard from listing rows nobody can open.
func TestModelTokenUsageHidesUsageWhoseConversationIsGone(t *testing.T) {
	usage, db := newUsageStore(t)
	usageSeedConversation(t, db, "c-1", "u-1", "")
	writeDetail(t, db, usage, "c-1", "m-1", "d-1", UsageEventType, usagePayload)
	if _, err := db.Exec(`DELETE FROM conversations WHERE id = 'c-1'`); err != nil {
		t.Fatal(err)
	}
	rows, err := usage.List(TokenUsageFilter{Access: Access{Scope: ScopeAll}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("usage for a deleted conversation was returned: %+v", rows)
	}
}

func TestModelTokenUsageBackfillCarriesOverHistoryOnce(t *testing.T) {
	usage, db := newUsageStore(t)
	usageSeedConversation(t, db, "c-1", "u-1", "")
	// Rows written before this table existed: the timeline has them, the usage table does not.
	if _, err := db.Exec(`INSERT INTO messages (id) VALUES ('m-old')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO process_details (id, message_id, conversation_id, event_type, message, data, created_at)
		VALUES ('d-old', 'm-old', 'c-1', ?, 'note', ?, '2026-09-01 10:00:00')`, UsageEventType, usagePayload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO process_details (id, message_id, conversation_id, event_type, message, data, created_at)
		VALUES ('d-noise', 'm-old', 'c-1', 'tool_call', 'note', ?, '2026-09-01 10:00:00')`, usagePayload); err != nil {
		t.Fatal(err)
	}
	if err := usage.BackfillFromProcessDetails(); err != nil {
		t.Fatal(err)
	}
	rows, err := usage.List(TokenUsageFilter{Access: Access{Scope: ScopeAll}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ProcessDetailID != "d-old" {
		t.Fatalf("backfill produced %+v, want only d-old", rows)
	}
	if rows[0].CreatedAt.Year() != 2026 || rows[0].CreatedAt.Month() != time.September {
		t.Fatalf("the carried row lost its original instant: %v", rows[0].CreatedAt)
	}
	// A second pass must find nothing to do - the start-up call cannot keep rewriting the table.
	if err := usage.BackfillFromProcessDetails(); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if rows, err := usage.List(TokenUsageFilter{Access: Access{Scope: ScopeAll}}); err != nil || len(rows) != 1 {
		t.Fatalf("second backfill changed the table: %d rows (%v)", len(rows), err)
	}
}

// statsFixture builds a base with two conversations, one of them in a project, and usage spread over
// yesterday and today so the day grouping has something to separate.
func statsFixture(t *testing.T) (*ModelTokenUsage, *sql.DB) {
	t.Helper()
	usage, db := newUsageStore(t)
	if _, err := db.Exec(`INSERT INTO projects (id, owner_user_id) VALUES ('p-1', 'u-project')`); err != nil {
		t.Fatal(err)
	}
	usageSeedConversation(t, db, "c-own", "u-own", "p-1")
	usageSeedConversation(t, db, "c-other", "u-other", "")
	if _, err := db.Exec(`INSERT INTO rbac_resource_assignments (user_id, resource_type, resource_id)
		VALUES ('u-assigned', 'conversation', 'c-own'), ('u-project-assigned', 'project', 'p-1')`); err != nil {
		t.Fatal(err)
	}
	today := time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.Now().Location())
	writeDetailAt(t, db, usage, "c-own", "m-1", "d-1", today.AddDate(0, 0, -1).Format("2006-01-02 15:04:05"), `{"model":"gpt-a","promptTokens":10,"completionTokens":5,"totalTokens":15}`)
	writeDetailAt(t, db, usage, "c-own", "m-2", "d-2", today.Add(9*time.Hour).Format("2006-01-02 15:04:05"), `{"orchestration":"react","promptTokens":20,"completionTokens":10,"totalTokens":30}`)
	writeDetailAt(t, db, usage, "c-other", "m-3", "d-3", today.Add(15*time.Hour).Format("2006-01-02 15:04:05"), `{"model":"gpt-a","totalTokens":7}`)
	return usage, db
}

// writeDetailAt writes the timeline row and then the usage row at an instant of the caller's choice,
// which is the shape a carried-over history row has: the detail's created_at becomes the row's.
func writeDetailAt(t *testing.T, db *sql.DB, usage *ModelTokenUsage, conversationID, messageID, detailID, createdAt, payload string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO messages (id) VALUES (?)`, messageID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO process_details (id, message_id, conversation_id, event_type, message, data, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, detailID, messageID, conversationID, UsageEventType, "note", payload, createdAt); err != nil {
		t.Fatal(err)
	}
	row, ok := usageFromProcessDetail(messageID, conversationID, detailID, payload)
	if !ok {
		t.Fatalf("payload for %s was not a usable usage row", detailID)
	}
	at, err := time.Parse("2006-01-02 15:04:05", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	row.CreatedAt = at
	if err := usage.Upsert(row); err != nil {
		t.Fatalf("upsert %s: %v", detailID, err)
	}
}

func TestModelTokenUsageStatsGroupsAndToday(t *testing.T) {
	usage, _ := statsFixture(t)
	stats, err := usage.Stats(TokenUsageFilter{Access: Access{Scope: ScopeAll}, Days: 7, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summary.Events != 3 || stats.Summary.TotalTokens != 52 {
		t.Fatalf("summary = %+v, want 3 events / 52 tokens", stats.Summary)
	}
	// The yesterday row must not count towards today.
	if stats.Today.Events != 2 || stats.Today.TotalTokens != 37 {
		t.Fatalf("today = %+v, want 2 events / 37 tokens", stats.Today)
	}
	if len(stats.ByDay) != 2 || stats.ByDay[0].Events != 2 {
		t.Fatalf("byDay = %+v, want the newest day first with 2 events", stats.ByDay)
	}
	if len(stats.ByModel) != 2 {
		t.Fatalf("byModel = %+v, want gpt-a and the no-model fallback", stats.ByModel)
	}
	var fallback *TokenUsageBreakdown
	for i := range stats.ByModel {
		if stats.ByModel[i].Key == "unknown" {
			fallback = &stats.ByModel[i]
		}
	}
	if fallback == nil || fallback.Label != "Unknown" {
		t.Fatalf("a row with no model must group under unknown/Unknown, got %+v", stats.ByModel)
	}
	if len(stats.ByOrchestration) != 2 || stats.ByOrchestration[0].Key != "react" || stats.ByOrchestration[1].Key != "unknown" {
		t.Fatalf("byOrchestration = %+v", stats.ByOrchestration)
	}
	// The two groups above are react and unknown. A payload that leaves a field out must not create a
	// fourth group named after Go's printing of nil, which is what earlier builds stored.
	for _, group := range append(append([]TokenUsageBreakdown{}, stats.ByModel...), stats.ByOrchestration...) {
		if strings.Contains(group.Key, "<nil>") || strings.Contains(group.Label, "<nil>") {
			t.Fatalf("a grouping carries the literal text of a nil: %+v", group)
		}
	}
	rows, err := usage.List(TokenUsageFilter{Access: Access{Scope: ScopeAll}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Model == "<nil>" || r.Source == "<nil>" || r.Reason == "<nil>" || r.Orchestration == "<nil>" {
			t.Fatalf("a missing payload field was stored as %q: %+v", r.Model, r)
		}
	}
	if len(stats.Recent) != 3 {
		t.Fatalf("recent = %d rows, want 3", len(stats.Recent))
	}
}

func TestModelTokenUsageListOrderLimitAndFilters(t *testing.T) {
	usage, _ := statsFixture(t)
	// Newest first: the dashboard's recent list is read top-down.
	rows, err := usage.List(TokenUsageFilter{Access: Access{Scope: ScopeAll}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].ProcessDetailID != "d-3" {
		t.Fatalf("order = %+v", rows)
	}
	if one, err := usage.List(TokenUsageFilter{Access: Access{Scope: ScopeAll}, Limit: 1}); err != nil || len(one) != 1 {
		t.Fatalf("Limit 1 returned %d rows (%v)", len(one), err)
	}
	// A conversation filter is what the per-conversation page uses.
	scoped, err := usage.List(TokenUsageFilter{ConversationID: "c-own", Access: Access{Scope: ScopeAll}})
	if err != nil || len(scoped) != 2 {
		t.Fatalf("conversation filter: %d rows (%v)", len(scoped), err)
	}
	// A project filter narrows to that project's conversations...
	inProject, err := usage.List(TokenUsageFilter{ProjectID: "p-1", Access: Access{Scope: ScopeAll}})
	if err != nil || len(inProject) != 2 {
		t.Fatalf("project filter: %d rows (%v)", len(inProject), err)
	}
	// ...and the sentinel asks for the rows whose conversation has no project.
	unbound, err := usage.List(TokenUsageFilter{ProjectID: ProjectUnbound, Access: Access{Scope: ScopeAll}})
	if err != nil || len(unbound) != 1 || unbound[0].ProcessDetailID != "d-3" {
		t.Fatalf("unbound project filter: %+v (%v)", unbound, err)
	}
	// Since/Until cut on created_at.
	fromToday, err := usage.List(TokenUsageFilter{
		Access: Access{Scope: ScopeAll},
		Since:  time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.Now().Location()),
	})
	if err != nil || len(fromToday) != 2 {
		t.Fatalf("since today: %d rows (%v)", len(fromToday), err)
	}
}

func idsOf(rows []TokenUsage) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ProcessDetailID)
	}
	sort.Strings(out)
	return out
}

// Every reachability path the console can produce, checked against the row set the query actually
// returns. This is authorization: a paraphrase that widens or narrows it is a defect, not a refactor.
func TestModelTokenUsageAccessPaths(t *testing.T) {
	usage, _ := statsFixture(t)
	tests := []struct {
		access Access
		want   []string
		why    string
	}{
		{Access{UserID: "u-own", Scope: ScopeOwn}, []string{"d-1", "d-2"}, "the conversation's owner"},
		{Access{UserID: "u-assigned", Scope: ScopeAssigned}, []string{"d-1", "d-2"}, "assigned the conversation"},
		{Access{UserID: "u-project", Scope: ScopeOwn}, []string{"d-1", "d-2"}, "owns the conversation's project"},
		{Access{UserID: "u-project-assigned", Scope: ScopeAssigned}, []string{"d-1", "d-2"}, "assigned the project"},
		{Access{UserID: "u-other", Scope: ScopeOwn}, []string{"d-3"}, "sees only what they own"},
		{Access{UserID: "u-stranger", Scope: ScopeOwn}, []string{}, "nobody else's conversation or project"},
		{Access{Scope: ScopeAll}, []string{"d-1", "d-2", "d-3"}, "an unrestricted scope needs no user"},
		{Access{}, nil, "no session at all reaches nothing"},
	}
	for _, tc := range tests {
		rows, err := usage.List(TokenUsageFilter{Access: tc.access, Limit: 100})
		if err != nil {
			t.Fatalf("%s: %v", tc.why, err)
		}
		got := idsOf(rows)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("%s: got %v, want %v", tc.why, got, tc.want)
		}
	}
}

// legacyAccessClause is the data layer's own wording of the same rule, kept here as a fixture so the
// comparison is against what the extracted query replaced rather than against itself.
const legacyAccessClause = ` AND (c.owner_user_id = ? OR EXISTS (
	SELECT 1 FROM rbac_resource_assignments ra
	WHERE ra.user_id = ? AND ra.resource_type = 'conversation' AND ra.resource_id = c.id
) OR EXISTS (
	SELECT 1 FROM projects p
	WHERE p.id = c.project_id AND (
		p.owner_user_id = ? OR EXISTS (
			SELECT 1 FROM rbac_resource_assignments pra
			WHERE pra.user_id = ? AND pra.resource_type = 'project' AND pra.resource_id = p.id
		)
	)
))`

func legacyVisibleIDs(t *testing.T, db *sql.DB, userID string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT mtu.process_detail_id
FROM model_token_usage mtu
JOIN conversations c ON c.id = mtu.conversation_id WHERE 1=1`+legacyAccessClause,
		userID, userID, userID, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// The extracted query answers with the store layer's single access clause instead of carrying a third
// copy. For every identified caller the two wordings must return the same rows; the one place they
// deliberately differ is a caller with no user id, where the old wording fell through to "everything".
func TestModelTokenUsageAccessClauseMatchesTheExtractedQuery(t *testing.T) {
	usage, db := statsFixture(t)
	for _, userID := range []string{"u-own", "u-assigned", "u-project", "u-project-assigned", "u-stranger"} {
		for _, scope := range []string{ScopeOwn, ScopeAssigned} {
			rows, err := usage.List(TokenUsageFilter{Access: Access{UserID: userID, Scope: scope}, Limit: 100})
			if err != nil {
				t.Fatalf("%s/%s: %v", userID, scope, err)
			}
			want := legacyVisibleIDs(t, db, userID)
			if got := idsOf(rows); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("%s/%s: store clause %v, extracted query %v", userID, scope, got, want)
			}
		}
	}
	// The one documented divergence. With no user id, the extracted wording added no clause at all - so
	// a request that lost its identity read every row in the base. The store's clause answers 1=0.
	anonymous, err := usage.List(TokenUsageFilter{Access: Access{}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(anonymous) != 0 {
		t.Fatalf("a caller with no identity saw %d rows, want none", len(anonymous))
	}
	rows, err := db.Query(`SELECT mtu.process_detail_id
FROM model_token_usage mtu
JOIN conversations c ON c.id = mtu.conversation_id WHERE 1=1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var unfiltered []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		unfiltered = append(unfiltered, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(unfiltered) != 3 {
		t.Fatalf("the extracted wording would have returned %v, want the whole fixture", unfiltered)
	}
}

func TestModelTokenUsageRefusesNoConnection(t *testing.T) {
	none := NewModelTokenUsage(nil)
	if err := none.EnsureSchema(); err == nil {
		t.Fatal("a connectionless store created the table")
	}
	if err := none.Upsert(TokenUsage{ProcessDetailID: "d", MessageID: "m", ConversationID: "c", ModelCalls: 1}); err == nil {
		t.Fatal("a connectionless store wrote a row")
	}
	if err := none.RecordFromProcessDetail("m", "c", "d", UsageEventType, usagePayload); err == nil {
		t.Fatal("a connectionless store accepted a timeline payload")
	}
	if _, err := none.Stats(TokenUsageFilter{}); err == nil {
		t.Fatal("a connectionless store returned stats")
	}
	if _, err := none.List(TokenUsageFilter{}); err == nil {
		t.Fatal("a connectionless store returned rows")
	}
}
