package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"cyberstrike-ai/internal/routes"
)

// registrarPattern matches the per-domain registrar signature.
var registrarPattern = regexp.MustCompile(`func \(deps routeDeps\) (register\w+Routes)\(`)

// TestRouteTableMatchesGolden is the safety net for the per-domain wiring split.
//
// The registrars are separate functions in separate files, so a moved or dropped
// registration no longer shows up as an obvious diff in one giant function. This
// test makes the route table itself the contract: the set of METHOD path pairs the
// server serves must equal the golden list, which was captured before the split.
func TestRouteTableMatchesGolden(t *testing.T) {
	table, err := routes.Extract(".")
	if err != nil {
		t.Fatal(err)
	}
	goldenPath := filepath.Join("testdata", "routes.golden.txt")
	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden route table missing (regenerate with go test ./internal/routes -run TestWriteGolden): %v", err)
	}
	want := splitNonEmpty(string(data))
	got := table.Lines()

	missing, extra := diffSets(want, got)
	if len(missing) > 0 || len(extra) > 0 {
		for _, entry := range missing {
			t.Errorf("route no longer registered: %s", entry)
		}
		for _, entry := range extra {
			t.Errorf("route registered that the golden table does not have: %s", entry)
		}
		t.Fatalf("route table drifted: %d registered vs %d golden", len(got), len(want))
	}
	if len(got) < 250 {
		t.Fatalf("the extractor only found %d registrations; it is broken, not the wiring", len(got))
	}
}

// TestEveryDomainRegistrarIsWired catches the opposite mistake from the golden test:
// a registrar file that exists but is never called would pass a route-count check if
// another file happened to cover the same paths.
func TestEveryDomainRegistrarIsWired(t *testing.T) {
	files, err := filepath.Glob("routes_*.go")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	checked := 0
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range registrarPattern.FindAllStringSubmatch(string(data), -1) {
			fn := match[1]
			// Any single group argument counts: the public platform callbacks are registered on
			// the api group with no session middleware, so insisting on "(protected)" here would
			// push a future registrar to name its parameter something it is not.
			if !regexp.MustCompile(`deps\.` + fn + `\((?:protected|api|router)\)`).MatchString(body) {
				t.Errorf("%s declares %s but setupRoutes never calls it", name, fn)
			}
			checked++
		}
	}
	if checked < 20 {
		t.Fatalf("expected per-domain registrars, found %d", checked)
	}
}

func splitNonEmpty(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func diffSets(want, got []string) ([]string, []string) {
	seen := map[string]int{}
	for _, entry := range want {
		seen[entry]++
	}
	var missing, extra []string
	for _, entry := range got {
		if seen[entry] == 0 {
			extra = append(extra, entry)
			continue
		}
		seen[entry]--
	}
	for entry, count := range seen {
		if count > 0 {
			missing = append(missing, entry)
		}
	}
	return missing, extra
}
