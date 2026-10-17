package capability

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakePrincipal struct {
	userID      string
	permissions map[string]bool
	scope       Scope
}

func (f fakePrincipal) UserID() string { return f.userID }

func (f fakePrincipal) HasPermission(permission string) bool { return f.permissions[permission] }

func (f fakePrincipal) ScopeFor(string) Scope { return f.scope }

type fakeDeps struct {
	conversation string
	filter       string
	resources    map[string]bool
	executions   map[string]bool
	projects     map[string]string
	retests      map[string]RetestSnapshot
	err          error
}

func (d fakeDeps) CanAccessResource(_ string, _ Scope, resourceType, id string) bool {
	return d.resources[resourceType+"/"+id]
}

func (d fakeDeps) CanAccessToolExecution(_ string, _ Scope, id string) bool { return d.executions[id] }

func (d fakeDeps) ConversationID(context.Context) string { return d.conversation }

func (d fakeDeps) ProjectFilter(context.Context) string { return d.filter }

func (d fakeDeps) ResourceProjectID(resourceType, id string) (string, bool, error) {
	project, ok := d.projects[resourceType+"/"+id]
	return project, ok, d.err
}

func (d fakeDeps) RetestSnapshotOf(conversationID string) (RetestSnapshot, bool, error) {
	if d.err != nil {
		return RetestSnapshot{}, false, d.err
	}
	snapshot, ok := d.retests[conversationID]
	return snapshot, ok, nil
}

func (d fakeDeps) FindingProjectID(id string) (string, bool, error) {
	if d.err != nil {
		return "", false, d.err
	}
	project, ok := d.projects["vulnerability/"+id]
	return project, ok, nil
}

func (d fakeDeps) ConversationProjectID(string) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	return d.projects["conversation/"+d.conversation], nil
}

func spec(t *testing.T, layer string, manifest RecipeManifest) *Spec {
	t.Helper()
	created, err := SpecFromRecipe(manifest)
	if err != nil {
		t.Fatalf("build spec: %v", err)
	}
	return created
}

func TestRegistryLayeringReplacesOnlyItsOwnLayer(t *testing.T) {
	registry := NewRegistry()
	builtins := BuiltinSpecs()
	for _, s := range builtins {
		s.Builtin = true
	}
	if err := registry.RegisterAll(LayerBuiltin, builtins); err != nil {
		t.Fatal(err)
	}

	recipe, err := SpecFromRecipe(RecipeManifest{
		ID: "acme.scan", ToolName: "scan", Class: "mutating",
		Permission: "agent:local-execute", Grants: []string{"net.connect(target)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterAll(LayerRecipe, []*Spec{recipe}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lookup("scan"); err != nil {
		t.Fatal("recipe did not register")
	}

	// Replacing the recipe layer must leave every built-in policy in place, and a
	// shared identity (an internal: recipe bound to shipped code) must survive the
	// removal of either single layer.
	if err := registry.RegisterAll(LayerRecipe, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lookup("scan"); err == nil {
		t.Fatal("replaced recipe entry survived")
	}
	for _, name := range []string{CoreID("c2_task"), "core.query_execution_result"} {
		if _, ok := registry.LookupByID(name); !ok {
			t.Errorf("built-in identity %s was evicted by a recipe-layer reload", name)
		}
	}
}

func TestRegistryRefusesConflictingIdentities(t *testing.T) {
	registry := NewRegistry()
	first := spec(t, LayerRecipe, RecipeManifest{
		ID: "acme.one", ToolName: "one", Class: "readonly", Permission: "agent:execute",
		Approval: "never", Grants: []string{"net.connect(x)"},
	})
	if err := registry.Register(LayerRecipe, first); err != nil {
		t.Fatal(err)
	}
	second := spec(t, LayerRecipe, RecipeManifest{
		ID: "acme.two", ToolName: "one", Class: "readonly", Permission: "agent:execute",
		Approval: "never", Grants: []string{"net.connect(x)"},
	})
	conflict := registry.Register(LayerRecipe, second)
	if conflict == nil {
		t.Fatal("two identities claimed the same callable name")
	}
	var duplicate *DuplicateError
	if !errors.As(conflict, &duplicate) {
		t.Fatalf("expected a DuplicateError, got %T", conflict)
	}
}

func TestSpecValidationRules(t *testing.T) {
	cases := []struct {
		name     string
		manifest RecipeManifest
		why      string
	}{
		{"destructive on the coarse fallback", RecipeManifest{
			ID: "a.b", ToolName: "b", Class: "destructive", Permission: CoarseFallbackPermission,
			Grants: []string{"process.exec(sh)"},
		}, "fallback"},
		{"readonly forcing approval", RecipeManifest{
			ID: "a.c", ToolName: "c", Class: "readonly", Permission: "agent:execute", Approval: "always",
			Grants: []string{"net.connect(x)"},
		}, "approval"},
		{"exec recipe without grants", RecipeManifest{
			ID: "a.d", ToolName: "d", Class: "mutating", Permission: "agent:local-execute",
		}, "grants"},
		{"unknown class", RecipeManifest{
			ID: "a.e", ToolName: "e", Class: "apocalyptic", Permission: "agent:local-execute",
			Grants: []string{"process.exec(x)"},
		}, "class"},
		{"malformed identity", RecipeManifest{
			ID: "A.E", ToolName: "f", Class: "mutating", Permission: "agent:local-execute",
			Grants: []string{"process.exec(x)"},
		}, "identity"},
		{"publisher-derived identity without publisher", RecipeManifest{
			ToolName: "g", Class: "mutating", Permission: "agent:local-execute",
			Grants: []string{"process.exec(x)"},
		}, "publisher"},
	}
	for _, tc := range cases {
		if _, err := SpecFromRecipe(tc.manifest); err == nil {
			t.Errorf("%s: accepted (%s)", tc.name, tc.why)
		}
	}

	// A destructive manifest that omits approval still lands on the floor.
	implicit, err := SpecFromRecipe(RecipeManifest{
		ID: "acme.bad", ToolName: "bad", Class: "destructive", Permission: "agent:destructive-execute",
		Grants: []string{"process.exec(python3)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !implicit.RequiresHumanDecision() {
		t.Fatal("a destructive capability without an explicit approval field escaped the floor")
	}
}

func TestEvaluatorOrderAndFailClosedStages(t *testing.T) {
	registry := NewRegistry()
	allowed := spec(t, LayerRecipe, RecipeManifest{
		ID: "acme.ok", ToolName: "ok", Class: "mutating", Permission: "agent:local-execute",
		Grants: []string{"net.connect(target)"},
	})
	denied := spec(t, LayerRecipe, RecipeManifest{
		ID: "acme.blocked", ToolName: "blocked", Class: "mutating", Permission: "agent:local-execute",
		Grants: []string{"net.connect(target)"},
	})
	if err := registry.RegisterAll(LayerRecipe, []*Spec{allowed, denied}); err != nil {
		t.Fatal(err)
	}

	deps := fakeDeps{}
	evaluator := NewEvaluator(registry, WithDeps(deps))
	principal := fakePrincipal{userID: "u1", permissions: map[string]bool{"agent:local-execute": true}, scope: ScopeAll}
	ctx := WithPrincipal(context.Background(), principal)

	if decision := evaluator.Decide(ctx, "ok", nil); decision.Outcome != OutcomeAllow {
		t.Fatalf("expected allow, got %+v", decision)
	}
	if decision := evaluator.Decide(ctx, "unknown_tool", nil); decision.Outcome != OutcomeDeny {
		t.Fatalf("unregistered tool was not denied: %+v", decision)
	}
	if decision := evaluator.Decide(context.Background(), "ok", nil); decision.Outcome != OutcomeDeny {
		t.Fatal("a call without a principal was authorized")
	} else if !strings.Contains(decision.Reason, "principal") {
		t.Fatalf("wrong reason: %s", decision.Reason)
	}

	// A custom stage that blocks must name itself in the decision, so an audit can
	// tell which control stopped the call.
	withStage := NewEvaluator(registry,
		WithDeps(deps),
		WithStage("toolguard", func(context.Context, *Spec, Principal, map[string]any, Deps) (string, bool) {
			return "blocked by call guard", true
		}),
	)
	decision := withStage.Decide(ctx, "ok", nil)
	if decision.Outcome != OutcomeDeny || decision.Stage != "toolguard" {
		t.Fatalf("stage denial not attributed: %+v", decision)
	}

	// A missing permission denies before any stage runs, and the reason names it.
	withoutPermission := NewEvaluator(registry, WithDeps(deps))
	limited := WithPrincipal(context.Background(), fakePrincipal{userID: "u2", permissions: map[string]bool{"agent:execute": true}})
	decision = withoutPermission.Decide(limited, "ok", nil)
	if decision.Outcome != OutcomeDeny || !strings.Contains(decision.Reason, "agent:local-execute") {
		t.Fatalf("permission denial not surfaced: %+v", decision)
	}
}

func TestEvaluatorRecordsDenialsAndPanicsFailClosed(t *testing.T) {
	registry := NewRegistry()
	crashing := &Spec{
		ID: "acme.crash", Name: "crash", Class: ClassMutating, Permission: "agent:local-execute",
		Approval: ApprovalInherited, Builtin: true,
		Check: func(context.Context, Principal, map[string]any, Deps) error {
			panic("policy bug")
		},
	}
	if err := registry.Register(LayerBuiltin, crashing); err != nil {
		t.Fatal(err)
	}

	var recorded []Decision
	evaluator := NewEvaluator(registry,
		WithDeps(fakeDeps{}),
		WithAudit(func(d Decision) { recorded = append(recorded, d) }, func(d Decision) {}),
	)
	ctx := WithPrincipal(context.Background(), fakePrincipal{userID: "u", permissions: map[string]bool{"agent:local-execute": true}})

	decision := evaluator.Decide(ctx, "crash", nil)
	if decision.Outcome != OutcomeDeny {
		t.Fatalf("a panicking check must deny, got %s", decision.Outcome)
	}
	if !strings.Contains(decision.Reason, "panic") {
		t.Fatalf("panic not described in the record: %s", decision.Reason)
	}
	if len(recorded) != 1 {
		t.Fatalf("denials must be recorded, got %d entries", len(recorded))
	}
}

func TestResourceAndBoundaryChecks(t *testing.T) {
	deps := fakeDeps{
		resources: map[string]bool{"asset/a1": true, "asset/hidden": false, "project/p1": true},
		projects:  map[string]string{"asset/a1": "p1", "conversation/c1": "p1"},
	}
	args := map[string]any{"id": "a1"}

	if err := ResourceBoundary("asset:read", "asset", "id")(context.Background(),
		fakePrincipal{permissions: map[string]bool{"asset:read": true}, scope: ScopeAll}, args, deps); err != nil {
		t.Fatalf("reachable resource denied: %v", err)
	}
	if err := ResourceBoundary("asset:read", "asset", "id")(context.Background(),
		fakePrincipal{permissions: map[string]bool{"asset:read": true}, scope: ScopeAll},
		map[string]any{"id": "hidden"}, deps); err == nil {
		t.Fatal("unreachable resource allowed")
	}

	// Conversation project filter: a resource from another project is refused.
	filtered := fakeDeps{
		resources: map[string]bool{"asset/a1": true},
		projects:  map[string]string{"asset/a1": "other"},
		filter:    "p1",
	}
	err := ResourceBoundary("asset:write", "asset", "id")(context.Background(),
		fakePrincipal{permissions: map[string]bool{"asset:write": true}, scope: ScopeAll}, args, filtered)
	if err == nil || !strings.Contains(err.Error(), "belongs to project other") {
		t.Fatalf("cross-project resource allowed: %v", err)
	}

	// An unbound conversation may not touch a bound resource.
	unbound := fakeDeps{
		resources: map[string]bool{"asset/a1": true},
		projects:  map[string]string{"asset/a1": "p1"},
		filter:    ProjectFilterUnbound,
	}
	if err := ResourceBoundary("asset:write", "asset", "id")(context.Background(),
		fakePrincipal{permissions: map[string]bool{"asset:write": true}, scope: ScopeAll}, args, unbound); err == nil {
		t.Fatal("a bound resource was reachable from an unbound conversation")
	}

	// Project-bound tools need a conversation with a project the user can reach.
	bound := fakeDeps{
		conversation: "c1",
		resources:    map[string]bool{"conversation/c1": true, "project/p1": true},
		projects:     map[string]string{"conversation/c1": "p1"},
	}
	if err := ProjectBound("project:write")(context.Background(),
		fakePrincipal{permissions: map[string]bool{"project:write": true}, scope: ScopeAll}, nil, bound); err != nil {
		t.Fatalf("bound project tool refused: %v", err)
	}
	noProject := fakeDeps{conversation: "c2", resources: map[string]bool{"conversation/c2": true}}
	if err := ProjectBound("project:write")(context.Background(),
		fakePrincipal{permissions: map[string]bool{"project:write": true}, scope: ScopeAll}, nil, noProject); err == nil {
		t.Fatal("project tool worked without a bound project")
	}
}

func TestGrantCeilingAndMediationRefusal(t *testing.T) {
	registry := NewRegistry()
	limited := spec(t, LayerRecipe, RecipeManifest{
		ID: "acme.limited", ToolName: "limited", Class: "mutating", Permission: "agent:local-execute",
		Grants: []string{"net.connect(10.0.0.1:445)"},
	})
	if err := registry.Register(LayerRecipe, limited); err != nil {
		t.Fatal(err)
	}
	evaluator := NewEvaluator(registry)

	if !evaluator.GrantAllowed("limited", "net.connect(10.0.0.1:445)") {
		t.Error("an exactly declared grant was refused")
	}
	if evaluator.GrantAllowed("limited", "net.connect(10.0.0.2:445)") {
		t.Error("mediation allowed a target outside the declared ceiling")
	}
	if evaluator.GrantAllowed("limited", "fs.read(/etc/passwd)") {
		t.Error("mediation allowed an undeclared capability")
	}
	if evaluator.GrantAllowed("not_installed", "net.connect(x)") {
		t.Error("an unknown capability was granted mediation")
	}
}

func TestJSONSchemaGenerationAndValidation(t *testing.T) {
	schema := JSONSchema([]Param{
		{Name: "target", Type: "string", Required: true, Description: "host"},
		{Name: "ports", Type: "array", ItemType: "number", Description: "port list"},
		{Name: "mode", Type: "string", Enum: []string{"fast", "full"}, Default: "fast"},
		{Name: "deep", Type: "bool", Description: "deep scan"},
	})
	if len(schema) == 0 {
		t.Fatal("no schema produced")
	}
	doc := map[string]any{}
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["type"] != "object" {
		t.Fatalf("schema type = %v", doc["type"])
	}
	properties := doc["properties"].(map[string]any)
	if got := properties["ports"].(map[string]any)["items"]; got == nil {
		t.Fatal("array parameter produced no item schema")
	}

	if err := ValidateArgs(schema, map[string]any{"mode": "fast"}); err == nil {
		t.Error("a missing required parameter was accepted")
	}
	if err := ValidateArgs(schema, map[string]any{"target": "  "}); err == nil {
		t.Error("a blank required parameter was accepted")
	}
	if err := ValidateArgs(schema, map[string]any{"target": "h", "mode": "sideways"}); err == nil {
		t.Error("an out-of-enum value was accepted")
	}
	if err := ValidateArgs(schema, map[string]any{"target": "h", "mode": "full", "deep": true}); err != nil {
		t.Errorf("valid arguments refused: %v", err)
	}
	if err := ValidateArgs(nil, map[string]any{}); err != nil {
		t.Errorf("empty schema should be inert: %v", err)
	}
}

func TestApprovalLedgerScopeAndExpiry(t *testing.T) {
	registry := NewRegistry()
	destructive := &Spec{
		ID: "acme.boom", Name: "boom", Class: ClassDestructive, Permission: "agent:destructive-execute",
		Approval: ApprovalAlways, Builtin: true, Grants: []CapabilityGrant{{Name: "process.exec"}},
	}
	if err := registry.Register(LayerBuiltin, destructive); err != nil {
		t.Fatal(err)
	}

	ledger := NewApprovalLedger(50 * time.Millisecond).UseRegistry(registry)
	ledger.Grant("conv", "boom", 1)
	if !ledger.Consume("conv", "boom") {
		t.Fatal("grant did not authorize the approved call")
	}
	if ledger.Consume("conv", "boom") {
		t.Fatal("grant authorized a second call")
	}

	ledger.Grant("conv", "boom", 2)
	ledger.RevokeConversation("conv")
	if ledger.Consume("conv", "boom") {
		t.Fatal("revoking the conversation left approvals behind")
	}

	ledger.Grant("conv", "boom", 1)
	time.Sleep(80 * time.Millisecond)
	if ledger.Consume("conv", "boom") {
		t.Fatal("an expired approval still authorized execution")
	}

	// Granting to a capability with no approval floor must be a no-op: a token is not
	// a way to skip the permission check.
	harmless := &Spec{ID: "acme.peek", Name: "peek", Class: ClassReadonly, Approval: ApprovalNever, Builtin: true}
	peekRegistry := NewRegistry()
	if err := peekRegistry.Register(LayerBuiltin, harmless); err != nil {
		t.Fatal(err)
	}
	ledger.UseRegistry(peekRegistry)
	ledger.Grant("conv", "peek", 3)
	if ledger.Pending("conv") != 0 {
		t.Fatal("a readonly capability received an approval token")
	}
}

func TestCanonicalIdentityParsing(t *testing.T) {
	if got := CoreID("c2_listener"); got != "core.c2_listener" {
		t.Fatalf("CoreID = %q", got)
	}
	if _, _, err := ParseID("acme.tool.name"); err != nil {
		t.Fatalf("multi-segment identity rejected: %v", err)
	}
	for _, bad := range []string{"tool", "Acme.tool", "acme.", ".tool", "acme.tool space"} {
		if _, _, err := ParseID(bad); err == nil {
			t.Errorf("identity %q accepted", bad)
		}
	}
}

func TestTyposquatDetection(t *testing.T) {
	registry := NewRegistry()
	victim := spec(t, LayerRecipe, RecipeManifest{
		ID: "core.nmap", ToolName: "nmap", Class: "mutating", Permission: "agent:local-execute",
		Grants: []string{"net.connect(target)"},
	})
	if err := registry.Register(LayerRecipe, victim); err != nil {
		t.Fatal(err)
	}
	if hits := registry.CheckTyposquat("core.nrnap"); len(hits) == 0 {
		t.Error("a one-edit impersonation was not flagged")
	}
	if hits := registry.CheckTyposquat("totallydifferent.vendor.tool"); len(hits) != 0 {
		t.Errorf("unrelated identity flagged: %v", hits)
	}
}

func TestDefaultTimeoutSemantics(t *testing.T) {
	specimen := &Spec{ID: "acme.x", Name: "x", Class: ClassReadonly, Permission: "agent:execute", Approval: ApprovalNever}
	if specimen.Timeout != 0 {
		t.Fatal("a spec without a timeout should mean the platform default")
	}
	if specimen.Destructive() {
		t.Fatal("a readonly spec reported destructive")
	}
	withTimeout, err := SpecFromRecipe(RecipeManifest{
		ID: "acme.y", ToolName: "y", Class: "mutating", Permission: "agent:local-execute",
		Grants: []string{"process.exec(x)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	withTimeout.Timeout = 3 * time.Second
	if withTimeout.Timeout < time.Second {
		t.Fatal("timeout lost")
	}
}
