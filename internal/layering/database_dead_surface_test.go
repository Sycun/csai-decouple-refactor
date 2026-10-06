package layering

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// dbSurfaceDeadAllowList names the exported methods on *database.DB that no production code calls.
// They are the leftovers of the RBAC migration: each one has an `...ForAccess` or `...Page` sibling
// that the HTTP layer actually uses, and each is still exercised by a data-layer test, so removing it
// means moving that test onto the live sibling first. The list is one-directional - a name added here
// is a new unreachable method accepted as normal, and a name whose method got deleted or started
// being called stays here failing, so the list cannot quietly fossilise.
var dbSurfaceDeadAllowList = map[string]string{
	"CountConversations":          "superseded by the access-scoped conversation count the handler uses",
	"ListAssignableRBACResources": "superseded by ListAssignableRBACResourcesPage",
	"ListC2Events":                "superseded by ListC2EventsForAccess",
	"ListConversationPlanTasks":   "superseded by ListConversationPlanTasksSince",
	"LoadToolExecutionListPage":   "superseded by LoadToolExecutionListPageForAccess",
}

// deadDBSurface reports exported *DB methods with no production call site and no consumer-interface
// member line anywhere in the tree.
//
// The direction of its error is deliberate: a method whose name also belongs to a handler type can be
// mistaken for "called", so this scan can under-report deadness but never invent a violation. The
// compiler is the other half - dropping a method an outside interface still needs breaks the build,
// which is exactly how SaveToolStats was found to be dead-but-declared and removed together with its
// member line in internal/mcp.
func deadDBSurface(t *testing.T, root string) (dead []string, scanned int) {
	t.Helper()
	names := map[string]bool{}
	dbDir := filepath.Join(root, "internal", "database")
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(dbDir, name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			decl, ok := n.(*ast.FuncDecl)
			if !ok || decl.Recv == nil || len(decl.Recv.List) != 1 || !decl.Name.IsExported() {
				return true
			}
			rec, ok := decl.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				return true
			}
			if id, ok := rec.X.(*ast.Ident); ok && id.Name == "DB" {
				names[decl.Name.Name] = true
			}
			return true
		})
	}
	// 269 exported methods measured as this slice landed, 296 when the scan was first written. The floor
	// sits well under the measurement on purpose: its job is to catch a scan that stopped reading the
	// directory, not to freeze a number that every domain cut is expected to lower.
	if len(names) < 260 {
		// The floor exists
		// so a scan that silently stops reading the directory cannot report an empty dead set.
		t.Fatalf("only %d exported *DB methods parsed (floor 260): the scan is not reading the package", len(names))
	}

	var production []string
	for _, base := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, base), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				production = append(production, path)
			}
			return nil
		})
	}
	if len(production) < 200 {
		t.Fatalf("only %d production files walked: the reference scan would prove nothing", len(production))
	}

	// One read per file, then every name is tested against the same text. A declaration of the method
	// itself and an interface member line are both excluded from being counted as a call.
	texts := map[string]string{}
	for _, path := range production {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		texts[path] = string(data)
	}
	for name := range names {
		call := regexp.MustCompile(`\.` + name + `\(`)
		member := regexp.MustCompile(`(?m)^\t` + name + `\(`)
		found := false
		for _, text := range texts {
			// internal/database counts as a user: a method another data-layer method calls is used,
			// even when nothing outside the package names it. Excluding the package would report the
			// helper layer of a live query as unreachable.
			if call.MatchString(text) || member.MatchString(text) {
				found = true
				break
			}
		}
		scanned++
		if !found {
			dead = append(dead, name)
		}
	}
	sort.Strings(dead)
	return dead, scanned
}

func TestDatabaseSurfaceHasNoUnreachableMethods(t *testing.T) {
	root := moduleRoot(t)
	dead, scanned := deadDBSurface(t, root)
	if len(dead) > len(dbSurfaceDeadAllowList) {
		var fresh []string
		for _, name := range dead {
			if _, listed := dbSurfaceDeadAllowList[name]; !listed {
				fresh = append(fresh, name)
			}
		}
		t.Fatalf("%d exported *DB methods are unreachable from production code (allow-list holds %d). "+
			"New ones: %v. A method belongs on the store that owns the table, and a store method has to "+
			"be called by someone.", len(dead), len(dbSurfaceDeadAllowList), fresh)
	}
	for name := range dbSurfaceDeadAllowList {
		found := false
		for _, got := range dead {
			if got == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s is on the dead-surface allow-list but is no longer dead: either it got called, "+
				"or it was deleted - remove the entry so the list keeps shrinking", name)
		}
	}
	t.Logf("database surface: %d exported methods scanned, %d unreachable (all listed as RBAC leftovers)", scanned, len(dead))
}
