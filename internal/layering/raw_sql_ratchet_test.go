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

// rawSQLByFile is the remaining reach of the embedded *sql.DB on internal/database.DB: every site
// where a package outside the two layers that own SQL still executes a statement.
//
// internal/database owns the schema-wide queries and internal/store owns per-domain tables, so both
// are excluded from the count by definition: a store writing SQL is the design working, not leaking.
// Everything else that reaches through a database handle is the god object still being reachable.
//
// The numbers are today's measurement, and each is a ceiling. Extracting knowledge's SQL into its own
// store lowers them; a new file appearing here is a new leak and fails without needing anyone to
// remember the rule.
var rawSQLByFile = map[string]int{
	"internal/knowledge/manager.go":        24,
	"internal/knowledge/schema_migrate.go": 3,
	"internal/knowledge/indexer.go":        3,
}

// rawSQLReceiver matches only a handle-shaped variable, so scope.guard.Prepare (a process guard) does
// not read as SQL.
var rawSQLReceiver = regexp.MustCompile(`\b(?:db|d|conn|sqlDB)\.(?:Exec|Query|QueryRow|Begin|Prepare|MustExec)\(`)

func TestRawSQLOutsideDataAndStoreLayersOnlyShrinks(t *testing.T) {
	root := moduleRoot(t)
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
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		found := len(rawSQLReceiver.FindAllStringIndex(string(data), -1))
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

	if counted < 1 || len(rawSQLByFile) < 1 {
		t.Fatalf("the scan found %d files with raw SQL and the baseline lists %d: one of them is broken",
			counted, len(rawSQLByFile))
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("SQL is being written through the database handle outside internal/store:\n%s\n"+
			"The destination is a store that owns those tables - see internal/store/skill_stats.go "+
			"for the shape (SQL plus schema plus EnsureSchema, tested against a real database).",
			strings.Join(offenders, "\n"))
	}
	total := 0
	for _, n := range rawSQLByFile {
		total += n
	}
	t.Logf("raw SQL outside the two owning layers: %d statements in %d files (next slice: internal/knowledge)",
		total, len(rawSQLByFile))
}
