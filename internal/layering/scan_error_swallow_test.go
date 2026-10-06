package layering

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A loop that answers a failed row.Scan with `continue` does not skip a bad row - it deletes a record
// from a page with no error and no log line. This repo has been bitten twice by exactly that shape:
// the notification digest dropped every finding whose conversation_id was NULL, and the vulnerability
// list dropped every finding whose tag columns were still NULL from before the ALTER TABLE that added
// them. Neither shows up in a test that only ever exercises a freshly created database.
//
// The claim is split by layer on purpose. internal/store owns its own DDL, so it knows which columns
// can be NULL: its reads either COALESCE those columns or scan them through a Null* type, and a scan
// failure that survives that is a fault to return rather than a row to lose - hence the hard zero.
// internal/database still carries a listed, measured debt, which each domain cut must reduce.
//
// The detector reads the syntax tree, not a text window. A window match was tried first and reported
// four `continue`s that have nothing to do with scanning (a NULL-data skip, an unmarkable id, two
// "column already exists" checks); a gate that cries wolf gets its list widened until it means
// nothing. It knows both spellings in use here: `if err := rows.Scan(..); err != nil { continue }`
// and the two-statement form where the scan is the previous statement.

// scanSwallowDataLayerCeilings is the debt ledger for internal/database, measured as this gate was
// written: 27 sites across five files, and zero in internal/store. When a domain moves to a store it
// takes its share with it, and its entry is deleted rather than left behind - the C2 domain's twelve
// sites left on 2026-10-07, eleven of which the store converted into returned errors (the twelfth was
// the now-deleted ListC2Events), and the monitor domain's eight followed the same day - all eight
// converted, none skipped. The seven sites in the store layer that the first sweep cleared are
// listed with their reasons in docs/zh-CN/capability-platform-decoupling-research.md §11 第二十五刀.
var scanSwallowDataLayerCeilings = map[string]int{
	"internal/database/conversation.go": 3,
	"internal/database/database.go":     3,
	"internal/database/webshell.go":     1,
}

func scanSwallowSites(t *testing.T, root string) (map[string]int, int, int) {
	t.Helper()
	counts := map[string]int{}
	files := 0
	scans := 0
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "generated" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", rel, parseErr)
			}
			files++
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			scans += strings.Count(string(data), ".Scan(")
			if found := countScanSwallows(file); found > 0 {
				counts[rel] = found
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", dir, err)
		}
	}
	return counts, files, scans
}

func countScanSwallows(file *ast.File) int {
	found := 0
	visit := func(stmts []ast.Stmt) {
		for i, stmt := range stmts {
			ifStmt, ok := stmt.(*ast.IfStmt)
			if !ok || !isErrNotNil(ifStmt.Cond) || len(ifStmt.Body.List) == 0 {
				continue
			}
			// The body may log before it continues - that is the shape this gate most needs to catch,
			// because the log line is what made the dropped rows invisible for so long.
			if !bodyContinues(ifStmt.Body) {
				continue
			}
			if ifStmt.Init != nil && callsScan(ifStmt.Init) {
				found++
				continue
			}
			if i > 0 && callsScan(stmts[i-1]) {
				found++
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch block := n.(type) {
		case *ast.BlockStmt:
			visit(block.List)
		case *ast.CaseClause:
			visit(block.Body)
		}
		return true
	})
	return found
}

func bodyContinues(block *ast.BlockStmt) bool {
	for _, stmt := range block.List {
		if branch, ok := stmt.(*ast.BranchStmt); ok && branch.Tok == token.CONTINUE {
			return true
		}
	}
	return false
}

func isErrNotNil(e ast.Expr) bool {
	bin, ok := e.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	id, ok := bin.X.(*ast.Ident)
	return ok && id.Name == "err"
}

func callsScan(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(e ast.Node) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Scan" {
			found = true
		}
		return true
	})
	return found
}

func TestScanErrorsAreNotAnsweredByDroppingRows(t *testing.T) {
	root := moduleRoot(t)
	counts, files, scans := scanSwallowSites(t, root)
	// A detector that reads nothing reports no debt, so the traversal is part of the claim.
	if files < 400 || scans < 200 {
		t.Fatalf("the scan read %d files and %d .Scan( sites, want at least 400 and 200 - the ceilings below mean nothing without it", files, scans)
	}

	var offenders []string
	for path, ceiling := range scanSwallowDataLayerCeilings {
		if got := counts[path]; got > ceiling {
			offenders = append(offenders, fmt.Sprintf("%s: %d sites, ceiling %d - a row dropped by a failed scan is a record missing from a page; return the error, or COALESCE/Null* the column in the query", path, got, ceiling))
		}
		delete(counts, path)
	}
	for _, path := range sortedKeys(counts) {
		claim := "ceiling 0 (this file has never been classified here)"
		if strings.HasPrefix(path, "internal/store/") {
			claim = "ceiling 0 - this package owns its DDL, so a scan failure there is never a row to skip"
		}
		offenders = append(offenders, fmt.Sprintf("%s: %d sites, %s", path, counts[path], claim))
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("a failed row.Scan is being answered by dropping the row:\n%s", strings.Join(offenders, "\n"))
	}
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
