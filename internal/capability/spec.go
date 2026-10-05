// Package capability is the single source of truth for what this product can do.
//
// One Spec drives identity, authorization, approval, the LLM tool schema, the
// frontend form and the OpenAPI fragment. A callable that has no Spec cannot be
// executed: the registry is fail-closed, so an unregistered tool is a denial
// rather than an implicit fallback.
package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Class is the blast radius of a capability. It is declared, never inferred from
// the tool name, because the store must never be able to widen what a human approved.
type Class string

const (
	// ClassReadonly observes state without changing it.
	ClassReadonly Class = "readonly"
	// ClassMutating changes local state or touches a target non-destructively.
	ClassMutating Class = "mutating"
	// ClassDestructive executes arbitrary code or is otherwise unrecoverable.
	ClassDestructive Class = "destructive"
)

func (c Class) valid() bool {
	switch c {
	case ClassReadonly, ClassMutating, ClassDestructive:
		return true
	}
	return false
}

// Runtime says where the code executes. Recipe and plugin runtimes are out of
// process or mediated; go-builtin is in-process and therefore trusted.
type Runtime string

const (
	RuntimeGoBuiltin  Runtime = "go-builtin"
	RuntimeRecipeExec Runtime = "recipe:exec"
	RuntimePluginHost Runtime = "plugin-host:python"
	// RuntimePluginAbi is a capability the plugin binary itself provides and the host discovers
	// over capabilities/list, as opposed to a recipe whose script the host runs. The ":python"
	// in RuntimePluginHost is historical: the ABI is language-neutral, so a discovered capability
	// must not be labelled with an interpreter nobody chose. Both share the "plugin-host:" prefix
	// that pluginhost.IsPluginRuntime keys on, so out-of-process routing needs no new branch -
	// and adding one would be the mistake, because the prefix check is what keeps untrusted code
	// out of this process.
	RuntimePluginAbi Runtime = "plugin-host:abi"
	RuntimeMCPRemote Runtime = "mcp:remote"
)

// Approval controls whether a call needs a human decision before it runs.
type Approval int

const (
	// ApprovalInherited defers to the session's HITL configuration.
	ApprovalInherited Approval = iota
	// ApprovalAlways demands a human decision even in a session that never
	// opted into HITL. Destructive capabilities use this.
	ApprovalAlways
	// ApprovalNever is only valid for readonly capabilities.
	ApprovalNever
)

// Scope is the authority a decision is evaluated under.
type Scope string

const (
	ScopeAll      Scope = "all"
	ScopeAssigned Scope = "assigned"
	ScopeOwn      Scope = "own"
)

// Principal is the authenticated caller. Declaring it here keeps the registry
// independent of the RBAC package, which would otherwise be a cycle risk.
type Principal interface {
	UserID() string
	HasPermission(permission string) bool
	ScopeFor(permission string) Scope
}

// Deps are the side effects a policy check may need. Everything a check consults
// is injected here so the evaluator has no hidden global reads.
type Deps interface {
	// CanAccessResource reports whether the principal may act on one resource.
	CanAccessResource(userID string, scope Scope, resourceType, id string) bool
	// CanAccessToolExecution reports ownership of a background execution.
	CanAccessToolExecution(userID string, scope Scope, executionID string) bool
	// ConversationID returns the conversation bound to the request, if any.
	ConversationID(ctx context.Context) string
	// ProjectFilter returns the effective project filter of the request. It
	// returns ProjectFilterUnbound when the conversation has no project, and an
	// empty string when no filter applies.
	ProjectFilter(ctx context.Context) string
	// ResourceProjectID resolves which project owns a resource. ok is false for
	// resource types that are not project-scoped.
	ResourceProjectID(resourceType, resourceID string) (projectID string, ok bool, err error)
	// ConversationProjectID resolves the project a conversation is bound to.
	ConversationProjectID(conversationID string) (string, error)
}

// CheckFunc is an imperative policy check for capabilities whose decision depends
// on argument values. Returning an error denies the call; a denial must never be
// represented by a nil return.
type CheckFunc func(ctx context.Context, p Principal, args map[string]any, d Deps) error

// CapabilityGrant is a mediated side effect the core will honour on behalf of a
// plugin or recipe, e.g. "net.connect(10.0.0.0/8:445)". Grants are a ceiling:
// they are the union of what the operator approved, and a request outside the
// set is refused at the execution site rather than escalated.
type CapabilityGrant struct {
	Name   string
	Target string
}

func ParseGrant(s string) (CapabilityGrant, error) {
	open := strings.Index(s, "(")
	if open < 0 || !strings.HasSuffix(s, ")") {
		return CapabilityGrant{}, fmt.Errorf("capability grant %q must look like name(target)", s)
	}
	name := strings.TrimSpace(s[:open])
	if name == "" {
		return CapabilityGrant{}, fmt.Errorf("capability grant %q has an empty name", s)
	}
	return CapabilityGrant{Name: name, Target: strings.TrimSpace(s[open+1 : len(s)-1])}, nil
}

func (g CapabilityGrant) String() string { return g.Name + "(" + g.Target + ")" }

// Params is the JSON Schema for the capability arguments. It drives the
// generated form, the server-side validation, the LLM tool schema and OpenAPI.
type Params = json.RawMessage

// Spec is the manifest of one callable capability.
type Spec struct {
	// ID is the canonical identity: publisher.capability.name, with core.*
	// reserved for the shipped binary. ID is what authorization rules, approval
	// records and revocation lists are keyed on.
	ID      string
	Version string

	// Name is the wire-compatible tool name (legacy MCP tool name).
	Name        string
	Title       string
	Description string

	Class   Class
	Runtime Runtime

	// Permission is required before any of Check runs. Empty means the
	// capability is available to every authenticated principal, which is only
	// defensible for readonly metadata.
	Permission string

	// Check carries argument-dependent rules that a permission string cannot express.
	Check CheckFunc

	// Approval pins whether a human must decide before execution.
	Approval Approval

	// Grants is the ceiling of mediated side effects this capability may request.
	Grants []CapabilityGrant

	// Evidence requires the output to enter the evidence chain.
	Evidence bool

	// Timeout bounds one execution. Zero means the platform default.
	Timeout time.Duration

	// ParamsSchema, when set, replaces any hand-written tool schema.
	ParamsSchema Params

	// Source distinguishes shipped code from submitted artifacts.
	Source string

	// Provenance ties an installed capability back to the signed artifact it came
	// from, so revocation can be enforced by digest before every call rather than
	// only at install time.
	Publisher      string
	ArtifactDigest string

	// Virtual marks a policy that is not reachable by that name through the MCP
	// server: either a namespace-level gate (external MCP invocation) or a tool the
	// agent runtime registers itself. Name-parity checks skip them; lookups do not.
	Virtual bool

	// Builtin marks names owned by the Go binary, so identity checks can tell a
	// missing registration apart from a recipe that was never declared.
	Builtin bool
}

// Validate rejects specs that cannot be enforced, before they reach the registry.
func (s *Spec) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("capability spec: ID is required")
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("capability %s: tool name is required", s.ID)
	}
	if !s.Class.valid() {
		return fmt.Errorf("capability %s: unknown class %q", s.ID, s.Class)
	}
	if s.Approval == ApprovalNever && s.Class != ClassReadonly {
		return fmt.Errorf("capability %s: only readonly capabilities may skip approval", s.ID)
	}
	if s.Approval == ApprovalAlways && s.Class == ClassReadonly {
		return fmt.Errorf("capability %s: readonly capabilities cannot force approval", s.ID)
	}
	if s.Runtime == RuntimeRecipeExec && len(s.Grants) == 0 {
		return fmt.Errorf("capability %s: exec recipes must declare at least one capability grant", s.ID)
	}
	for _, g := range s.Grants {
		if strings.TrimSpace(g.Name) == "" {
			return fmt.Errorf("capability %s: malformed grant", s.ID)
		}
	}
	return nil
}

// RequiresHumanDecision reports whether execution must pause for approval.
func (s *Spec) RequiresHumanDecision() bool { return s.Approval == ApprovalAlways }

// Destructive reports whether the capability can run arbitrary code.
func (s *Spec) Destructive() bool { return s.Class == ClassDestructive }

// SetProvenance stamps the signed artifact an installed capability came from. The
// installer writes it from the verified manifest, never from publisher-supplied
// metadata, so a submission cannot choose which revocation list entry matches it.
func (s *Spec) SetProvenance(publisher, digest string) {
	if s == nil {
		return
	}
	s.Publisher = strings.TrimSpace(publisher)
	s.ArtifactDigest = strings.ToLower(strings.TrimSpace(digest))
}
