package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// rawSQL counts statements built in the HTTP layer. The refactor plan measured 32
// of these, but that figure only looked at `h.db.` - the AgentHandler. The
// HITLManager in the same package was writing 17 more on `m.db.`, so the layer's
// real starting total was 49. Both receivers are counted here.
//
// The counts are a ratchet, not an endorsement: they may only go down as domains
// move into internal/store. Lower a constant when you migrate one and update the
// per-file floors alongside it.
var rawSQLBaselines = map[string]int{
	// AgentHandler: the HITL domain moved to internal/store.HITL (11 statements), then the
	// messages domain followed - one UPDATE repeated at fifteen sites across six files, plus a
	// near-twin CASE append pair, now internal/store.Session - and finally the notification
	// digest's two reads of other domains' tables, which are now
	// store.Vulnerabilities.RecentFindings and store.Execution.FailedSince.
	// Zero means zero: the transport layer assembles no SQL any more.
	"h.db": 0,
	// HITLManager: 17 statements moved into internal/store (schema creation and the
	// reviewer/decided_by migration, the restart reconciliation over messages and
	// process_details, the interrupt insert, the conversation config, and every
	// decision write). Zero now means zero: the manager keeps in-memory state and
	// asks a store to do the durable part.
	"m.db": 0,
}

// rawSQLFloors pins what each file may keep, keyed by "receiver:file", so progress
// on one domain is not cancelled by drift on another.
//
// notification.go: its own read-state table moved to internal/store, and the HITL
// approval read moved with it. The two left here read vulnerabilities and tool
// executions - other domains' tables, so those queries belong in the vulnerability
// and execution stores rather than being folded into the notification store, which
// is exactly the cross-domain reach this refactor exists to remove.
var rawSQLFloors = map[string]int{
	"h.db:hitl.go":           0,
	"h.db:hitl_logs.go":      0,
	"h.db:hitl_execution.go": 0,
	"h.db:notification.go":   0,
	"m.db:hitl_logs.go":      0,
	// messages: the run paths ask a store to rewrite the assistant row instead of spelling
	// out the statement themselves.
	"h.db:agent.go":                0,
	"h.db:multi_agent.go":          0,
	"h.db:eino_single_agent.go":    0,
	"h.db:workflow_integration.go": 0,
	"h.db:finalization_helpers.go": 0,
	"h.db:batch_queue_executor.go": 0,
}

func countRawSQL(t *testing.T, receiver string) (int, map[string]int) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(receiver) + `\.(Exec|Query|QueryRow|Begin|Prepare)\(`)

	total := 0
	counts := map[string]int{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		hits := len(pattern.FindAllStringIndex(string(data), -1))
		if hits == 0 {
			continue
		}
		counts[name] = hits
		total += hits
	}
	return total, counts
}

func TestHandlerRawSQLRatchet(t *testing.T) {
	totals := map[string]map[string]int{}
	for receiver, baseline := range rawSQLBaselines {
		total, counts := countRawSQL(t, receiver)
		totals[receiver] = counts
		if total > baseline {
			var keys []string
			for k := range counts {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var detail []string
			for _, k := range keys {
				detail = append(detail, k+"="+strconv.Itoa(counts[k]))
			}
			t.Fatalf("raw SQL on %s in the HTTP layer grew to %d (baseline %d): %v. "+
				"New queries belong in a domain store under internal/store.", receiver, total, baseline, detail)
		}
		if total < baseline {
			t.Logf("raw SQL on %s dropped to %d; tighten rawSQLBaselines and the floors", receiver, total)
		}
	}

	for key, floor := range rawSQLFloors {
		if !strings.Contains(key, ":") {
			t.Fatalf("floor %q must be keyed \"receiver:file\"", key)
		}
		receiver, file, _ := strings.Cut(key, ":")
		counts, ok := totals[receiver]
		if !ok {
			t.Fatalf("floor %q names a receiver with no baseline", key)
		}
		if got := counts[file]; got > floor {
			t.Errorf("%s on %s migrated to a store must keep raw SQL at %d, found %d", file, receiver, floor, got)
		}
	}
}

// TestStorePackageHoldsNoHTTPConcerns guards the boundary the extraction depends
// on: a domain store must not reach back into the layers above it.
func TestStorePackageHoldsNoHTTPConcerns(t *testing.T) {
	files, err := filepath.Glob("../store/*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("internal/store is not visible from here")
	}
	forbidden := regexp.MustCompile(`"cyberstrike-ai/internal/(database|handler|config)"|"github\.com/gin-gonic/gin"`)
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if hit := forbidden.FindString(string(data)); hit != "" {
			t.Errorf("%s imports %s: a domain store takes a connection, not the layers above it", filepath.Base(name), hit)
		}
	}
}
