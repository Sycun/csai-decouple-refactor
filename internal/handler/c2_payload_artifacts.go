package handler

import (
	"strings"

	"cyberstrike-ai/internal/c2"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"
)

// c2PayloadArtifacts reaches the ownership table through the manager's connection. The store owns the
// table; whether a caller may reach a listener stays with RBAC, which is why the gate below asks two
// questions instead of one.
func c2PayloadArtifacts(mgr *c2.Manager) *store.C2PayloadArtifacts {
	return store.NewC2PayloadArtifacts(mgr.DB().DB)
}

// userMayFetchPayloadArtifact is the download gate: an unrestricted scope reaches any recorded
// payload, otherwise the caller must own the artifact or be able to reach the listener it was built
// for. An artifact with no ownership record is refused - the file may exist on disk and still belong
// to nobody, and that is how a payload built before this table existed behaves. The `!found` guard is
// not observable from a test: an absent record leaves the owner and the listener empty, and both
// later checks deny an empty id anyway. It states the rule; no test proves it is necessary.
func userMayFetchPayloadArtifact(mgr *c2.Manager, session security.Session, filename string) bool {
	if session.Scope == database.RBACScopeAll {
		return true
	}
	artifact, found, err := c2PayloadArtifacts(mgr).Lookup(filename)
	if err != nil || !found {
		return false
	}
	if artifact.OwnerUserID == strings.TrimSpace(session.UserID) {
		return true
	}
	return mgr.DB().UserCanAccessResource(session.UserID, session.Scope, "c2_listener", artifact.ListenerID)
}
