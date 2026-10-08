package handler

import (
	"strings"

	"cyberstrike-ai/internal/plugin"
)

// capabilityTrust is what the console asks about the client-enforced block list. The capability
// table and the revocation list live in internal/app (the assembly point), so they reach the
// handler through this interface rather than an import that would run the wrong way.
//
// The per-unit publisher and artifact digest do *not* come through here: a plugin unit's own
// declaration already carries both (they are exactly the values its registration would stamp),
// and reading them from the same file the enable switch reads keeps one source of truth.
type capabilityTrust interface {
	Revocations() RevocationView
}

// RevocationView is the read-only picture of the block list: where it was loaded from and what it
// currently holds. Loaded distinguishes "no list installed" from "a list with zero entries",
// which are different answers to "why was this build callable".
type RevocationView struct {
	Source     string   `json:"source,omitempty"`
	Loaded     bool     `json:"loaded"`
	Digests    []string `json:"digests,omitempty"`
	Publishers []string `json:"publishers,omitempty"`
}

// revocationView is the nil-safe accessor: an unwired provider degrades the panel to
// "not loaded" instead of failing the whole state read.
func (h *PluginHandler) revocationView() RevocationView {
	if h == nil || h.trust == nil {
		return RevocationView{Loaded: false}
	}
	return h.trust.Revocations()
}

// revokedMatch reports whether a publisher or artifact digest is on the block list. The match
// rule mirrors artifact.Revocations.CheckProvenance - the same two fields, checked the same way -
// so a row marked 已撤销 here is a call the execution path would refuse.
func revokedMatch(view RevocationView, publisher, digest string) bool {
	if !view.Loaded {
		return false
	}
	publisher = strings.TrimSpace(publisher)
	digest = strings.TrimSpace(digest)
	if digest != "" && containsString(view.Digests, digest) {
		return true
	}
	if publisher != "" && containsString(view.Publishers, publisher) {
		return true
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// markPluginProvenance fills a plugin unit's row with where its code came from, and whether the
// block list currently refuses it. A declaration that cannot be read leaves the row empty: the
// unit's served reason and the drift list already carry that failure, and inventing provenance
// for a file nobody could read would be worse than showing none.
func (h *PluginHandler) markPluginProvenance(u plugin.Unit, v *unitView) {
	if u.Kind != plugin.KindPlugin || h.trust == nil {
		return
	}
	decl, err := h.loadPluginDeclaration(u)
	if err != nil {
		return
	}
	v.Publisher = decl.Publisher
	v.ArtifactDigest = decl.BinaryDigest
	v.Revoked = revokedMatch(h.revocationView(), decl.Publisher, decl.BinaryDigest)
}
