package database

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

func newRBACTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := NewDB(filepath.Join(t.TempDir(), "rbac.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRBACToolExecutionOwnershipAccess(t *testing.T) {
	db := newRBACTestDB(t)
	for _, exec := range []*mcp.ToolExecution{
		{ID: "exec-u1", ToolName: "one", Status: "completed", StartTime: time.Now(), OwnerUserID: "u1"},
		{ID: "exec-u2", ToolName: "two", Status: "completed", StartTime: time.Now(), OwnerUserID: "u2"},
		{ID: "exec-legacy", ToolName: "legacy", Status: "completed", StartTime: time.Now()},
	} {
		if err := NewMonitor(db).SaveToolExecution(exec); err != nil {
			t.Fatal(err)
		}
	}
	access := store.Access{UserID: "u1", Scope: RBACScopeAssigned}
	rows, err := NewMonitor(db).LoadToolExecutionListPageForAccess(0, 20, "", "", access)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "exec-u1" {
		t.Fatalf("rows = %#v, want only exec-u1", rows)
	}
	summary, err := NewMonitor(db).LoadToolStatsSummaryForAccess(10, access)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Summary.TotalCalls != 1 || summary.Summary.ToolCount != 1 || len(summary.TopTools) != 1 || summary.TopTools[0].ToolName != "one" {
		t.Fatalf("scoped summary = %#v", summary)
	}
	if !NewMonitor(db).UserCanAccessToolExecution("u1", RBACScopeAssigned, "exec-u1") {
		t.Fatal("owner could not access execution")
	}
	if NewMonitor(db).UserCanAccessToolExecution("u1", RBACScopeAssigned, "exec-u2") {
		t.Fatal("foreign execution was accessible")
	}
	if NewMonitor(db).UserCanAccessToolExecution("u1", RBACScopeAssigned, "exec-legacy") {
		t.Fatal("ownerless legacy execution did not fail closed")
	}
}

func TestSystemRoleBootstrapDoesNotLeakManagementReadPermissions(t *testing.T) {
	db := newRBACTestDB(t)
	catalog := map[string]string{
		"auth:self": "self", "project:read": "projects", "project:write": "project writes",
		"agent:local-execute": "local tools",
		"rbac:read":           "rbac", "config:read": "config", "audit:read": "audit", "terminal:execute": "terminal",
		"mcp:execute": "invoke", "mcp:write": "manage", "mcp:external:execute": "external invoke",
		"workflow:execute": "run", "workflow:write": "manage definitions", "knowledge:write": "manage knowledge",
	}
	if err := NewRBAC(db).BootstrapRBAC("hash", catalog); err != nil {
		t.Fatal(err)
	}
	viewer, err := NewRBAC(db).CreateRBACUser("viewer-policy", "Viewer", "hash", true, []string{RBACSystemRoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	viewerAccess, err := NewRBAC(db).ResolveRBACAccess(viewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !viewerAccess.Permissions["project:read"] || viewerAccess.Permissions["rbac:read"] || viewerAccess.Permissions["config:read"] || viewerAccess.Permissions["audit:read"] {
		t.Fatalf("unexpected viewer permissions: %#v", viewerAccess.Permissions)
	}
	auditor, err := NewRBAC(db).CreateRBACUser("auditor-policy", "Auditor", "hash", true, []string{RBACSystemRoleAuditor})
	if err != nil {
		t.Fatal(err)
	}
	auditorAccess, err := NewRBAC(db).ResolveRBACAccess(auditor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !auditorAccess.Permissions["audit:read"] || auditorAccess.Permissions["config:read"] || auditorAccess.Permissions["rbac:read"] {
		t.Fatalf("unexpected auditor permissions: %#v", auditorAccess.Permissions)
	}
	operator, err := NewRBAC(db).CreateRBACUser("operator-policy", "Operator", "hash", true, []string{RBACSystemRoleOperator})
	if err != nil {
		t.Fatal(err)
	}
	operatorAccess, err := NewRBAC(db).ResolveRBACAccess(operator.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !operatorAccess.Permissions["mcp:execute"] || operatorAccess.Permissions["mcp:write"] || operatorAccess.Permissions["mcp:external:execute"] {
		t.Fatalf("unexpected operator MCP permissions: %#v", operatorAccess.Permissions)
	}
	if !operatorAccess.Permissions["workflow:execute"] || operatorAccess.Permissions["workflow:write"] || operatorAccess.Permissions["knowledge:write"] {
		t.Fatalf("operator received global definition mutation permissions: %#v", operatorAccess.Permissions)
	}
	if !operatorAccess.Permissions["agent:local-execute"] {
		t.Fatalf("operator is missing explicit local tool permission: %#v", operatorAccess.Permissions)
	}
}

func TestPermissionScopeDoesNotWidenAcrossUnrelatedRoles(t *testing.T) {
	db := newRBACTestDB(t)
	catalog := map[string]string{"auth:self": "self", "project:read": "read", "project:write": "write", "audit:read": "audit"}
	if err := NewRBAC(db).BootstrapRBAC("hash", catalog); err != nil {
		t.Fatal(err)
	}
	ownWrite, err := NewRBAC(db).UpsertRBACRole("", "own-writer", "", store.ScopeOwn, []string{"project:write"})
	if err != nil {
		t.Fatal(err)
	}
	user, err := NewRBAC(db).CreateRBACUser("mixed-scope", "Mixed", "hash", true, []string{RBACSystemRoleAuditor, ownWrite.ID})
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewRBAC(db).ResolveRBACAccess(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if access.Scope != store.ScopeAll {
		t.Fatalf("compatibility scope = %q, want all", access.Scope)
	}
	if got := access.PermissionScopes["project:read"]; got != store.ScopeAll {
		t.Fatalf("project:read scope = %q, want all", got)
	}
	if got := access.PermissionScopes["project:write"]; got != store.ScopeOwn {
		t.Fatalf("project:write scope widened to %q, want own", got)
	}
}

func TestRoleRejectsUnknownPermission(t *testing.T) {
	db := newRBACTestDB(t)
	if err := NewRBAC(db).BootstrapRBAC("hash", map[string]string{"auth:self": "self"}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRBAC(db).UpsertRBACRole("", "future-role", "", RBACScopeAssigned, []string{"future:permission"}); err == nil {
		t.Fatal("unknown permission was persisted")
	}
	if _, err := db.Exec(`INSERT INTO rbac_permissions (key, description, created_at) VALUES ('stale:permission', '', ?)`, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := NewRBAC(db).BootstrapRBAC("hash", map[string]string{"auth:self": "self"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rbac_permissions WHERE key = 'stale:permission'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale permission survived bootstrap: count=%d err=%v", count, err)
	}
}

func TestRBACProjectAndConversationListAccess(t *testing.T) {
	db := newRBACTestDB(t)
	p1, _ := db.CreateProject(&Project{Name: "visible"})
	p2, _ := db.CreateProject(&Project{Name: "hidden"})
	if err := NewRBAC(db).SetResourceOwner("project", p1.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	c1, _ := NewConversations(db).CreateConversation("visible conv", ConversationCreateMeta{ProjectID: p1.ID})
	c2, _ := NewConversations(db).CreateConversation("hidden conv", ConversationCreateMeta{ProjectID: p2.ID})
	_ = NewRBAC(db).SetResourceOwner("conversation", c1.ID, "u1")
	_ = NewRBAC(db).SetResourceOwner("conversation", c2.ID, "u2")

	projects, err := db.ListProjectsForAccess("", "", 50, 0, "u1", store.ScopeOwn)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ID != p1.ID {
		t.Fatalf("projects = %#v, want only %s", projects, p1.ID)
	}

	convs, err := NewConversations(db).ListConversationsForAccess(50, 0, "", "", "", "u1", store.ScopeOwn)
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 1 || convs[0].ID != c1.ID {
		t.Fatalf("conversations = %#v, want only %s", convs, c1.ID)
	}
}

func TestRBACVulnerabilityAccessInheritsProject(t *testing.T) {
	db := newRBACTestDB(t)
	user, err := NewRBAC(db).CreateRBACUser("u1", "User 1", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	p1, _ := db.CreateProject(&Project{Name: "visible"})
	p2, _ := db.CreateProject(&Project{Name: "hidden"})
	if err := NewRBAC(db).AssignResourceToUser(user.ID, "project", p1.ID); err != nil {
		t.Fatal(err)
	}
	v1, _ := NewFindings(db).Create(&store.Vulnerability{ProjectID: p1.ID, Title: "v1", Severity: "high"})
	v2, _ := NewFindings(db).Create(&store.Vulnerability{ProjectID: p2.ID, Title: "v2", Severity: "high"})

	items, err := NewFindings(db).List(50, 0, store.VulnerabilityListFilter{}, store.Access{UserID: user.ID, Scope: RBACScopeAssigned})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != v1.ID {
		t.Fatalf("vulnerabilities = %#v, want only %s; hidden %s", items, v1.ID, v2.ID)
	}
	if !NewRBAC(db).UserCanAccessResource(user.ID, RBACScopeAssigned, "vulnerability", v1.ID) {
		t.Fatalf("expected project assignment to allow vulnerability detail")
	}
	if NewRBAC(db).UserCanAccessResource(user.ID, RBACScopeAssigned, "vulnerability", v2.ID) {
		t.Fatalf("unexpected access to hidden vulnerability")
	}
}

func TestRBACConversationAccessInheritsProject(t *testing.T) {
	db := newRBACTestDB(t)
	user, err := NewRBAC(db).CreateRBACUser("project-member", "Project Member", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(&Project{Name: "assigned project"})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := NewConversations(db).CreateConversation("project conversation", ConversationCreateMeta{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewRBAC(db).AssignResourceToUser(user.ID, "project", project.ID); err != nil {
		t.Fatal(err)
	}

	rows, err := NewConversations(db).ListConversationsForAccess(50, 0, "", "", "", user.ID, RBACScopeAssigned)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != conversation.ID {
		t.Fatalf("conversations = %#v, want project conversation %s", rows, conversation.ID)
	}
	if !NewRBAC(db).UserCanAccessResource(user.ID, RBACScopeAssigned, "conversation", conversation.ID) {
		t.Fatal("expected project assignment to allow conversation detail")
	}
}

func TestRBACBatchResourceAssignmentValidationAndAtomicity(t *testing.T) {
	db := newRBACTestDB(t)
	user, err := NewRBAC(db).CreateRBACUser("batch-member", "Batch Member", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := db.CreateProject(&Project{Name: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := db.CreateProject(&Project{Name: "p2"})
	if err != nil {
		t.Fatal(err)
	}
	p3, err := db.CreateProject(&Project{Name: "p3"})
	if err != nil {
		t.Fatal(err)
	}
	options, err := NewRBAC(db).ListAssignableRBACResourcesPage("project", "p1", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].ID != p1.ID || options[0].Label != "p1" {
		t.Fatalf("resource options = %#v, want p1", options)
	}
	firstPage, err := NewRBAC(db).ListAssignableRBACResourcesPage("project", "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	secondPage, err := NewRBAC(db).ListAssignableRBACResourcesPage("project", "", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 2 || len(secondPage) != 1 {
		t.Fatalf("paged resource options = %d + %d, want 2 + 1", len(firstPage), len(secondPage))
	}
	seen := map[string]bool{}
	for _, option := range append(firstPage, secondPage...) {
		seen[option.ID] = true
	}
	if !seen[p1.ID] || !seen[p2.ID] || !seen[p3.ID] {
		t.Fatalf("paged resource options missed resources: %#v", seen)
	}
	if _, err := NewRBAC(db).ListAssignableRBACResourcesPage("secret_table", "", 50, 0); err == nil {
		t.Fatal("expected unsupported picker resource type to fail")
	}

	if _, err := NewRBAC(db).AssignResourcesToUser(user.ID, "unknown_type", []string{p1.ID}); err == nil {
		t.Fatal("expected unsupported resource type to fail")
	}
	if _, err := NewRBAC(db).AssignResourcesToUser(user.ID, "project", []string{p1.ID, "missing-project"}); err == nil {
		t.Fatal("expected missing resource to fail the entire batch")
	}
	rows, err := NewRBAC(db).ListRBACResourceAssignments(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("partial grants persisted after failed batch: %#v", rows)
	}

	created, err := NewRBAC(db).AssignResourcesToUser(user.ID, "project", []string{p1.ID, p1.ID, p2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if created != 2 {
		t.Fatalf("created = %d, want 2 unique grants", created)
	}
	created, err = NewRBAC(db).AssignResourcesToUser(user.ID, "project", []string{p1.ID, p2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("idempotent retry created = %d, want 0", created)
	}
	rows, err = NewRBAC(db).ListRBACResourceAssignments(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("assignment count = %d, want 2", len(rows))
	}
}

func TestRBACWebshellAndBatchListAccess(t *testing.T) {
	db := newRBACTestDB(t)
	ws1 := store.WebShellConnection{ID: "ws_visible", ProjectID: "p1", URL: "http://a", Type: "php", Method: "post", CreatedAt: time.Now()}
	ws2 := store.WebShellConnection{ID: "ws_hidden", ProjectID: "p2", URL: "http://b", Type: "php", Method: "post", CreatedAt: time.Now()}
	ws3 := store.WebShellConnection{ID: "ws_other_project", ProjectID: "p2", URL: "http://c", Type: "php", Method: "post", CreatedAt: time.Now()}
	ws4 := store.WebShellConnection{ID: "ws_unbound", URL: "http://d", Type: "php", Method: "post", CreatedAt: time.Now()}
	if err := NewWebshell(db).Create(&ws1); err != nil {
		t.Fatal(err)
	}
	if err := NewWebshell(db).Create(&ws2); err != nil {
		t.Fatal(err)
	}
	if err := NewWebshell(db).Create(&ws3); err != nil {
		t.Fatal(err)
	}
	if err := NewWebshell(db).Create(&ws4); err != nil {
		t.Fatal(err)
	}
	_ = NewRBAC(db).SetResourceOwner("webshell", ws1.ID, "u1")
	_ = NewRBAC(db).SetResourceOwner("webshell", ws2.ID, "u2")
	_ = NewRBAC(db).SetResourceOwner("webshell", ws3.ID, "u1")
	_ = NewRBAC(db).SetResourceOwner("webshell", ws4.ID, "u1")
	webshells, err := NewWebshell(db).List(store.Access{UserID: "u1", Scope: store.ScopeOwn}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(webshells) != 3 {
		t.Fatalf("webshells = %#v, want 3 owned webshells including unbound", webshells)
	}
	webshells, err = NewWebshell(db).List(store.Access{UserID: "u1", Scope: store.ScopeOwn}, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(webshells) != 1 || webshells[0].ID != ws1.ID {
		t.Fatalf("webshells scoped to p1 = %#v, want only %s", webshells, ws1.ID)
	}
	webshells, err = NewWebshell(db).List(store.Access{UserID: "u1", Scope: store.ScopeOwn}, store.ProjectUnbound)
	if err != nil {
		t.Fatal(err)
	}
	if len(webshells) != 1 || webshells[0].ID != ws4.ID {
		t.Fatalf("unbound webshells = %#v, want only %s", webshells, ws4.ID)
	}

	if err := NewBatchTasks(db).CreateBatchQueue("q_visible", "visible", "", "eino_single", "manual", "", nil, "", 1, []map[string]interface{}{{"id": "t1", "message": "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := NewBatchTasks(db).CreateBatchQueue("q_hidden", "hidden", "", "eino_single", "manual", "", nil, "", 1, []map[string]interface{}{{"id": "t2", "message": "b"}}); err != nil {
		t.Fatal(err)
	}
	_ = NewRBAC(db).SetResourceOwner("batch_task", "q_visible", "u1")
	_ = NewRBAC(db).SetResourceOwner("batch_task", "q_hidden", "u2")
	queues, err := NewBatchTasks(db).ListBatchQueuesForAccess(50, 0, "all", "", "u1", store.ScopeOwn)
	if err != nil {
		t.Fatal(err)
	}
	if len(queues) != 1 || queues[0].ID != "q_visible" {
		t.Fatalf("queues = %#v, want only q_visible", queues)
	}
}

func TestRBACC2AccessInheritsListener(t *testing.T) {
	db := newRBACTestDB(t)
	c2store := NewC2(db)
	now := time.Now()
	l1 := &store.C2Listener{ID: "l_visible", ProjectID: "p1", Name: "visible", Type: "http_beacon", BindHost: "127.0.0.1", BindPort: 9001, OwnerUserID: "u1", CreatedAt: now}
	l2 := &store.C2Listener{ID: "l_hidden", ProjectID: "p2", Name: "hidden", Type: "http_beacon", BindHost: "127.0.0.1", BindPort: 9002, OwnerUserID: "u2", CreatedAt: now}
	l3 := &store.C2Listener{ID: "l_other_project", ProjectID: "p2", Name: "other project", Type: "http_beacon", BindHost: "127.0.0.1", BindPort: 9003, OwnerUserID: "u1", CreatedAt: now}
	l4 := &store.C2Listener{ID: "l_unbound", Name: "unbound", Type: "http_beacon", BindHost: "127.0.0.1", BindPort: 9004, OwnerUserID: "u1", CreatedAt: now}
	if err := c2store.CreateC2Listener(l1); err != nil {
		t.Fatal(err)
	}
	if err := c2store.CreateC2Listener(l2); err != nil {
		t.Fatal(err)
	}
	if err := c2store.CreateC2Listener(l3); err != nil {
		t.Fatal(err)
	}
	if err := c2store.CreateC2Listener(l4); err != nil {
		t.Fatal(err)
	}
	if err := c2store.UpsertC2Session(&store.C2Session{ID: "s_visible", ListenerID: l1.ID, ImplantUUID: "implant-visible", Status: "active", FirstSeenAt: now, LastCheckIn: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.UpsertC2Session(&store.C2Session{ID: "s_hidden", ListenerID: l2.ID, ImplantUUID: "implant-hidden", Status: "active", FirstSeenAt: now, LastCheckIn: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.UpsertC2Session(&store.C2Session{ID: "s_other_project", ListenerID: l3.ID, ImplantUUID: "implant-other-project", Status: "active", FirstSeenAt: now, LastCheckIn: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.UpsertC2Session(&store.C2Session{ID: "s_unbound", ListenerID: l4.ID, ImplantUUID: "implant-unbound", Status: "active", FirstSeenAt: now, LastCheckIn: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.CreateC2Task(&store.C2Task{ID: "t_visible", SessionID: "s_visible", TaskType: "shell", Status: "queued", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.CreateC2Task(&store.C2Task{ID: "t_hidden", SessionID: "s_hidden", TaskType: "shell", Status: "queued", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.CreateC2Task(&store.C2Task{ID: "t_other_project", SessionID: "s_other_project", TaskType: "shell", Status: "queued", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.CreateC2Task(&store.C2Task{ID: "t_unbound", SessionID: "s_unbound", TaskType: "shell", Status: "queued", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.AppendC2Event(&store.C2Event{ID: "e_visible", Level: "info", Category: "task", SessionID: "s_visible", TaskID: "t_visible", Message: "visible", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.AppendC2Event(&store.C2Event{ID: "e_hidden", Level: "info", Category: "task", SessionID: "s_hidden", TaskID: "t_hidden", Message: "hidden", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.AppendC2Event(&store.C2Event{ID: "e_other_project", Level: "info", Category: "task", SessionID: "s_other_project", TaskID: "t_other_project", Message: "other project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.AppendC2Event(&store.C2Event{ID: "e_unbound", Level: "info", Category: "task", SessionID: "s_unbound", TaskID: "t_unbound", Message: "unbound", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	access := store.Access{UserID: "u1", Scope: store.ScopeOwn}
	listeners, err := c2store.ListC2ListenersForAccess(access, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 3 {
		t.Fatalf("listeners = %#v, want 3 owned listeners including unbound", listeners)
	}
	listeners, err = c2store.ListC2ListenersForAccess(access, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 1 || listeners[0].ID != l1.ID {
		t.Fatalf("listeners scoped to p1 = %#v, want only %s", listeners, l1.ID)
	}
	listeners, err = c2store.ListC2ListenersForAccess(access, store.ProjectUnbound)
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 1 || listeners[0].ID != l4.ID {
		t.Fatalf("unbound listeners = %#v, want only %s", listeners, l4.ID)
	}
	sessions, err := c2store.ListC2SessionsForAccess(store.ListC2SessionsFilter{}, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("sessions = %#v, want 3 owned sessions including unbound", sessions)
	}
	sessions, err = c2store.ListC2SessionsForAccess(store.ListC2SessionsFilter{ProjectID: "p1"}, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "s_visible" {
		t.Fatalf("sessions scoped to p1 = %#v, want only s_visible", sessions)
	}
	sessions, err = c2store.ListC2SessionsForAccess(store.ListC2SessionsFilter{ProjectID: store.ProjectUnbound}, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "s_unbound" {
		t.Fatalf("unbound sessions = %#v, want only s_unbound", sessions)
	}
	tasks, err := c2store.ListC2TasksForAccess(store.ListC2TasksFilter{}, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks = %#v, want 3 owned tasks including unbound", tasks)
	}
	tasks, err = c2store.ListC2TasksForAccess(store.ListC2TasksFilter{ProjectID: "p1"}, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "t_visible" {
		t.Fatalf("tasks scoped to p1 = %#v, want only t_visible", tasks)
	}
	tasks, err = c2store.ListC2TasksForAccess(store.ListC2TasksFilter{ProjectID: store.ProjectUnbound}, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "t_unbound" {
		t.Fatalf("unbound tasks = %#v, want only t_unbound", tasks)
	}
	events, err := c2store.ListC2EventsForAccess(store.ListC2EventsFilter{}, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %#v, want 3 owned events including unbound", events)
	}
	events, err = c2store.ListC2EventsForAccess(store.ListC2EventsFilter{ProjectID: "p1"}, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "e_visible" {
		t.Fatalf("events scoped to p1 = %#v, want only e_visible", events)
	}
	events, err = c2store.ListC2EventsForAccess(store.ListC2EventsFilter{ProjectID: store.ProjectUnbound}, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "e_unbound" {
		t.Fatalf("unbound events = %#v, want only e_unbound", events)
	}
	if !NewRBAC(db).UserCanAccessResource("u1", store.ScopeOwn, "c2_task", "t_visible") {
		t.Fatalf("expected listener ownership to allow task detail")
	}
	if NewRBAC(db).UserCanAccessResource("u1", store.ScopeOwn, "c2_task", "t_hidden") {
		t.Fatalf("unexpected access to hidden task")
	}
}

func TestRBACC2AssignedDeleteIsScoped(t *testing.T) {
	db := newRBACTestDB(t)
	c2store := NewC2(db)
	user, err := NewRBAC(db).CreateRBACUser("u1", "User 1", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := c2store.CreateC2Listener(&store.C2Listener{ID: "l_assigned", Name: "assigned", Type: "http_beacon", BindHost: "127.0.0.1", BindPort: 9001, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := c2store.CreateC2Listener(&store.C2Listener{ID: "l_hidden", Name: "hidden", Type: "http_beacon", BindHost: "127.0.0.1", BindPort: 9002, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := NewRBAC(db).AssignResourceToUser(user.ID, "c2_listener", "l_assigned"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		sessionID string
		listener  string
		taskID    string
		eventID   string
	}{
		{"s_assigned", "l_assigned", "t_assigned", "e_assigned"},
		{"s_hidden", "l_hidden", "t_hidden", "e_hidden"},
	} {
		if err := c2store.UpsertC2Session(&store.C2Session{ID: row.sessionID, ListenerID: row.listener, ImplantUUID: row.sessionID + "_uuid", Status: "active", FirstSeenAt: now, LastCheckIn: now}); err != nil {
			t.Fatal(err)
		}
		if err := c2store.CreateC2Task(&store.C2Task{ID: row.taskID, SessionID: row.sessionID, TaskType: "shell", Status: "queued", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := c2store.AppendC2Event(&store.C2Event{ID: row.eventID, Level: "info", Category: "task", SessionID: row.sessionID, TaskID: row.taskID, Message: row.eventID, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	access := store.Access{UserID: user.ID, Scope: RBACScopeAssigned}
	n, err := c2store.DeleteC2TasksByIDsForAccess([]string{"t_assigned", "t_hidden"}, access)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted tasks = %d, want 1", n)
	}
	if task, _ := c2store.GetC2Task("t_hidden"); task == nil {
		t.Fatalf("hidden task was deleted")
	}
	n, err = c2store.DeleteC2EventsByIDsForAccess([]string{"e_assigned", "e_hidden"}, access)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted events = %d, want 1", n)
	}
	hiddenEvents, err := c2store.ListC2EventsForAccess(store.ListC2EventsFilter{TaskID: "t_hidden"}, store.Access{Scope: store.ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(hiddenEvents) != 1 {
		t.Fatalf("hidden event count = %d, want 1", len(hiddenEvents))
	}
}

func TestRBACAssignmentLabelsAndWeakTitles(t *testing.T) {
	db := newRBACTestDB(t)
	user, err := NewRBAC(db).CreateRBACUser("label-member", "Label Member", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(&Project{Name: "Alpha Project"})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := NewConversations(db).CreateConversation("1", ConversationCreateMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRBAC(db).AssignResourcesToUser(user.ID, "project", []string{project.ID}); err != nil {
		t.Fatal(err)
	}

	options, err := NewRBAC(db).ListAssignableRBACResourcesPage("conversation", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) == 0 {
		t.Fatal("expected conversation options")
	}
	for _, option := range options {
		if option.ID == conversation.ID && !strings.Contains(option.Label, "1 ·") {
			t.Fatalf("weak conversation label = %q, want suffix with short id", option.Label)
		}
	}

	rows, err := NewRBAC(db).ListRBACResourceAssignments(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("assignments = %#v, want 1", rows)
	}
	if rows[0].ResourceLabel != "Alpha Project" {
		t.Fatalf("assignment label = %q, want Alpha Project", rows[0].ResourceLabel)
	}
}

func TestDeleteRBACResourceAssignmentWithDetails(t *testing.T) {
	db := newRBACTestDB(t)
	user, err := NewRBAC(db).CreateRBACUser("revoke-member", "Revoke Member", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(&Project{Name: "Revoked Project"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRBAC(db).AssignResourcesToUser(user.ID, "project", []string{project.ID}); err != nil {
		t.Fatal(err)
	}
	rows, err := NewRBAC(db).ListRBACResourceAssignments(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("assignments = %#v, want 1", rows)
	}

	deleted, err := NewRBAC(db).DeleteRBACResourceAssignmentWithDetails(rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.ID != rows[0].ID || deleted.UserID != user.ID || deleted.ResourceType != "project" || deleted.ResourceID != project.ID {
		t.Fatalf("deleted assignment = %#v", deleted)
	}
	remaining, err := NewRBAC(db).ListRBACResourceAssignments(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining assignments = %#v, want none", remaining)
	}
	if _, err := NewRBAC(db).DeleteRBACResourceAssignmentWithDetails(rows[0].ID); err == nil {
		t.Fatal("second delete unexpectedly succeeded")
	}
}
