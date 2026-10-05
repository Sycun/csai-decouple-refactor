package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Guards on the openapi.go -> openapi_paths_<group>.go split, declared in the order they
// must run: structure first, then per-file floors, then the golden. A duplicated path makes
// the merger panic, and a panic kills the test binary before any later test is reported -
// so the check that names the two colliding files has to come before anything that serves a
// document.

type openapiGroup struct {
	name  string
	paths map[string]interface{}
}

func openAPIGroups() []openapiGroup {
	return []openapiGroup{
		{"chat", openAPIPathsChat()},
		{"knowledge", openAPIPathsKnowledge()},
		{"capabilities", openAPIPathsCapabilities()},
		{"mcp", openAPIPathsMCP()},
		{"ops", openAPIPathsOps()},
	}
}

// TestOpenAPIGroupsDoNotOverlap checks the two invariants splitting data across files
// introduces: the groups are disjoint, and together they are the whole document (the parts
// must sum to the merged size). Overlap is the dangerous one — a plain merge lets whichever
// group runs later win, so a consumer silently gets the other file's operationId.
func TestOpenAPIGroupsDoNotOverlap(t *testing.T) {
	groups := openAPIGroups()
	for _, group := range groups {
		if len(group.paths) == 0 {
			t.Errorf("the %s group declares no paths at all", group.name)
		}
	}
	for i, a := range groups {
		for _, b := range groups[i+1:] {
			for path := range a.paths {
				if _, dup := b.paths[path]; dup {
					t.Errorf("path %s is declared by both the %s and the %s group", path, a.name, b.name)
				}
			}
		}
	}
	if t.Failed() {
		t.Fatal("the groups must be disjoint before anything merges them")
	}

	sum := 0
	for _, group := range groups {
		sum += len(group.paths)
	}
	if merged := openAPIPaths(); sum != len(merged) {
		t.Errorf("the groups declare %d paths but the merged document serves %d — the parts must sum to the whole", sum, len(merged))
	}
}

// TestOpenAPIGroupFloors is the per-file half of "a group file stopped being compiled in".
// The merger's floor catches the document collapsing overall, but one group going empty
// while the others are intact only trims it. Each group keeps at least the paths it has
// today: raise a floor when you document a new path in that group, never lower one.
func TestOpenAPIGroupFloors(t *testing.T) {
	floors := map[string]int{"chat": 41, "knowledge": 25, "capabilities": 32, "mcp": 6, "ops": 14}
	for _, group := range openAPIGroups() {
		floor, ok := floors[group.name]
		if !ok {
			t.Errorf("the %s group has no floor — a new group file needs one here", group.name)
			continue
		}
		if len(group.paths) < floor {
			t.Errorf("the %s group documents %d paths, floor is %d — a path or the whole group stopped being compiled in", group.name, len(group.paths), floor)
		}
		if len(group.paths) > floor {
			t.Logf("the %s group grew to %d paths; tighten the floor", group.name, len(group.paths))
		}
	}
}

// openapiPathsSpec is the paths node as an API consumer receives it. The golden reads this
// rather than the group maps: only the handler chain proves the merged map is served.
func openapiPathsSpec(t *testing.T) map[string]map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/openapi/spec", nil)
	c.Request.Host = "localhost:8080"

	NewOpenAPIHandler(nil, zap.NewNop(), nil, nil).GetOpenAPISpec(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("spec handler returned %d", recorder.Code)
	}
	var spec struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	if len(spec.Paths) < 100 {
		t.Fatalf("the served document carries only %d paths; the split lost a group file", len(spec.Paths))
	}
	return spec.Paths
}

var openapiHTTPMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
}

// openapiOperationLines renders the golden table: one "METHOD /path|operationId|tag" line
// per operation, sorted.
func openapiOperationLines(t *testing.T) []string {
	t.Helper()
	var lines []string
	for path, item := range openapiPathsSpec(t) {
		for method, raw := range item {
			if !openapiHTTPMethods[strings.ToLower(method)] {
				continue
			}
			op, ok := raw.(map[string]any)
			if !ok {
				t.Errorf("%s %s is not an operation object", strings.ToUpper(method), path)
				continue
			}
			id, _ := op["operationId"].(string)
			if id == "" {
				t.Errorf("%s %s has no operationId", strings.ToUpper(method), path)
			}
			// The table has one tag column, so an operation that gains a second tag or
			// loses its group would make that column start lying.
			tags, _ := op["tags"].([]any)
			if len(tags) != 1 {
				t.Errorf("%s %s carries %d tags, the golden table has room for exactly 1", strings.ToUpper(method), path, len(tags))
			}
			tag, _ := tags[0].(string)
			lines = append(lines, fmt.Sprintf("%s %s|%s|%s", strings.ToUpper(method), path, id, tag))
		}
	}
	if len(lines) < 150 {
		t.Fatalf("only %d operations were extracted, the extractor is broken", len(lines))
	}
	sort.Strings(lines)
	seen := map[string]bool{}
	for _, line := range lines {
		key := line[:strings.Index(line, "|")]
		if seen[key] {
			t.Errorf("%s is documented twice", key)
		}
		seen[key] = true
	}
	return lines
}

// TestOpenAPIOperationsGolden pins the operation table the hand-written document produces.
//
// The split itself was proven the strong way: the served document diffed byte-for-byte
// against the pre-split one (155,278 B, identical). This golden is the durable half of that
// claim. It is derived from the same source it guards, so on its own it catches drift that
// changes a (method, path, operationId, tag) tuple — a path renamed, a path moved between
// files, an operation losing its group. Probes run against it: renaming one path printed
// that line in both directions; deleting one path block went 157 -> 156 and tripped the ops
// floor.
//
// Regenerate deliberately, never by habit:
//
//	CSAI_WRITE_OPENAPI_GOLDEN=1 go test ./internal/handler -run TestOpenAPIOperationsGolden
func TestOpenAPIOperationsGolden(t *testing.T) {
	lines := openapiOperationLines(t)
	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("testdata", "openapi-operations.golden.txt")

	if os.Getenv("CSAI_WRITE_OPENAPI_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s with %d operations", path, len(lines))
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — if the document changed on purpose, regenerate it with CSAI_WRITE_OPENAPI_GOLDEN=1", path, err)
	}
	if len(strings.TrimSpace(string(want))) == 0 {
		t.Fatalf("%s is empty; an empty baseline passes against anything", path)
	}
	if got == string(want) {
		return
	}
	only := func(a, b []string) []string {
		member := map[string]bool{}
		for _, line := range b {
			member[line] = true
		}
		var out []string
		for _, line := range a {
			if !member[line] {
				out = append(out, line)
			}
		}
		return out
	}
	before := strings.Split(strings.TrimSuffix(strings.TrimSpace(string(want)), "\n"), "\n")
	t.Errorf("the operation table moved (%d golden vs %d now).\n  dropped:\n    %s\n  added:\n    %s",
		len(before), len(lines), strings.Join(only(before, lines), "\n    "), strings.Join(only(lines, before), "\n    "))
}
