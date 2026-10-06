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
		// mustNotChangeSQL is the stronger form of "no second copy": no statement of any kind -
		// CREATE, index, ALTER, or a write - may be run against the store's tables from the boot file.
		// An index left in the general sweep is as much a second owner as a CREATE is, and prose about
		// the tables (which the ordering comments legitimately need) is not what is being banned here,
		// so the match is on SQL syntax rather than on the bare table name.
		mustNotChangeSQL string
	}
	cases := []bootCase{
		{
			storeConstructor: "NewVulnerabilityAlerts",
			tablePrefix:      "vulnerability_alert_",
			anchorCall:       "initRBACTables",
			anchorReason:     "both alert tables have foreign keys onto rbac_users",
			mustNotChangeSQL: "",
		},
		{
			storeConstructor: "NewAttackChain",
			tablePrefix:      "attack_chain_",
			anchorCall:       "NewMonitor",
			anchorReason:     "attack_chain_nodes has a foreign key onto tool_executions, which store.Monitor's EnsureSchema creates now - the anchor is the call that builds that store",
			mustNotChangeSQL: "attack_chain",
		},
		{
			storeConstructor: "NewMonitor",
			tablePrefix:      "tool_executions",
			anchorCall:       "createConversationsTable",
			anchorReason:     "tool_executions carries a conversation_id column (no foreign key), so the anchor only has to be a stable earlier boot step",
			mustNotChangeSQL: "tool_executions",
		},
		{
			storeConstructor: "NewWorkflows",
			tablePrefix:      "workflow_",
			anchorCall:       "createConversationsTable",
			anchorReason:     "workflow_runs.conversation_id has a foreign key onto conversations",
			mustNotChangeSQL: "workflow_",
		},
		{
			storeConstructor: "NewVulnerabilities",
			tablePrefix:      "vulnerabilit",
			anchorCall:       "createConversationsTable",
			anchorReason:     "vulnerabilities.conversation_id has a foreign key onto conversations, so the table cannot be created before it",
			mustNotChangeSQL: "vulnerabilities",
		},
		{
			storeConstructor: "NewAssets",
			tablePrefix:      "assets",
			anchorCall:       "createProjectsTable",
			anchorReason:     "assets.project_id has a foreign key onto projects",
			mustNotChangeSQL: "assets",
		},
		{
			storeConstructor: "NewBatchTasks",
			tablePrefix:      "batch_task",
			anchorCall:       "createProjectsTable",
			anchorReason:     "the batch tables are still created in the block right after the projects / blackboard / findings creates - the findings table moved into its own store, so the last inline CREATE before them is the anchor",
			mustNotChangeSQL: "batch_task",
		},
		{
			storeConstructor: "NewWebshell",
			tablePrefix:      "webshell_connection",
			anchorCall:       "createProjectsTable",
			anchorReason:     "WebShell 的两张表原本排在这一批内联建表的最后；批量任务两张表现在也归自己的 store 建，所以锚点退到仍然内联建的那一张，钉住的还是同一个位置",
			mustNotChangeSQL: "webshell_connection",
		},
		{
			storeConstructor: "NewSession",
			tablePrefix:      "messages",
			anchorCall:       "createConversationsTable",
			anchorReason:     "both tables cascade off conversations, so they cannot be created before it",
			mustNotChangeSQL: "process_details",
		},
		{
			storeConstructor: "NewFacts",
			tablePrefix:      "project_fact",
			anchorCall:       "createProjectsTable",
			anchorReason:     "both blackboard tables have a foreign key onto projects",
			mustNotChangeSQL: "project_fact",
		},
		{
			storeConstructor: "NewC2",
			tablePrefix:      "c2_",
			anchorCall:       "createConversationsTable",
			anchorReason:     "the six C2 tables only reference each other, so the anchor is any stable earlier boot step (c2_tasks keeps a conversation_id column, but no foreign key onto conversations)",
			mustNotChangeSQL: "c2_",
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
		if tc.mustNotChangeSQL != "" {
			sql := regexp.MustCompile(`(?i)\b(?:CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS|ALTER\s+TABLE|CREATE\s+(?:UNIQUE\s+)?INDEX\s+\w+\s+ON|UPDATE|DELETE\s+FROM|INSERT\s+INTO)\s+` + tc.mustNotChangeSQL)
			if hit := sql.FindString(string(src)); hit != "" {
				t.Errorf("%s: the boot file still runs %q against tables %s owns - a statement left here "+
					"is a second writer to find later", tc.storeConstructor, hit, tc.storeConstructor)
			}
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

// TestMultiPhaseSchemaStepsRunInOrder pins the order of the phases a table's owner splits its DDL
// into. idx_batch_task_queues_title sits on a column that only the column backfill creates, so
// 建表 -> 补列 -> 建索引 is not a style preference: a fresh install passes either way, and only a
// database written by the first release fails at start-up when someone reorders the three calls.
//
// That is the same defect the assets cut ran into, and it is caught here rather than by reading.
func TestMultiPhaseSchemaStepsRunInOrder(t *testing.T) {
	root := moduleRoot(t)
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
	offsets := map[string]int{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		outer, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		inner, ok := outer.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		ctor, ok := inner.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := ctor.X.(*ast.Ident); !ok || id.Name != "store" {
			return true
		}
		key := ctor.Sel.Name + "." + outer.Sel.Name
		offset := fset.Position(call.Pos()).Offset
		if prev, seen := offsets[key]; !seen || offset < prev {
			offsets[key] = offset
		}
		return true
	})

	cases := []struct {
		constructor string
		phases      []string
		why         string
	}{
		{"NewBatchTasks", []string{"EnsureSchema", "MigrateQueueColumns", "EnsureIndexes"},
			"the title index is on a column only the backfill creates"},
		{"NewWebshell", []string{"EnsureSchema", "MigrateConnectionsTable"},
			"the connection row reads columns the backfill adds"},
		{"NewWorkflows", []string{"EnsureSchema", "MigrateRunsTable"},
			"the run table reads columns the backfill adds"},
		{"NewC2", []string{"EnsureSchema", "MigrateListenerColumns", "EnsureIndexes"},
			"idx_c2_listeners_project_id sits on the column only the backfill creates"},
		{"NewMonitor", []string{"EnsureSchema", "MigrateLateColumns", "EnsureIndexes"},
			"the four late columns are read back by partial-output flows, and the indexes follow the original boot order - this pins it rather than a real dependency"},
	}
	checked := 0
	for _, tc := range cases {
		var last int = -1
		for _, phase := range tc.phases {
			key := tc.constructor + "." + phase
			offset, ok := offsets[key]
			if !ok {
				t.Errorf("%s never calls %s on the boot path: the phases of that table's schema are no longer all wired", tc.constructor, phase)
				last = -1
				break
			}
			if last >= 0 && offset <= last {
				t.Errorf("%s calls %s out of order: %s must run first, because %s",
					tc.constructor, phase, tc.phases[0], tc.why)
			}
			last = offset
		}
		checked++
	}
	if checked < 5 {
		t.Fatalf("only %d multi-phase boot sequences inspected (want 5): the scan has gone blind", checked)
	}
	// A one-phase owner must not quietly grow a second sweep: assets builds table, columns and indexes
	// inside its own EnsureSchema, so any extra phase call here is a split nobody asked for.
	split := []string{}
	for _, phase := range []string{"MigrateColumns", "EnsureIndexes", "MigrateTable"} {
		if _, found := offsets["NewAssets."+phase]; found {
			split = append(split, "NewAssets."+phase)
		}
	}
	if len(split) > 0 {
		sort.Strings(split)
		t.Errorf("store.Assets grew a second boot phase (%v): its EnsureSchema is the one place that orders 建表 -> 补列 -> 建索引", split)
	}
}
