package capability

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// NamespaceCore is reserved for capabilities shipped in the binary. Submitted
// artifacts must use a publisher namespace they own.
const NamespaceCore = "core"

// ParseID splits a canonical publisher.capability.name identity.
func ParseID(id string) (publisher, name string, err error) {
	parts := strings.Split(strings.TrimSpace(id), ".")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("capability id %q must be publisher.name", id)
	}
	for _, p := range parts {
		if p == "" {
			return "", "", fmt.Errorf("capability id %q has an empty segment", id)
		}
		if strings.ToLower(p) != p {
			return "", "", fmt.Errorf("capability id %q must be lowercase", id)
		}
		if !isIDChars(p) {
			return "", "", fmt.Errorf("capability id %q has an illegal character", id)
		}
	}
	return parts[0], strings.Join(parts[1:], "."), nil
}

func isIDChars(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// CoreID builds the reserved identity for a shipped tool name.
func CoreID(toolName string) string {
	return NamespaceCore + "." + strings.ReplaceAll(toolName, "::", ".")
}

// Registry holds every callable capability. Registration is idempotent and
// layered: a reload of the recipe layer replaces only the identities that layer
// claims, so an unrelated "apply config" cannot drop a built-in policy. Entries
// claimed by more than one layer (a recipe bound to an in-process handler)
// survive removal of either single layer.
type Registry struct {
	mu      sync.RWMutex
	byID    map[string]*Spec
	byName  map[string]*Spec
	layers  map[string]map[string]bool // layer -> claimed identities
	subsets map[string]map[string]map[string]bool
	// layer -> owner -> claimed identities. Owners are things like one external MCP server,
	// so a refresh can replace exactly what it produced.
}

// Layer identifiers.
const (
	LayerBuiltin = "builtin"
	LayerRecipe  = "recipe"
	LayerRemote  = "remote"
	// LayerPlugin holds the capabilities a capability pack's plugin binary provides, one subset per
	// unit. They are their own layer because their authority chain is different: a recipe is
	// content the operator wrote, a remote tool is inventory a server reported, and a plugin
	// capability is code that arrived in a pack and was verified against a reviewed list at the
	// moment the operator switched it on. Unplugging the pack must be able to erase exactly those.
	LayerPlugin = "plugin"
)

func NewRegistry() *Registry {
	return &Registry{
		byID:    map[string]*Spec{},
		byName:  map[string]*Spec{},
		layers:  map[string]map[string]bool{},
		subsets: map[string]map[string]map[string]bool{},
	}
}

// DuplicateError describes a conflicting registration.
type DuplicateError struct {
	ID       string
	Existing string
	Incoming string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("capability %s already registered by %s, refusing %s", e.ID, e.Existing, e.Incoming)
}

// Register adds or replaces one spec inside a layer.
func (r *Registry) Register(layer string, s *Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if prev, ok := r.byID[s.ID]; ok && (layer == LayerBuiltin || prev.Builtin) {
		if prev.Name != s.Name {
			return &DuplicateError{ID: s.ID, Existing: prev.Name, Incoming: s.Name}
		}
	}
	if prev, ok := r.byName[s.Name]; ok && prev.ID != s.ID {
		return &DuplicateError{ID: s.Name, Existing: prev.ID, Incoming: s.ID}
	}

	r.byID[s.ID] = s
	r.byName[s.Name] = s
	if r.layers[layer] == nil {
		r.layers[layer] = map[string]bool{}
	}
	r.layers[layer][s.ID] = true
	return nil
}

// RegisterAll registers a batch and rolls the layer back if any entry is invalid,
// so a half-applied community artifact set can never go live.
func (r *Registry) RegisterAll(layer string, specs []*Spec) error {
	r.mu.Lock()
	previous := map[string]*Spec{}
	for id := range r.layers[layer] {
		if spec, ok := r.byID[id]; ok {
			previous[id] = spec
		}
	}
	r.mu.Unlock()

	// Drop the layer first so re-registration is a replacement, not an append.
	r.removeLayer(layer)

	for _, spec := range specs {
		if err := r.Register(layer, spec); err != nil {
			// Roll the layer back to the snapshot rather than leaving a partial
			// policy set live: half-registered community artifacts are worse than
			// none, because the refused tools would fail closed at execution.
			r.removeLayer(layer)
			for _, old := range previous {
				_ = r.Register(layer, old)
			}
			return err
		}
	}
	return nil
}

func (r *Registry) removeLayer(layer string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	claimed := r.layers[layer]
	delete(r.layers, layer)
	delete(r.subsets, layer)
	for id := range claimed {
		stillClaimed := false
		for other := range r.layers {
			if r.layers[other][id] {
				stillClaimed = true
				break
			}
		}
		if stillClaimed {
			continue
		}
		if spec, ok := r.byID[id]; ok {
			delete(r.byID, id)
			delete(r.byName, spec.Name)
		}
	}
}

// Lookup resolves a callable by tool name. A miss is a denial, not a fallback.
func (r *Registry) Lookup(toolName string) (*Spec, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s, ok := r.byName[toolName]; ok {
		return s, nil
	}
	return nil, &NotRegisteredError{ToolName: toolName}
}

// LookupByID resolves a canonical identity.
func (r *Registry) LookupByID(id string) (*Spec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byID[id]
	return s, ok
}

// NotRegisteredError is the fail-closed outcome.
type NotRegisteredError struct{ ToolName string }

func (e *NotRegisteredError) Error() string {
	return fmt.Sprintf("no capability policy registered for %q", e.ToolName)
}

// Names returns every callable name, sorted, for generated tables.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// BuiltinNames returns the names owned by the Go binary.
func (r *Registry) BuiltinNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byName))
	for n, s := range r.byName {
		if s.Builtin {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Specs returns every spec sorted by identity, for auditing and code generation.
func (r *Registry) Specs() []*Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Spec, 0, len(r.byID))
	for _, s := range r.byID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CheckTyposquat reports whether a proposed identity is one edit away from an
// existing one, which is how a community publisher can shadow a popular name.
func (r *Registry) CheckTyposquat(id string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var hits []string
	for existing := range r.byID {
		if existing == id {
			continue
		}
		if editDistanceBelow(id, existing, 2) {
			hits = append(hits, existing)
		}
	}
	sort.Strings(hits)
	return hits
}

func editDistanceBelow(a, b string, max int) bool {
	d := len(a) - len(b)
	if d < 0 {
		d = -d
	}
	if d > max {
		return false
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)] <= max
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

var (
	globalOnce     sync.Once
	globalRegistry *Registry
	globalErr      error
)

// Global returns the process-wide registry, seeded with the built-in policy
// table. Every entry point installs recipe layers into this same registry, so
// two assembly paths cannot drift apart.
func Global() *Registry {
	globalOnce.Do(func() {
		globalRegistry = NewRegistry()
		builtins := BuiltinSpecs()
		for _, spec := range builtins {
			spec.Builtin = true
		}
		globalErr = globalRegistry.RegisterAll(LayerBuiltin, builtins)
	})
	return globalRegistry
}

// GlobalErr reports whether the built-in table failed to install.
func GlobalErr() error { Global(); return globalErr }

// IsBuiltin asks the registry instead of a hand-maintained switch.
func IsBuiltin(toolName string) bool {
	spec, err := Global().Lookup(toolName)
	return err == nil && spec != nil && spec.Builtin
}

// BuiltinNames returns every name owned by the binary, sorted.
func BuiltinNames() []string { return Global().BuiltinNames() }

// RegisterSubset replaces the entries one named owner contributed inside a layer, leaving every
// other owner's entries alone.
//
// LayerRemote needs this: the layer holds the tools of every configured external MCP server, and
// reconnecting one server must not drop the others'. The all-or-nothing rule is inherited from
// RegisterAll for the same reason - a half-applied set of remote tools would leave some of a
// server's tools unauthorized while the operator believes the refresh succeeded.
func (r *Registry) RegisterSubset(layer, owner string, specs []*Spec) error {
	r.mu.Lock()
	previous := make([]*Spec, 0, len(r.subsets[layer][owner]))
	for id := range r.subsets[layer][owner] {
		if spec, ok := r.byID[id]; ok {
			previous = append(previous, spec)
		}
	}
	r.mu.Unlock()

	r.UnregisterSubset(layer, owner)
	for _, spec := range specs {
		if err := r.Register(layer, spec); err != nil {
			r.UnregisterSubset(layer, owner)
			for _, old := range previous {
				_ = r.Register(layer, old)
			}
			return err
		}
		r.claimSubset(layer, owner, spec.ID)
	}
	return nil
}

// claimSubset records that `owner` contributed `id` inside `layer`.
func (r *Registry) claimSubset(layer, owner, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.subsets[layer] == nil {
		r.subsets[layer] = map[string]map[string]bool{}
	}
	if r.subsets[layer][owner] == nil {
		r.subsets[layer][owner] = map[string]bool{}
	}
	r.subsets[layer][owner][id] = true
}

// UnregisterSubset drops everything one owner registered inside a layer and reports how many
// identities left the table.
func (r *Registry) UnregisterSubset(layer, owner string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	claimed := r.subsets[layer][owner]
	if len(claimed) == 0 {
		return 0
	}
	delete(r.subsets[layer], owner)
	if len(r.subsets[layer]) == 0 {
		delete(r.subsets, layer)
	}

	removed := 0
	for id := range claimed {
		if !r.claimedElsewhereLocked(layer, id) {
			if spec, ok := r.byID[id]; ok {
				delete(r.byID, id)
				delete(r.byName, spec.Name)
				removed++
			}
		}
		for other := range r.layers {
			delete(r.layers[other], id)
		}
	}
	return removed
}

// claimedElsewhereLocked reports whether some layer or owner other than `layer` still wants id.
func (r *Registry) claimedElsewhereLocked(layer, id string) bool {
	for name, claimed := range r.layers {
		if name != layer && claimed[id] {
			return true
		}
	}
	for l, owners := range r.subsets {
		if l == layer {
			continue
		}
		for owner, ids := range owners {
			for other := range ids {
				if other == id && owner != "" {
					return true
				}
			}
		}
	}
	return false
}

// Remove drops one identity from the registry, used when a revocation lands.
func (r *Registry) Remove(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	spec, ok := r.byID[id]
	if !ok {
		return false
	}
	delete(r.byID, id)
	delete(r.byName, spec.Name)
	for layer, claimed := range r.layers {
		delete(claimed, id)
		if len(claimed) == 0 {
			delete(r.layers, layer)
		}
	}
	return true
}

// IsBuiltinName reports whether a tool name belongs to the shipped binary, so a
// revocation sweep can tell publisher content from product code.
func IsBuiltinName(toolName string) bool {
	spec, err := Global().Lookup(toolName)
	return err == nil && spec != nil && spec.Builtin
}
