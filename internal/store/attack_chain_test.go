package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// chainTestSchema is what the chain's foreign keys point at, in the shape the boot creates them -
// conversations and tool_executions. Foreign keys are not enforced in this fixture (the DSN turns them
// off), which is what lets the chain rows be written without a full conversation ledger behind them.
const chainTestSchema = `
CREATE TABLE conversations (id TEXT PRIMARY KEY, title TEXT);
CREATE TABLE tool_executions (id TEXT PRIMARY KEY, tool_name TEXT NOT NULL, status TEXT NOT NULL);
`

func newChainStore(t *testing.T) (*AttackChain, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(chainTestSchema); err != nil {
		t.Fatalf("create the joined tables: %v", err)
	}
	chain := NewAttackChain(db)
	if err := chain.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return chain, db
}

func TestChainEnsureSchemaIsIdempotentAndOwnsBothTablesAndIndexes(t *testing.T) {
	chain, db := newChainStore(t)
	if err := chain.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	for _, object := range []struct {
		kind, name string
	}{
		{"table", "attack_chain_nodes"},
		{"table", "attack_chain_edges"},
		{"index", "idx_chain_nodes_conversation"},
		{"index", "idx_chain_edges_conversation"},
	} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = ? AND name = ?`, object.kind, object.name).Scan(&name); err != nil {
			t.Fatalf("%s %s was not created by EnsureSchema: %v", object.kind, object.name, err)
		}
	}
}

func TestChainSaveAndLoadRoundTrip(t *testing.T) {
	chain, _ := newChainStore(t)
	if err := chain.SaveNode("c1", "n1", "vulnerability", "SQLi on /login", "te-1", `{"severity":"high","cvss":9.8}`, 7); err != nil {
		t.Fatalf("SaveNode: %v", err)
	}
	if err := chain.SaveNode("c1", "n2", "target", "10.0.0.1", "", "", 0); err != nil {
		t.Fatalf("SaveNode without a tool execution: %v", err)
	}
	if err := chain.SaveEdge("c1", "e1", "n1", "n2", "leads_to", 2); err != nil {
		t.Fatalf("SaveEdge: %v", err)
	}

	nodes, err := chain.LoadNodes("c1")
	if err != nil {
		t.Fatalf("LoadNodes: %v", err)
	}
	if len(nodes) != 2 || nodes[0].ID != "n1" || nodes[1].ID != "n2" {
		t.Fatalf("nodes = %+v, want n1 then n2 (build order)", nodes)
	}
	if nodes[0].ToolExecutionID != "te-1" || nodes[0].RiskScore != 7 {
		t.Fatalf("node columns lost: %+v", nodes[0])
	}
	if nodes[0].Metadata["severity"] != "high" {
		t.Fatalf("metadata = %#v, want the JSON decoded", nodes[0].Metadata)
	}
	if nodes[1].ToolExecutionID != "" || nodes[1].Metadata == nil {
		t.Fatalf("a node with no tool execution and no metadata came back as %+v", nodes[1])
	}

	edges, err := chain.LoadEdges("c1")
	if err != nil {
		t.Fatalf("LoadEdges: %v", err)
	}
	if len(edges) != 1 || edges[0].Source != "n1" || edges[0].Target != "n2" || edges[0].Weight != 2 {
		t.Fatalf("edges = %+v", edges)
	}

	// A conversation sees only its own chain.
	if err := chain.SaveNode("c2", "n9", "exploit", "other conversation", "", "{}", 1); err != nil {
		t.Fatal(err)
	}
	other, err := chain.LoadNodes("c1")
	if err != nil || len(other) != 2 {
		t.Fatalf("c1 nodes = %d / %v, want 2 - the conversation filter is the whole isolation story", len(other), err)
	}
	// Empty conversation id reads nothing rather than everything.
	if rows, err := chain.LoadNodes(""); err != nil || len(rows) != 0 {
		t.Fatalf("blank id read %d rows / %v, want none", len(rows), err)
	}
}

// TestChainSaveReplacesTheSameId is the regenerate path: the builder re-saves every node under the
// same id, so an upsert must overwrite rather than accumulate a second copy.
func TestChainSaveReplacesTheSameId(t *testing.T) {
	chain, db := newChainStore(t)
	if err := chain.SaveNode("c1", "n1", "tool", "first label", "", `{"a":1}`, 1); err != nil {
		t.Fatal(err)
	}
	if err := chain.SaveNode("c1", "n1", "tool", "second label", "", `{"a":2}`, 5); err != nil {
		t.Fatal(err)
	}
	nodes, err := chain.LoadNodes("c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM attack_chain_nodes`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("nodes = %d (table holds %d), want the re-saved id to replace", len(nodes), count)
	}
	if nodes[0].Label != "second label" || nodes[0].RiskScore != 5 || nodes[0].Metadata["a"] != float64(2) {
		t.Fatalf("the replacement did not take: %+v", nodes[0])
	}
}

// TestChainReadsSurviveTheShapesAStoredChainCanActuallyHold covers the two columns that are nullable
// with defaults and the metadata blob that a older or hand-edited row may not parse.
func TestChainReadsSurviveTheShapesAStoredChainCanActuallyHold(t *testing.T) {
	chain, db := newChainStore(t)
	// An explicit NULL risk_score (possible because the column is nullable with only a DEFAULT) must
	// read back as 0, and the node must still appear.
	if _, err := db.Exec(`INSERT INTO attack_chain_nodes (id, conversation_id, node_type, node_name, risk_score)
		VALUES ('null-score','c1','target','no score', NULL)`); err != nil {
		t.Fatalf("seed a NULL risk score: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO attack_chain_edges (id, conversation_id, source_node_id, target_node_id, edge_type, weight)
		VALUES ('null-weight','c1','a','b','leads_to', NULL)`); err != nil {
		t.Fatalf("seed a NULL weight: %v", err)
	}
	nodes, err := chain.LoadNodes("c1")
	if err != nil {
		t.Fatalf("LoadNodes with a NULL risk score: %v", err)
	}
	if len(nodes) != 1 || nodes[0].RiskScore != 0 {
		t.Fatalf("nodes = %+v, want the NULL-scored node back with 0", nodes)
	}
	edges, err := chain.LoadEdges("c1")
	if err != nil {
		t.Fatalf("LoadEdges with a NULL weight: %v", err)
	}
	if len(edges) != 1 || edges[0].Weight != 1 {
		t.Fatalf("edges = %+v, want the NULL-weighted edge back with the column default of 1", edges)
	}

	// Metadata that is not JSON at all comes back as an empty map: the chain page still shows the node,
	// which is what the data layer did after logging a warning.
	if _, err := db.Exec(`INSERT INTO attack_chain_nodes (id, conversation_id, node_type, node_name, metadata, risk_score)
		VALUES ('bad-json','c1','tool','broken', 'not json at all', 3)`); err != nil {
		t.Fatalf("seed unparseable metadata: %v", err)
	}
	nodes, err = chain.LoadNodes("c1")
	if err != nil {
		t.Fatalf("LoadNodes with unparseable metadata: %v", err)
	}
	var bad *AttackChainNode
	for i := range nodes {
		if nodes[i].ID == "bad-json" {
			bad = &nodes[i]
		}
	}
	if bad == nil {
		t.Fatalf("the node with unparseable metadata disappeared: %+v", nodes)
	}
	if len(bad.Metadata) != 0 {
		t.Fatalf("metadata = %#v, want the empty map", bad.Metadata)
	}
}

func TestChainDeleteForConversationClearsBothTables(t *testing.T) {
	chain, db := newChainStore(t)
	if err := chain.SaveNode("c1", "n1", "tool", "x", "", "{}", 1); err != nil {
		t.Fatal(err)
	}
	if err := chain.SaveNode("c1", "n2", "tool", "y", "", "{}", 1); err != nil {
		t.Fatal(err)
	}
	if err := chain.SaveEdge("c1", "e1", "n1", "n2", "leads_to", 1); err != nil {
		t.Fatal(err)
	}
	if err := chain.SaveNode("c2", "n3", "tool", "keep", "", "{}", 1); err != nil {
		t.Fatal(err)
	}

	if err := chain.DeleteForConversation("c1"); err != nil {
		t.Fatalf("DeleteForConversation: %v", err)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM attack_chain_nodes WHERE conversation_id = 'c1'`,
		`SELECT COUNT(*) FROM attack_chain_edges WHERE conversation_id = 'c1'`,
	} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s = %d, want 0", query, count)
		}
	}
	// Another conversation's chain is untouched.
	if rows, err := chain.LoadNodes("c2"); err != nil || len(rows) != 1 || rows[0].ID != "n3" {
		t.Fatalf("c2 nodes = %+v / %v, want only n3", rows, err)
	}
	// Deleting a chain that is not there is not an error.
	if err := chain.DeleteForConversation("nothing-here"); err != nil {
		t.Fatalf("delete of an absent chain: %v", err)
	}
}

func TestChainRefusesAConnectionlessHandle(t *testing.T) {
	var missing *AttackChain
	if err := missing.EnsureSchema(); err == nil {
		t.Fatal("EnsureSchema accepted a store without a database")
	}
	if err := missing.SaveNode("c", "n", "tool", "x", "", "{}", 1); err == nil {
		t.Fatal("SaveNode accepted a store without a database")
	}
	if err := missing.SaveEdge("c", "e", "a", "b", "leads_to", 1); err == nil {
		t.Fatal("SaveEdge accepted a store without a database")
	}
	if _, err := missing.LoadNodes("c"); err == nil {
		t.Fatal("LoadNodes accepted a store without a database")
	}
	if _, err := missing.LoadEdges("c"); err == nil {
		t.Fatal("LoadEdges accepted a store without a database")
	}
	if err := missing.DeleteForConversation("c"); err == nil {
		t.Fatal("DeleteForConversation accepted a store without a database")
	}
}
