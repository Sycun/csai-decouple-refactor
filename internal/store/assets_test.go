package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// The assets table, its column backfill and its seven indexes moved here from the connection
// wrapper. These three cases cover what only the owner of the schema can answer: that the objects
// come out in an order SQLite accepts, that a handle without a connection refuses instead of
// panicking, and that leaving a project clears the stamp without touching the row.
//
// The statements themselves are covered by the eleven real-schema cases in
// internal/database/asset_test.go. They stay in that package on purpose: they boot the whole
// schema, and a store test that stubbed batch_tasks or vulnerabilities would be testing its own
// fake instead of the real columns.

func openAssetDB(t *testing.T) (*Assets, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewAssets(db), db
}

func objectCount(t *testing.T, db *sql.DB, kind, name string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`, kind, name).Scan(&count); err != nil {
		t.Fatalf("look up %s %s: %v", kind, name, err)
	}
	return count
}

func columnCount(t *testing.T, db *sql.DB, column string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('assets') WHERE name = ?`, column).Scan(&count); err != nil {
		t.Fatalf("look up column %s: %v", column, err)
	}
	return count
}

func TestAssetsEnsureSchemaBuildsColumnsBeforeTheIndexThatNeedsThem(t *testing.T) {
	a, db := openAssetDB(t)
	if err := a.EnsureSchema(); err != nil {
		t.Fatalf("first EnsureSchema: %v", err)
	}
	if err := a.EnsureSchema(); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	if got := objectCount(t, db, "table", "assets"); got != 1 {
		t.Fatalf("assets table present %d times, want 1", got)
	}
	// last_scan_at and its three companions are not in the CREATE TABLE: the index below is only
	// valid once the backfill has run, which is the whole point of this case.
	for _, column := range []string{"last_scan_at", "last_scan_conversation_id", "last_scan_queue_id", "last_scan_task_id",
		"project_id", "responsible_person", "department", "business_system", "environment", "criticality",
		"vulnerability_count", "risk_score", "risk_level"} {
		if got := columnCount(t, db, column); got != 1 {
			t.Fatalf("column %s present %d times, want 1", column, got)
		}
	}
	for _, index := range []string{"idx_assets_last_seen", "idx_assets_last_scan", "idx_assets_ip", "idx_assets_domain",
		"idx_assets_status", "idx_assets_owner", "idx_assets_project",
		"idx_assets_vulnerability_count", "idx_assets_risk_score", "idx_assets_risk_level"} {
		if got := objectCount(t, db, "index", index); got != 1 {
			t.Fatalf("index %s present %d times, want 1", index, got)
		}
	}
	// The count is the point: a fresh install once turned up three more assets indexes that the
	// connection wrapper's global sweep was still creating, which no per-name check would notice.
	var owned int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name LIKE 'idx_assets%'`).Scan(&owned); err != nil {
		t.Fatalf("count assets indexes: %v", err)
	}
	if owned != 10 {
		t.Fatalf("assets carries %d idx_assets_* indexes, want 10 - the owner of the table builds them all", owned)
	}
	if got := strings.Count(assetsIndexes, "CREATE INDEX"); got != 10 {
		t.Fatalf("assetsIndexes holds %d statements, want 10", got)
	}

	// Positive control: the same two statements in the other order must fail, otherwise the case
	// above would pass whether or not the backfill ran first.
	if _, err := db.Exec(`DROP TABLE assets`); err != nil {
		t.Fatalf("drop assets: %v", err)
	}
	if _, err := db.Exec(assetsSchema); err != nil {
		t.Fatalf("recreate assets bare: %v", err)
	}
	if _, err := db.Exec(assetsIndexes); err == nil {
		t.Fatal("indexes built on a table that never had last_scan_at: the ordering claim is unproven")
	} else if !strings.Contains(err.Error(), "last_scan_at") {
		t.Fatalf("indexes failed for the wrong reason: %v", err)
	}
}

func TestAssetsRefusesAConnectionlessHandle(t *testing.T) {
	a := NewAssets(nil)
	want := "store: assets requires a database"
	checks := []struct {
		name string
		call func() error
	}{
		{"EnsureSchema", func() error { return a.EnsureSchema() }},
		{"UnlinkProject", func() error { return a.UnlinkProject("p1") }},
		{"UpsertAssets", func() error {
			_, err := a.UpsertAssets([]*Asset{{Host: "example.com"}}, "u1")
			return err
		}},
		{"MarkAssetScanned", func() error { return a.MarkAssetScanned("a1", "c1", "q1", "t1", Access{Scope: ScopeAll}) }},
		{"CompleteAssetScan", func() error { return a.CompleteAssetScan("a1", "c1", Access{Scope: ScopeAll}) }},
		{"RefreshAssetRiskCache", func() error { return a.RefreshAssetRiskCache("a1") }},
		{"AssetIDsForVulnerabilityConversations", func() error {
			_, err := a.AssetIDsForVulnerabilityConversations([]string{"c1"})
			return err
		}},
		{"RefreshAssetRiskCacheForConversations", func() error { return a.RefreshAssetRiskCacheForConversations("c1") }},
		{"ListAssets", func() error {
			_, _, err := a.ListAssets(10, 0, AssetListFilter{}, Access{Scope: ScopeAll})
			return err
		}},
		{"ListAssetsForOperation", func() error {
			_, _, err := a.ListAssetsForOperation(10, AssetListFilter{}, Access{Scope: ScopeAll})
			return err
		}},
		{"GetAsset", func() error {
			_, err := a.GetAsset("a1", Access{Scope: ScopeAll})
			return err
		}},
		{"UpdateAsset", func() error { return a.UpdateAsset("a1", &Asset{Host: "example.com"}, Access{Scope: ScopeAll}) }},
		{"UpdateAssetsBulk", func() error {
			_, err := a.UpdateAssetsBulk([]string{"a1"}, AssetBulkPatch{}, Access{Scope: ScopeAll})
			return err
		}},
		{"DeleteAssets", func() error {
			_, err := a.DeleteAssets([]string{"a1"}, Access{Scope: ScopeAll})
			return err
		}},
		{"MergeAssets", func() error {
			_, err := a.MergeAssets(&Asset{ID: "a1", Host: "example.com"}, []string{"a2"}, Access{Scope: ScopeAll}, Access{Scope: ScopeAll})
			return err
		}},
		{"UpdateAssetsProject", func() error {
			_, err := a.UpdateAssetsProject([]string{"a1"}, "p1", Access{Scope: ScopeAll})
			return err
		}},
		{"DeleteAsset", func() error { return a.DeleteAsset("a1", Access{Scope: ScopeAll}) }},
		{"GetAssetStats", func() error {
			_, err := a.GetAssetStats(Access{Scope: ScopeAll})
			return err
		}},
	}
	for _, check := range checks {
		err := check.call()
		if err == nil {
			t.Fatalf("%s answered no error without a connection", check.name)
		}
		if err.Error() != want {
			t.Fatalf("%s answered %q, want %q", check.name, err.Error(), want)
		}
	}
	// The one predicate that answers with a bool has no error to return, so it must answer false.
	if a.BatchTaskBelongsToQueue("t1", "q1") {
		t.Fatal("BatchTaskBelongsToQueue claimed a link without a connection")
	}
}

func seedAsset(t *testing.T, db *sql.DB, id, host, projectID, owner string) {
	t.Helper()
	now := time.Now()
	var project any
	if projectID != "" {
		project = projectID
	}
	if _, err := db.Exec(`INSERT INTO assets (id, dedup_key, project_id, host, status, owner_user_id,
		first_seen_at, last_seen_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		id, "host:"+host, project, host, owner, now, now, now, now); err != nil {
		t.Fatalf("seed asset %s: %v", id, err)
	}
}

func storedProject(t *testing.T, db *sql.DB, id string) (string, bool) {
	t.Helper()
	var project sql.NullString
	var exists int
	err := db.QueryRow(`SELECT COUNT(*), COALESCE(project_id, '<null>') FROM assets WHERE id = ?`, id).Scan(&exists, &project)
	if err != nil {
		t.Fatalf("read asset %s: %v", id, err)
	}
	if exists == 0 {
		return "", false
	}
	return project.String, true
}

func TestAssetsUnlinkProjectClearsTheStampAndKeepsTheRow(t *testing.T) {
	a, db := openAssetDB(t)
	if err := a.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	seedAsset(t, db, "a1", "one.example", "p-1", "u1")
	seedAsset(t, db, "a2", "two.example", "p-1", "u1")
	seedAsset(t, db, "a3", "three.example", "p-2", "u1")
	seedAsset(t, db, "a4", "four.example", "", "u1")

	// DeleteProject hands over a value, not a statement, so the argument is trimmed by the caller's
	// own standard; a padded id must still find its rows.
	if err := a.UnlinkProject("  p-1  "); err != nil {
		t.Fatalf("UnlinkProject: %v", err)
	}

	for _, id := range []string{"a1", "a2"} {
		stamp, present := storedProject(t, db, id)
		if !present {
			t.Fatalf("%s disappeared: unlinking a project must keep its assets", id)
		}
		if stamp != "<null>" {
			t.Fatalf("%s still stamped with %q, want cleared", id, stamp)
		}
	}
	if stamp, _ := storedProject(t, db, "a3"); stamp != "p-2" {
		t.Fatalf("a3 stamp=%q, want p-2 untouched", stamp)
	}
	if stamp, _ := storedProject(t, db, "a4"); stamp != "<null>" {
		t.Fatalf("a4 stamp=%q, want the unassigned asset untouched", stamp)
	}

	// An unrelated project id, and the empty id, must both be no-ops rather than a full-table clear.
	if err := a.UnlinkProject("p-nope"); err != nil {
		t.Fatalf("UnlinkProject(unknown): %v", err)
	}
	if err := a.UnlinkProject(""); err != nil {
		t.Fatalf("UnlinkProject(empty): %v", err)
	}
	if stamp, _ := storedProject(t, db, "a3"); stamp != "p-2" {
		t.Fatalf("a3 stamp=%q after an empty unlink, want p-2", stamp)
	}
}
