package handler

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

// HITLManager's lock, its pending map, its per-conversation runtime table, its approved-execution
// queue, its operator whitelist and its decision channels are private because reading them
// correctly means knowing when to hold `mu` and what a full `decideCh` means.
// DismissHITLInterrupt did exactly that from the transport layer - lock the manager, delete from
// the map, send a rejection - which is one type's locking rules inlined into another type's HTTP
// code. The manager now owns that step as DropPending, and this gate keeps the next caller from
// writing it by hand.
//
// Three things this scan had to get right, each learned the hard way on the first draft:
//
//   - Comments are excluded by walking the AST. Two lines in batch_task_manager.go read
//     "必须在持有 BatchTaskManager.mu 下调用" - a comment naming a different type's lock, which a
//     regexp over lines reported as a leak.
//   - Only fields declared on a struct are handles. `*ast.Field` also covers receivers and
//     parameters, and the receiver of HITLManager's own methods is named `m`: collected as a
//     handle, it flagged every `m.mu` in the package, including AgentTaskManager's.
//   - The watched shape is therefore the two-hop one (`h.hitlManager.mu`, `q.manager.pending`),
//     which is how a second owner actually reaches this state. Known limit: rebinding the manager
//     to a local first and reading `local.mu` walks around it - what guards that is review, plus
//     DropPending being the obvious method to call.
var hitlPrivateFields = map[string]bool{
	"mu": true, "pending": true, "runtime": true,
	"approvedExec": true, "globalWhitelist": true, "decideCh": true,
}

// hitlManagerHandleFields returns the struct field names that hold a *HITLManager.
func hitlManagerHandleFields(root *token.FileSet, files []string) map[string]bool {
	handles := map[string]bool{}
	for _, name := range files {
		file, err := parser.ParseFile(root, name, nil, 0)
		if err != nil {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			decl, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			strukt, ok := decl.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range strukt.Fields.List {
				star, ok := field.Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				if ident, ok := star.X.(*ast.Ident); !ok || ident.Name != "HITLManager" {
					continue
				}
				for _, named := range field.Names {
					handles[named.Name] = true
				}
			}
			return true
		})
	}
	return handles
}

func isHITLManagerReceiver(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "HITLManager"
}

func TestHITLManagerPrivateStateStaysPrivate(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, err := os.Stat(name); err == nil {
			files = append(files, name)
		}
	}
	// The whole directory: a second owner of this state can appear in any file, and a scan that
	// reads a subset would report clean while the leak lands in one it skipped.
	if len(files) < 40 {
		t.Fatalf("only %d non-test handler files found; the gate is reading a subset", len(files))
	}

	root := token.NewFileSet()
	handles := hitlManagerHandleFields(root, files)
	if len(handles) < 2 {
		t.Fatalf("only %d struct fields of type *HITLManager were found (%v) - the handle scan stopped working, so this gate would pass against anything", len(handles), handles)
	}

	var leaks []string
	for _, name := range files {
		file, err := parser.ParseFile(root, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Recv != nil && len(fn.Recv.List) == 1 && isHITLManagerReceiver(fn.Recv.List[0].Type) {
				continue // the owner's own methods
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || !hitlPrivateFields[sel.Sel.Name] {
					return true
				}
				holder, ok := sel.X.(*ast.SelectorExpr)
				if !ok || !handles[holder.Sel.Name] {
					return true
				}
				pos := root.Position(sel.Sel.Pos())
				leaks = append(leaks, filepath.Base(pos.Filename)+":"+strconv.Itoa(pos.Line)+" "+holder.Sel.Name+"."+sel.Sel.Name)
				return true
			})
		}
	}
	sort.Strings(leaks)
	if len(leaks) > 0 {
		t.Errorf("these lines reach into HITLManager's private state instead of asking it to do the work:\n  %s\nDropPending is the pattern: the manager owns its lock, its pending map and the channel a waiter reads.", strings.Join(leaks, "\n  "))
	}
}
