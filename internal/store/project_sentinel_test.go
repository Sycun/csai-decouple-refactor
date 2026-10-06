package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The unbound-project sentinel is one fact asserted in three places: the console sends it, the API
// document describes it, and Go compares against it. Two of those used to be the same constant
// declared twice - internal/database carried its own copy with the same literal - which is how a
// rename on one side compiles cleanly and makes the other side quietly stop matching.

const unboundSentinelValue = "__none__"

func TestUnboundProjectSentinelIsDeclaredOnce(t *testing.T) {
	root := moduleRoot(t)
	declared := regexp.MustCompile(`(?m)^const\s+([A-Za-z][A-Za-z0-9_]*)\s*=\s*"__none__"\s*$`)

	var found []string
	for _, dir := range []string{"internal", "cmd"} {
		for path := range productionGoFiles(t, filepath.Join(root, dir)) {
			rel := mustRel(t, root, path)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			for _, m := range declared.FindAllStringSubmatch(string(data), -1) {
				found = append(found, rel+": "+m[1])
			}
		}
	}
	if len(found) == 0 {
		t.Fatal(`no production declaration of a const equal to "` + unboundSentinelValue + `" - the scan is not reading the tree`)
	}
	if len(found) > 1 {
		t.Fatalf("the unbound-project sentinel is declared %d times: %v. One name, one place: compare against store.ProjectUnbound.", len(found), found)
	}
	if !strings.HasPrefix(found[0], "internal/store/") {
		t.Fatalf("the sentinel is owned by %s, want internal/store - the store package writes the listings that use it", found[0])
	}
	if ProjectUnbound != unboundSentinelValue {
		t.Fatalf("store.ProjectUnbound = %q, want %q: this string is on the wire, it is not a name to rename", ProjectUnbound, unboundSentinelValue)
	}
}

// TestConsoleSendsTheSameUnboundSentinel reads the console's own literal, because that is the side
// that decides what the handler actually receives. The assertion is about the value, and a parse that
// finds nothing fails rather than passing.
func TestConsoleSendsTheSameUnboundSentinel(t *testing.T) {
	root := moduleRoot(t)
	path := filepath.Join(root, "web", "static", "js", "chat.js")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	{
		m := regexp.MustCompile(`(?m)^const\s+CONVERSATION_PROJECT_FILTER_NONE\s*=\s*['"]([^'"]+)['"]\s*;`).FindStringSubmatch(string(data))
		if m == nil {
			t.Fatal("the console no longer names its unbound-project sentinel - the comparison would be vacuous")
		}
		if m[1] != ProjectUnbound {
			t.Fatalf("console sends %q, Go compares against %q", m[1], ProjectUnbound)
		}
	}
}

// TestOpenAPIDocumentsTheSameUnboundSentinel keeps the hand-written document honest about the value a
// caller has to pass. It parses the string literals of the chat path file rather than trusting any
// summary of it.
func TestOpenAPIDocumentsTheSameUnboundSentinel(t *testing.T) {
	root := moduleRoot(t)
	path := filepath.Join(root, "internal", "handler", "openapi_paths_chat.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	sentinelHits, descriptionHits := 0, 0
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatalf("unquote a literal in %s: %v", path, err)
		}
		if strings.Contains(value, unboundSentinelValue) {
			sentinelHits++
		}
		if strings.Contains(value, "\u6309\u9879\u76ee\u7b5b\u9009") {
			descriptionHits++
			if !strings.Contains(value, unboundSentinelValue) {
				t.Fatalf("the project filter description no longer names the sentinel callers must pass: %q", value)
			}
		}
		return true
	})
	if descriptionHits == 0 {
		t.Fatal("no project filter description in the OpenAPI document - this test would prove nothing")
	}
	if sentinelHits == 0 {
		t.Fatalf("the document never states the value %q, although %d descriptions reference the project filter", unboundSentinelValue, descriptionHits)
	}
}
