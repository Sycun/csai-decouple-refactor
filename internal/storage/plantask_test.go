package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The task board is read straight off Eino's per-conversation directory, so these cases write real
// files: ordering, the ID fallback, the two skip rules and the "no directory yet" answer are all
// things a caller shows an operator.

func writeTask(t *testing.T, dir, number, body string) string {
	t.Helper()
	name := filepath.Join(dir, number+".json")
	if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return name
}

func TestReadPlanTasksOrdersSkipsAndTolerates(t *testing.T) {
	dir := t.TempDir()

	// Written out of numeric order on purpose: the board order is the file number, not mtime.
	writeTask(t, dir, "10", `{"subject":"第十步","status":"pending"}`)
	writeTask(t, dir, "2", `{"subject":"第二步","status":"in_progress","activeForm":"正在跑第二步"}`)
	writeTask(t, dir, "1", `{"id":"explicit-1","subject":"第一步","status":"completed"}`)
	// Deleted rows stay on disk for model continuity but must not be listed.
	writeTask(t, dir, "3", `{"subject":"已删的","status":"DELETED"}`)
	// Not a task file at all.
	writeTask(t, dir, "not-a-number", `{"subject":"忽略"}`)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "4"), 0o755); err != nil {
		t.Fatal(err)
	}

	tasks, err := ReadPlanTasks(dir, time.Time{}, nil)
	if err != nil {
		t.Fatalf("ReadPlanTasks: %v", err)
	}
	got := make([]string, 0, len(tasks))
	for _, task := range tasks {
		got = append(got, task.ID+"|"+task.Subject)
	}
	// 1 keeps the id it carries; the others fall back to the file name.
	want := []string{"explicit-1|第一步", "2|第二步", "10|第十步"}
	if len(got) != len(want) {
		t.Fatalf("tasks=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order/value at %d: got %v want %v", i, got, want)
		}
	}
	if tasks[1].ActiveForm != "正在跑第二步" {
		t.Fatalf("activeForm not decoded: %+v", tasks[1])
	}

	// A malformed file (a concurrent TaskUpdate caught mid-write) is skipped, not fatal.
	writeTask(t, dir, "5", `{"subject":"写到一半`)
	after, err := ReadPlanTasks(dir, time.Time{}, nil)
	if err != nil {
		t.Fatalf("a partial file must not fail the read: %v", err)
	}
	if len(after) != 3 {
		t.Fatalf("after adding a broken file the board has %d rows, want the 3 good ones", len(after))
	}

	// since filters by file mtime, which is how a new run hides the previous run's board.
	fresh := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(filepath.Join(dir, "20.json"), []byte(`{"subject":"这一步是新的"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "20.json"), fresh, fresh); err != nil {
		t.Fatal(err)
	}
	limited, err := ReadPlanTasks(dir, fresh, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].Subject != "这一步是新的" {
		t.Fatalf("since filter returned %+v, want only the freshly stamped file", limited)
	}
}

func TestReadPlanTasksMissingDirectoryIsEmpty(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-conversation")
	tasks, err := ReadPlanTasks(missing, time.Time{}, nil)
	if err != nil {
		t.Fatalf("a conversation with no board yet must not error: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("tasks=%v, want empty", tasks)
	}
}
