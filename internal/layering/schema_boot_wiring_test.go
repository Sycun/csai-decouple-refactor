package layering

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A store that owns a table also owns its DDL, which moves the create out of the data layer's start-up
// sweep and into one call. That call is the whole reason a fresh installation gets the table at all,
// and it is exactly the line that is easy to lose in a later edit: nothing else fails to compile, and
// every test that builds its own schema never touches the boot path.
//
// This gate reads the boot file rather than a database, because the claim is about the wiring: the
// alert store's EnsureSchema is called exactly once, after the RBAC tables it has foreign keys onto.
// A store that owns a table also owns its DDL, which moves the CREATE out of the data layer's
// start-up sweep into one call. That call is the whole reason a fresh installation gets the table at
// all, and it is the line easiest to lose in a later edit: nothing else fails to compile, and every
// test that builds its own schema never walks the boot path.
//
// Each case below therefore asserts three things about the boot file: the store's EnsureSchema is
// called exactly once, it is called after the table its foreign keys point at, and no second copy of
// the CREATE survived in the file the store took it from.
func TestSchemaEnsuresAreWiredAtBoot(t *testing.T) {
	// constructor is the store's New... function, and anchor is the call the ensure has to
	// follow. Both are matched by name, so renaming a store constructor or a boot step fails
	// loudly here rather than silently dropping an ordering rule.
	type bootCase struct {
		storeConstructor string
		tablePrefix      string
		anchorCall       string
		anchorReason     string
		// mustNotMention is the stronger form of "no second copy": where the owning store's tables
		// have a recognisable name, the boot file should not mention them at all - not just not
		// CREATE them, because an index left behind is as much a second owner as a CREATE is.
		mustNotMention string
	}
	cases := []bootCase{
		{
			storeConstructor: "NewVulnerabilityAlerts",
			tablePrefix:      "vulnerability_alert_",
			anchorCall:       "initRBACTables",
			anchorReason:     "both alert tables have foreign keys onto rbac_users",
			mustNotMention:   "",
		},
		{
			storeConstructor: "NewAttackChain",
			tablePrefix:      "attack_chain_",
			anchorCall:       "createToolExecutionsTable",
			anchorReason:     "attack_chain_nodes has a foreign key onto tool_executions",
			mustNotMention:   "attack_chain",
		},
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "internal", "database", "database.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the boot file: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse the boot file: %v", err)
	}

	// ensureOffsets records, per store constructor, the earliest EnsureSchema call built on it.
	// execOffsets records the earliest db.Exec(<identifier>) for a named variable, which is how the
	// tables that are still created inline are used as ordering anchors.
	ensureOffsets := map[string]int{}
	ensureCounts := map[string]int{}
	execOffsets := map[string]int{}
	fnCallOffsets := map[string]int{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		offset := fset.Position(call.Pos()).Offset
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			// db.Exec(createXTable) is how a table still created inline is anchored: the argument's
			// name is the only thing that identifies which table this statement creates.
			if fun.Sel.Name == "Exec" && len(call.Args) == 1 {
				if recv, ok := fun.X.(*ast.Ident); ok && recv.Name == "db" {
					if id, ok := call.Args[0].(*ast.Ident); ok {
						if prev, seen := execOffsets[id.Name]; !seen || offset < prev {
							execOffsets[id.Name] = offset
						}
					}
				}
			}
			switch fun.Sel.Name {
			case "EnsureSchema":
				if inner, ok := fun.X.(*ast.CallExpr); ok {
					if s, ok := inner.Fun.(*ast.SelectorExpr); ok {
						if id, ok := s.X.(*ast.Ident); ok && id.Name == "store" {
							ensureCounts[s.Sel.Name]++
							if prev, seen := ensureOffsets[s.Sel.Name]; !seen || offset < prev {
								ensureOffsets[s.Sel.Name] = offset
							}
						}
					}
				}
			default:
				if prev, seen := fnCallOffsets[fun.Sel.Name]; !seen || offset < prev {
					fnCallOffsets[fun.Sel.Name] = offset
				}
			}
		}
		return true
	})

	for _, tc := range cases {
		count := ensureCounts[tc.storeConstructor]
		if count != 1 {
			t.Errorf("%s: EnsureSchema is called %d times on the boot path, want exactly 1 "+
				"(zero means a fresh installation never creates %s; more than one means someone added a "+
				"second sweep)", tc.storeConstructor, count, tc.tablePrefix)
			continue
		}
		anchor, ok := anchorOffset(tc.anchorCall, execOffsets, fnCallOffsets)
		if !ok {
			t.Errorf("%s: the ordering anchor %q was not found in the boot file - the ordering claim "+
				"cannot be checked, which is a failure rather than a pass", tc.storeConstructor, tc.anchorCall)
			continue
		}
		if ensureOffsets[tc.storeConstructor] < anchor {
			t.Errorf("%s: ensured at offset %d, before the anchor %q at %d; %s",
				tc.storeConstructor, ensureOffsets[tc.storeConstructor], tc.anchorCall, anchor, tc.anchorReason)
		}
		if strings.Contains(string(src), "CREATE TABLE IF NOT EXISTS "+tc.tablePrefix) {
			t.Errorf("%s: a CREATE for %s is still written in the boot file next to the store that owns it",
				tc.storeConstructor, tc.tablePrefix)
		}
		if tc.mustNotMention != "" && strings.Contains(string(src), tc.mustNotMention) {
			t.Errorf("%s: the boot file still mentions %q - the store owns these tables, their indexes "+
				"included, so any remaining mention is a second owner to find later",
				tc.storeConstructor, tc.mustNotMention)
		}
	}
}

func anchorOffset(anchorCall string, execOffsets, fnCallOffsets map[string]int) (int, bool) {
	if offset, ok := execOffsets[anchorCall]; ok {
		return offset, true
	}
	offset, ok := fnCallOffsets[anchorCall]
	return offset, ok
}
