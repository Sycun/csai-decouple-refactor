package app

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/artifact"
	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/capability"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// principalAdapter exposes the transport principal to the policy engine without
// the capability package needing to know about RBAC internals.
type principalAdapter struct{ p authctx.Principal }

func (a principalAdapter) UserID() string { return a.p.UserID }

func (a principalAdapter) HasPermission(permission string) bool {
	return a.p.HasPermission(permission)
}

func (a principalAdapter) ScopeFor(permission string) capability.Scope {
	return capability.Scope(a.p.ScopeFor(strings.TrimSpace(permission)))
}

// depsAdapter is the only bridge between policy checks and the database.
type depsAdapter struct {
	db *database.DB
	// c2 is the beacon ledger the resource-to-project lookup needs; the SQL lives in store.C2.
	c2 *store.C2
	// executions answers the tool-execution access question; the SQL lives in store.Monitor.
	executions *store.Monitor
	// rbac answers the resource-visibility question; the SQL lives in store.RBAC.
	rbac *store.RBAC
}

func (d depsAdapter) CanAccessResource(userID string, scope capability.Scope, resourceType, id string) bool {
	if d.db == nil {
		return false
	}
	return d.rbac.UserCanAccessResource(userID, string(scope), resourceType, id)
}

func (d depsAdapter) CanAccessToolExecution(userID string, scope capability.Scope, executionID string) bool {
	if d.db == nil {
		return false
	}
	return d.executions.UserCanAccessToolExecution(userID, string(scope), executionID)
}

func (d depsAdapter) ConversationID(ctx context.Context) string {
	return mcpAuthorizationConversationID(ctx)
}

func (d depsAdapter) ProjectFilter(ctx context.Context) string {
	filter := mcpEffectiveProjectFilter(ctx, d.db)
	if filter == store.ProjectUnbound {
		return capability.ProjectFilterUnbound
	}
	return filter
}

func (d depsAdapter) ResourceProjectID(resourceType, resourceID string) (string, bool, error) {
	return mcpResourceProjectID(d.db, d.c2, resourceType, resourceID)
}

func (d depsAdapter) ConversationProjectID(conversationID string) (string, error) {
	if d.db == nil {
		return "", fmt.Errorf("database is not available")
	}
	return d.db.GetConversationProjectID(conversationID)
}

// capabilityRuntime owns the one registry and the one evaluator the whole
// process authorizes through. Both entry points (cmd/server and cmd/mcp-stdio)
// must install one, so a second assembly path cannot run without a policy.
type capabilityRuntime struct {
	mu          sync.RWMutex
	registry    *capability.Registry
	delegate    *capability.Evaluator
	logger      *zap.Logger
	revocations *artifact.Revocations
	// provenance records which verified artifact each installed capability came
	// from. It is kept outside the registry because rebuilding the recipe layer on a
	// config reload recreates specs: stamped there, it would vanish and silently
	// disable publisher revocation.
	provenance map[string]provenanceRecord
}

type provenanceRecord struct {
	Publisher string
	Digest    string
}

var sharedCapability = &capabilityRuntime{
	registry:   capability.Global(),
	provenance: map[string]provenanceRecord{},
}

// InstallCapabilityRegistry builds the registry for the process: the built-in
// policy table plus every recipe layer entry declared by tools/*.yaml.
// It returns the specs that were refused, so a broken community artifact can be
// reported instead of silently narrowing or widening what is executable.
func InstallCapabilityRegistry(tools []config.ToolConfig, logger *zap.Logger) ([]capability.Rejection, error) {
	registry := capability.Global()
	if err := capability.GlobalErr(); err != nil {
		return nil, fmt.Errorf("built-in capability registration failed: %w", err)
	}
	builtins := registry.BuiltinNames()

	specs, rejections := RecipeSpecs(tools)
	if err := registry.RegisterAll(capability.LayerRecipe, specs); err != nil {
		return rejections, err
	}

	sharedCapability.mu.Lock()
	sharedCapability.registry = registry
	sharedCapability.logger = logger
	sharedCapability.delegate = nil
	sharedCapability.mu.Unlock()

	if logger != nil {
		logger.Info("capability registry installed",
			zap.Int("builtin", len(builtins)),
			zap.Int("recipe", len(specs)),
			zap.Int("rejected", len(rejections)))
		for _, r := range rejections {
			logger.Warn("capability rejected, tool will fail closed",
				zap.String("tool", r.ToolName), zap.String("reason", r.Reason))
		}
	}
	return rejections, nil
}

// RecipeSpecs converts recipe manifests into specs, collecting the refused ones.
func RecipeSpecs(tools []config.ToolConfig) ([]*capability.Spec, []capability.Rejection) {
	specs := make([]*capability.Spec, 0, len(tools))
	var rejections []capability.Rejection
	for _, tool := range tools {
		if tool.Capability == nil {
			rejections = append(rejections, capability.Rejection{
				ToolName: tool.Name,
				Reason:   "recipe has no capability manifest",
			})
			continue
		}
		spec, err := capability.SpecFromRecipe(capability.RecipeManifest{
			ID:         tool.Capability.ID,
			Version:    tool.Capability.Version,
			Class:      tool.Capability.Class,
			Permission: tool.Capability.Permission,
			Approval:   tool.Capability.Approval,
			Runtime:    tool.Capability.Runtime,
			Grants:     tool.Capability.Grants,
			Evidence:   tool.Capability.Evidence,
			Timeout:    tool.Capability.TimeoutDuration(),
			ToolName:   tool.Name,
			Title:      tool.ShortDescription,
			Publisher:  tool.Capability.Publisher,
		})
		if err != nil {
			rejections = append(rejections, capability.Rejection{ToolName: tool.Name, Reason: err.Error()})
			continue
		}
		applyProvenance(spec)
		// An internal: recipe is a YAML front for code that already lives in the
		// binary. It keeps its manifest metadata but inherits the imperative check
		// registered under the same identity, so ownership rules cannot be dropped
		// by expressing them in YAML.
		if existing, ok := capability.Global().LookupByID(spec.ID); ok && existing.Check != nil {
			spec.Check = existing.Check
		}
		spec.ParamsSchema = capability.JSONSchema(recipeParams(tool.Parameters))
		specs = append(specs, spec)
	}
	return specs, rejections
}

// RefreshRecipeLayer re-registers only the recipe layer, so applying config can
// never drop the Go-registered tools.
func RefreshRecipeLayer(tools []config.ToolConfig) ([]capability.Rejection, error) {
	specs, rejections := RecipeSpecs(tools)
	sharedCapability.mu.RLock()
	registry := sharedCapability.registry
	sharedCapability.mu.RUnlock()
	if err := registry.RegisterAll(capability.LayerRecipe, specs); err != nil {
		return rejections, err
	}
	return rejections, nil
}

// CapabilityRegistryForTest exposes the installed registry for contract tests.
func CapabilityRegistryForTest() *capability.Registry {
	sharedCapability.mu.RLock()
	defer sharedCapability.mu.RUnlock()
	return sharedCapability.registry
}

// CapabilityEvaluatorForTest exposes the installed evaluator.
func CapabilityEvaluatorForTest() *capability.Evaluator { return evaluatorFor() }

func evaluatorFor() *capability.Evaluator {
	sharedCapability.mu.RLock()
	registry := sharedCapability.registry
	delegate := sharedCapability.delegate
	logger := sharedCapability.logger
	sharedCapability.mu.RUnlock()

	if delegate != nil {
		return delegate
	}
	e := capability.NewEvaluator(registry,
		// Revocation is enforced on the execution path, not only at install time: a
		// revoked artifact must stop working on the next call, on a machine that
		// never talks to the registry again.
		capability.WithStage("revocation", func(_ context.Context, spec *capability.Spec, _ capability.Principal, _ map[string]any, _ capability.Deps) (string, bool) {
			sharedCapability.mu.RLock()
			list := sharedCapability.revocations
			sharedCapability.mu.RUnlock()
			if list == nil {
				return "", false
			}
			if err := list.CheckProvenance(spec.Publisher, spec.ArtifactDigest); err != nil {
				return fmt.Sprintf("capability %s is revoked: %s", spec.ID, err.Error()), true
			}
			return "", false
		}),
		capability.WithAudit(func(d capability.Decision) {
			if logger != nil {
				logger.Warn("capability decision denied",
					zap.String("capability", d.CapabilityID),
					zap.String("tool", d.ToolName),
					zap.String("principal", d.PrincipalID),
					zap.String("stage", d.Stage),
					zap.String("outcome", string(d.Outcome)),
					zap.String("reason", d.Reason))
			}
		}, func(d capability.Decision) {}),
	)
	sharedCapability.mu.Lock()
	sharedCapability.delegate = e
	sharedCapability.mu.Unlock()
	return e
}

// authorizeWithCapability is the single execution-path decision entry point.
// Every denial carries the stage that produced it, and an unregistered tool is a
// denial rather than a fallback.
func authorizeWithCapability(ctx context.Context, db *database.DB, toolName string, args map[string]any) error {
	if p, ok := authctx.PrincipalFromContext(ctx); ok {
		ctx = capability.WithPrincipal(ctx, principalAdapter{p: p})
	}
	ctx = capability.WithRequestDeps(ctx, depsAdapter{db: db, c2: database.NewC2(db), executions: database.NewMonitor(db), rbac: database.NewRBAC(db)})
	decision := evaluatorFor().Decide(ctx, toolName, args)
	switch decision.Outcome {
	case capability.OutcomeAllow:
		return nil
	case capability.OutcomeAsk:
		return fmt.Errorf("capability %s requires per-call human authorization: %s", decision.CapabilityID, decision.Reason)
	default:
		return fmt.Errorf("%s", decision.Reason)
	}
}

// WithCapabilityApproval marks a context as carrying a human decision for one
// destructive invocation, produced by the HITL layer after an approver accepts.
func WithCapabilityApproval(ctx context.Context) context.Context {
	return capability.WithApprovalGranted(ctx)
}

func mcpToolAuthorizer(db *database.DB) func(context.Context, string, map[string]interface{}) error {
	return func(ctx context.Context, toolName string, args map[string]interface{}) error {
		return authorizeWithCapability(ctx, db, toolName, args)
	}
}

// externalMCPNamespacePermission is the floor every remote tool inherits. It stays the fallback
// for a tool the registry has not seen yet (a server that has never connected, or an inventory
// refresh in flight), which is a declared namespace policy rather than an unregistered pass.
const externalMCPNamespacePermission = "mcp:external:execute"

func externalMCPToolAuthorizer() func(context.Context, string, map[string]interface{}) error {
	return func(ctx context.Context, toolName string, _ map[string]interface{}) error {
		p, ok := authctx.PrincipalFromContext(ctx)
		if !ok {
			return fmt.Errorf("missing authenticated principal")
		}
		ctx = capability.WithPrincipal(ctx, principalAdapter{p: p})
		ctx = capability.WithRequestDeps(ctx, depsAdapter{db: nil})

		// Decide on the tool's own identity when it has one, so a rule can name a single remote
		// tool; otherwise fall back to the namespace policy. Both branches are registered
		// policies - neither is "unknown name, assume allowed".
		target := externalMCPNamespacePermission
		if spec, found := lookupRemoteToolSpec(toolName); found {
			target = spec.Name
		}
		decision := evaluatorFor().Decide(ctx, target, nil)
		if decision.Outcome != capability.OutcomeAllow {
			return fmt.Errorf("%s", decision.Reason)
		}
		return nil
	}
}

// mcpAuthorizationConversationID keeps the conversation source of truth in one
// place for both the HTTP and MCP transports.
func mcpAuthorizationConversationID(ctx context.Context) string {
	if id := strings.TrimSpace(agent.ConversationIDFromContext(ctx)); id != "" {
		return id
	}
	return strings.TrimSpace(mcp.MCPConversationIDFromContext(ctx))
}

// InstallStdioPolicy gives the stdio assembly path the same decision point as
// the HTTP server. Identity comes from config: an operator must declare which
// principal the bridge acts as and which permissions it holds. Nothing is
// granted by default, so a second entry point cannot execute by omission.
func InstallStdioPolicy(cfg *config.Config, logger *zap.Logger) (func(context.Context, string, map[string]interface{}) error, func(context.Context) context.Context, error) {
	if _, err := InstallCapabilityRegistry(cfg.Security.Tools, logger); err != nil {
		return nil, nil, err
	}

	permissions := map[string]bool{}
	scopes := map[string]string{}
	for _, permission := range cfg.MCPStdio.Permissions {
		permission = strings.TrimSpace(permission)
		if permission == "" {
			continue
		}
		permissions[permission] = true
		if cfg.MCPStdio.Scope != "" {
			scopes[permission] = cfg.MCPStdio.Scope
		}
	}

	principal := authctx.NewPrincipalWithScopes(
		cfg.MCPStdio.PrincipalUserID, cfg.MCPStdio.PrincipalUsername,
		cfg.MCPStdio.Scope, permissions, scopes)
	if strings.TrimSpace(principal.UserID) == "" {
		principal.UserID = "stdio-bridge"
	}

	if logger != nil {
		if len(permissions) == 0 {
			logger.Warn("mcp_stdio 未声明权限集，stdio 路径将拒绝所有工具调用",
				zap.String("principal", principal.UserID))
		} else {
			logger.Info("mcp_stdio 能力策略已装配",
				zap.String("principal", principal.UserID),
				zap.Int("permissions", len(permissions)))
		}
	}

	bound := capability.WithPrincipal(context.Background(), principalAdapter{p: principal})
	decorate := func(ctx context.Context) context.Context {
		if _, ok := authctx.PrincipalFromContext(ctx); ok {
			return ctx
		}
		return capability.WithPrincipal(ctx, principalAdapter{p: principal})
	}
	_ = bound
	return mcpToolAuthorizer(nil), decorate, nil
}

// recipeParams maps a recipe's declared parameters onto the manifest schema so
// argument validation, the generated form and the LLM tool schema share one
// definition instead of three hand-written copies.
func recipeParams(params []config.ParameterConfig) []capability.Param {
	out := make([]capability.Param, 0, len(params))
	for _, p := range params {
		out = append(out, capability.Param{
			Name: p.Name, Type: p.Type, Description: firstLine(p.Description),
			Required: p.Required, Default: p.Default, Enum: p.Options, ItemType: p.ItemType,
		})
	}
	return out
}

// firstLine keeps tool descriptions compact: recipe descriptions are multi-line
// prose for humans, while the schema description reaches the model.
func firstLine(description string) string {
	description = strings.TrimSpace(description)
	if idx := strings.IndexByte(description, '\n'); idx >= 0 {
		return strings.TrimSpace(description[:idx])
	}
	return description
}

// LoadRevocations installs the client-enforced block list. A missing file means no
// revocations yet, which is a valid state; a malformed file is an error the operator
// must see, because a silently ignored revocation list is worse than no list.
func LoadRevocations(path string, logger *zap.Logger) error {
	list, err := artifact.LoadRevocations(path)
	if err != nil {
		return err
	}
	sharedCapability.mu.Lock()
	sharedCapability.revocations = list
	sharedCapability.mu.Unlock()
	if logger != nil {
		logger.Info("capability revocation list installed",
			zap.Int("digests", len(list.RevokedDigests())),
			zap.Int("publishers", len(list.RevokedPublishers())))
	}
	return nil
}

// MergeRevocations folds in a refreshed list and reports how many entries were new.
func MergeRevocations(path string) (int, error) {
	next, err := artifact.LoadRevocations(path)
	if err != nil {
		return 0, err
	}
	sharedCapability.mu.Lock()
	defer sharedCapability.mu.Unlock()
	if sharedCapability.revocations == nil {
		sharedCapability.revocations = artifact.NewRevocations()
	}
	return sharedCapability.revocations.Merge(next), nil
}

// IsolateRevoked removes every capability whose provenance is revoked from the
// registry, so the model cannot even see it, and reports the affected identities.
func IsolateRevoked() []string {
	sharedCapability.mu.RLock()
	list := sharedCapability.revocations
	registry := sharedCapability.registry
	sharedCapability.mu.RUnlock()
	if list == nil {
		return nil
	}

	var isolated []string
	for _, spec := range registry.Specs() {
		if err := list.CheckProvenance(spec.Publisher, spec.ArtifactDigest); err == nil {
			continue
		}
		if capability.IsBuiltinName(spec.Name) {
			// Shipped code is not publisher content; it is reported, not removed.
			continue
		}
		registry.Remove(spec.ID)
		isolated = append(isolated, spec.ID)
	}
	return isolated
}

// StampProvenance records the verified artifact behind an installed capability. The
// installer calls this after signature verification; publisher content cannot set
// these fields, so a submission cannot choose which revocation entry matches it.
func StampProvenance(capabilityID, publisher, digest string) {
	sharedCapability.mu.Lock()
	if sharedCapability.provenance == nil {
		sharedCapability.provenance = map[string]provenanceRecord{}
	}
	sharedCapability.provenance[capabilityID] = provenanceRecord{Publisher: publisher, Digest: digest}
	sharedCapability.mu.Unlock()

	for _, spec := range capability.Global().Specs() {
		if spec.ID == capabilityID {
			spec.SetProvenance(publisher, digest)
		}
	}
}

// applyProvenance re-applies a stored stamp to a freshly built spec.
func applyProvenance(spec *capability.Spec) {
	sharedCapability.mu.RLock()
	record, ok := sharedCapability.provenance[spec.ID]
	sharedCapability.mu.RUnlock()
	if !ok {
		return
	}
	spec.SetProvenance(record.Publisher, record.Digest)
}

// ProvenanceOf reports the recorded artifact stamp for a capability identity.
func ProvenanceOf(capabilityID string) (publisher, digest string, ok bool) {
	sharedCapability.mu.RLock()
	defer sharedCapability.mu.RUnlock()
	record, found := sharedCapability.provenance[capabilityID]
	return record.Publisher, record.Digest, found
}
