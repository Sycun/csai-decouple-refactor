package store

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The project blackboard's vocabulary: one fact row, the edge that links two facts, the shapes the
// write paths take, and the two validators that decide what a key or an edge type may be.
//
// These declarations used to sit in internal/database next to the SQL that produces them. They travel
// first because everything that consumes a fact - the HTTP handler, the MCP tools, the blackboard and
// graph builders in internal/project, the attack-chain promotion path - has to agree on one row shape,
// and a row shape owned by the connection wrapper is how "any package can touch any table" starts.
// The statements that fill these structs move to a store of their own in the next slice; until then
// internal/database still writes them, and it now writes store's types rather than its own.

var factKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*$`)

// ValidateFactKey 校验事实 key（项目内唯一标识）。
func ValidateFactKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("fact_key 不能为空")
	}
	if len(key) > 128 {
		return fmt.Errorf("fact_key 过长（最多 128 字符）")
	}
	if !factKeyPattern.MatchString(key) {
		return fmt.Errorf("fact_key 格式无效，仅允许字母、数字及 . _ / -，且须以字母或数字开头（支持驼峰命名）")
	}
	return nil
}

// ProjectFact 项目事实（黑板条目）。
type ProjectFact struct {
	ID                     string    `json:"id"`
	ProjectID              string    `json:"project_id"`
	FactKey                string    `json:"fact_key"`
	Category               string    `json:"category"`
	Summary                string    `json:"summary"`
	Body                   string    `json:"body"`
	Confidence             string    `json:"confidence"` // confirmed | tentative | deprecated
	SourceConversationID   string    `json:"source_conversation_id,omitempty"`
	SourceMessageID        string    `json:"source_message_id,omitempty"`
	Pinned                 bool      `json:"pinned"`
	RelatedVulnerabilityID string    `json:"related_vulnerability_id,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// ProjectFactListFilter 事实列表筛选。
type ProjectFactListFilter struct {
	Category               string
	Confidence             string
	Search                 string
	RelatedVulnerabilityID string
	ExcludeDeprecated      bool // 为 true 时排除 confidence=deprecated
}

// ProjectFactSparseRow is one fact's sparse-check input. It used to be an anonymous struct in the
// return type, which cannot be named in a consumer-side interface - internal/project.Store needs
// to mention this method, so the row type has to have a name.
type ProjectFactSparseRow struct {
	Category string
	FactKey  string
	Body     string
}

// ValidProjectFactEdgeTypes 项目事实图允许的边类型。
var ValidProjectFactEdgeTypes = map[string]struct{}{
	"depends_on":    {},
	"leads_to":      {},
	"enables":       {},
	"exploits":      {},
	"discovered_on": {},
	"contains":      {},
	"part_of":       {},
	"supports":      {},
}

// ProjectFactEdge 项目事实关系边（source → target）。
type ProjectFactEdge struct {
	ID                   string    `json:"id"`
	ProjectID            string    `json:"project_id"`
	SourceFactKey        string    `json:"source_fact_key"`
	TargetFactKey        string    `json:"target_fact_key"`
	EdgeType             string    `json:"edge_type"`
	Confidence           string    `json:"confidence"` // confirmed | tentative | deprecated
	SourceConversationID string    `json:"source_conversation_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// ProjectFactEdgeInput 写入边时的输入（出边：source → To）。
type ProjectFactEdgeInput struct {
	To         string `json:"to"`
	Type       string `json:"type"`
	Confidence string `json:"confidence,omitempty"`
}

// ProjectFactEdgeFromInput 写入入边时的输入（From → 当前事实）。
type ProjectFactEdgeFromInput struct {
	From       string `json:"from"`
	Type       string `json:"type"`
	Confidence string `json:"confidence,omitempty"`
}

// ValidateProjectFactEdgeType 校验边类型。
func ValidateProjectFactEdgeType(edgeType string) error {
	edgeType = strings.TrimSpace(strings.ToLower(edgeType))
	if edgeType == "" {
		return fmt.Errorf("edge type 不能为空")
	}
	if _, ok := ValidProjectFactEdgeTypes[edgeType]; !ok {
		return fmt.Errorf("无效的 edge type: %s", edgeType)
	}
	return nil
}

// ProjectFactGraphNode 图 API 节点。
type ProjectFactGraphNode struct {
	ID         string `json:"id"`
	FactKey    string `json:"fact_key"`
	Category   string `json:"category"`
	Label      string `json:"label"`   // 图节点短标签（截断）
	Summary    string `json:"summary"` // 完整摘要（侧栏等详情用）
	Confidence string `json:"confidence"`
	Type       string `json:"type"`
	Pinned     bool   `json:"pinned"`
}

// ProjectFactGraphEdge 图 API 边。
type ProjectFactGraphEdge struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Target     string `json:"target"`
	Type       string `json:"type"`
	Confidence string `json:"confidence"`
}

// ProjectFactGraph 项目事实图。
type ProjectFactGraph struct {
	Nodes []ProjectFactGraphNode `json:"nodes"`
	Edges []ProjectFactGraphEdge `json:"edges"`
}
