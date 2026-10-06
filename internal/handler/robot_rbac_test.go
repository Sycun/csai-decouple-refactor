package handler

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"

	"go.uber.org/zap"
)

func TestRobotUsersAreResourceIsolated(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "robot-rbac.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := &config.Config{}
	cfg.Project.Enabled = true
	h := NewRobotHandler(cfg, db, nil, zap.NewNop())
	if err := database.NewRBAC(db).BootstrapRBAC("hash", security.PermissionCatalog); err != nil {
		t.Fatal(err)
	}
	if err := h.identity.EnsureSchema(); err != nil {
		t.Fatalf("ensure robot identity schema: %v", err)
	}
	alice, err := database.NewRBAC(db).CreateRBACUser("robot-alice", "Robot Alice", "hash", true, []string{database.RBACSystemRoleOperator})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := database.NewRBAC(db).CreateRBACUser("robot-bob", "Robot Bob", "hash", true, []string{database.RBACSystemRoleOperator})
	if err != nil {
		t.Fatal(err)
	}
	for externalID, user := range map[string]*database.RBACUser{"alice": alice, "bob": bob} {
		code := "TEST-" + strings.ToUpper(externalID)
		if err := h.identity.CreateBindingCode(user.ID, hashRobotBindingCode(code), time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		boundID, err := h.identity.ConsumeBindingCode("wecom", externalID, hashRobotBindingCode(code))
		if err != nil {
			t.Fatal(err)
		}
		_ = boundID
	}
	boundID, found, err := h.identity.ResolveBoundUser("wecom", "alice")
	if err != nil || !found {
		t.Fatalf("alice's binding did not resolve: found=%v err=%v", found, err)
	}
	aliceAccess, err := h.rbac.ResolveRBACAccess(boundID)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.HandleMessage("wecom", "alice", "身份"); !strings.Contains(got, "Robot Alice") || !strings.Contains(got, alice.ID) || !strings.Contains(got, "user_binding") {
		t.Fatalf("bound identity output is incomplete: %s", got)
	}

	conversationID, _ := h.getOrCreateConversation("wecom", "alice", "alice conversation", aliceAccess)
	if conversationID == "" {
		t.Fatal("alice conversation was not created")
	}
	if got := h.cmdList("wecom", "bob"); strings.Contains(got, conversationID) {
		t.Fatalf("bob listed alice conversation: %s", got)
	}
	if got := h.cmdSwitch("wecom", "bob", conversationID); !strings.Contains(got, "不存在") && !strings.Contains(got, "无权访问") {
		t.Fatalf("bob switched to alice conversation: %s", got)
	}
	if got := h.cmdDelete("wecom", "bob", conversationID); !strings.Contains(got, "无权访问") {
		t.Fatalf("bob deleted alice conversation: %s", got)
	}
	if _, err := db.GetConversation(conversationID); err != nil {
		t.Fatalf("alice conversation was deleted: %v", err)
	}

	createReply := h.cmdNewProject("wecom", "alice", "alice project")
	if !strings.Contains(createReply, "已创建项目") {
		t.Fatalf("create project reply: %s", createReply)
	}
	if got := h.cmdProjects("wecom", "bob"); strings.Contains(got, "alice project") {
		t.Fatalf("bob listed alice project: %s", got)
	}
}

func TestWecomReplayGuardRequiresFreshUniqueRequest(t *testing.T) {
	h := NewRobotHandler(&config.Config{}, nil, nil, zap.NewNop())
	timestamp := time.Now().Unix()
	if !h.acceptFreshWecomRequest(fmt.Sprintf("%d", timestamp), "nonce", "signature") {
		t.Fatal("fresh request was rejected")
	}
	if h.acceptFreshWecomRequest(fmt.Sprintf("%d", timestamp), "nonce", "signature") {
		t.Fatal("duplicate request was accepted")
	}
	if h.acceptFreshWecomRequest(fmt.Sprintf("%d", timestamp-600), "old", "signature") {
		t.Fatal("stale request was accepted")
	}
}

func TestRobotServiceAccountRequiresExactSenderAllowlist(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "robot-service.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.NewRBAC(db).BootstrapRBAC("hash", security.PermissionCatalog); err != nil {
		t.Fatal(err)
	}
	serviceUser, err := database.NewRBAC(db).CreateRBACUser("robot-service-user", "Robot Service", "hash", true, []string{database.RBACSystemRoleOperator})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Robots.Lark.Auth = config.RobotAuthorizationConfig{
		Mode: config.RobotAuthModeServiceAccount, ServiceUserID: serviceUser.ID,
		AllowedExternalUsers: []string{"t:tenant|u:allowed"},
	}
	h := NewRobotHandler(cfg, db, nil, zap.NewNop())
	if got := h.HandleMessage("lark", "t:tenant|u:allowed", "列表"); strings.Contains(got, "白名单") || strings.Contains(got, "尚未绑定") {
		t.Fatalf("allowed service account sender was denied: %s", got)
	}
	if got := h.HandleMessage("lark", "t:tenant|u:denied", "列表"); !strings.Contains(got, "白名单") {
		t.Fatalf("non-allowlisted sender was not denied: %s", got)
	}
	if got := h.HandleMessage("lark", "t:tenant|u:allowed", "绑定 ABCD-1234"); !strings.Contains(got, "服务账号模式") {
		t.Fatalf("service-account robot accepted user binding: %s", got)
	}
	if got := h.HandleMessage("lark", "t:tenant|u:allowed", "whoami"); !strings.Contains(got, "Robot Service") || !strings.Contains(got, serviceUser.ID) || !strings.Contains(got, "service_account") {
		t.Fatalf("service-account identity output is incomplete: %s", got)
	}
	if got := h.HandleMessage("lark", "t:tenant|u:denied", "whoami"); !strings.Contains(got, "鉴权状态：拒绝") || strings.Contains(got, serviceUser.ID) {
		t.Fatalf("denied identity output leaked or omitted status: %s", got)
	}

	adminCfg := &config.Config{}
	adminCfg.Robots.Lark.Auth = config.RobotAuthorizationConfig{
		Mode: config.RobotAuthModeServiceAccount, ServiceUserID: "admin",
		AllowedExternalUsers: []string{"t:tenant|u:owner"},
	}
	adminHandler := NewRobotHandler(adminCfg, db, nil, zap.NewNop())
	if got := adminHandler.HandleMessage("lark", "t:tenant|u:owner", "身份"); !strings.Contains(got, "admin") || !strings.Contains(got, "鉴权状态：已授权") {
		t.Fatalf("allowlisted admin service account was denied: %s", got)
	}
}
