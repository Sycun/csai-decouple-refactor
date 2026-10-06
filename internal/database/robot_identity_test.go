package database_test

import (
	"testing"
	"time"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// The binding tables belong to store.RobotIdentity now; the RBAC record and what it may do belong to
// the data layer. These two tests walk the composition exactly as the robot handler does it, because
// that split is the thing worth pinning: a binding answers "which account", never "what is allowed".

func TestRobotBindingCodeIsSingleUseAndPermissionsAreResolvedLive(t *testing.T) {
	db, err := database.NewDB(t.TempDir()+"/robot-identity.db", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.BootstrapRBAC("hash", security.PermissionCatalog); err != nil {
		t.Fatal(err)
	}
	// Start-up creates these tables through the store that owns them; a test that builds the data layer
	// directly has to make the same call.
	identity := store.NewRobotIdentity(db.DB)
	if err := identity.EnsureSchema(); err != nil {
		t.Fatalf("ensure robot identity schema: %v", err)
	}
	user, err := db.CreateRBACUser("bound-user", "Bound User", "hash", true, []string{database.RBACSystemRoleOperator})
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.CreateBindingCode(user.ID, "code-hash", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The platform name is case-insensitive and both halves are trimmed on the way in.
	bound, err := identity.ConsumeBindingCode("LARK", "t:tenant|u:user", "code-hash")
	if err != nil || bound != user.ID {
		t.Fatalf("consume binding code: user=%q err=%v", bound, err)
	}
	if _, err := identity.ConsumeBindingCode("lark", "t:tenant|u:other", "code-hash"); err == nil {
		t.Fatal("single-use binding code was accepted twice")
	}
	resolved, found, err := identity.ResolveBoundUser("lark", "t:tenant|u:user")
	if err != nil || !found || resolved != user.ID {
		t.Fatalf("resolve bound user: %q found=%v err=%v", resolved, found, err)
	}
	access, err := db.ResolveRBACAccess(resolved)
	if err != nil || !access.Permissions["agent:execute"] {
		t.Fatalf("resolved access does not include live role permissions: %#v err=%v", access, err)
	}
	// Disabling the account takes the robot's authority away without deleting the binding row: the
	// resolution joins on enabled=1 rather than the binding being unmade.
	disabled := false
	if err := db.UpdateRBACUser(user.ID, user.DisplayName, &disabled, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, err := identity.ResolveBoundUser("lark", "t:tenant|u:user"); err != nil || found {
		t.Fatalf("a disabled account still resolved as a binding: found=%v err=%v", found, err)
	}
}

func TestRobotBindingCodeExpiryAndOwnerScopedRevocation(t *testing.T) {
	db, err := database.NewDB(t.TempDir()+"/robot-revoke.db", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.BootstrapRBAC("hash", security.PermissionCatalog); err != nil {
		t.Fatal(err)
	}
	identity := store.NewRobotIdentity(db.DB)
	if err := identity.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	u1, err := db.CreateRBACUser("binding-owner", "Owner", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	u2, err := db.CreateRBACUser("binding-other", "Other", "hash", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := db.Exec(`INSERT INTO robot_binding_codes (code_hash, rbac_user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`, "expired-hash", u1.ID, now.Add(-time.Minute), now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.ConsumeBindingCode("wecom", "t:corp|u:expired", "expired-hash"); err == nil {
		t.Fatal("expired binding code was accepted")
	}
	if err := identity.CreateBindingCode(u1.ID, "valid-hash", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.ConsumeBindingCode("wecom", "t:corp|u:one", "valid-hash"); err != nil {
		t.Fatal(err)
	}
	bindings, err := identity.ListBindings(u1.ID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%v err=%v", bindings, err)
	}
	// The binding id alone is not a capability: the owner is part of the delete predicate.
	if err := identity.DeleteBindingForUser(bindings[0].ID, u2.ID); err == nil {
		t.Fatal("another user revoked a binding they do not own")
	}
	if _, found, err := identity.ResolveBoundUser("wecom", "t:corp|u:one"); err != nil || !found {
		t.Fatalf("the unauthorized attempt changed the binding: found=%v err=%v", found, err)
	}
	if err := identity.DeleteBindingForUser(bindings[0].ID, u1.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := identity.ResolveBoundUser("wecom", "t:corp|u:one"); err != nil || found {
		t.Fatalf("a revoked binding still resolves: found=%v err=%v", found, err)
	}
}
