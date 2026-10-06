package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// The two tables have foreign keys onto rbac_users, so the fixture creates that one parent: an account
// row with the enabled flag, which is the only column these queries read from it.
func newRobotIdentityStore(t *testing.T) (*RobotIdentity, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "robot-identity.db")+"?_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE rbac_users (id TEXT PRIMARY KEY, display_name TEXT, enabled INTEGER NOT NULL DEFAULT 1);`); err != nil {
		t.Fatal(err)
	}
	identity := NewRobotIdentity(db)
	if err := identity.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	return identity, db
}

func seedAccount(t *testing.T, db *sql.DB, id string, enabled bool) {
	t.Helper()
	n := 0
	if enabled {
		n = 1
	}
	if _, err := db.Exec(`INSERT INTO rbac_users (id, display_name, enabled) VALUES (?, ?, ?)`, id, strings.Title(id), n); err != nil {
		t.Fatalf("seed account %s: %v", id, err)
	}
}

func TestRobotIdentityEnsureSchemaIsIdempotent(t *testing.T) {
	identity, db := newRobotIdentityStore(t)
	if err := identity.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	for _, index := range []string{"idx_robot_user_bindings_user", "idx_robot_binding_codes_expiry"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name = ?`, index).Scan(&name); err != nil {
			t.Fatalf("index %s was not created: %v", index, err)
		}
	}
}

// Refusals at the boundary: an empty identity would make a binding that matches everybody on that
// platform, a code without an expiry would outlive its purpose, and a past expiry is not a code.
func TestRobotIdentityRefusesIncompleteIdentitiesAndCodes(t *testing.T) {
	identity, _ := newRobotIdentityStore(t)
	for _, pair := range [][2]string{{"", "u"}, {"wecom", ""}, {"  ", "u"}} {
		if _, _, err := identity.ResolveBoundUser(pair[0], pair[1]); err == nil {
			t.Fatalf("ResolveBoundUser(%q,%q) accepted an incomplete identity", pair[0], pair[1])
		}
		if err := identity.DeleteBindingByIdentity(pair[0], pair[1]); err == nil {
			t.Fatalf("DeleteBindingByIdentity(%q,%q) accepted an incomplete identity", pair[0], pair[1])
		}
	}
	if err := identity.CreateBindingCode("u-1", "hash", time.Now().Add(-time.Minute)); err == nil {
		t.Fatal("a code that expires in the past was issued")
	}
	if err := identity.CreateBindingCode("", "hash", time.Now().Add(time.Minute)); err == nil {
		t.Fatal("a code with no issuing account was created")
	}
	if _, err := identity.ConsumeBindingCode("wecom", "ext", "  "); err == nil {
		t.Fatal("consuming with no code hash was allowed")
	}
}

func TestRobotIdentityConsumeIsSingleUse(t *testing.T) {
	identity, db := newRobotIdentityStore(t)
	seedAccount(t, db, "u-1", true)
	if err := identity.CreateBindingCode("u-1", "hash-1", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	bound, err := identity.ConsumeBindingCode("WECOM", " ext-user ", "hash-1")
	if err != nil || bound != "u-1" {
		t.Fatalf("consume answered %q (%v), want u-1", bound, err)
	}
	// The platform is stored lower-cased and the identity trimmed, so the same pair resolves again.
	if got, found, err := identity.ResolveBoundUser("wecom", "ext-user"); err != nil || !found || got != "u-1" {
		t.Fatalf("resolve after consume: %q found=%v err=%v", got, found, err)
	}
	// Sequential reuse is refused by the lookup ("invalid or expired"); the UPDATE guard behind it is
	// what refuses a *concurrent* loser ("already been used"), which TestRobotIdentityConsumeRace proves.
	second, err := identity.ConsumeBindingCode("wecom", "another", "hash-1")
	if err == nil {
		t.Fatalf("second consume of one code succeeded and bound %q", second)
	}
	if second != "" {
		t.Fatalf("a refused consume still answered an account: %q", second)
	}
	if !strings.Contains(err.Error(), "binding code") {
		t.Fatalf("unexpected refusal for a spent code: %v", err)
	}
	// The row that was refused must not have been written either.
	bindings, err := identity.ListBindings("u-1")
	if err != nil || len(bindings) != 1 {
		t.Fatalf("%d bindings after a refused second consume (%v)", len(bindings), err)
	}
}

// Eight requests for one code at once: exactly one may win. Note what this does and does not prove -
// with one SQLite connection the losers are refused by the lookup, not by the one-row guard, so a probe
// that removes the guard still passes here. The guard is kept for the interleaving the connection
// pooling can expose elsewhere, and this test is the evidence that the outcome is single-use either way.
func TestRobotIdentityConsumeRaceHasOneWinner(t *testing.T) {
	identity, db := newRobotIdentityStore(t)
	seedAccount(t, db, "u-1", true)
	if err := identity.CreateBindingCode("u-1", "race-hash", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	wins := make([]string, 0, 8)
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			bound, err := identity.ConsumeBindingCode("wecom", "competitor", "race-hash")
			if err == nil {
				mu.Lock()
				wins = append(wins, bound)
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if len(wins) != 1 {
		t.Fatalf("%d goroutines consumed one code, want exactly 1: %v", len(wins), wins)
	}
	var used int
	if err := db.QueryRow(`SELECT COUNT(*) FROM robot_binding_codes WHERE used_at IS NOT NULL`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 1 {
		t.Fatalf("%d code rows marked used, want 1", used)
	}
}

// One active code per account, and nothing expired or spent left behind: the prune rides in the same
// transaction as the insert, so a concurrent issue cannot leave two live codes.
func TestRobotIdentityCreateCodePrunesOlderOnes(t *testing.T) {
	identity, db := newRobotIdentityStore(t)
	seedAccount(t, db, "u-1", true)
	seedAccount(t, db, "u-2", true)
	if err := identity.CreateBindingCode("u-1", "old-hash", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := identity.CreateBindingCode("u-2", "other-account", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := identity.CreateBindingCode("u-1", "new-hash", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var live int
	if err := db.QueryRow(`SELECT COUNT(*) FROM robot_binding_codes WHERE rbac_user_id = 'u-1'`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("u-1 holds %d codes, want only the newest", live)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM robot_binding_codes WHERE code_hash = 'other-account'`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatal("another account's code was pruned as if it were ours")
	}
	if _, err := identity.ConsumeBindingCode("wecom", "ext", "old-hash"); err == nil {
		t.Fatal("the pruned code still consumes")
	}
}

// A disabled account's code is not consumable and its existing binding stops resolving. Both are
// predicates on the join, not deletions: re-enabling the account restores the association.
func TestRobotIdentityDisabledAccountResolvesNowhere(t *testing.T) {
	identity, db := newRobotIdentityStore(t)
	seedAccount(t, db, "u-1", true)
	if err := identity.CreateBindingCode("u-1", "hash-1", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.ConsumeBindingCode("wecom", "ext", "hash-1"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := identity.ResolveBoundUser("wecom", "ext"); !found {
		t.Fatal("the binding did not resolve while the account is enabled")
	}
	if _, err := db.Exec(`UPDATE rbac_users SET enabled = 0 WHERE id = 'u-1'`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := identity.ResolveBoundUser("wecom", "ext"); err != nil || found {
		t.Fatalf("a disabled account still resolved: found=%v err=%v", found, err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM robot_user_bindings`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("disabling the account deleted its binding row (%d), want it kept", rows)
	}
	// A fresh code for a disabled account cannot be spent either.
	seedAccount(t, db, "u-off", false)
	if err := identity.CreateBindingCode("u-off", "hash-off", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.ConsumeBindingCode("wecom", "ext-off", "hash-off"); err == nil {
		t.Fatal("a disabled account's code was consumed")
	}
}

// Rebinding the same platform identity replaces the account it points at: that is how a user recovers
// from a wrong association without an administrator deleting anything.
func TestRobotIdentityRebindReplacesTheAccount(t *testing.T) {
	identity, db := newRobotIdentityStore(t)
	seedAccount(t, db, "u-1", true)
	seedAccount(t, db, "u-2", true)
	if err := identity.CreateBindingCode("u-1", "h-1", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := identity.CreateBindingCode("u-2", "h-2", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.ConsumeBindingCode("wecom", "ext", "h-1"); err != nil {
		t.Fatal(err)
	}
	if bound, err := identity.ConsumeBindingCode("wecom", "ext", "h-2"); err != nil || bound != "u-2" {
		t.Fatalf("rebind answered %q (%v), want u-2", bound, err)
	}
	rows, err := identity.ListBindings("u-2")
	if err != nil || len(rows) != 1 {
		t.Fatalf("u-2 holds %+v (%v), want the one rebound identity", rows, err)
	}
	if old, err := identity.ListBindings("u-1"); err != nil || len(old) != 0 {
		t.Fatalf("u-1 still lists a binding it no longer has: %+v (%v)", old, err)
	}
	if got, found, _ := identity.ResolveBoundUser("wecom", "ext"); !found || got != "u-2" {
		t.Fatalf("resolve after rebind: %q found=%v", got, found)
	}
	// Unbinding by identity works without knowing our own row id.
	if err := identity.DeleteBindingByIdentity("wecom", "ext"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := identity.ResolveBoundUser("wecom", "ext"); found {
		t.Fatal("the identity still resolves after being unbound")
	}
}

// Deleting a local account takes its bindings and its unused codes with it, through the cascade the
// tables are created with. A leftover row would otherwise resolve to an account that no longer exists.
func TestRobotIdentityBindingGoesWithItsAccount(t *testing.T) {
	identity, db := newRobotIdentityStore(t)
	seedAccount(t, db, "u-1", true)
	if err := identity.CreateBindingCode("u-1", "h-1", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.ConsumeBindingCode("wecom", "ext", "h-1"); err != nil {
		t.Fatal(err)
	}
	if err := identity.CreateBindingCode("u-1", "h-2", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM rbac_users WHERE id = 'u-1'`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"robot_user_bindings", "robot_binding_codes"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s kept %d rows for a deleted account", table, n)
		}
	}
}

// The timestamps are read back through the one stored-instant reader, so a row written by an older
// build in another text form still shows a real date on the account page.
func TestRobotIdentityListReadsLegacyTimestamps(t *testing.T) {
	identity, db := newRobotIdentityStore(t)
	seedAccount(t, db, "u-1", true)
	if _, err := db.Exec(`INSERT INTO robot_user_bindings (id, platform, external_user_id, rbac_user_id, enabled, created_at, updated_at)
		VALUES ('b-1','wecom','ext','u-1',1,'2026-01-02 03:04:05','2026-01-02T03:04:05Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO robot_user_bindings (id, platform, external_user_id, rbac_user_id, enabled, created_at, updated_at)
		VALUES ('b-2','lark','ext','u-1',0,'2026-02-02 03:04:05.123+08:00','2026-02-02 03:04:05')`); err != nil {
		t.Fatal(err)
	}
	rows, err := identity.ListBindings("u-1")
	if err != nil || len(rows) != 2 {
		t.Fatalf("%d bindings (%v)", len(rows), err)
	}
	// Most recently touched first.
	if rows[0].ID != "b-2" || rows[1].ID != "b-1" {
		t.Fatalf("order = %v", rows)
	}
	if rows[0].CreatedAt.Year() != 2026 || rows[0].CreatedAt.Month() != time.February {
		t.Fatalf("the offset-bearing text did not read back as an instant: %v", rows[0].CreatedAt)
	}
	if rows[1].UpdatedAt.IsZero() {
		t.Fatal("the Z-form text did not read back as an instant")
	}
	if rows[0].Enabled {
		t.Fatal("enabled=0 read back as true")
	}
}

func TestRobotIdentityRefusesNoConnection(t *testing.T) {
	none := NewRobotIdentity(nil)
	if err := none.EnsureSchema(); err == nil {
		t.Fatal("a connectionless store created the tables")
	}
	if err := none.CreateBindingCode("u", "h", time.Now().Add(time.Minute)); err == nil {
		t.Fatal("a connectionless store issued a code")
	}
	if _, err := none.ConsumeBindingCode("wecom", "ext", "h"); err == nil {
		t.Fatal("a connectionless store consumed a code")
	}
	if _, _, err := none.ResolveBoundUser("wecom", "ext"); err == nil {
		t.Fatal("a connectionless store resolved an identity")
	}
	if _, err := none.ListBindings("u"); err == nil {
		t.Fatal("a connectionless store listed bindings")
	}
	if err := none.DeleteBindingForUser("b", "u"); err == nil {
		t.Fatal("a connectionless store deleted a binding")
	}
	if err := none.DeleteBindingByIdentity("wecom", "ext"); err == nil {
		t.Fatal("a connectionless store unbound an identity")
	}
}
