package layering

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// rawSQLByFile is the remaining reach of SQL outside the two layers that own it: every file where a
// package still executes a statement.
//
// It is empty, and that is the point of the data-layer slice. internal/knowledge was the last owner
// outside internal/database and internal/store holding its SQL inline (the vector rows on
// knowledge_embeddings), and those statements are store.KnowledgeEmbeddings' now.
//
// The map stays rather than the gate turning off, because a half-migrated table has to be listable
// with a ceiling. Adding a file here is a review item; deleting one is progress.
//
// This axis being zero does not mean the SQL is gone: the same scan counts 448 statements still
// written inside internal/database, which owns the schema-wide queries. Those are tracked by the
// method ceiling in database_surface_ratchet_test.go, not here.
var rawSQLByFile = map[string]int{}

// rawSQLReceiver matches an executing call through a database-shaped handle. It is deliberately
// narrow (a name), so scope.guard.Prepare, a process guard, does not read as SQL - which is why
// sqlTextAlone exists next to it.
var rawSQLReceiver = regexp.MustCompile(`\b(?:db|d|conn|sqlDB)\.(?:Exec|ExecContext|Query|QueryContext|QueryRow|QueryRowContext|Begin|BeginTx|Prepare|PrepareContext|MustExec)\(`)

// sqlTextAlone catches a statement wherever it is written, however the handle is named: a string
// literal that opens with SQL. Matching only on the receiver name is how a gate goes blind to the
// next migration - the query someone builds in a helper called buildQuery and runs on mgr.sqlite.
var sqlTextAlone = regexp.MustCompile("(?:[\"]|`)\\s*(?:SELECT\\b|INSERT\\b\\s+INTO\\b|UPDATE\\b|DELETE\\b\\s+FROM\\b)")

// rawSQLControls prove both scanners fire. With an empty baseline, "found nothing" and "the scanner
// is broken" look identical, so the scanner carries its own positive control instead of borrowing
// evidence from real debt.
var rawSQLControls = []string{
	`db.Exec("DELETE FROM t WHERE id = ?", id)`,
	`rows, err := db.QueryContext(ctx, q, args...)`,
	`tx, err := db.BeginTx(ctx, nil)`,
	`row := sqlDB.QueryRow("SELECT 1")`,
	`c, err := conn.Prepare("SELECT 2")`,
	"stmt := \"UPDATE t SET x = 1\"",
	"q := `INSERT INTO t (a) VALUES (?)`",
	"body := `SELECT id, name FROM t`",
}

// rawSQLWalkFloor is a lower bound on the production files the walk must consider for the scan to
// have any claim of covering the layer. 552 non-test .go files sit under internal/ and cmd/ today,
// 45 of them inside the two owning layers, so 507 remain to be scanned.
const rawSQLWalkFloor = 500

func TestRawSQLIsOnlyWrittenByTheLayersThatOwnIt(t *testing.T) {
	for _, control := range rawSQLControls {
		if rawSQLReceiver.FindStringIndex(control) == nil && sqlTextAlone.FindStringIndex(control) == nil {
			t.Fatalf("neither scanner matched the control %q: this gate cannot see the SQL it claims to prevent", control)
		}
	}

	root := moduleRoot(t)
	walked := 0
	counted := 0
	var offenders []string

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !(strings.HasPrefix(rel, "internal/") || strings.HasPrefix(rel, "cmd/")) {
			return nil
		}
		if strings.HasPrefix(rel, "internal/database/") || strings.HasPrefix(rel, "internal/store/") {
			return nil
		}
		walked++
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		found := len(rawSQLReceiver.FindAllStringIndex(string(data), -1)) + len(sqlTextAlone.FindAllStringIndex(string(data), -1))
		if found == 0 {
			return nil
		}
		counted++
		baseline, listed := rawSQLByFile[rel]
		if !listed {
			offenders = append(offenders, rel+": "+strconv.Itoa(found)+" raw statement(s), a file not reviewed for this list")
			return nil
		}
		if found > baseline {
			offenders = append(offenders, rel+": "+strconv.Itoa(found)+" raw statements, ceiling "+strconv.Itoa(baseline))
		}
		return nil
	})

	// A broken walk reports an empty layer the same way a clean one does, so the coverage of the scan
	// is asserted separately from its result.
	if walked < rawSQLWalkFloor {
		t.Fatalf("the walk considered %d production files, want at least %d: the scan is not reading internal/ and cmd/",
			walked, rawSQLWalkFloor)
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("SQL is being written outside internal/store and internal/database:\n%s\n"+
			"The destination is a store that owns those tables - see internal/store/skill_stats.go "+
			"for the shape (SQL plus schema plus EnsureSchema, tested against a real database).",
			strings.Join(offenders, "\n"))
	}
	t.Logf("raw SQL outside the two owning layers: %d statements in %d files, over %d production files scanned",
		counted, len(rawSQLByFile), walked)
}
