package store

import (
	"strings"
	"testing"
	"time"
)

// The window filter is the part of the audit page that can be wrong without anyone noticing: a
// comparison that ignores the zone, or a parameter bound as a local-time string, silently drops rows
// from the page while the count says otherwise.

func TestAuditWhereTimeFilterIsAnEpochComparisonOnBothSides(t *testing.T) {
	since := time.Date(2026, 6, 16, 17, 2, 0, 0, time.UTC)
	until := time.Date(2026, 6, 17, 3, 3, 0, 0, time.UTC)
	where, args := auditWhere(AuditListFilter{Since: &since, Until: &until})
	for _, want := range []string{"strftime('%s', created_at) >=", "strftime('%s', created_at) <="} {
		if !strings.Contains(where, want) {
			t.Fatalf("expected %q in %q", want, where)
		}
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 time args, got %d", len(args))
	}
	for i, arg := range args {
		text, ok := arg.(string)
		if !ok || !strings.HasSuffix(text, "Z") {
			t.Fatalf("arg %d must be a UTC text, got %v", i, arg)
		}
	}
}

func TestAuditWhereRelatedUserIDCoversBothDetailSpellings(t *testing.T) {
	where, args := auditWhere(AuditListFilter{Category: "rbac", RelatedUserID: "user-123"})
	if !strings.Contains(where, "resource_id = ?") || !strings.Contains(where, "detail_json LIKE ?") {
		t.Fatalf("expected related-user predicates, got %q", where)
	}
	if len(args) != 4 {
		t.Fatalf("expected category plus 3 related-user args, got %#v", args)
	}
	if args[1] != "user-123" || args[2] != `%"user_id":"user-123"%` || args[3] != `%"userId":"user-123"%` {
		t.Fatalf("unexpected related-user args: %#v", args)
	}
}

// Written and read back on one database: rows outside the window must not appear, and the count must
// agree with the page - the two queries share the filter builder for exactly that reason.
func TestAuditListWindowAndCountAgreeOnRealRows(t *testing.T) {
	logs := NewAuditLogs(openDB(t))
	if err := logs.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{base.Add(-time.Hour), base, base.Add(time.Hour), base.Add(2 * time.Hour)} {
		if err := logs.Append(&AuditLog{
			ID: "id-" + string(rune('a'+i)), CreatedAt: at, Category: "auth",
			Action: "login", Result: "success", Actor: "admin", Message: "m",
		}); err != nil {
			t.Fatal(err)
		}
	}
	since, until := base, base.Add(time.Hour)
	filter := AuditListFilter{Since: &since, Until: &until, Limit: 10}
	page, err := logs.List(filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Fatalf("window returned %d rows, want 2: %v", len(page), page)
	}
	if page[0].CreatedAt.Before(page[1].CreatedAt) {
		t.Fatalf("the page is not newest first: %v, %v", page[0].CreatedAt, page[1].CreatedAt)
	}
	count, err := logs.Count(filter)
	if err != nil {
		t.Fatal(err)
	}
	if int(count) != len(page) {
		t.Fatalf("count %d disagrees with the page %d", count, len(page))
	}
}

func TestAuditLogsStoreWithoutDatabaseIsRefused(t *testing.T) {
	none := NewAuditLogs(nil)
	if err := none.EnsureSchema(); err == nil {
		t.Fatal("a connectionless store created the table")
	}
	if err := none.Append(&AuditLog{ID: "x"}); err == nil {
		t.Fatal("a connectionless store wrote a record")
	}
	if _, err := none.Count(AuditListFilter{}); err == nil {
		t.Fatal("a connectionless store counted records")
	}
	if _, err := none.DeleteBefore(time.Now()); err == nil {
		t.Fatal("a connectionless store deleted records")
	}
}
