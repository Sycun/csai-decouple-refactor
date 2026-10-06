package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/mcp/builtin"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

func TestMCPToolAuthorizerEnforcesPermissionAndResource(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "mcp-authz.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := database.NewRBAC(db).CreateRBACUser("mcp-user", "MCP User", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ws_allowed", "ws_hidden"} {
		if err := database.NewWebshell(db).Create(&store.WebShellConnection{ID: id, URL: "http://127.0.0.1/" + id, Type: "php", Method: "post", CmdParam: "cmd", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.NewRBAC(db).AssignResourceToUser(user.ID, "webshell", "ws_allowed"); err != nil {
		t.Fatal(err)
	}

	principal := authctx.NewPrincipal(user.ID, user.Username, database.RBACScopeAssigned, map[string]bool{"mcp:write": true, "webshell:write": true})
	ctx := authctx.WithPrincipal(context.Background(), principal)
	authorize := mcpToolAuthorizer(db)

	// webshell_exec 是 destructive 能力：即使权限与资源都通过，没有人工授权也必须拒绝。
	if err := authorize(ctx, builtin.ToolWebshellExec, map[string]interface{}{"connection_id": "ws_allowed"}); err == nil {
		t.Fatal("destructive capability ran without a human decision")
	} else if !strings.Contains(err.Error(), "per-call human authorization") {
		t.Fatalf("destructive gate produced the wrong denial: %v", err)
	}

	approved := WithCapabilityApproval(ctx)
	if err := authorize(approved, builtin.ToolWebshellExec, map[string]interface{}{"connection_id": "ws_allowed"}); err != nil {
		t.Fatalf("allowed resource denied after approval: %v", err)
	}
	if err := authorize(approved, builtin.ToolWebshellExec, map[string]interface{}{"connection_id": "ws_hidden"}); err == nil {
		t.Fatal("foreign webshell resource was allowed")
	}
	if err := authorize(ctx, builtin.ToolManageWebshellDelete, map[string]interface{}{"connection_id": "ws_allowed"}); err == nil {
		t.Fatal("delete without webshell:delete was allowed")
	}
}

func TestMCPToolAuthorizerEnforcesConversationProjectBoundary(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "mcp-project-boundary.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := database.NewRBAC(db).CreateRBACUser("boundary-user", "Boundary User", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(&database.Project{Name: "Project 123"})
	if err != nil {
		t.Fatal(err)
	}
	projectConv, err := db.CreateConversation("project conversation", database.ConversationCreateMeta{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	unboundConv, err := db.CreateConversation("unbound conversation", database.ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}

	wsProject := store.WebShellConnection{ID: "ws_project", ProjectID: project.ID, URL: "http://127.0.0.1/project.php", Type: "php", Method: "post", CreatedAt: time.Now()}
	wsUnbound := store.WebShellConnection{ID: "ws_unbound", URL: "http://127.0.0.1/unbound.php", Type: "php", Method: "post", CreatedAt: time.Now()}
	if err := database.NewWebshell(db).Create(&wsProject); err != nil {
		t.Fatal(err)
	}
	if err := database.NewWebshell(db).Create(&wsUnbound); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{wsProject.ID, wsUnbound.ID} {
		if err := database.NewRBAC(db).AssignResourceToUser(user.ID, "webshell", id); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now()
	listener := &store.C2Listener{ID: "l_project", ProjectID: project.ID, Name: "project listener", Type: "tcp_reverse", BindHost: "127.0.0.1", BindPort: 5555, OwnerUserID: user.ID, CreatedAt: now}
	if err := database.NewC2(db).CreateC2Listener(listener); err != nil {
		t.Fatal(err)
	}
	if err := database.NewRBAC(db).AssignResourceToUser(user.ID, "c2_listener", listener.ID); err != nil {
		t.Fatal(err)
	}
	session := &store.C2Session{ID: "s_project", ListenerID: listener.ID, ImplantUUID: "implant-project", Status: "active", FirstSeenAt: now, LastCheckIn: now}
	if err := database.NewC2(db).UpsertC2Session(session); err != nil {
		t.Fatal(err)
	}

	principal := authctx.NewPrincipal(user.ID, user.Username, database.RBACScopeAssigned, map[string]bool{
		"webshell:read": true, "webshell:write": true,
		"c2:read": true, "c2:write": true,
	})
	authorize := mcpToolAuthorizer(db)
	unboundCtx := authctx.WithPrincipal(mcp.WithMCPConversationID(context.Background(), unboundConv.ID), principal)
	projectCtx := authctx.WithPrincipal(mcp.WithMCPProjectID(mcp.WithMCPConversationID(context.Background(), projectConv.ID), project.ID), principal)
	projectCtxFromConversationOnly := authctx.WithPrincipal(mcp.WithMCPConversationID(context.Background(), projectConv.ID), principal)

	if err := authorize(WithCapabilityApproval(unboundCtx), builtin.ToolWebshellExec, map[string]interface{}{"connection_id": wsProject.ID}); err == nil {
		t.Fatal("unbound conversation was allowed to use project-bound webshell")
	}
	if err := authorize(WithCapabilityApproval(unboundCtx), builtin.ToolWebshellExec, map[string]interface{}{"connection_id": wsUnbound.ID}); err != nil {
		t.Fatalf("unbound webshell denied in unbound conversation: %v", err)
	}
	if err := authorize(WithCapabilityApproval(projectCtx), builtin.ToolWebshellExec, map[string]interface{}{"connection_id": wsProject.ID}); err != nil {
		t.Fatalf("project webshell denied in project conversation: %v", err)
	}
	if err := authorize(WithCapabilityApproval(projectCtxFromConversationOnly), builtin.ToolWebshellExec, map[string]interface{}{"connection_id": wsProject.ID}); err != nil {
		t.Fatalf("project webshell denied when only conversation id is present: %v", err)
	}
	if err := authorize(WithCapabilityApproval(projectCtx), builtin.ToolWebshellExec, map[string]interface{}{"connection_id": wsUnbound.ID}); err == nil {
		t.Fatal("project conversation was allowed to use unbound webshell by id")
	}
	if err := authorize(unboundCtx, builtin.ToolC2Session, map[string]interface{}{"action": "get", "session_id": session.ID}); err == nil {
		t.Fatal("unbound conversation was allowed to use project-bound c2 session")
	}
	if err := authorize(projectCtx, builtin.ToolC2Session, map[string]interface{}{"action": "get", "session_id": session.ID}); err != nil {
		t.Fatalf("project c2 session denied in project conversation: %v", err)
	}
	if err := authorize(projectCtxFromConversationOnly, builtin.ToolC2Session, map[string]interface{}{"action": "get", "session_id": session.ID}); err != nil {
		t.Fatalf("project c2 session denied when only conversation id is present: %v", err)
	}
}

func TestEveryBuiltinMCPToolHasExplicitAuthorizationPolicy(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "mcp-policy-inventory.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	permissions := map[string]bool{}
	for permission := range security.PermissionCatalog {
		permissions[permission] = true
	}
	ctx := authctx.WithPrincipal(context.Background(), authctx.NewPrincipal("admin", "admin", database.RBACScopeAll, permissions))
	authorize := mcpToolAuthorizer(db)
	args := map[string]interface{}{
		"action": "get", "connection_id": "x", "queue_id": "x", "listener_id": "x",
		"session_id": "x", "task_id": "x", "id": "x", "conversation_id": "x", "execution_id": "x",
	}
	for _, toolName := range builtin.GetAllBuiltinTools() {
		err := authorize(ctx, toolName, args)
		if err != nil && strings.Contains(err.Error(), "no authorization policy registered") {
			t.Errorf("builtin tool %s has no explicit policy", toolName)
		}
	}
}

func TestMCPExecutionControlAuthorizationUsesExecutionScope(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "mcp-exec-authz.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := database.NewRBAC(db).CreateRBACUser("exec-user", "Exec User", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.NewMonitor(db).SaveToolExecution(&mcp.ToolExecution{
		ID:          "exec-owned",
		ToolName:    "lab::slow",
		Status:      "running",
		StartTime:   time.Now(),
		OwnerUserID: user.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.NewMonitor(db).SaveToolExecution(&mcp.ToolExecution{
		ID:        "exec-hidden",
		ToolName:  "lab::slow",
		Status:    "running",
		StartTime: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	principal := authctx.NewPrincipal(user.ID, user.Username, database.RBACScopeAssigned, map[string]bool{"monitor:read": true, "monitor:write": true})
	ctx := authctx.WithPrincipal(context.Background(), principal)
	authorize := mcpToolAuthorizer(db)
	if err := authorize(ctx, builtin.ToolWaitToolExecution, map[string]interface{}{"execution_id": "exec-owned"}); err != nil {
		t.Fatalf("owned execution denied: %v", err)
	}
	if err := authorize(ctx, builtin.ToolCancelToolExecution, map[string]interface{}{"execution_id": "exec-hidden"}); err == nil {
		t.Fatal("foreign execution was allowed")
	}
}

func TestMCPAssetToolAuthorizationUsesAssetPermissionsAndScope(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "mcp-asset-authz.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := database.NewRBAC(db).CreateRBACUser("asset-user", "Asset User", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	owned := &store.Asset{IP: "192.0.2.10", Port: 443, Protocol: "https"}
	hidden := &store.Asset{IP: "192.0.2.20", Port: 443, Protocol: "https"}
	if _, err := database.NewAssets(db).UpsertAssets([]*store.Asset{owned}, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.NewAssets(db).UpsertAssets([]*store.Asset{hidden}, ""); err != nil {
		t.Fatal(err)
	}

	permissions := map[string]bool{"asset:read": true, "asset:write": true}
	ctx := authctx.WithPrincipal(context.Background(), authctx.NewPrincipal(user.ID, user.Username, database.RBACScopeAssigned, permissions))
	authorize := mcpToolAuthorizer(db)
	if err := authorize(ctx, builtin.ToolQueryAssets, nil); err != nil {
		t.Fatalf("asset query denied: %v", err)
	}
	if err := authorize(ctx, builtin.ToolGetAsset, map[string]interface{}{"id": owned.ID}); err != nil {
		t.Fatalf("owned asset denied: %v", err)
	}
	if err := authorize(ctx, builtin.ToolGetAsset, map[string]interface{}{"id": hidden.ID}); err == nil {
		t.Fatal("unassigned asset was readable")
	}
	if err := authorize(ctx, builtin.ToolUpdateAsset, map[string]interface{}{"id": owned.ID}); err != nil {
		t.Fatalf("owned asset update denied: %v", err)
	}
	if err := authorize(ctx, builtin.ToolDeleteAsset, map[string]interface{}{"id": owned.ID}); err == nil {
		t.Fatal("asset delete without asset:delete was allowed")
	}
}

func TestExternalMCPRequiresDedicatedPermission(t *testing.T) {
	authorize := externalMCPToolAuthorizer()
	ctx := authctx.WithPrincipal(context.Background(), authctx.NewPrincipal("u1", "user", database.RBACScopeAssigned, map[string]bool{"agent:execute": true}))
	if err := authorize(ctx, "server::tool", nil); err == nil {
		t.Fatal("agent:execute alone authorized an external MCP tool")
	}
	ctx = authctx.WithPrincipal(context.Background(), authctx.NewPrincipal("u1", "user", database.RBACScopeAll, map[string]bool{"mcp:external:execute": true}))
	if err := authorize(ctx, "server::tool", nil); err != nil {
		t.Fatalf("dedicated external MCP permission rejected: %v", err)
	}
}

func TestConfiguredCommandToolRequiresLocalExecutePermission(t *testing.T) {
	// A recipe is only executable when its manifest is registered. This replaces
	// the old default branch, where any tool name outside the builtin switch fell
	// through to agent:local-execute.
	rejections, err := RefreshRecipeLayer([]config.ToolConfig{{
		Name:    "nmap_scan",
		Command: "nmap",
		Capability: &config.CapabilityManifest{
			ID: "core.nmap_scan", Class: "mutating",
			Permission: "agent:local-execute", Grants: []string{"net.connect(target)"},
		},
	}})
	if err != nil {
		t.Fatalf("recipe registration failed: %v (%+v)", err, rejections)
	}
	t.Cleanup(func() {
		_, _ = RefreshRecipeLayer(nil)
	})

	authorize := mcpToolAuthorizer(nil)
	agentOnly := authctx.WithPrincipal(context.Background(), authctx.NewPrincipal("u1", "user", database.RBACScopeAssigned, map[string]bool{"agent:execute": true}))
	if err := authorize(agentOnly, "nmap_scan", nil); err == nil {
		t.Fatal("agent:execute alone authorized a configured command tool")
	}
	local := authctx.WithPrincipal(context.Background(), authctx.NewPrincipal("u1", "user", database.RBACScopeAssigned, map[string]bool{"agent:local-execute": true}))
	if err := authorize(local, "nmap_scan", nil); err != nil {
		t.Fatalf("agent:local-execute rejected: %v", err)
	}
	// Undeclared names must fail closed instead of inheriting the coarse permission.
	if err := authorize(local, "community_unregistered_tool", nil); err == nil {
		t.Fatal("an unregistered capability was allowed by the coarse fallback")
	}
}
