package capability

import (
	"context"
	"fmt"
	"strings"
)

// ProjectFilterUnbound is the adapter's sentinel for "this conversation has no
// project". Resource checks must refuse bound resources under it.
const ProjectFilterUnbound = "__unbound__"

type ctxKey int

const (
	ctxKeyPrincipal ctxKey = iota
	ctxKeyApproval
	ctxKeyDeps
)

// WithRequestDeps binds the storage a single request should consult. Callers
// that serve more than one datastore (HTTP and stdio) must use this instead of
// baking deps into the evaluator, or a request would be checked against the
// wrong database.
func WithRequestDeps(ctx context.Context, d Deps) context.Context {
	return context.WithValue(ctx, ctxKeyDeps, d)
}

func depsFromContext(ctx context.Context) (Deps, bool) {
	d, ok := ctx.Value(ctxKeyDeps).(Deps)
	return d, ok && d != nil
}

// WithPrincipal binds the authenticated caller. Callers must use the evaluator
// rather than reading this directly.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal, p)
}

func principalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKeyPrincipal).(Principal)
	return p, ok && p != nil
}

// WithApprovalGranted marks a context whose destructive call has already been
// approved by a human for this specific invocation.
func WithApprovalGranted(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyApproval, true)
}

func approvalGranted(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeyApproval).(bool)
	return v
}

// ApprovalPending is returned by the evaluator when a call stopped at the
// approval floor. Callers must translate it into a durable pending request.
type ApprovalPending struct{ Decision Decision }

func (a *ApprovalPending) Error() string { return a.Decision.Reason }

// All runs every check in order and returns the first denial.
func All(checks ...CheckFunc) CheckFunc {
	return func(ctx context.Context, p Principal, args map[string]any, d Deps) error {
		for _, c := range checks {
			if err := c(ctx, p, args, d); err != nil {
				return err
			}
		}
		return nil
	}
}

// Require denies when the principal lacks the permission.
func Require(permission string) CheckFunc {
	return func(_ context.Context, p Principal, _ map[string]any, _ Deps) error {
		if !p.HasPermission(permission) {
			return fmt.Errorf("missing permission %s", permission)
		}
		return nil
	}
}

// Resource requires the permission and access to one named resource.
func Resource(permission, resourceType, argument string) CheckFunc {
	return func(_ context.Context, p Principal, args map[string]any, d Deps) error {
		if !p.HasPermission(permission) {
			return fmt.Errorf("missing permission %s", permission)
		}
		id := argString(args, argument)
		if id == "" || !d.CanAccessResource(p.UserID(), p.ScopeFor(permission), resourceType, id) {
			return fmt.Errorf("no access to %s %s", resourceType, id)
		}
		return nil
	}
}

// ResourceBoundary is Resource plus the conversation project boundary check.
func ResourceBoundary(permission, resourceType, argument string) CheckFunc {
	return func(ctx context.Context, p Principal, args map[string]any, d Deps) error {
		if err := Resource(permission, resourceType, argument)(ctx, p, args, d); err != nil {
			return err
		}
		return boundary(ctx, p, d, resourceType, argString(args, argument))
	}
}

// boundary refuses a resource that belongs to a different project than the
// conversation is filtered to. An empty filter means no constraint.
func boundary(ctx context.Context, p Principal, d Deps, resourceType, resourceID string) error {
	filter := d.ProjectFilter(ctx)
	if filter == "" {
		return nil
	}
	projectID, ok, err := d.ResourceProjectID(resourceType, resourceID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if filter == ProjectFilterUnbound {
		if projectID != "" {
			return fmt.Errorf("resource %s %s belongs to project %s, current conversation is unbound", resourceType, resourceID, projectID)
		}
		return nil
	}
	if projectID != filter {
		if projectID == "" {
			return fmt.Errorf("resource %s %s is unbound, current conversation project is %s", resourceType, resourceID, filter)
		}
		return fmt.Errorf("resource %s %s belongs to project %s, current conversation project is %s", resourceType, resourceID, projectID, filter)
	}
	return nil
}

// RetestAwareFinding is Resource over a finding plus the branch rule a retest needs.
//
// An ordinary conversation gets exactly what ResourceBoundary would give it. A retest branch is
// narrower on purpose: it may reach the one finding it was snapshotted from and nothing else, and
// only while the project scope it is running under still matches the one the snapshot was taken
// from. Without the second half, a branch whose source conversation has since been rebound to
// another project would keep writing evidence onto a finding that is no longer in scope.
func RetestAwareFinding(permission, argument string) CheckFunc {
	return func(ctx context.Context, p Principal, args map[string]any, d Deps) error {
		if err := Resource(permission, "vulnerability", argument)(ctx, p, args, d); err != nil {
			return err
		}
		id := argString(args, argument)
		conversationID := d.ConversationID(ctx)
		if conversationID == "" {
			return boundary(ctx, p, d, "vulnerability", id)
		}
		snapshot, isRetest, err := d.RetestSnapshotOf(conversationID)
		if err != nil {
			return err
		}
		if !isRetest {
			return boundary(ctx, p, d, "vulnerability", id)
		}
		if snapshot.FindingID != id {
			return fmt.Errorf("no access to retest vulnerability %s", id)
		}
		if !d.CanAccessResource(p.UserID(), p.ScopeFor(permission), "conversation", conversationID) {
			return fmt.Errorf("no access to retest conversation %s", conversationID)
		}
		switch filter := d.ProjectFilter(ctx); filter {
		case "":
		case ProjectFilterUnbound:
			if snapshot.SourceProjectID != "" {
				return fmt.Errorf("retest conversation conflicts with current project scope")
			}
		default:
			if filter != snapshot.SourceProjectID {
				return fmt.Errorf("retest conversation conflicts with current project scope")
			}
		}
		// The branch's own conversation is the anchor: the snapshot records the project the source
		// was bound to, and both it and the finding must still agree with where the branch sits now.
		projectID, err := d.ConversationProjectID(conversationID)
		if err != nil {
			return err
		}
		if snapshot.SourceProjectID != projectID {
			return fmt.Errorf("retest conversation project no longer matches vulnerability %s", id)
		}
		findingProjectID, found, err := d.FindingProjectID(id)
		if err != nil {
			return err
		}
		if !found || (findingProjectID != "" && findingProjectID != projectID) {
			return fmt.Errorf("retest conversation project no longer matches vulnerability %s", id)
		}
		return nil
	}
}

// Conversation requires access to a conversation taken from args, falling back
// to the request context when the argument is absent.
func Conversation(permission, argument string, fromContextOnly bool) CheckFunc {
	return func(ctx context.Context, p Principal, args map[string]any, d Deps) error {
		if !p.HasPermission(permission) {
			return fmt.Errorf("missing permission %s", permission)
		}
		id := ""
		if !fromContextOnly {
			id = argString(args, argument)
		}
		if id == "" {
			id = d.ConversationID(ctx)
		}
		if id == "" || !d.CanAccessResource(p.UserID(), p.ScopeFor(permission), "conversation", id) {
			return fmt.Errorf("no access to conversation %s", id)
		}
		return nil
	}
}

// ProjectBound requires a conversation-bound project, used by the blackboard tools.
func ProjectBound(permission string) CheckFunc {
	return func(ctx context.Context, p Principal, _ map[string]any, d Deps) error {
		if !p.HasPermission(permission) {
			return fmt.Errorf("missing permission %s", permission)
		}
		conversationID := d.ConversationID(ctx)
		if conversationID == "" || !d.CanAccessResource(p.UserID(), p.ScopeFor(permission), "conversation", conversationID) {
			return fmt.Errorf("no access to conversation %s", conversationID)
		}
		projectID, err := d.ConversationProjectID(conversationID)
		if err != nil {
			return fmt.Errorf("no access to project: %w", err)
		}
		if strings.TrimSpace(projectID) == "" {
			return fmt.Errorf("当前对话未绑定项目，无法使用项目黑板工具，请先在对话中选择项目或创建带项目的对话")
		}
		if !d.CanAccessResource(p.UserID(), p.ScopeFor(permission), "project", projectID) {
			return fmt.Errorf("no access to project %s", projectID)
		}
		return nil
	}
}

// Execution requires ownership of a background tool execution.
func Execution(permission string) CheckFunc {
	return func(_ context.Context, p Principal, args map[string]any, d Deps) error {
		if !p.HasPermission(permission) {
			return fmt.Errorf("missing permission %s", permission)
		}
		id := argString(args, "execution_id")
		if id == "" || !d.CanAccessToolExecution(p.UserID(), p.ScopeFor(permission), id) {
			return fmt.Errorf("no access to tool execution %s", id)
		}
		return nil
	}
}

// GlobalScopeRequired refuses unless the permission is held at all-scope.
func GlobalScopeRequired(permission string) CheckFunc {
	return func(_ context.Context, p Principal, _ map[string]any, _ Deps) error {
		if !p.HasPermission(permission) {
			return fmt.Errorf("missing permission %s", permission)
		}
		if p.ScopeFor(permission) != ScopeAll {
			return fmt.Errorf("%s mutation requires global scope", permission)
		}
		return nil
	}
}

// OptionalProject refuses when a project_id argument exists but is unreachable.
func OptionalProject(base CheckFunc, permission string) CheckFunc {
	return func(ctx context.Context, p Principal, args map[string]any, d Deps) error {
		if err := base(ctx, p, args, d); err != nil {
			return err
		}
		projectID := argString(args, "project_id")
		if projectID != "" && !d.CanAccessResource(p.UserID(), p.ScopeFor(permission), "project", projectID) {
			return fmt.Errorf("no access to project %s", projectID)
		}
		return nil
	}
}

// C2Action reproduces the action-dispatched C2 policy: read-only actions need
// c2:read, mutations c2:write, deletions c2:delete, and every touched resource
// must be reachable under the conversation project boundary.
func C2Action(resourceType, argument string) CheckFunc {
	readActions := map[string]bool{"list": true, "get": true, "get_result": true, "wait": true}
	deleteActions := map[string]bool{"delete": true, "delete_batch": true}
	return func(ctx context.Context, p Principal, args map[string]any, d Deps) error {
		action := argString(args, "action")
		permission := "c2:write"
		switch {
		case readActions[action]:
			permission = "c2:read"
		case deleteActions[action]:
			permission = "c2:delete"
		}
		if !p.HasPermission(permission) {
			return fmt.Errorf("missing permission %s", permission)
		}
		scope := p.ScopeFor(permission)

		if action == "delete_batch" {
			ids := argStrings(args, argument+"s")
			if len(ids) == 0 {
				return fmt.Errorf("missing resource identifiers %ss", argument)
			}
			for _, candidate := range ids {
				if !d.CanAccessResource(p.UserID(), scope, resourceType, candidate) {
					return fmt.Errorf("no access to %s %s", resourceType, candidate)
				}
				if err := boundary(ctx, p, d, resourceType, candidate); err != nil {
					return err
				}
			}
			return nil
		}

		id := argString(args, argument)
		if id == "" {
			if action == "create" {
				projectID := argString(args, "project_id")
				if projectID == "" {
					projectID = d.ProjectFilter(ctx)
					if projectID == ProjectFilterUnbound {
						projectID = ""
					}
				}
				if projectID != "" && !d.CanAccessResource(p.UserID(), scope, "project", projectID) {
					return fmt.Errorf("no access to project %s", projectID)
				}
				return nil
			}
			if action == "list" {
				return nil
			}
			return fmt.Errorf("missing resource identifier %s", argument)
		}
		if !d.CanAccessResource(p.UserID(), scope, resourceType, id) {
			return fmt.Errorf("no access to %s %s", resourceType, id)
		}
		return boundary(ctx, p, d, resourceType, id)
	}
}

// FirstMatch runs each branch until one applies. A branch applies when its
// guard returns true; the first applicable branch decides and later branches
// never run, which keeps precedence explicit.
func FirstMatch(branches ...Branch) CheckFunc {
	return func(ctx context.Context, p Principal, args map[string]any, d Deps) error {
		for _, b := range branches {
			if b.When == nil || b.When(ctx, p, args, d) {
				return b.Check(ctx, p, args, d)
			}
		}
		return fmt.Errorf("no policy branch matched")
	}
}

// Branch is one guarded check inside FirstMatch.
type Branch struct {
	When  func(ctx context.Context, p Principal, args map[string]any, d Deps) bool
	Check CheckFunc
}

// WhenArgPresent guards a branch on a non-empty argument.
func WhenArgPresent(argument string) func(context.Context, Principal, map[string]any, Deps) bool {
	return func(_ context.Context, _ Principal, args map[string]any, _ Deps) bool {
		return argString(args, argument) != ""
	}
}

// WhenProjectFilterSet guards on the request carrying a project filter.
func WhenProjectFilterSet() func(context.Context, Principal, map[string]any, Deps) bool {
	return func(ctx context.Context, _ Principal, _ map[string]any, d Deps) bool {
		return d.ProjectFilter(ctx) != ""
	}
}

// ByAction dispatches on the action argument, defaulting to fallback.
func ByAction(fallback CheckFunc, byAction map[string]CheckFunc) CheckFunc {
	return func(ctx context.Context, p Principal, args map[string]any, d Deps) error {
		if fn, ok := byAction[argString(args, "action")]; ok {
			return fn(ctx, p, args, d)
		}
		return fallback(ctx, p, args, d)
	}
}

func argString(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func argStrings(args map[string]any, key string) []string {
	out := []string{}
	switch raw := args[key].(type) {
	case []string:
		for _, v := range raw {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
	case []any:
		for _, item := range raw {
			if v, ok := item.(string); ok {
				if v = strings.TrimSpace(v); v != "" {
					out = append(out, v)
				}
			}
		}
	}
	return out
}
