package project

import (
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/store"
)

// Store is the persistence surface this package needs: the project row plus the fact index and
// fact-edge ledger that the blackboard, stats and graph builders read and write.
//
// Every function here used to take the 361-method *database.DB, which is how "project" code ended
// able to touch any table - and it is why the HTTP layer could not narrow its own storage field:
// the handle escaped into these signatures.
//
// The ledger half's list lives in database.BlackboardLedger (store.Facts answers it). The project-row
// half is declared here, on the consumer side, now that both its statements and its row types moved
// into store.Projects.
type Projects interface {
	CreateProject(p *store.Project) (*store.Project, error)
	GetProject(id string) (*store.Project, error)
	GetProjectStatsCounts(projectID string) (*store.ProjectStats, error)
}

// Findings is the slice of the findings domain the resume preview reads: how many vulnerabilities a
// project carries and the most recent few, so a resumed turn starts from the current count instead of
// from whatever the last summary happened to say. Read-only on purpose - the blackboard never writes a
// finding, and a write would belong to the store that owns the table.
type Findings interface {
	CountAll(filter store.VulnerabilityListFilter) (int, error)
	ListAll(limit, offset int, filter store.VulnerabilityListFilter) ([]*store.Vulnerability, error)
}

// Conversations answers the one question the checkpoint path may not skip: which project the
// conversation is bound to right now. A summarization run carries the project ID it started with, and
// writing working memory under a stale binding would put it in a project the conversation has left.
type Conversations interface {
	GetConversationProjectID(conversationID string) (string, error)
}

type Store struct {
	Projects
	Findings
	Conversations
	database.BlackboardLedger
}

// NewStore pairs the providers a blackboard call needs: the project rows still belong to the
// connection wrapper, the fact and edge ledger belongs to store.Facts, and the resume preview reads
// the findings domain. Embedding rather than nesting is what keeps every call site in this package
// unchanged - db.GetProject and db.UpsertProjectFact resolve to whichever half answers them, and the
// compiler reports the overlap instead of guessing.
func NewStore(rows Projects, ledger database.BlackboardLedger, findings Findings, conversations Conversations) Store {
	return Store{Projects: rows, BlackboardLedger: ledger, Findings: findings, Conversations: conversations}
}

// Missing reports an unwired half. A store built with one nil side would answer the promoted methods
// of the other and quietly skip a write, so the callers that can check refuse early.
func (s Store) Missing() bool {
	return s.Projects == nil || s.BlackboardLedger == nil || s.Findings == nil || s.Conversations == nil
}
