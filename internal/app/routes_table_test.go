package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"cyberstrike-ai/internal/routes"
	"cyberstrike-ai/internal/security"
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

// TestEveryProtectedRouteHasAPermission —— 挂在 protected 组（AuthMiddleware + RBAC 中间件）
// 上的每条路由都必须在 permissionForRequest 里有映射。漏一条的代价不是编译错，是运行时
// 403「未配置访问权限」：编译过、单测绿，直到真机点验才现形（/api/agent-modes 就是这么
// 被抓出来的）。这里把它变成编译期近邻的判据。
func TestEveryProtectedRouteHasAPermission(t *testing.T) {
	table, err := routes.Extract(".")
	if err != nil {
		t.Fatal(err)
	}
	protectedCount := 0
	for _, entry := range table.Sorted() {
		if entry.Receiver != "protected" {
			continue
		}
		protectedCount++
		if security.RoutePermission(entry.Method, entry.GinPath) == "" {
			t.Errorf("protected 路由 %s %s 没有 RBAC 权限映射：所有角色都会收到 403「未配置访问权限」",
				entry.Method, entry.GinPath)
		}
	}
	if protectedCount < 200 {
		t.Fatalf("只识别出 %d 条 protected 路由——提取器口径变了，这道门禁会变成空断言", protectedCount)
	}
}
