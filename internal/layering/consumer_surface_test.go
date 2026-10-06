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

// consumerSurfaceFiles are the two places internal/database declares the narrow interfaces its
// consumers hold. They are the whole point of the narrowing: a handler can only reach what it names.
//
// That only holds if every name in the list is a name somebody actually invokes. A member nobody
// calls is a promise of reach that no code exercises - and it is how a surface accretes: the next
// person adds a method because "the interface already has five unrelated ones".
var consumerSurfaceFiles = []string{
	"internal/database/stores.go",
	"internal/database/surfaces.go",
}

// consumerSurfaceMemberFloor is a floor, not a target: the declared surface measures 286 members
// across the two files, and deleting members is progress the gate must not punish. The number exists
// so an interface parser that silently finds nothing cannot report a clean bill of health.
// 162 measured after the batch run ledger left: BatchTaskStore, its 22 members, went with the 22
// delegations it described, so the surfaces are one domain smaller than they were.
// 158 measured after the C2 domain left: ResourceExistence lost its three C2 lookups and
// NotificationStore lost ListC2EventsForAccess (the handler holds store.C2 now).
// 140 measured after the monitor domain left: MonitorStore shrank to the one RBAC member
// (MonitorContextStore), AgentStore lost its ToolExecutionLedger embed, ResourceExistence lost
// GetToolExecution and the run finalizer's ledger moved to internal/agentfinalizer.
// 95 measured after the RBAC domain left: thirteen interfaces dropped their RBAC members
// (RBACStore / VulnerabilityStore / ConfigStore / MonitorContextStore vanished whole), and the
// remaining members live on store.RBAC handles in the handlers instead.
// 34 measured after the conversation domain left: ConversationStore / OpenAPIStore /
// AttackChainStore / ChatUploadsStore / WebShellStore / RobotStore / AgentStore / WorkflowStore
// each dropped their conversation members (most now hold store.Conversations instead), and
// ResourceExistence vanished whole.
const consumerSurfaceMemberFloor = 34

func TestConsumerSurfacesDeclareOnlyCalledMethods(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	members := map[string][]string{}
	declaredIn := map[string]bool{}
	for _, rel := range consumerSurfaceFiles {
		file, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		declaredIn[filepath.Join(root, rel)] = true
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			iface, ok := spec.Type.(*ast.InterfaceType)
			if !ok {
				return true
			}
			for _, field := range iface.Methods.List {
				if len(field.Names) == 0 {
					continue // an embedded interface is checked through its own declaration
				}
				name := field.Names[0].Name
				members[name] = append(members[name], spec.Name.Name)
			}
			return true
		})
	}
	total := 0
	for _, list := range members {
		total += len(list)
	}
	if total < consumerSurfaceMemberFloor {
		t.Fatalf("only %d members parsed from the consumer surfaces (floor %d): the parser is not reading %v",
			total, consumerSurfaceMemberFloor, consumerSurfaceFiles)
	}

	var production []string
	for _, base := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, base), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && !declaredIn[path] {
				production = append(production, path)
			}
			return nil
		})
	}
	texts := make([]string, 0, len(production))
	for _, path := range production {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		texts = append(texts, string(data))
	}

	var uncalled []string
	for name, interfaces := range members {
		call := regexp.MustCompile(`\.` + name + `\(`)
		found := false
		for _, text := range texts {
			if call.MatchString(text) {
				found = true
				break
			}
		}
		if !found {
			sort.Strings(interfaces)
			uncalled = append(uncalled, name+" (declared in "+strings.Join(interfaces, ", ")+")")
		}
	}
	if len(uncalled) > 0 {
		sort.Strings(uncalled)
		t.Fatalf("%d consumer-surface methods have no production call site:\n%s\n"+
			"Delete the member (and the *DB method too, if nothing else needs it): a surface that "+
			"declares reach nobody uses is how the narrowing stops meaning anything.",
			len(uncalled), strings.Join(uncalled, "\n"))
	}
	t.Logf("consumer surfaces: %d declared members across %d interfaces, all called from production code",
		total, len(consumerSurfaceFiles))
}
