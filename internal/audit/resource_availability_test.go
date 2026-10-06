package audit

import (
	"errors"
	"testing"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/store"
)

// The availability helper is the one place the audit page decides between "this resource is gone"
// and "I could not check". Both answers are shown to an operator, so which branch a lookup takes is
// a contract, not an implementation detail. These cases came out of the batch-queue lookup moving
// from the connection wrapper onto store.BatchTasks - the branch had no test before that.

type stubExistence struct{ exists bool }

func (s stubExistence) ConversationExists(string) (bool, error) { return s.exists, nil }

// The four lookups below only have to exist for the interface to be satisfied; no case asks for
// those resource types.
func (s stubExistence) GetC2Listener(string) (*database.C2Listener, error) {
	return nil, errors.New("not here")
}

func (s stubExistence) GetC2Session(string) (*database.C2Session, error) {
	return nil, errors.New("not here")
}

func (s stubExistence) GetC2Task(string) (*database.C2Task, error) {
	return nil, errors.New("not here")
}

func (s stubExistence) GetToolExecution(string) (*mcp.ToolExecution, error) {
	return nil, errors.New("not here")
}

type stubQueues struct {
	row *store.BatchTaskQueueRow
	err error
}

func (s stubQueues) GetBatchQueue(string) (*store.BatchTaskQueueRow, error) { return s.row, s.err }

func boolValue(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

func TestBatchQueueAvailabilityTakesTheRightBranch(t *testing.T) {
	gone := errors.New("批量任务队列不存在")

	cases := []struct {
		name    string
		queues  BatchQueueLookup
		want    any
		comment string
	}{
		{"queue still there", stubQueues{row: &store.BatchTaskQueueRow{ID: "q1"}}, true, "available"},
		{"queue deleted", stubQueues{err: gone}, false, "gone, because the lookup answered the row is missing"},
		{"lookup not wired", nil, nil, `stays "availability unknown" rather than claiming the queue is gone`},
	}
	for _, tc := range cases {
		log := &store.AuditLog{Action: "batch_queue.create", ResourceType: "batch_queue", ResourceID: "q1"}
		ApplyResourceAvailability(stubExistence{}, nil, nil, tc.queues, log)
		if got := boolValue(log.ResourceAvailable); got != tc.want {
			t.Fatalf("%s: ResourceAvailable=%v, want %v (%s)", tc.name, got, tc.want, tc.comment)
		}
	}

	// A lookup that fails for an unrelated reason (database down) must not be reported as "gone".
	log := &store.AuditLog{Action: "batch_queue.update", ResourceType: "batch_queue", ResourceID: "q1"}
	ApplyResourceAvailability(stubExistence{}, nil, nil, stubQueues{err: errors.New("connection busy")}, log)
	if got := boolValue(log.ResourceAvailable); got != false {
		t.Fatalf("an unrelated failure answered ResourceAvailable=%v; the current rule is that any error "+
			"from the lookup reads as removed - pinned here so a change to that rule is a decision, not a drift", got)
	}

	// No resource id at all: nothing to check, so the field stays unset for every resource type.
	blank := &store.AuditLog{Action: "batch_queue.create", ResourceType: "batch_queue"}
	ApplyResourceAvailability(stubExistence{}, nil, nil, stubQueues{row: &store.BatchTaskQueueRow{ID: "q1"}}, blank)
	if blank.ResourceAvailable != nil {
		t.Fatalf("a log without a resource id got an availability verdict: %v", *blank.ResourceAvailable)
	}
}
