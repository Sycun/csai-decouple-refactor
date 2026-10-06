package layering

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// assignmentKinds reports, for one module-relative file, how each narrowed storage field is
// assigned: "Narrow" when the value goes through database.Narrow, otherwise the offending text.
//
// The field names come from the caller because they are what the AST scan found in this file -
// matching a hard-coded `db` key silently skipped every narrowed field that is not called db.
//
// The judgement is line-scoped on purpose. Every assignment in this codebase fits on one line, and
// a regexp over text is the right tool for a shape rule - the AST walker would not know which
// composite-literal key belongs to which struct without type information this package deliberately
// does not have. It is NOT a semantic analysis: if a multi-line assignment is ever introduced here,
// internal/handler/narrow_db_test.go (which builds each handler with a nil *DB and inspects the field
// by reflection) is the gate that still catches the leak.
func assignmentKinds(root, rel string, fields []string) map[string][]string {
	out := map[string][]string{}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return out
	}
	alternation := strings.Join(fields, "|")
	keyRe := regexp.MustCompile(`(^|[\s{,])(` + alternation + `):\s*([^,\n]+)`)
	// `\b([a-z]+)\.db = ` followed by a character that is not another `=`: RE2 has no negative
	// lookahead, and without this the ten `if h.db == nil` guards in this package read as
	// assignments whose "value" is "= nil {" - a false positive that fails the gate on a clean tree.
	setterRe := regexp.MustCompile(`\b([a-z]+)\.(` + alternation + `) = ([^=\n][^\n]*)`)
	for _, line := range strings.Split(string(data), "\n") {
		if m := keyRe.FindStringSubmatch(line); m != nil {
			out[rel] = append(out[rel], classifyAssignment(m[3]))
		}
		if m := setterRe.FindStringSubmatch(line); m != nil {
			out[rel] = append(out[rel], classifyAssignment(m[3]))
		}
	}
	return out
}

func classifyAssignment(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.Contains(trimmed, "Narrow[") {
		return "Narrow"
	}
	return trimmed
}

// The transport layer must not hold the process's persistence god object.
//
// This started as a ratchet: internal/handler had 19 structs with a `*database.DB` field, and each
// slice lowered one file's ceiling. It is now a hard zero, so the invariant is "no struct in the
// HTTP layer can reach any table or any of the 361 methods", not "fewer than yesterday".
//
// Getting the last three (AgentHandler, ProjectHandler, WorkflowHandler) required declaring the
// consumer interface at the far end of each chain rather than faking it here:
//   - multiagent never touches the database itself; it forwards the handle to internal/project, so
//     the surface is database.ProjectFactStore and multiagent takes project.Store.
//   - agentfinalizer needed two methods (read/save a tool execution) - database.ToolExecutionLedger.
//   - attackchain needed the chain rows plus the fact ledger - database.AttackChainLedger.
//   - the workflow engine needs its own run ledger plus project facts - workflow.Store.
//
// Surfaces that more than one package needs are declared in internal/database (the consumers import
// it, so declaring them in the consumer would cycle) and aliased back: project.Store,
// agentfinalizer.Store, attackchain.Store. One list to edit, and `var _ X = (*DB)(nil)` beside each.
//
// knowledge.go is the counter-example worth keeping: its `db` field was never read, so the honest
// fix was deleting the field and the constructor parameter rather than inventing a store for it.

// narrowedStoreFloor is how many handler domains are reached through *something narrower than the
// whole connection* - a consumer-shaped database interface, or a per-table store from internal/store.
// It only goes up; a drop means a domain was widened back to the god object.
//
// A domain that graduates from an interface field to a `*store.X` field must still count - and must
// count once, not as a loss. skill_stats is the first: its handler field used to be
// `database.SkillsStore` and is now `*store.SkillStats`, which is the same migration taken one step
// further (one table, one owner, its own schema), so the detector sums both shapes.
// It only goes up; a drop means a domain was widened back to the god object.
//
// The detector used to guess "is this a narrowed surface?" from the type's *name*
// (`database.` prefix + `Store` suffix) and from the field always being called `db`. Both guesses
// are now replaced by facts read from source: the interface list is parsed out of
// internal/database, and the field list comes from the AST scan. Measured with the new detector
// the count is the same 19 - and every one of those fields is named `db` - so this is not a fix to
// a number that was wrong. It is the removal of two assumptions that would have made the gate
// blind without complaining: a surface renamed away from the "Store" suffix, a field named
// anything but `db`, or a struct that *embeds* an interface (refused outright above, since an
// embedded field has no assignment key to check).
const narrowedStoreFloor = 19

// narrowedAssignmentFloor counts how many *interface* assignments the shape scan inspects. It is a
// coverage guard, not a progress ratchet: its job is to fail when the scanner stops seeing fields it
// used to see. skill_stats left that surface - its handler field is a *store.SkillStats now - and an
// owned store needs no Narrow rule, because the typed-nil leak this gate hunts is specific to
// interfaces: `var s *store.SkillStats = nil` is nil, and no assignment can make it a non-nil value
// holding nil. So the floor tracks the interface count (18) rather than pretending to be immutable.
const narrowedAssignmentFloor = 17

// narrowedInterfaceFloor keeps a broken interface scan from turning the floors above into a green
// no-op: internal/database declares 21 interfaces across stores.go and surfaces.go today
// (ToolExecutionLedger left with the monitor domain).
const narrowedInterfaceFloor = 19

// scannedFieldFloor keeps a broken scan from producing a green zero: the handler package declares
// ~990 struct fields, so a walk that sees a tenth of that is not reporting the truth.
const scannedFieldFloor = 500

// narrowedHandlerFields is the one detection both gates share: which handler struct fields hold
// an interface declared in internal/database. Kept in one place so the two cannot drift into
// disagreeing about what "narrowed" means. A database package that parses to almost no interfaces
// is reported as an error rather than a small count, because both gates below would otherwise go
// green while inspecting nothing.
func narrowedHandlerFields(root string) (map[string][]string, map[string][]string, int, error) {
	interfaces, err := DatabaseInterfaces(root)
	if err != nil {
		return nil, nil, 0, err
	}
	if len(interfaces) < narrowedInterfaceFloor {
		return nil, nil, len(interfaces), fmt.Errorf(
			"only %d interfaces found in internal/database (floor %d): the parser is not reading the package",
			len(interfaces), narrowedInterfaceFloor)
	}
	named, embedded, err := NarrowedFieldsByFile(root, "internal/handler", interfaces)
	if err != nil {
		return nil, nil, len(interfaces), err
	}
	return named, embedded, len(interfaces), nil
}

// storeOwnedFields counts handler fields whose type is a per-table store from internal/store.
func storeOwnedFields(byFile map[string]map[string]int) (int, []string) {
	total := 0
	var list []string
	for file, types := range byFile {
		for text, count := range types {
			if strings.HasPrefix(text, "*store.") {
				total += count
				list = append(list, file+"="+text)
			}
		}
	}
	sort.Strings(list)
	return total, list
}

func TestHandlerLayerHoldsNoGodObject(t *testing.T) {
	root := moduleRoot(t)
	byFile, err := FieldTypesByFile(root, "internal/handler")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	fields := 0
	concrete := []string{}
	for file, types := range byFile {
		for text, count := range types {
			fields += count
			if text == "*database.DB" || text == "*sql.DB" || text == "sql.DB" {
				concrete = append(concrete, file+": "+intStr(count)+" field(s) of type "+text)
			}
		}
	}
	if fields < scannedFieldFloor {
		t.Fatalf("the scan saw only %d struct fields in internal/handler (floor %d): the walker is broken, "+
			"a zero here would be meaningless", fields, scannedFieldFloor)
	}

	named, _, _, err := narrowedHandlerFields(root)
	if err != nil {
		t.Fatalf("narrowed field scan: %v", err)
	}
	narrowed := 0
	var narrowedList []string
	for file, names := range named {
		narrowed += len(names)
		narrowedList = append(narrowedList, file+"="+strings.Join(names, ","))
	}
	storeOwned, storeList := storeOwnedFields(byFile)
	sort.Strings(narrowedList)
	sort.Strings(storeList)
	if narrowed+storeOwned < narrowedStoreFloor {
		t.Fatalf("only %d handler domains are narrowed (%d database interfaces + %d owned stores, floor %d): "+
			"a handler was widened back to *database.DB. Interfaces: %v. Stores: %v",
			narrowed+storeOwned, narrowed, storeOwned, narrowedStoreFloor, narrowedList, storeList)
	}
	t.Logf("transport layer: %d structs hold *database.DB, %d fields hold a narrowed database interface, "+
		"%d hold their own table store, %d struct fields scanned (started 19/0)",
		len(concrete), narrowed, storeOwned, fields)

	if len(concrete) > 0 {
		sort.Strings(concrete)
		t.Fatalf("%d handler struct fields still hold the whole database handle:\n%s\n"+
			"The destination is the consumer's own surface: declare what it needs in "+
			"internal/database/surfaces.go (or stores.go) with `var _ X = (*DB)(nil)`, alias it in the "+
			"consuming package, then narrow the field with database.Narrow - a plain assignment of a "+
			"possibly-nil *DB leaks a non-nil interface and silently inverts every h.db == nil guard.",
			len(concrete), strings.Join(concrete, "\n"))
	}
}

// TestNarrowedFieldsAreOnlyAssignedThroughNarrow keeps the typed-nil contract honest at source level:
// an interface field must never be assigned a bare variable. internal/handler/narrow_db_test.go
// proves the behaviour; this proves the shape, so a "simplification" back to `db: db` is caught even
// where no test exercises the nil path.
func TestNarrowedFieldsAreOnlyAssignedThroughNarrow(t *testing.T) {
	root := moduleRoot(t)
	named, embedded, ifaces, err := narrowedHandlerFields(root)
	if err != nil {
		t.Fatalf("narrowed field scan: %v", err)
	}
	// Embedding is the way a field escapes a key-based assignment scan, so it is refused
	// outright rather than counted.
	if len(embedded) > 0 {
		detail := make([]string, 0, len(embedded))
		for file, types := range embedded {
			detail = append(detail, file+": "+strings.Join(types, ", "))
		}
		sort.Strings(detail)
		t.Fatalf("%d handler structs embed a database interface, which widens their method set "+
			"past any per-field check:\n%s", len(embedded), strings.Join(detail, "\n"))
	}

	fields := 0
	for _, names := range named {
		fields += len(names)
	}
	byFileType, err := FieldTypesByFile(root, "internal/handler")
	if err != nil {
		t.Fatalf("store field scan: %v", err)
	}
	storeOwned, _ := storeOwnedFields(byFileType)
	if fields+storeOwned < narrowedStoreFloor {
		t.Fatalf("only %d handler domains are narrowed (%d interfaces + %d owned stores, floor %d): "+
			"a domain was widened back to *database.DB", fields+storeOwned, fields, storeOwned, narrowedStoreFloor)
	}

	violations := []string{}
	seen := 0
	for rel, names := range named {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			// The scanner named a file it cannot open. Everything below would then inspect
			// nothing and the gate would go green while checking nothing - which is exactly how
			// the first version of this helper passed a probe it should have failed.
			t.Fatalf("%s declares a narrowed storage field but cannot be read: %v", rel, err)
		}
		for _, kind := range assignmentKinds(root, rel, names)[rel] {
			seen++
			if kind != "Narrow" {
				violations = append(violations, rel+" assigns a narrowed storage field via "+kind)
			}
		}
	}
	if seen < narrowedAssignmentFloor {
		t.Fatalf("only %d narrowed assignments inspected across the package (floor %d): the scan misses them",
			seen, narrowedAssignmentFloor)
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("narrowed fields assigned without database.Narrow (a nil *DB becomes a non-nil interface):\n%s",
			strings.Join(violations, "\n"))
	}
	t.Logf("%d files hold %d narrowed fields, %d assignments inspected, every one goes through "+
		"database.Narrow (%d interfaces found in internal/database)", len(named), fields, seen, ifaces)
}
