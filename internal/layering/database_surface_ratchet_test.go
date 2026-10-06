package layering

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// dbMethodCeiling is the measured number of methods declared on the data layer's connection wrapper.
// It is a ratchet in one direction only: a cut into a per-domain store lowers it, and adding a method
// back onto *database.DB* is a refusal, not a discussion.
//
// skill_stats is the sixth domain to leave (after HITL, sessions, notification reads, the
// vulnerability digest reads, failed-execution entries and the capability switches): five methods
// moved to store.SkillStats and one, SaveSkillStats, turned out to have no caller at all and was
// deleted rather than relocated.
//
// The traversal domain is every non-test file in internal/database - the same claim as "one writer
// per table" needs a count that cannot be satisfied by reading one file.
//
// 13 of those identities were then deleted outright: they had no caller anywhere in production
// (TestDatabaseSurfaceHasNoUnreachableMethods keeps that from happening again).
const dbMethodCeiling = 337

func TestDatabaseSurfaceOnlyShrinks(t *testing.T) {
	root := moduleRoot(t)
	dir := filepath.Join(root, "internal", "database")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	perFile := map[string]int{}
	files := 0
	total := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		files++
		ast.Inspect(file, func(n ast.Node) bool {
			decl, ok := n.(*ast.FuncDecl)
			if !ok || decl.Recv == nil || len(decl.Recv.List) != 1 {
				return true
			}
			rec, ok := decl.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				return true
			}
			id, ok := rec.X.(*ast.Ident)
			if !ok || id.Name != "DB" {
				return true
			}
			perFile[name]++
			total++
			return true
		})
	}
	if files < 20 {
		t.Fatalf("only %d production files scanned in internal/database: the walker is not reading the package", files)
	}
	if total > dbMethodCeiling {
		var worst []string
		for name, count := range perFile {
			worst = append(worst, name+": "+strconv.Itoa(count))
		}
		sort.Strings(worst)
		t.Fatalf("%d methods on *database.DB (ceiling %d). A new one belongs in the store that owns "+
			"the table, not on the connection every package can reach. Per file: %v",
			total, dbMethodCeiling, worst)
	}
	if total < dbMethodCeiling {
		t.Logf("database surface dropped to %d methods (ceiling %d): tighten dbMethodCeiling", total, dbMethodCeiling)
	}
}
