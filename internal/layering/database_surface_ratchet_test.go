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
// 337 -> 333: chat_upload_artifacts moved to its own store.
// 333 -> 328: audit_logs moved to its own store.
// 328 -> 327: the embeddings column backfill left *database.DB for store.KnowledgeEmbeddings, and
// migrateKnowledgeEmbeddingsColumns was deleted rather than relocated. That slice left the ceiling one
// above the measurement, which a ratchet only mentions in a log line - so the descent trail below is
// the record, and a number written here has to be re-measured rather than carried over.
// 327 -> 320: model_token_usage moved whole - two public reads, the upsert, the history backfill, the
// timeline hook and the two private query helpers. The last two are why the drop is seven rather than
// the four the method list shows: the counter counts every *DB receiver, exported or not.
// 320 -> 316: robot_user_sessions moved whole - the read, the write, the delete, and the private
// migrateRobotUserSessionsTable that backfilled its one late column.
// 316 -> 310: robot_user_bindings + robot_binding_codes moved whole - the code issue, the spend, the
// resolve, the list, both deletes, and the private normalizer they shared.
// 310 -> 308: the connection wrapper stopped being a callback registry. SetVulnerabilityCreatedHook and
// NotifyVulnerabilityCreated are gone; the route lives in internal/app, where the ordering reason for
// an indirection (tools registered before the listener exists) is actually a wiring concern.
// 308 -> 306: c2_payload_artifacts moved whole - the record and the ownership lookup. Its access check
// was a third method on the wrapper; it became a composition at the download gate instead, so the
// table's store never answers a permission question.
// 306 -> 295: the findings record moved whole - create, get, the two lists, the two counts, update,
// both deletes, the statistics and the filter suggestions, plus the private transaction helper that
// collected the conversations a batch delete touched. What stays on the wrapper for that handler is
// the RBAC trio and the alert subscription, which are not this table's rows. Two more lifecycle writes
// came along: the retired-conversation source stamp and the project unlink, both of which the
// conversation and project deletes used to write inline.
// 295 -> 294: the private best-effort wrapper the findings writes used lost its last caller when the
// SQL left this package; the adapter in findings_store.go now does that job next to the logger.
// 294 -> 287: the vulnerability alert domain moved whole - the subscription read and write, the
// recipient expansion, the outbox enqueue, the due-list drain and both mark-backoff writes - together
// with the DDL of its two tables, which EnsureSchema now creates itself.
// 287 -> 282: the attack chain moved whole - both saves, both loads, the delete-a-conversation's-chain
// step - and the two tables' DDL with their conversation indexes went to store.AttackChain.EnsureSchema.
// 282 -> 262: the workflow domain moved whole - definitions, runs, node runs and the
// two package-exchange tables, twenty methods including the private scanners, the hash helpers and the
// runs-table column backfill. The engine no longer reaches them through a data-layer interface.
// 262 -> 259: the blackboard's SQL and both tables' schema moved to store.Facts, leaving one-line
// delegations behind. 259 -> 241: those delegations are gone too - the ledger half of the old
// ProjectFactStore became its own interface, answered by *store.Facts, and every consumer that needed
// it now holds that store as a field. Nothing on the connection wrapper can change these tables.
// 232 -> 231: the assets domain moved whole - the upsert, the six list/read/update/delete/merge
// writes, the scan bookkeeping, the risk-cache refresh and the project unlink, with the table's DDL,
// its thirteen late columns and its ten indexes. Seventeen one-line delegations held the call sites,
// so the drop was the one private migration that had no reason to be on a connection at all.
// 231 -> 214: those seventeen delegations are gone. AssetHandler and the MCP asset tools now hold
// *store.Assets as their own field, the findings adapter asks it for the risk-cache refresh, and
// AssetStore shrank to the two members that belong to other domains (the project row and the RBAC
// existence check) under the name AssetContextStore. Nothing on the connection wrapper touches
// `assets` now, in either direction - the write ledger and the fresh-install check both confirm it.
// 214 -> 213: the batch run ledger moved whole - the queue row and its twelve late columns, the task
// rows, the schedule stamps, the rerun reset and the single-task prepare - with both tables' DDL and
// their three indexes. Twenty-two one-line delegations hold the manager and the handlers, so the drop
// is migrateBatchTaskQueuesTable, the private backfill the table's owner now runs itself.
// 191: the batch run ledger stopped reaching these tables through the connection at all. Twenty-two
// one-line delegations are gone, BatchTaskManager holds *store.BatchTasks as its own field, and the
// audit "is this queue still there" lookup went to the queue's owner the same way the finding and
// WebShell lookups did - which is why the drop is 22 rather than 0.
// 189: the findings table's own schema left the data layer too - the CREATE TABLE, its seven indexes,
// the seven late columns and the conversation-key rebuild that used to run at start-up. The two
// migration methods are gone; EnsureSchema now runs from store.Vulnerabilities at the same boot
// position, still after conversations because of the foreign key onto it.
// 188: the migration that backfilled messages.updated_at / reasoning_content left *DB too, so the
// table's owner now runs its own schema, its indexes, its late columns and its start-up backfill.
// 181: the connection object stopped being the place that knows how an agent run's files are laid
// out. Two setters became one, and the six directory helpers (the scoped remove, the uploads date
// walk, the project-scoped remove, the two default-resolving roots) moved to internal/storage; the
// two exported roots stay because the handler reads them through its own store interface.
// 181 -> 133: the C2 domain moved whole - the forty-seven methods over listeners, sessions, tasks,
// files, events and malleable profiles, plus the listener's project_id backfill that had no reason
// to sit on a connection. One of the forty-seven, ListC2Events, was the dead one the surface
// allow-list had been holding; it was deleted rather than relocated, and its data-layer test moved
// onto the live sibling ListC2EventsForAccess.
// 133 -> 109: the monitor domain moved whole - the twenty-two methods over tool_executions and
// tool_stats, plus the partial-output column backfill and the legacy-guard data migration that
// only ever touched those two tables. One of the twenty-two, LoadToolExecutionListPage, was the
// dead one on the allow-list; deleted, with its two data-layer cases moved onto the live sibling
// LoadToolExecutionListPageForAccess.
const dbMethodCeiling = 109

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
