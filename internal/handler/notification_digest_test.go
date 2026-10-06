package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// TestDigestListsFindingsWithoutAConversation is the end-to-end half of the same contract the
// store test pins. Findings are stored with a NULL conversation (`nullIfEmpty` on insert),
// and the digest used to scan that column into a plain string and answer the Scan error with
// `continue` - so a project-level finding appeared in neither the item list nor the severity
// counters, while the vulnerabilities page listed it normally.

func TestDigestListsFindingsWithoutAConversation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.NewDB(filepath.Join(t.TempDir(), "digest.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := database.NewFindings(db).Create(&store.Vulnerability{
		Title:    "SQL injection in the export endpoint",
		Severity: "high",
		Status:   "open",
	}); err != nil {
		t.Fatalf("create finding: %v", err)
	}
	// The attached finding needs its conversation to exist: the foreign key is real.
	if _, err := db.Exec(`INSERT INTO conversations (id, title, created_at, updated_at)
		VALUES ('c-seeded', 'seeded', datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	// A second one that *is* attached, so the test would notice a query that only ever
	// returns one row for some other reason.
	if _, err := database.NewFindings(db).Create(&store.Vulnerability{
		ConversationID: "c-seeded",
		Title:          "Exposed keys",
		Severity:       "critical",
		Status:         "open",
	}); err != nil {
		t.Fatalf("create attached finding: %v", err)
	}

	handler := NewNotificationHandler(db, nil, zap.NewNop())
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(security.ContextSessionKey, security.Session{
			UserID:      "u-owner",
			Scope:       database.RBACScopeAll,
			Permissions: map[string]bool{"vulnerability:read": true},
		})
		c.Next()
	})
	router.GET("/notifications/summary", handler.GetSummary)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/notifications/summary?limit=50", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("summary status = %d: %s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		Items  []map[string]interface{} `json:"items"`
		Counts map[string]int           `json:"counts"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode summary: %v (%s)", err, recorder.Body.String())
	}
	types := map[string]int{}
	for _, item := range body.Items {
		id, _ := item["id"].(string)
		if strings.HasPrefix(id, "vuln:") {
			types["vuln"]++
		}
	}
	if types["vuln"] != 2 {
		t.Fatalf("digest listed %d vulnerability items, want 2 (the orphan plus the attached one): %s",
			types["vuln"], recorder.Body.String())
	}
	if body.Counts["newHighVulns"] != 1 {
		t.Fatalf("newHighVulns = %d, want 1: the conversation-less finding used to be counted out entirely (items=%s)",
			body.Counts["newHighVulns"], recorder.Body.String())
	}
	if body.Counts["newCriticalVulns"] != 1 {
		t.Fatalf("newCriticalVulns = %d, want 1", body.Counts["newCriticalVulns"])
	}
}
