package store

import "strings"

// Read scopes for a listing. `all` is above per-resource visibility; anything
// else is constrained by what belongs to or is assigned to the caller. `own` and
// `assigned` share one clause here because the difference between them is decided
// by which permission was requested, not by this query.
const (
	ScopeAll      = "all"
	ScopeOwn      = "own"
	ScopeAssigned = "assigned"
)

// Access is what a query needs to know about the caller: who they are and how far that reaches.
// It is now the only declaration of that pair. The data layer carried its own copy under another
// name - same two fields, same three scope strings, declared twice - which meant a rename or a
// widened default on one side compiled cleanly and changed reachability on the other.
type Access struct {
	UserID string
	Scope  string
}

// SeeAll reports whether the caller is unrestricted.
func (a Access) SeeAll() bool { return a.Scope == ScopeAll }

// ConstrainConversation limits a query's `column` (a conversation id in the
// queried table) to conversations the caller can reach: ones they own, ones
// assigned to them, and ones whose project they own or are assigned to.
//
// An empty user id yields `1=0`, not an unconstrained query: callers reach this
// where a session is expected, so a missing id means "no session", never
// "superuser".
func ConstrainConversation(query string, args []any, column string, access Access) (string, []any) {
	if access.SeeAll() {
		return query, args
	}
	userID := strings.TrimSpace(access.UserID)
	if userID == "" {
		return query + " AND 1=0", args
	}
	return query + ` AND ` + column + ` IS NOT NULL AND ` + column + ` <> '' AND (
		EXISTS (SELECT 1 FROM conversations c WHERE c.id = ` + column + ` AND c.owner_user_id = ?)
		OR EXISTS (
			SELECT 1 FROM rbac_resource_assignments ra
			WHERE ra.user_id = ? AND ra.resource_type = 'conversation' AND ra.resource_id = ` + column + `
		)
		OR EXISTS (
			SELECT 1 FROM conversations c
			JOIN projects p ON p.id = c.project_id
			WHERE c.id = ` + column + ` AND p.owner_user_id = ?
		)
		OR EXISTS (
			SELECT 1 FROM conversations c
			JOIN rbac_resource_assignments pra ON pra.resource_id = c.project_id
			WHERE c.id = ` + column + ` AND pra.user_id = ? AND pra.resource_type = 'project'
		)
	)`, append(args, userID, userID, userID, userID)
}
