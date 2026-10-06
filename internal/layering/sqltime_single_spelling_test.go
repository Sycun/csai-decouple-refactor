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

// readingStoredInstant matches a call that turns stored DATETIME text back into a time.Time. Formatting
// an instant for display is a different decision and stays with the display code; ParseInLocation is a
// bucket key in the machine's own zone, also a different decision. Only the read is shared, because it
// was thirteen readers with thirteen accepted sets, and the same column answered differently to each.
var readingStoredInstant = regexp.MustCompile(`time\.Parse\(\s*"(?:2006-01-02[ T]15:04:05[^"]*|2006-01-02)"`)

// storedInstantReadingByFile is the debt this cut was meant to clear. It is empty: every read of a
// stored instant now goes through sqltime.Parse. The map is kept because a half-converged table has to
// be listable with a ceiling rather than by turning the gate off.
var storedInstantReadingByFile = map[string]int{}

// readingStoredInstantControls keep an empty baseline from being indistinguishable from a broken
// scanner, in both directions: the pattern must fire on the shapes it claims to catch, and must not
// fire on the display-side spellings it deliberately allows.
var readingStoredInstantControls = []string{
	`parsed, err := time.Parse("2006-01-02 15:04:05", createdAt)`,
	`msg.CreatedAt, err = time.Parse("2006-01-02 15:04:05.999999999-07:00", createdAt)`,
	`t := time.Parse("2006-01-02", day)`,
}

var storedInstantReadingAllowed = []string{
	`sb.WriteString(conn.CreatedAt.Format("2006-01-02 15:04:05"))`,
	`return time.ParseInLocation("2006-01-02 15:04:05", bucketStr, time.Local)`,
	`sqltime.Parse(createdAt)`,
}

func TestStoredInstantIsReadInOnePlace(t *testing.T) {
	for _, control := range readingStoredInstantControls {
		if readingStoredInstant.FindStringIndex(control) == nil {
			t.Fatalf("the reader pattern missed %q: this gate cannot see the parse it claims to prevent", control)
		}
	}
	for _, allowed := range storedInstantReadingAllowed {
		if loc := readingStoredInstant.FindStringIndex(allowed); loc != nil {
			t.Fatalf("the reader pattern fired on an allowed spelling %q at %d: display-side formatting and "+
				"zone-local bucket keys are deliberately outside this rule", allowed, loc[0])
		}
	}

	root := moduleRoot(t)
	walked := 0
	var offenders []string
	for _, base := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, base), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			if strings.HasPrefix(filepath.ToSlash(mustRel(t, root, path)), "internal/sqltime/") {
				return nil
			}
			walked++
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			found := len(readingStoredInstant.FindAllStringIndex(string(data), -1))
			if found == 0 {
				return nil
			}
			rel := filepath.ToSlash(mustRel(t, root, path))
			ceiling, listed := storedInstantReadingByFile[rel]
			if !listed {
				offenders = append(offenders, rel+": "+strconv.Itoa(found)+" stored-instant read(s), a file not on the list")
				return nil
			}
			if found > ceiling {
				offenders = append(offenders, rel+": "+strconv.Itoa(found)+" reads, ceiling "+strconv.Itoa(ceiling))
			}
			return nil
		})
	}
	if walked < 400 {
		t.Fatalf("the walk considered %d production files, want at least 400: the scan is not reading the tree", walked)
	}
	t.Logf("stored-instant reads outside internal/sqltime: %d files, over %d production files scanned",
		len(storedInstantReadingByFile), walked)
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("a stored DATETIME is parsed by hand outside internal/sqltime:\n%s\n"+
			"The accepted forms are one decision about one column type; thirteen local copies of it meant "+
			"a row carried a real time or the zero time depending on which query read it.",
			strings.Join(offenders, "\n"))
	}
}

func mustRel(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("rel %s: %v", path, err)
	}
	return rel
}
