package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// AttackChain owns the two tables a conversation's attack chain is stored in. The chain can be
// rebuilt from the conversation's evidence at any time (that is what the regenerate endpoint does),
// so these rows are a cache with a shape of their own: node/edge identity, the geometry between them,
// and the risk score the console draws.
type AttackChain struct {
	db *sql.DB
}

func NewAttackChain(db *sql.DB) *AttackChain {
	return &AttackChain{db: db}
}

func (c *AttackChain) requireDB() error {
	if c == nil || c.db == nil {
		return errors.New("store: attack chain requires a database")
	}
	return nil
}

// chainSchema is both tables and all four of their indexes. The two per-node lookup indexes used to
// sit in the data layer's general index sweep, which is a second place to change whenever this
// table's shape moves; they came here with the tables. The node table has a foreign key onto
// tool_executions and both have one onto conversations, so the boot path must call EnsureSchema after
// those - asserted by TestAttackChainSchemaIsEnsuredAtBoot.
const chainSchema = `
	CREATE TABLE IF NOT EXISTS attack_chain_nodes (
		id TEXT PRIMARY KEY,
		conversation_id TEXT NOT NULL,
		node_type TEXT NOT NULL,
		node_name TEXT NOT NULL,
		tool_execution_id TEXT,
		metadata TEXT,
		risk_score INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
		FOREIGN KEY (tool_execution_id) REFERENCES tool_executions(id) ON DELETE SET NULL
	);
	CREATE TABLE IF NOT EXISTS attack_chain_edges (
		id TEXT PRIMARY KEY,
		conversation_id TEXT NOT NULL,
		source_node_id TEXT NOT NULL,
		target_node_id TEXT NOT NULL,
		edge_type TEXT NOT NULL,
		weight INTEGER DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
		FOREIGN KEY (source_node_id) REFERENCES attack_chain_nodes(id) ON DELETE CASCADE,
		FOREIGN KEY (target_node_id) REFERENCES attack_chain_nodes(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_chain_nodes_conversation ON attack_chain_nodes(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_chain_edges_conversation ON attack_chain_edges(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_chain_edges_source ON attack_chain_edges(source_node_id);
	CREATE INDEX IF NOT EXISTS idx_chain_edges_target ON attack_chain_edges(target_node_id);`

func (c *AttackChain) EnsureSchema() error {
	if err := c.requireDB(); err != nil {
		return err
	}
	if _, err := c.db.Exec(chainSchema); err != nil {
		return fmt.Errorf("create attack chain tables: %w", err)
	}
	return nil
}

// AttackChainNode is one node as the page renders it. Metadata is a free-form map: it is stored as
// JSON text, and a row whose JSON no longer parses comes back with an empty map rather than failing
// the whole chain (see LoadNodes for what that costs).
type AttackChainNode struct {
	ID              string                 `json:"id"`
	Type            string                 `json:"type"` // tool, vulnerability, target, exploit
	Label           string                 `json:"label"`
	ToolExecutionID string                 `json:"tool_execution_id,omitempty"`
	Metadata        map[string]interface{} `json:"metadata"`
	RiskScore       int                    `json:"risk_score"`
}

// AttackChainEdge is one edge; Source and Target are node ids.
type AttackChainEdge struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"` // leads_to, exploits, enables, depends_on
	Weight int    `json:"weight"`
}

// SaveNode replaces a node. Re-saving the same id is how a regenerate overwrites a stale chain, so
// the statement is an upsert by design (INSERT OR REPLACE), not an insert.
func (c *AttackChain) SaveNode(conversationID, nodeID, nodeType, nodeName, toolExecutionID, metadata string, riskScore int) error {
	if err := c.requireDB(); err != nil {
		return err
	}
	var toolExecID sql.NullString
	if toolExecutionID != "" {
		toolExecID = sql.NullString{String: toolExecutionID, Valid: true}
	}
	var metadataJSON sql.NullString
	if metadata != "" {
		metadataJSON = sql.NullString{String: metadata, Valid: true}
	}

	_, err := c.db.Exec(`
		INSERT OR REPLACE INTO attack_chain_nodes 
		(id, conversation_id, node_type, node_name, tool_execution_id, metadata, risk_score, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, nodeID, conversationID, nodeType, nodeName, toolExecID, metadataJSON, riskScore)
	if err != nil {
		return fmt.Errorf("保存攻击链节点失败: %w", err)
	}
	return nil
}

func (c *AttackChain) SaveEdge(conversationID, edgeID, sourceNodeID, targetNodeID, edgeType string, weight int) error {
	if err := c.requireDB(); err != nil {
		return err
	}
	_, err := c.db.Exec(`
		INSERT OR REPLACE INTO attack_chain_edges 
		(id, conversation_id, source_node_id, target_node_id, edge_type, weight, created_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, edgeID, conversationID, sourceNodeID, targetNodeID, edgeType, weight)
	if err != nil {
		return fmt.Errorf("保存攻击链边失败: %w", err)
	}
	return nil
}

// LoadNodes returns the chain's nodes in build order: created_at first, rowid to break ties inside the
// same timestamp. A node whose metadata is not JSON comes back with an empty map.
func (c *AttackChain) LoadNodes(conversationID string) ([]AttackChainNode, error) {
	if err := c.requireDB(); err != nil {
		return nil, err
	}
	// risk_score and weight are nullable columns that carry defaults; reading them through COALESCE
	// keeps a row holding an explicit NULL in the page instead of answering it by dropping the node.
	rows, err := c.db.Query(`
		SELECT id, node_type, node_name, tool_execution_id, metadata, COALESCE(risk_score, 0)
		FROM attack_chain_nodes
		WHERE conversation_id = ?
		ORDER BY created_at ASC, rowid ASC
	`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("查询攻击链节点失败: %w", err)
	}
	defer rows.Close()

	var nodes []AttackChainNode
	for rows.Next() {
		var node AttackChainNode
		var toolExecID sql.NullString
		var metadataJSON sql.NullString
		// Every column this query reads is either NOT NULL in the table or scanned through a Null*
		// above, so a scan error means the table no longer matches the query - it is returned rather
		// than answered by dropping the node.
		if err := rows.Scan(&node.ID, &node.Type, &node.Label, &toolExecID, &metadataJSON, &node.RiskScore); err != nil {
			return nil, fmt.Errorf("扫描攻击链节点失败: %w", err)
		}
		if toolExecID.Valid {
			node.ToolExecutionID = toolExecID.String
		}
		node.Metadata = map[string]interface{}{}
		if metadataJSON.Valid && metadataJSON.String != "" {
			if err := json.Unmarshal([]byte(metadataJSON.String), &node.Metadata); err != nil {
				// Kept as a fallback rather than an error: the data layer logged a warning and carried
				// on with an empty map, and a chain page must not go blank because one node's metadata
				// was written badly. What this move costs is that warning line - the store has no
				// logger, and inventing one to carry a single line was not worth the new dependency.
				node.Metadata = map[string]interface{}{}
			}
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历攻击链节点失败: %w", err)
	}
	return nodes, nil
}

func (c *AttackChain) LoadEdges(conversationID string) ([]AttackChainEdge, error) {
	if err := c.requireDB(); err != nil {
		return nil, err
	}
	rows, err := c.db.Query(`
		SELECT id, source_node_id, target_node_id, edge_type, COALESCE(weight, 1)
		FROM attack_chain_edges
		WHERE conversation_id = ?
		ORDER BY created_at ASC, rowid ASC
	`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("查询攻击链边失败: %w", err)
	}
	defer rows.Close()

	var edges []AttackChainEdge
	for rows.Next() {
		var edge AttackChainEdge
		if err := rows.Scan(&edge.ID, &edge.Source, &edge.Target, &edge.Type, &edge.Weight); err != nil {
			return nil, fmt.Errorf("扫描攻击链边失败: %w", err)
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历攻击链边失败: %w", err)
	}
	return edges, nil
}

// DeleteForConversation drops the whole chain. The edges go first because they hold the foreign keys
// onto the nodes, and an edge failure is not fatal: the node delete below answers for it.
func (c *AttackChain) DeleteForConversation(conversationID string) error {
	if err := c.requireDB(); err != nil {
		return err
	}
	// Best effort, exactly as before: a leftover edge row for a conversation being deleted is not a
	// failure the caller can act on, and the node delete below decides the answer. The data layer
	// logged this one warning line; the store has no logger, so the line is what this move costs.
	_, _ = c.db.Exec("DELETE FROM attack_chain_edges WHERE conversation_id = ?", conversationID)
	if _, err := c.db.Exec("DELETE FROM attack_chain_nodes WHERE conversation_id = ?", conversationID); err != nil {
		return fmt.Errorf("删除攻击链节点失败: %w", err)
	}
	return nil
}
