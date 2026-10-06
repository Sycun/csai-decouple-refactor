package audit

import (
	"errors"
	"testing"

	"cyberstrike-ai/internal/store"
)

// The availability helper is the one place the audit page decides between "this resource is gone"
// and "I could not check". Both answers are shown to an operator, so which branch a lookup takes is
// a contract, not an implementation detail. These cases came out of the batch-queue lookup moving
// from the connection wrapper onto store.BatchTasks - the branch had no test before that.

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
		ApplyResourceAvailability(nil, nil, nil, nil, nil, tc.queues, log)
		if got := boolValue(log.ResourceAvailable); got != tc.want {
			t.Fatalf("%s: ResourceAvailable=%v, want %v (%s)", tc.name, got, tc.want, tc.comment)
		}
	}

	// A lookup that fails for an unrelated reason (database down) must not be reported as "gone".
	log := &store.AuditLog{Action: "batch_queue.update", ResourceType: "batch_queue", ResourceID: "q1"}
	ApplyResourceAvailability(nil, nil, nil, nil, nil, stubQueues{err: errors.New("connection busy")}, log)
	if got := boolValue(log.ResourceAvailable); got != false {
		t.Fatalf("an unrelated failure answered ResourceAvailable=%v; the current rule is that any error "+
			"from the lookup reads as removed - pinned here so a change to that rule is a decision, not a drift", got)
	}

	// No resource id at all: nothing to check, so the field stays unset for every resource type.
	blank := &store.AuditLog{Action: "batch_queue.create", ResourceType: "batch_queue"}
	ApplyResourceAvailability(nil, nil, nil, nil, nil, stubQueues{row: &store.BatchTaskQueueRow{ID: "q1"}}, blank)
	if blank.ResourceAvailable != nil {
		t.Fatalf("a log without a resource id got an availability verdict: %v", *blank.ResourceAvailable)
	}
}

// TestC2AvailabilityTakesTheRightBranch pins the same two answers for the C2 lookups, which left the
// connection wrapper on 2026-10-07 and are answered by store.C2 now.
func TestC2AvailabilityTakesTheRightBranch(t *testing.T) {
	// A store that was never wired (no database) answers "unknown", not "gone": the operator keeps
	// seeing the resource as unchecked.
	log := &store.AuditLog{Action: "listener_update", ResourceType: "c2_listener", ResourceID: "l_1"}
	ApplyResourceAvailability(nil, nil, nil, nil, nil, nil, log)
	if log.ResourceAvailable != nil {
		t.Fatalf("an unwired C2 lookup produced a verdict: %v", *log.ResourceAvailable)
	}

	// A store bound to no connection refuses the call with an error; any lookup error reads as
	// "removed", the rule pinned in the batch-queue cases. What this checks is that the refusal is an
	// error rather than a panic on a nil receiver.
	log = &store.AuditLog{Action: "listener_update", ResourceType: "c2_listener", ResourceID: "l_1"}
	ApplyResourceAvailability(nil, store.NewC2(nil), nil, nil, nil, nil, log)
	if log.ResourceAvailable == nil || *log.ResourceAvailable {
		t.Fatalf("a refusing C2 store answered ResourceAvailable=%v; want false", boolValue(log.ResourceAvailable))
	}
}
