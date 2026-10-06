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

type Store struct {
	Projects
	database.BlackboardLedger
}

// NewStore pairs the two providers a blackboard call needs: the project rows still belong to the
// connection wrapper, the fact and edge ledger belongs to store.Facts. Embedding rather than nesting
// is what keeps every call site in this package unchanged - db.GetProject and db.UpsertProjectFact
// resolve to whichever half answers them, and the compiler reports the overlap instead of guessing.
func NewStore(rows Projects, ledger database.BlackboardLedger) Store {
	return Store{Projects: rows, BlackboardLedger: ledger}
}

// Missing reports an unwired half. A store built with one nil side would answer the promoted methods
// of the other and quietly skip a write, so the callers that can check refuse early.
func (s Store) Missing() bool {
	return s.Projects == nil || s.BlackboardLedger == nil
}
