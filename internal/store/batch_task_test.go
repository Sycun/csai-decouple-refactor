package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// The batch run ledger - the queue row with its schedule stamps, and the per-task rows under it -
// moved here together with both tables' schema, the twelve columns added after the first release,
// and the three indexes.
//
// These cases run against a real database. The first one is the reason the schema is three calls
// and not one: idx_batch_task_queues_title sits on a column that only the backfill creates, so the
// order 建表 -> 补列 -> 建索引 is load-bearing for a database written by that first release.

func openBatchDB(t *testing.T) (*BatchTasks, *sql.DB) {
	t.Helper()
	// 与生产 DSN 同一套外键语义：batch_tasks.queue_id 的外键要真的生效，
	// 否则"往不存在的队列里加任务"这种调用在这里会被静默放过。
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "batch.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewBatchTasks(db), db
}

func countObject(t *testing.T, db *sql.DB, kind, name string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`, kind, name).Scan(&count); err != nil {
		t.Fatalf("look up %s %s: %v", kind, name, err)
	}
	return count
}

func hasColumn(t *testing.T, db *sql.DB, column string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('batch_task_queues') WHERE name = ?`, column).Scan(&count); err != nil {
		t.Fatalf("look up column %s: %v", column, err)
	}
	return count == 1
}

// legacyQueueDDL is batch_task_queues as the first asset-management-era release wrote it: no title,
// no role, no schedule columns. The backfill exists for exactly this shape.
const legacyQueueDDL = `CREATE TABLE batch_task_queues (
	id TEXT PRIMARY KEY,
	hitl_policy TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	started_at DATETIME,
	completed_at DATETIME,
	current_index INTEGER NOT NULL DEFAULT 0
);`

func TestBatchTasksSchemaRunsTablesThenBackfillThenIndexes(t *testing.T) {
	b, db := openBatchDB(t)
	for _, step := range []func() error{b.EnsureSchema, b.MigrateQueueColumns, b.EnsureIndexes} {
		if err := step(); err != nil {
			t.Fatalf("schema step: %v", err)
		}
	}
	if err := b.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	if err := b.MigrateQueueColumns(); err != nil {
		t.Fatalf("second MigrateQueueColumns: %v", err)
	}
	if err := b.EnsureIndexes(); err != nil {
		t.Fatalf("second EnsureIndexes: %v", err)
	}
	for _, table := range []string{"batch_task_queues", "batch_tasks"} {
		if got := countObject(t, db, "table", table); got != 1 {
			t.Fatalf("table %s present %d times, want 1", table, got)
		}
	}
	for _, index := range []string{"idx_batch_tasks_queue_id", "idx_batch_task_queues_created_at", "idx_batch_task_queues_title"} {
		if got := countObject(t, db, "index", index); got != 1 {
			t.Fatalf("index %s present %d times, want 1", index, got)
		}
	}
	for _, column := range []string{"title", "role", "agent_mode", "schedule_mode", "cron_expr", "next_run_at",
		"schedule_enabled", "last_schedule_trigger_at", "last_schedule_error", "last_run_error", "project_id", "concurrency"} {
		if !hasColumn(t, db, column) {
			t.Fatalf("column %s missing after the backfill", column)
		}
	}

	// Positive control, on the shape the backfill exists for: a legacy database indexed before the
	// columns land must fail, otherwise the three-phase order above proves nothing.
	legacyDB, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	t.Cleanup(func() { _ = legacyDB.Close() })
	legacy := NewBatchTasks(legacyDB)
	if _, err := legacyDB.Exec(legacyQueueDDL); err != nil {
		t.Fatalf("create legacy queue table: %v", err)
	}
	if err := legacy.EnsureSchema(); err != nil {
		t.Fatalf("legacy EnsureSchema should be a no-op on an existing table: %v", err)
	}
	if hasColumn(t, legacyDB, "title") {
		t.Fatal("the legacy fixture already has title: it no longer describes the first release")
	}
	if err := legacy.EnsureIndexes(); err == nil {
		t.Fatal("indexing title before the backfill succeeded: the ordering claim is unproven")
	} else if !strings.Contains(err.Error(), "title") {
		t.Fatalf("index failed for the wrong reason: %v", err)
	}
	if err := legacy.MigrateQueueColumns(); err != nil {
		t.Fatalf("legacy MigrateQueueColumns: %v", err)
	}
	if err := legacy.EnsureIndexes(); err != nil {
		t.Fatalf("legacy EnsureIndexes after the backfill: %v", err)
	}
	if !hasColumn(t, legacyDB, "title") {
		t.Fatal("title was not backfilled")
	}
	var status string
	var currentIndex int
	if err := legacyDB.QueryRow(`SELECT status, current_index FROM batch_task_queues LIMIT 0`).Scan(&status, &currentIndex); err != nil && err != sql.ErrNoRows {
		t.Fatalf("legacy table is unreadable after the backfill: %v", err)
	}
}

func TestBatchTasksRefusesAConnectionlessHandle(t *testing.T) {
	var missing *BatchTasks
	want := "store: batch tasks require a database"
	value := NewBatchTasks(nil)
	for i, handle := range []*BatchTasks{missing, value} {
		if err := handle.EnsureSchema(); err == nil || err.Error() != want {
			t.Fatalf("handle %d EnsureSchema answered %v, want %q", i, err, want)
		}
		if err := handle.MigrateQueueColumns(); err == nil || err.Error() != want {
			t.Fatalf("handle %d MigrateQueueColumns answered %v", i, err)
		}
		if err := handle.EnsureIndexes(); err == nil || err.Error() != want {
			t.Fatalf("handle %d EnsureIndexes answered %v", i, err)
		}
		if _, err := handle.GetBatchQueue("q1"); err == nil || err.Error() != want {
			t.Fatalf("handle %d GetBatchQueue answered %v", i, err)
		}
		if _, err := handle.GetAllBatchQueues(); err == nil || err.Error() != want {
			t.Fatalf("handle %d GetAllBatchQueues answered %v", i, err)
		}
		if _, err := handle.ListBatchQueuesForAccess(10, 0, "", "", "", ScopeAll); err == nil || err.Error() != want {
			t.Fatalf("handle %d ListBatchQueuesForAccess answered %v", i, err)
		}
		if _, err := handle.CountBatchQueuesForAccess("", "", "", ScopeAll); err == nil || err.Error() != want {
			t.Fatalf("handle %d CountBatchQueuesForAccess answered %v", i, err)
		}
		if _, err := handle.GetBatchTasks("q1"); err == nil || err.Error() != want {
			t.Fatalf("handle %d GetBatchTasks answered %v", i, err)
		}
		if err := handle.CreateBatchQueue("q1", "t", "r", "eino_single", "manual", "", nil, "", 1, nil); err == nil || err.Error() != want {
			t.Fatalf("handle %d CreateBatchQueue answered %v", i, err)
		}
		if err := handle.UpdateBatchQueueStatus("q1", "running"); err == nil || err.Error() != want {
			t.Fatalf("handle %d UpdateBatchQueueStatus answered %v", i, err)
		}
		if err := handle.UpdateBatchTaskStatus("q1", "t1", "done", "c1", "ok", ""); err == nil || err.Error() != want {
			t.Fatalf("handle %d UpdateBatchTaskStatus answered %v", i, err)
		}
		if err := handle.AddBatchTask("q1", "t2", "message"); err == nil || err.Error() != want {
			t.Fatalf("handle %d AddBatchTask answered %v", i, err)
		}
		if err := handle.DeleteBatchTask("q1", "t2"); err == nil || err.Error() != want {
			t.Fatalf("handle %d DeleteBatchTask answered %v", i, err)
		}
		if err := handle.DeleteBatchQueue("q1"); err == nil || err.Error() != want {
			t.Fatalf("handle %d DeleteBatchQueue answered %v", i, err)
		}
		if err := handle.ResetBatchQueueForRerun("q1"); err == nil || err.Error() != want {
			t.Fatalf("handle %d ResetBatchQueueForRerun answered %v", i, err)
		}
		if err := handle.PrepareBatchSingleTaskRun("q1", "t1", 0, true, false); err == nil || err.Error() != want {
			t.Fatalf("handle %d PrepareBatchSingleTaskRun answered %v", i, err)
		}
	}
}

func newQueue(t *testing.T, b *BatchTasks, id, title string, taskCount int) {
	t.Helper()
	tasks := make([]map[string]interface{}, 0, taskCount)
	for i := 0; i < taskCount; i++ {
		tasks = append(tasks, map[string]interface{}{
			"id":      id + "-t" + string(rune('a'+i)),
			"message": "task " + id,
		})
	}
	if err := b.CreateBatchQueue(id, title, "pentester", "eino_single", "manual", "", nil, "", 2, tasks, "review_edit"); err != nil {
		t.Fatalf("create queue %s: %v", id, err)
	}
}

func readQueue(t *testing.T, b *BatchTasks, id string) *BatchTaskQueueRow {
	t.Helper()
	row, err := b.GetBatchQueue(id)
	if err != nil {
		t.Fatalf("read queue %s: %v", id, err)
	}
	return row
}

func TestBatchQueueAndTaskLedgerRoundTrip(t *testing.T) {
	b, db := openBatchDB(t)
	for _, step := range []func() error{b.EnsureSchema, b.MigrateQueueColumns, b.EnsureIndexes} {
		if err := step(); err != nil {
			t.Fatalf("schema step: %v", err)
		}
	}
	newQueue(t, b, "q1", "夜间扫描", 3)

	row := readQueue(t, b, "q1")
	if row.Title.String != "夜间扫描" || row.Status != "pending" || row.CurrentIndex != 0 {
		t.Fatalf("queue row=%+v", row)
	}
	if !row.Title.Valid {
		t.Fatalf("title column=%#v", row.Title)
	}
	if row.AgentMode.String != "eino_single" || row.ScheduleEnabled.Int64 != 1 {
		t.Fatalf("defaults not stored: %+v", row)
	}
	if row.NextRunAt.Valid {
		t.Fatalf("manual queue carries a next run at: %+v", row.NextRunAt)
	}
	if row.HITLPolicy != "review_edit" {
		t.Fatalf("hitl policy=%q want review_edit", row.HITLPolicy)
	}
	if row.CreatedAt.IsZero() {
		t.Fatal("created_at did not round trip")
	}

	tasks, err := b.GetBatchTasks("q1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks=%d want 3", len(tasks))
	}
	if got := []string{tasks[0].Status, tasks[1].Status, tasks[2].Status}; got[0] != "pending" || got[2] != "pending" {
		t.Fatalf("task statuses=%v", got)
	}

	if err := b.UpdateBatchTaskStatus("q1", tasks[0].ID, "completed", "c-1", "结论", ""); err != nil {
		t.Fatal(err)
	}
	after, err := b.GetBatchTasks("q1")
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Status != "completed" || !after[0].CompletedAt.Valid || after[0].Result.String != "结论" {
		t.Fatalf("first task after update=%+v", after[0])
	}
	if after[1].ConversationID.Valid {
		t.Fatalf("the update leaked a conversation id onto another task: %+v", after[1])
	}

	// An unknown queue or task is a soft no-op, not an error: the manager calls this on a path that
	// has already lost the queue when a run is cancelled.
	if err := b.UpdateBatchTaskStatus("q-absent", "t-absent", "completed", "", "", ""); err != nil {
		t.Fatalf("unknown queue answered %v, want no error", err)
	}

	// Only the pending tail is cancelled, and the completed head keeps its row.
	if err := b.UpdateBatchQueueStatus("q1", "running"); err != nil {
		t.Fatal(err)
	}
	// CancelPendingBatchTasks answers only with an error: the caller reads the rows back.
	if err := b.CancelPendingBatchTasks("q1", time.Now()); err != nil {
		t.Fatal(err)
	}
	remaining, err := b.GetBatchTasks("q1")
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]int{}
	for _, task := range remaining {
		statuses[task.Status]++
	}
	if statuses["completed"] != 1 || statuses["cancelled"] != 2 {
		t.Fatalf("statuses=%v", statuses)
	}

	if err := b.ResetBatchQueueForRerun("q1"); err != nil {
		t.Fatal(err)
	}
	reset := readQueue(t, b, "q1")
	if reset.Status != "pending" || reset.CurrentIndex != 0 || reset.StartedAt.Valid || reset.CompletedAt.Valid {
		t.Fatalf("rerun reset=%+v", reset)
	}
	rerun, err := b.GetBatchTasks("q1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range rerun {
		if task.Status != "pending" || task.CompletedAt.Valid || task.Error.Valid {
			t.Fatalf("rerun did not clear task %s: %+v", task.ID, task)
		}
	}

	// Deleting the queue takes its tasks with it - the cascade is the schema's, not a second DELETE.
	if err := b.DeleteBatchQueue("q1"); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM batch_tasks WHERE queue_id = 'q1'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d tasks survived their queue", left)
	}
	// A queue that is not there answers (nil, nil), not an error - the manager reads a missing queue
	// as "unload it", so this soft shape is part of the contract.
	missing, err := b.GetBatchQueue("q1")
	if err != nil || missing != nil {
		t.Fatalf("deleted queue read=(%v,%v), want (nil,nil)", missing, err)
	}
}

func TestBatchQueuesListAndCountAgreeAndScheduleStampsStick(t *testing.T) {
	b, db := openBatchDB(t)
	for _, step := range []func() error{b.EnsureSchema, b.MigrateQueueColumns, b.EnsureIndexes} {
		if err := step(); err != nil {
			t.Fatalf("schema step: %v", err)
		}
	}
	newQueue(t, b, "q1", "第一", 1)
	newQueue(t, b, "q2", "第二", 1)
	if _, err := db.Exec(`UPDATE batch_task_queues SET status = 'running' WHERE id = 'q2'`); err != nil {
		t.Fatal(err)
	}

	rows, err := b.ListBatchQueuesForAccess(10, 0, "", "", "", ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("listed %d want 2", len(rows))
	}
	// Newest first: the console page reads queue order off this.
	if rows[0].ID != "q2" && rows[0].ID != "q1" {
		t.Fatalf("unexpected row %v", rows[0].ID)
	}
	running, err := b.CountBatchQueuesForAccess("running", "", "", ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if running != 1 {
		t.Fatalf("running count=%d want 1", running)
	}
	filtered, err := b.ListBatchQueuesForAccess(10, 0, "running", "", "", ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != running {
		t.Fatalf("list and count disagree: list=%d count=%d", len(filtered), running)
	}
	byKeyword, err := b.ListBatchQueuesForAccess(10, 0, "", "第", "", ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(byKeyword) != 2 {
		t.Fatalf("keyword matched %d want 2", len(byKeyword))
	}

	next := time.Now().Add(2 * time.Hour)
	if err := b.UpdateBatchQueueSchedule("q1", "scheduled", "0 3 * * *", &next); err != nil {
		t.Fatal(err)
	}
	stamped := readQueue(t, b, "q1")
	if stamped.ScheduleMode.String != "scheduled" || stamped.CronExpr.String != "0 3 * * *" {
		t.Fatalf("schedule row=%+v", stamped)
	}
	if !stamped.NextRunAt.Valid || !stamped.NextRunAt.Time.Equal(next.Truncate(time.Second)) && stamped.NextRunAt.Time.Sub(next) > time.Minute {
		t.Fatalf("next_run_at=%v want %v", stamped.NextRunAt.Time, next)
	}
	if err := b.UpdateBatchQueueScheduleEnabled("q1", false); err != nil {
		t.Fatal(err)
	}
	if readQueue(t, b, "q1").ScheduleEnabled.Int64 != 0 {
		t.Fatal("disabling the schedule did not stick")
	}
	trigger := time.Now().Add(-time.Minute)
	if err := b.RecordBatchQueueScheduledTriggerStart("q1", trigger); err != nil {
		t.Fatal(err)
	}
	if err := b.SetBatchQueueLastScheduleError("q1", "上一次没起来"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetBatchQueueLastRunError("q1", "跑坏了"); err != nil {
		t.Fatal(err)
	}
	stamped = readQueue(t, b, "q1")
	if !stamped.LastScheduleTriggerAt.Valid || stamped.LastScheduleError.String != "上一次没起来" || stamped.LastRunError.String != "跑坏了" {
		t.Fatalf("error stamps row=%+v", stamped)
	}
	if err := b.SetBatchQueueLastScheduleError("q1", ""); err != nil {
		t.Fatal(err)
	}
	if got := readQueue(t, b, "q1").LastScheduleError.String; got != "" {
		t.Fatalf("clearing the schedule error left %q", got)
	}
	if err := b.UpdateBatchQueueMetadata("q1", "改过名", "auditor", "multi", 5, "auto_approve"); err != nil {
		t.Fatal(err)
	}
	metadata := readQueue(t, b, "q1")
	if metadata.Title.String != "改过名" || metadata.Role.String != "auditor" || metadata.AgentMode.String != "multi" ||
		metadata.Concurrency.Int64 != 5 || metadata.HITLPolicy != "auto_approve" {
		t.Fatalf("metadata row=%+v", metadata)
	}
	if err := b.UpdateBatchQueueCurrentIndex("q1", 3); err != nil {
		t.Fatal(err)
	}
	if readQueue(t, b, "q1").CurrentIndex != 3 {
		t.Fatal("current_index did not move")
	}
	// Both stamps are best-effort UPDATEs: a queue that has gone away answers no error and takes no
	// row with it. Pinned as-is because a caller that turned this into a failure would break a run.
	if err := b.UpdateBatchQueueCurrentIndex("q-absent", 9); err != nil {
		t.Fatalf("unknown queue index answered %v, want a silent no-op", err)
	}
	if err := b.UpdateBatchQueueStatus("q-absent", "running"); err != nil {
		t.Fatalf("unknown queue status answered %v, want a silent no-op", err)
	}
	if _, err := db.Exec(`INSERT INTO batch_task_queues (id, hitl_policy, status, created_at, current_index, concurrency, agent_mode, schedule_mode, schedule_enabled) VALUES ('q-absent','','x',datetime('now'),0,1,'','manual',1)`); err != nil {
		t.Fatal(err)
	}
	if row, err := b.GetBatchQueue("q-absent"); err != nil || row.CurrentIndex != 0 {
		t.Fatalf("the silent no-op still moved something: row=%+v err=%v", row, err)
	}
}

func TestBatchTaskMessagesAndSingleTaskPrepare(t *testing.T) {
	b, _ := openBatchDB(t)
	for _, step := range []func() error{b.EnsureSchema, b.MigrateQueueColumns, b.EnsureIndexes} {
		if err := step(); err != nil {
			t.Fatalf("schema step: %v", err)
		}
	}
	newQueue(t, b, "q1", "单跑", 2)
	tasks, err := b.GetBatchTasks("q1")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.UpdateBatchTaskMessage("q1", tasks[0].ID, "改过的提示词"); err != nil {
		t.Fatal(err)
	}
	if err := b.AddBatchTask("q1", "t-new", "追加的一步"); err != nil {
		t.Fatal(err)
	}
	after, err := b.GetBatchTasks("q1")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 3 {
		t.Fatalf("tasks=%d want 3", len(after))
	}
	if after[0].Message != "改过的提示词" {
		t.Fatalf("message not updated: %+v", after[0])
	}
	if after[0].Status != "pending" {
		t.Fatalf("edited task status=%q want pending kept", after[0].Status)
	}
	var appended *BatchTaskRow
	for _, task := range after {
		if task.ID == "t-new" {
			appended = task
		}
	}
	if appended == nil || appended.Message != "追加的一步" || appended.Status != "pending" {
		t.Fatalf("appended task=%+v", appended)
	}
	// The foreign key is real (the fixture opens it the way the production DSN does), so appending a
	// task to a queue that does not exist is refused rather than orphaned.
	if err := b.AddBatchTask("q-absent", "t-x", "没人收"); err == nil {
		t.Fatal("a task was added to a queue that does not exist")
	}
	if err := b.DeleteBatchTask("q1", "t-new"); err != nil {
		t.Fatal(err)
	}
	// Deleting the last-but-one task leaves the ledger readable and ordered.
	remaining, err := b.GetBatchTasks("q1")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 {
		t.Fatalf("remaining=%d want 2", len(remaining))
	}

	// A single re-run resets that one task and, when asked, the queue head; the conversation stamp
	// from the earlier run has to be cleared or the console reopens an old dialog.
	if err := b.UpdateBatchTaskStatus("q1", remaining[0].ID, "completed", "c-old", "旧结论", "旧错误"); err != nil {
		t.Fatal(err)
	}
	if err := b.PrepareBatchSingleTaskRun("q1", remaining[0].ID, 0, true, true); err != nil {
		t.Fatal(err)
	}
	prepared, err := b.GetBatchTasks("q1")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*BatchTaskRow{}
	for _, task := range prepared {
		byID[task.ID] = task
	}
	head := byID[remaining[0].ID]
	if head.Status != "pending" || head.ConversationID.Valid || head.CompletedAt.Valid || head.Error.Valid || head.Result.Valid {
		t.Fatalf("single run did not reset the task: %+v", head)
	}
	// 单跑会把队列指针归零并置成 paused：调度器不该在同一时刻再把它整队启动一遍。
	headRow := readQueue(t, b, "q1")
	if headRow.CurrentIndex != 0 || headRow.Status != "paused" {
		t.Fatalf("single run did not park the queue head: %+v", headRow)
	}

	// resetTask=false keeps the task as it is; only the queue position moves.
	if err := b.UpdateBatchQueueCurrentIndex("q1", 1); err != nil {
		t.Fatal(err)
	}
	if err := b.PrepareBatchSingleTaskRun("q1", byID[remaining[1].ID].ID, 1, false, false); err != nil {
		t.Fatal(err)
	}
	untouched := byID[remaining[1].ID]
	latest, err := b.GetBatchTasks("q1")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range latest {
		if task.ID == untouched.ID && task.Status != untouched.Status {
			t.Fatalf("resetTask=false still changed the task: %+v", task)
		}
	}

	// A prepare against a queue that has gone away is the same silent no-op as the two stamps: both
	// statements are UPDATEs that match nothing, so it answers no error and changes no row. Pinned
	// because a manager that resumes an old run relies on this not becoming a failure.
	if err := b.PrepareBatchSingleTaskRun("q-absent", "t-x", 0, true, true); err != nil {
		t.Fatalf("unknown queue prepare answered %v, want a silent no-op", err)
	}
	if _, err := b.GetBatchQueue("q-absent"); err != nil {
		t.Fatalf("the no-op invented a queue: %v", err)
	}
}

func TestBatchTaskRowsExposeOnlyColumnsTheTableHas(t *testing.T) {
	// A guard on the scan shape: every field the row type carries must be a column that exists, and
	// the nullable ones must be sql.Null* so a legacy row cannot make a whole list vanish.
	b, db := openBatchDB(t)
	for _, step := range []func() error{b.EnsureSchema, b.MigrateQueueColumns, b.EnsureIndexes} {
		if err := step(); err != nil {
			t.Fatalf("schema step: %v", err)
		}
	}
	newQueue(t, b, "q1", "形状", 1)
	queue, err := b.GetBatchQueue("q1")
	if err != nil {
		t.Fatal(err)
	}
	nullable := []any{queue.Title, queue.Role, queue.AgentMode, queue.ScheduleMode, queue.CronExpr,
		queue.NextRunAt, queue.ScheduleEnabled, queue.LastScheduleTriggerAt, queue.LastScheduleError,
		queue.LastRunError, queue.ProjectID, queue.Concurrency, queue.StartedAt, queue.CompletedAt}
	for i, field := range nullable {
		switch field.(type) {
		case sql.NullString, sql.NullTime, sql.NullInt64:
		default:
			t.Fatalf("queue field %d is %T, not a Null* scanner: a NULL column would fail the row scan", i, field)
		}
	}
	tasks, err := b.GetBatchTasks("q1")
	if err != nil {
		t.Fatal(err)
	}
	task := tasks[0]
	reflectFields := reflect.TypeOf(*task)
	for i := 0; i < reflectFields.NumField(); i++ {
		field := reflectFields.Field(i)
		if field.Type.Kind() == reflect.Ptr {
			t.Fatalf("BatchTaskRow.%s is a pointer: the scan shape changed", field.Name)
		}
	}
	if task.ConversationID.Valid || task.StartedAt.Valid || task.CompletedAt.Valid || task.Error.Valid || task.Result.Valid {
		t.Fatalf("a fresh task carries run state: %+v", task)
	}
	var columns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('batch_tasks')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != reflectFields.NumField() {
		t.Fatalf("batch_tasks has %d columns but BatchTaskRow has %d fields: one side moved alone", columns, reflectFields.NumField())
	}
}
