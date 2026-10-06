package database

import (
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/store"

	"go.uber.org/zap"
)

// TestListAuditLogs_timeFilterMixedStorageFormats runs against the operator's own database when one
// exists, because the point of it is that real rows - written by different builds, some before the
// UTC storage rule - still fall inside a window query. It skips when that file is absent, so a clean
// checkout is not a failure. The window filter itself is pinned on synthetic rows in
// internal/store/audit_logs_filter_test.go.
func TestListAuditLogs_timeFilterMixedStorageFormats(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Skip(err)
	}
	dbPath := filepath.Join(root, "..", "..", "data", "conversations.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skip("conversations.db not found")
	}
	db, err := NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	since, _ := ParseRFC3339Time("2026-06-16T17:02:00Z")
	until, _ := ParseRFC3339Time("2026-06-17T03:03:00Z")
	logs, err := store.NewAuditLogs(db.DB).List(store.AuditListFilter{Since: &since, Until: &until, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range logs {
		at := row.CreatedAt.UTC()
		if at.Before(since) || at.After(until) {
			t.Fatalf("log %s at %s outside [%s, %s]", row.ID, at, since, until)
		}
	}
}
