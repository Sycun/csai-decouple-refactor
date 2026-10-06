package store

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// Both blackboard tables came over from the connection wrapper, together with the unlink the findings
// domain hands to this one. The four cases at the top are the ones that lived in internal/database and
// travelled with the statements they cover.

const projectsJoinedSchema = `
CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '');
`

func newFactStore(t *testing.T) (*Facts, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "facts.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(projectsJoinedSchema); err != nil {
		t.Fatalf("create the joined table: %v", err)
	}
	f := NewFacts(db)
	if err := f.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name) VALUES ('p1', 'board')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return f, db
}

func TestFactsEnsureSchemaBuildsBothTablesAndSixIndexes(t *testing.T) {
	f, db := newFactStore(t)
	if err := f.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema must be idempotent: %v", err)
	}
	for _, object := range []struct{ kind, name string }{
		{"table", "project_facts"}, {"table", "project_fact_edges"},
		{"index", "idx_project_facts_project_id"}, {"index", "idx_project_facts_confidence"},
		{"index", "idx_project_facts_related_vuln"}, {"index", "idx_project_fact_edges_project"},
		{"index", "idx_project_fact_edges_source"}, {"index", "idx_project_fact_edges_target"},
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`,
			object.kind, object.name).Scan(&count); err != nil {
			t.Fatalf("look up %s %s: %v", object.kind, object.name, err)
		}
		if count != 1 {
			t.Fatalf("%s %s present %d times, want 1", object.kind, object.name, count)
		}
	}
}

func TestFactsUpsertPreservesBodyOnEmptyUpdate(t *testing.T) {
	f, _ := newFactStore(t)
	const body = "## 攻击链\n1. step\n```http\nGET / HTTP/1.1\n```\n"
	if _, err := f.Upsert(&ProjectFact{ProjectID: "p1", FactKey: "finding/sqli-login",
		Category: "finding", Summary: "SQLi on /login", Body: body}); err != nil {
		t.Fatal(err)
	}
	updated, err := f.Upsert(&ProjectFact{ProjectID: "p1", FactKey: "finding/sqli-login",
		Summary: "SQLi on /login (confirmed)", Body: ""})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Summary != "SQLi on /login (confirmed)" || updated.Body != body {
		t.Fatalf("update returned summary=%q body=%q, want the attack chain preserved", updated.Summary, updated.Body)
	}
	stored, err := f.GetByKey("p1", "finding/sqli-login")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Body != body {
		t.Fatalf("stored body=%q, want preserved", stored.Body)
	}
}

func TestFactsUpsertReplacesBodyWhenProvided(t *testing.T) {
	f, _ := newFactStore(t)
	if _, err := f.Upsert(&ProjectFact{ProjectID: "p1", FactKey: "target/primary", Summary: "v1", Body: "old body"}); err != nil {
		t.Fatal(err)
	}
	updated, err := f.Upsert(&ProjectFact{ProjectID: "p1", FactKey: "target/primary", Summary: "v2", Body: "new body with evidence"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Body != "new body with evidence" {
		t.Fatalf("body=%q", updated.Body)
	}
}

func TestFactsRestoreOnlyFromDeprecated(t *testing.T) {
	f, _ := newFactStore(t)
	if _, err := f.Upsert(&ProjectFact{ProjectID: "p1", FactKey: "target/restore-me",
		Summary: "s", Confidence: "confirmed"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Restore("p1", "target/restore-me", "tentative"); err == nil ||
		!strings.Contains(err.Error(), "未处于废弃状态") {
		t.Fatalf("restore of a live fact = %v, want the 未处于废弃状态 refusal", err)
	}
	if err := f.Deprecate("p1", "target/restore-me"); err != nil {
		t.Fatal(err)
	}
	if err := f.Restore("p1", "target/restore-me", "confirmed"); err != nil {
		t.Fatal(err)
	}
	restored, err := f.GetByKey("p1", "target/restore-me")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Confidence != "confirmed" {
		t.Fatalf("confidence=%q, want confirmed", restored.Confidence)
	}
	// An unknown key answers the same string the handler turns into a 404.
	if err := f.Deprecate("p1", "target/absent"); err == nil || err.Error() != "事实不存在" {
		t.Fatalf("deprecate unknown = %v, want 事实不存在", err)
	}
}

func TestMergeFactBody(t *testing.T) {
	if got := mergeFactBody("", "keep"); got != "keep" {
		t.Fatalf("empty incoming: got %q", got)
	}
	if got := mergeFactBody("  ", "keep"); got != "keep" {
		t.Fatalf("whitespace incoming: got %q", got)
	}
	if got := mergeFactBody("new", "old"); got != "new" {
		t.Fatalf("non-empty incoming: got %q", got)
	}
}

// TestFactsNullabilityRoundTrip pins the asymmetry the write path actually has: body is handed over as
// the empty string and stored that way, while the two source ids and the finding link go through
// nullIfEmpty and become NULL. Every one of those four columns is COALESCEd on read, so the console
// never sees a null - and a cleared link must read back as "" rather than error the scan.
func TestFactsNullabilityRoundTrip(t *testing.T) {
	f, db := newFactStore(t)
	created, err := f.Upsert(&ProjectFact{ProjectID: "p1", FactKey: "note/plain", Summary: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Body != "" || created.SourceConversationID != "" || created.RelatedVulnerabilityID != "" {
		t.Fatalf("fresh row = %+v, want the four optional columns read as empty strings", created)
	}
	var rawBody sql.NullString
	var rawSource, rawLink sql.NullString
	if err := db.QueryRow(`SELECT body, source_conversation_id, related_vulnerability_id FROM project_facts WHERE id = ?`,
		created.ID).Scan(&rawBody, &rawSource, &rawLink); err != nil {
		t.Fatal(err)
	}
	if rawBody.String != "" || !rawBody.Valid {
		t.Fatalf("body stored = %q (valid=%v), want the empty string the write path hands over", rawBody.String, rawBody.Valid)
	}
	if rawSource.Valid || rawLink.Valid {
		t.Fatalf("an unwritten source id or link must be NULL, got %q / %q", rawSource.String, rawLink.String)
	}

	// A link written and then cleared through the findings domain's unlink.
	if _, err := db.Exec(`UPDATE project_facts SET related_vulnerability_id = 'v1' WHERE id = ?`, created.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.UnlinkFindingReferences(tx, []string{"v1", "v2"}); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after, err := f.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RelatedVulnerabilityID != "" {
		t.Fatalf("link after unlink = %q, want cleared", after.RelatedVulnerabilityID)
	}
}

// TestFactsUnlinkOnlyClearsTheNamedFindings is the claim the handover exists to keep: ids in, only
// those links out. Chunking must not change it, so one id in the batch sits past the 500 boundary.
func TestFactsUnlinkOnlyClearsTheNamedFindings(t *testing.T) {
	f, db := newFactStore(t)
	ids := make([]string, 0, 502)
	for i := 0; i < 502; i++ {
		key := "note.f" + strconv.Itoa(i)
		if _, err := f.Upsert(&ProjectFact{ProjectID: "p1", FactKey: key, Summary: "s",
			RelatedVulnerabilityID: "v" + strconv.Itoa(i)}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		if i < 501 {
			ids = append(ids, "v"+strconv.Itoa(i))
		}
	}
	survivor, err := f.GetByKey("p1", "note.f501")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.UnlinkFindingReferences(tx, ids); err != nil {
		t.Fatalf("unlink across two chunks: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	first, err := f.GetByKey("p1", "note.f0")
	if err != nil {
		t.Fatal(err)
	}
	if first.RelatedVulnerabilityID != "" {
		t.Fatalf("the first chunk left %q", first.RelatedVulnerabilityID)
	}
	boundary, err := f.GetByKey("p1", "note.f500")
	if err != nil {
		t.Fatal(err)
	}
	if boundary.RelatedVulnerabilityID != "" {
		t.Fatalf("the second chunk left %q", boundary.RelatedVulnerabilityID)
	}
	if survivor.RelatedVulnerabilityID != "v501" {
		t.Fatalf("the id that was not handed over lost its link: %q", survivor.RelatedVulnerabilityID)
	}
}

func TestFactsEdgeCascadesAndOrdering(t *testing.T) {
	f, _ := newFactStore(t)
	for _, key := range []string{"note.a", "note.b", "note.c"} {
		if _, err := f.Upsert(&ProjectFact{ProjectID: "p1", FactKey: key, Summary: key}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.ReplaceOutgoing("p1", "note.a", "conv-1", []ProjectFactEdgeInput{
		{To: "note.b", Type: "leads_to", Confidence: "confirmed"},
		{To: "note.c", Type: "depends_on"},
	}); err != nil {
		t.Fatal(err)
	}
	edges, err := f.ListOutgoing("p1", "note.a")
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 2 || edges[0].SourceConversationID != "conv-1" {
		t.Fatalf("outgoing edges = %+v, want both with the conversation stamped", edges)
	}
	// Insertion order is what the console draws, and it is created_at ASC with rowid as the tiebreak.
	if edges[0].TargetFactKey != "note.b" || edges[1].TargetFactKey != "note.c" {
		t.Fatalf("edge order = %v, %v", edges[0].TargetFactKey, edges[1].TargetFactKey)
	}
	// Replacing with an empty list clears them: "links were provided but are empty" is a delete.
	if err := f.ReplaceOutgoing("p1", "note.a", "", []ProjectFactEdgeInput{}); err != nil {
		t.Fatal(err)
	}
	if edges, err := f.ListOutgoing("p1", "note.a"); err != nil || len(edges) != 0 {
		t.Fatalf("edges after empty replace = %+v (%v), want none", edges, err)
	}

	// Deprecating a fact marks its edges; deleting it removes them.
	if _, err := f.AddEdge("p1", ProjectFactEdgeInput{To: "note.b", Type: "supports"}, "note.a", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.Deprecate("p1", "note.b"); err != nil {
		t.Fatal(err)
	}
	incoming, err := f.ListIncoming("p1", "note.b")
	if err != nil {
		t.Fatal(err)
	}
	if len(incoming) != 1 || incoming[0].Confidence != "deprecated" {
		t.Fatalf("incoming edges after deprecate = %+v, want the one marked deprecated", incoming)
	}
	// Renaming a key moves both ends of every edge that names it.
	if err := f.RenameKeyEdges("p1", "note.b", "note/renamed"); err != nil {
		t.Fatal(err)
	}
	all, err := f.ListEdges("p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].TargetFactKey != "note/renamed" {
		t.Fatalf("edges after rename = %+v", all)
	}
	if err := f.DeleteEdgesForKey("p1", "note.a"); err != nil {
		t.Fatal(err)
	}
	if all, err = f.ListEdges("p1"); err != nil || len(all) != 0 {
		t.Fatalf("edges after delete-for-key = %+v (%v), want none", all, err)
	}
	// The self-link and unknown-type refusals are the answers the MCP tool surfaces to the agent.
	if err := f.ReplaceOutgoing("p1", "note.a", "", []ProjectFactEdgeInput{{To: "note.a", Type: "leads_to"}}); err == nil ||
		!strings.Contains(err.Error(), "自身") {
		t.Fatalf("self edge = %v, want the 边不能指向自身 refusal", err)
	}
	if _, err := f.AddEdge("p1", ProjectFactEdgeInput{To: "note/renamed", Type: "invented"}, "note.a", ""); err == nil {
		t.Fatal("an unknown edge type was accepted")
	}
}

func TestFactsRefuseAConnectionlessHandle(t *testing.T) {
	f := NewFacts(nil)
	if _, err := f.List("p1", ProjectFactListFilter{}, 10, 0); err == nil {
		t.Fatal("List on a connectionless store answered no error")
	}
	if _, err := f.Upsert(&ProjectFact{ProjectID: "p1", FactKey: "note/x", Summary: "s"}); err == nil {
		t.Fatal("Upsert answered no error")
	}
	if _, err := f.ListEdges("p1"); err == nil {
		t.Fatal("ListEdges answered no error")
	}
	if err := f.EnsureSchema(); err == nil {
		t.Fatal("EnsureSchema answered no error")
	}
	// The unlink is the one method that runs on someone else's transaction, so a nil tx is refused too.
	if err := f.UnlinkFindingReferences(nil, []string{"v1"}); err == nil {
		t.Fatal("the unlink accepted a nil transaction")
	}
}
