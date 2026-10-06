package database

import (
	"cyberstrike-ai/internal/store"
)

// The project blackboard's SQL lives in store.Facts: both tables, their schema, and the unlink the
// findings domain owes them. What stays here is the delegation the consumers still call, because
// project.Store is threaded through the run loop, the workflow engine and the MCP tools as a
// connection-shaped handle; narrowing those signatures is the next slice, not this one.
//
// Every method below is one line and adds nothing: no default, no logging, no retry. The store is
// reached through NewFacts so the handle it writes through is the same *sql.DB this wrapper owns.
func NewFacts(db *DB) *store.Facts { return store.NewFacts(db.DB) }

func (db *DB) ListProjectFactsForIndex(projectID string, includeDeprecated bool) ([]*store.ProjectFact, error) {
	return NewFacts(db).ListForIndex(projectID, includeDeprecated)
}

func (db *DB) ListProjectFacts(projectID string, filter store.ProjectFactListFilter, limit, offset int) ([]*store.ProjectFact, error) {
	return NewFacts(db).List(projectID, filter, limit, offset)
}

func (db *DB) ListProjectFactsForSparseCheck(projectID string) ([]store.ProjectFactSparseRow, error) {
	return NewFacts(db).ListForSparseCheck(projectID)
}

func (db *DB) GetProjectFactByKey(projectID, factKey string) (*store.ProjectFact, error) {
	return NewFacts(db).GetByKey(projectID, factKey)
}

func (db *DB) GetProjectFact(id string) (*store.ProjectFact, error) {
	return NewFacts(db).Get(id)
}

func (db *DB) UpsertProjectFact(f *store.ProjectFact) (*store.ProjectFact, error) {
	return NewFacts(db).Upsert(f)
}

func (db *DB) DeprecateProjectFact(projectID, factKey string) error {
	return NewFacts(db).Deprecate(projectID, factKey)
}

func (db *DB) RestoreProjectFact(projectID, factKey, confidence string) error {
	return NewFacts(db).Restore(projectID, factKey, confidence)
}

func (db *DB) DeleteProjectFact(id string) error {
	return NewFacts(db).Delete(id)
}

func (db *DB) ListProjectFactEdgesByProject(projectID string) ([]*store.ProjectFactEdge, error) {
	return NewFacts(db).ListEdges(projectID)
}

func (db *DB) ListOutgoingProjectFactEdges(projectID, sourceFactKey string) ([]*store.ProjectFactEdge, error) {
	return NewFacts(db).ListOutgoing(projectID, sourceFactKey)
}

func (db *DB) ListIncomingProjectFactEdges(projectID, targetFactKey string) ([]*store.ProjectFactEdge, error) {
	return NewFacts(db).ListIncoming(projectID, targetFactKey)
}

func (db *DB) ReplaceOutgoingProjectFactEdges(projectID, sourceFactKey, sourceConversationID string, inputs []store.ProjectFactEdgeInput) error {
	return NewFacts(db).ReplaceOutgoing(projectID, sourceFactKey, sourceConversationID, inputs)
}

func (db *DB) ReplaceIncomingProjectFactEdges(projectID, targetFactKey string, inputs []store.ProjectFactEdgeFromInput) error {
	return NewFacts(db).ReplaceIncoming(projectID, targetFactKey, inputs)
}

func (db *DB) GetProjectFactEdge(edgeID string) (*store.ProjectFactEdge, error) {
	return NewFacts(db).GetEdge(edgeID)
}

func (db *DB) AddProjectFactEdge(projectID string, in store.ProjectFactEdgeInput, sourceFactKey, sourceConversationID string) (*store.ProjectFactEdge, error) {
	return NewFacts(db).AddEdge(projectID, in, sourceFactKey, sourceConversationID)
}

func (db *DB) DeleteProjectFactEdge(edgeID string) error {
	return NewFacts(db).DeleteEdge(edgeID)
}

func (db *DB) RenameProjectFactKeyEdges(projectID, oldKey, newKey string) error {
	return NewFacts(db).RenameKeyEdges(projectID, oldKey, newKey)
}
