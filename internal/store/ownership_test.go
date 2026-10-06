package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The data-layer item of the refactor plan is about ownership, not about where a
// file happens to sit: a table written from several places cannot be changed,
// tested or reasoned about as one thing. These are the tables this package owns,
// and the statements that reach them must exist here and nowhere else in
// production code.
//
// Test files in other packages are allowed to create fixtures directly: they set up
// a database, they are not a second write path.
// robot_user_bindings is on the list now that the alert-recipient query - the one statement outside
// store.RobotIdentity that joined it - moved to store.VulnerabilityAlerts with its own two tables.
// Claiming it half-way (a second writer still reading it from the data layer) would have been a leak
// reported as ownership.
//
// project_facts and project_fact_edges are deliberately NOT on this list even though store.Facts now
// owns both: the project dashboard and the project statistics aggregate read across them, and this
// scan bans reads as well as writes. They are claimed by writer instead - see writeLedger and
// TestProjectFactsHasOneWriter, the same treatment the findings table gets.
func TestOwnedTablesAreOnlyWrittenFromThisPackage(t *testing.T) {
	root := moduleRoot(t)
	owned := []string{"hitl_interrupts", "hitl_conversation_configs", "notification_reads_by_user", "skill_stats", "chat_upload_artifacts", "audit_logs",
		"knowledge_retrieval_logs", "knowledge_base_items", "knowledge_embeddings", "model_token_usage",
		"robot_user_sessions", "robot_binding_codes", "c2_payload_artifacts",
		"vulnerability_alert_subscriptions", "vulnerability_alert_deliveries", "robot_user_bindings",
		"attack_chain_nodes", "attack_chain_edges",
		"workflow_definitions", "workflow_runs", "workflow_node_runs",
		"workflow_package_inspections", "workflow_package_imports"}
	pattern := regexp.MustCompile(`(?i)\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM|FROM|JOIN)\s+` + `(` + strings.Join(owned, "|") + `)`)

	var offenders []string
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
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			inStore := strings.HasPrefix(filepath.ToSlash(rel), "internal/store/")
			if inStore || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if hit := pattern.FindString(string(data)); hit != "" {
				offenders = append(offenders, rel+": "+hit)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", dir, err)
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("statements against tables owned by internal/store leaked into production code: %v. "+
			"Add a method to the owning store instead of writing the table from elsewhere.", offenders)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test working directory")
		}
		dir = parent
	}
}

// TestFindingsTableHasOneWriter claims the findings table by writer rather than by reader.
//
// The owned-table scan above cannot carry this table: findings rows are also read by four other
// domains' aggregate queries (the asset risk trend, the project statistics, the RBAC resource search
// and the batch-task join inside the listing itself), and a claim that bans those reads would either
// be wrong or would have to be widened until it means nothing. So this table is claimed by the
// narrower, stronger question - who may change it.
//
// The count on the store side is asserted too, because a gate that passes on an empty set proves
// nothing: if the findings writes ever stop being here, that is the leak this test exists to see.
func TestFindingsTableHasOneWriter(t *testing.T) {
	root := moduleRoot(t)
	// The pattern is SQL-shaped on purpose: the permission catalogue in internal/security/rbac.go
	// literally says "Create and update vulnerabilities", and a keyword-plus-table match reports that
	// prose as a write. Requiring the syntax that follows the table name (the VALUES list, the SET
	// clause, the DELETE's own end of statement) is what keeps the scan about statements.
	// The word boundary belongs inside each alternative, not around the group: the insert is followed by
	// its column list, and a `\b` after a "(" never matches. That blind spot was found by the exact
	// count below, not by the leak check - the insert is the very statement this gate claims to see.
	writer := regexp.MustCompile(`(?i)\b(?:INSERT\s+INTO\s+vulnerabilities\s*\(|UPDATE\s+vulnerabilities\s+SET|DELETE\s+FROM\s+vulnerabilities\b)`)

	var offenders []string
	ownWrites := 0
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
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			hits := writer.FindAllString(string(data), -1)
			if strings.HasPrefix(filepath.ToSlash(rel), "internal/store/") {
				ownWrites += len(hits)
				return nil
			}
			for _, hit := range hits {
				offenders = append(offenders, rel+": "+hit)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", dir, err)
		}
	}

	// Six statements as this slice landed: the insert, the update, the two deletes (single row and by
	// filter), the retired-conversation source stamp and the project unlink. The count is an exact
	// expectation rather than a floor because the claim being pinned is "these and only these change
	// the table"; a new write belongs here deliberately, with the sentence above it updated.
	if ownWrites != 6 {
		t.Fatalf("internal/store writes the findings table in %d statements, want exactly 6 - "+
			"a scan that finds fewer (or none) here is not a claim about ownership", ownWrites)
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("the findings table is written from outside internal/store: %v. "+
			"The lifecycle steps that need a finding touched (conversation retirement, project deletion) "+
			"call store.Vulnerabilities for it; add a method there instead of writing the table here.", offenders)
	}
}

// writeLedger is every table internal/store changes, with the files here that change it.
//
// It is a declaration of this package's own write surface, not a list of everything this package
// reads: aggregate queries in other domains may select from a table without being allowed to change
// it, and the claims that ban reads would either be wrong or be widened until they mean nothing.
var writeLedger = map[string][]string{
	"attack_chain_edges":                {"attack_chain.go"},
	"attack_chain_nodes":                {"attack_chain.go"},
	"audit_logs":                        {"audit_logs.go"},
	"capability_unit_switches":          {"capability_switches.go"},
	"chat_upload_artifacts":             {"chat_upload.go"},
	"c2_payload_artifacts":              {"c2_payload.go"},
	"hitl_conversation_configs":         {"hitl_lifecycle.go"},
	"hitl_interrupts":                   {"hitl.go", "hitl_lifecycle.go"},
	"knowledge_base_items":              {"knowledge_items.go"},
	"knowledge_embeddings":              {"knowledge_embeddings.go"},
	"knowledge_retrieval_logs":          {"knowledge_retrieval.go"},
	"messages":                          {"session.go"},
	"model_token_usage":                 {"model_token_usage.go"},
	"notification_reads_by_user":        {"notification_reads.go"},
	"process_details":                   {"session.go"},
	"project_facts":                     {"facts_ledger.go"},
	"project_fact_edges":                {"facts_edges.go"},
	"robot_binding_codes":               {"robot_identity.go"},
	"robot_user_bindings":               {"robot_identity.go"},
	"robot_user_sessions":               {"robot_sessions.go"},
	"skill_stats":                       {"skill_stats.go"},
	"vulnerabilities":                   {"vulnerability.go"},
	"vulnerability_alert_deliveries":    {"vulnerability_alerts.go"},
	"vulnerability_alert_subscriptions": {"vulnerability_alerts.go"},
	"workflow_definitions":              {"workflows.go", "workflow_package.go"},
	"workflow_node_runs":                {"workflows.go"},
	"workflow_package_imports":          {"workflow_package.go"},
	"workflow_package_inspections":      {"workflow_package.go"},
	"workflow_runs":                     {"workflows.go"},
}

// TestStoreWritesOnlyTablesItOwns claims the other direction of ownership. The scan above keeps other
// packages off this package's tables; this one keeps a store off tables it does not own - the mistake
// the findings store made while it cleared project_facts inside its own delete transaction, which is
// a second writer for another domain's table wearing a store's clothes.
//
// Two details make the claim mean what it says:
//
//   - Only string literals are read, via go/ast. Prose cannot be mistaken for a statement: the RBAC
//     catalogue in internal/security/rbac.go contains "Create and update vulnerabilities", and the
//     comment above the findings store's delete names project_facts while writing nothing.
//   - A name counts as a table only if some production file creates it. That is what drops the upsert
//     clause - "ON CONFLICT(user_id) DO UPDATE SET" would otherwise report a table called "set".
//
// The ledger is checked in both directions. A write to a table that is not listed is an offender; a
// listed table that stops being written from the files named here is a dead claim. The second half
// matters: widening the list is the cheapest way to silence a failing version of this test, and an
// entry nobody writes is how such a widening shows up.
func TestStoreWritesOnlyTablesItOwns(t *testing.T) {
	root := moduleRoot(t)
	tables := createdTables(t, root)
	writes := packageWrites(t, filepath.Join(root, "internal", "store"), tables)

	if len(writes) == 0 {
		t.Fatal("the scan found no write statement in internal/store - an empty scan is not a claim about ownership")
	}

	var offenders []string
	for file, byTable := range writes {
		for table, count := range byTable {
			if _, listed := writeLedger[table]; listed {
				continue
			}
			offenders = append(offenders, file+": "+table+" ("+strconv.Itoa(count)+")")
		}
	}
	for table, files := range writeLedger {
		found := 0
		for _, f := range files {
			found += writes[f][table]
		}
		if found == 0 {
			offenders = append(offenders, "declared write with no statement behind it: "+table+" ("+strings.Join(files, ", ")+")")
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("internal/store changes tables it does not own, or its write ledger is stale: %v. "+
			"Ask the owning domain for a method through an injected interface, as store.FindingEffects does; "+
			"do not add another domain's table to the ledger.", offenders)
	}

	distinct := distinctTables(writes)
	statements := 0
	for _, counts := range writes {
		for _, count := range counts {
			statements += count
		}
	}
	t.Logf("internal/store writes %d statements over %d tables in %d files", statements, distinct, len(writes))
	// The ledger size is exact while the scan's coverage is a floor: this package grows as domains are
	// extracted, but every added table has to be an intentional edit with a file behind it. A floor on
	// the ledger would let a failing offender be silenced by listing the table it names.
	if len(writeLedger) != 29 {
		t.Fatalf("the write ledger lists %d tables, want exactly 29 - measured 2026-10-06 when the blackboard "+
			"arrived: %d statements over %d tables from %d files", len(writeLedger), statements, distinct, len(writes))
	}
	if len(writes) < 20 {
		t.Fatalf("write ledger covers %d files, want at least 20 - the scan has gone blind", len(writes))
	}
	if distinct < 29 {
		t.Fatalf("write ledger covers %d tables, want at least 29 - the scan has gone blind", distinct)
	}
}

// TestProjectFactsHasOneWriter pins both blackboard tables to the store that owns them.
//
// Two steps of drift are in this table's history, and the test guards both. Deleting a finding used to
// carry the unlink as SQL inside the findings store, so project_facts had two writers; the ids are
// handed over now, on the caller's transaction. Then the whole domain's SQL moved into store.Facts,
// which is where it has to stay - the delegation on *database.DB is one line and no statement, and a
// second copy of any of these writes would show up here.
func TestProjectFactsHasOneWriter(t *testing.T) {
	root := moduleRoot(t)
	tables := createdTables(t, root)
	owners := map[string]string{
		"project_facts":      "internal/store/facts_ledger.go",
		"project_fact_edges": "internal/store/facts_edges.go",
	}
	// Six statements for the facts (insert, four updates including the unlink this test exists for,
	// delete) and nine for the edges (two inserts, three updates, four deletes - measured with the
	// count this test prints when it goes red).
	wants := map[string]int{"project_facts": 6, "project_fact_edges": 9}

	counts := map[string]int{}
	var offenders []string
	for _, dir := range []string{"internal", "cmd"} {
		for path := range productionGoFiles(t, filepath.Join(root, dir)) {
			rel := mustRel(t, root, path)
			for _, w := range writeTargets(t, path, tables) {
				owner, tracked := owners[w.table]
				if !tracked {
					continue
				}
				if rel == owner {
					counts[w.table]++
					continue
				}
				offenders = append(offenders, rel+": "+w.statement)
			}
		}
	}

	for table, want := range wants {
		if counts[table] != want {
			t.Fatalf("%s is written in %d statements by its owner, want exactly %d - a scan finding fewer "+
				"is not a claim about ownership", table, counts[table], want)
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("the blackboard tables are written from outside store.Facts: %v. "+
			"Add a method to store.Facts (and delegate from *database.DB if a consumer still calls through "+
			"it) instead of writing project_facts or project_fact_edges somewhere else.", offenders)
	}
}

// factVocabulary is the project blackboard's row and input shapes, plus the two validators that
// decide what a legal key or edge type is. They are declared once, here, and every consumer names
// this package - which is the point of the check.
var factVocabulary = []string{
	"ProjectFact", "ProjectFactListFilter", "ProjectFactSparseRow", "factKeyPattern", "ValidateFactKey",
	"ValidProjectFactEdgeTypes", "ProjectFactEdge", "ProjectFactEdgeInput", "ProjectFactEdgeFromInput",
	"ValidateProjectFactEdgeType", "ProjectFactGraphNode", "ProjectFactGraphEdge", "ProjectFactGraph",
}

// TestFactVocabularyHasOneHome keeps a second row shape from appearing.
//
// A type declared twice is two contracts: the data layer would answer with its own struct while the
// HTTP layer serialises this one, and the drift only shows up as a JSON field that quietly disappears.
// The name is matched at its declaration (`type`/`var`/`func` at column 0) rather than by import,
// because a package may mention these names in prose and the store must not be the only place that
// does. Every name has to be found, so a scan that finds nothing cannot pass.
func TestFactVocabularyHasOneHome(t *testing.T) {
	root := moduleRoot(t)
	const home = "internal/store/facts.go"

	homes := map[string][]string{}
	for _, dir := range []string{"internal", "cmd"} {
		for path := range productionGoFiles(t, filepath.Join(root, dir)) {
			rel := mustRel(t, root, path)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			for _, name := range factVocabulary {
				declared := regexp.MustCompile(`(?m)^(?:type|var|func) ` + name + `\b`)
				if declared.Match(data) {
					homes[name] = append(homes[name], rel)
				}
			}
		}
	}

	var offenders []string
	for _, name := range factVocabulary {
		at := homes[name]
		if len(at) == 0 {
			offenders = append(offenders, name+": declared nowhere")
			continue
		}
		for _, file := range at {
			if file != home {
				offenders = append(offenders, name+": also declared in "+file)
			}
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("the project blackboard's vocabulary is not declared in exactly one place (%s): %v", home, offenders)
	}
}

type writeHit struct {
	table     string
	statement string
}

// createdTables is the schema's own vocabulary: every table name any production file creates.
func createdTables(t *testing.T, root string) map[string]bool {
	t.Helper()
	creator := regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	tables := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		for path := range productionGoFiles(t, filepath.Join(root, dir)) {
			for _, lit := range stringLiterals(t, path) {
				for _, m := range creator.FindAllStringSubmatch(lit, -1) {
					tables[strings.ToLower(m[1])] = true
				}
			}
		}
	}
	if len(tables) < 40 {
		t.Fatalf("found %d created tables, want at least 40 - the schema scan has gone blind", len(tables))
	}
	return tables
}

// packageWrites returns table -> statement count for every write statement in one directory.
func packageWrites(t *testing.T, dir string, tables map[string]bool) map[string]map[string]int {
	t.Helper()
	byFile := map[string]map[string]int{}
	for path := range productionGoFiles(t, dir) {
		counts := map[string]int{}
		for _, w := range writeTargets(t, path, tables) {
			counts[w.table]++
		}
		if len(counts) > 0 {
			byFile[filepath.Base(path)] = counts
		}
	}
	return byFile
}

func distinctTables(byFile map[string]map[string]int) int {
	set := map[string]bool{}
	for _, counts := range byFile {
		for table := range counts {
			set[table] = true
		}
	}
	return len(set)
}

// writeTargets finds INSERT/UPDATE/DELETE statements in one file's SQL. Only string literals are
// examined and only names the schema creates are reported, so the answer is about statements rather
// than about anything a sentence happens to mention.
func writeTargets(t *testing.T, path string, tables map[string]bool) []writeHit {
	t.Helper()
	writer := regexp.MustCompile(`(?i)\b(INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+([a-z_][a-z0-9_]*)`)
	var out []writeHit
	for _, lit := range stringLiterals(t, path) {
		for _, m := range writer.FindAllStringSubmatch(lit, -1) {
			table := strings.ToLower(m[2])
			if !tables[table] {
				continue
			}
			out = append(out, writeHit{table: table, statement: strings.Join(strings.Fields(m[0]), " ")})
		}
	}
	return out
}

func stringLiterals(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatalf("unquote %s: %v", path, err)
		}
		out = append(out, value)
		return true
	})
	return out
}

// productionGoFiles yields the non-test source files under dir, skipping fixtures.
func productionGoFiles(t *testing.T, dir string) <-chan string {
	ch := make(chan string)
	go func() {
		defer close(ch)
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				t.Errorf("walk %s: %v", path, err)
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "generated" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				ch <- path
			}
			return nil
		})
		if err != nil {
			t.Errorf("scan %s: %v", dir, err)
		}
	}()
	return ch
}

func mustRel(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("relativize %s: %v", path, err)
	}
	return filepath.ToSlash(rel)
}
