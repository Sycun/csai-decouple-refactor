package layering

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The one thing is "how a SQLite DATETIME column is compared to an instant". It used to have two
// spellings in two layers, each with its own parameter encoding; internal/sqltime emits them now.
//
// A fourth file copying the expression verbatim would still compile and still return rows, so this is
// checked as text rather than as a type: the strftime call that turns a column into epoch seconds may
// only be written inside the package that owns it.
func TestSQLiteInstantSpellingHasOneHome(t *testing.T) {
	root := moduleRoot(t)
	const marker = `strftime('%s'`
	owner := filepath.Join(root, "internal", "sqltime")

	var offenders []string
	for _, base := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, base), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			if strings.HasPrefix(filepath.Dir(path), owner) {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil // a test may spell the expectation it pins
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(data), marker) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("%s written outside internal/sqltime: %v. Add a function to that package instead of a "+
			"fourth dialect - two spellings already meant two parameter encodings for one rule.",
			marker, offenders)
	}
}
