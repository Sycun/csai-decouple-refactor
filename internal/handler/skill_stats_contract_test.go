package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/store"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// skill_stats moved from five methods on the data layer's connection wrapper into its own store, and
// these cases are the wire contract that had to survive: the same keys, the same time format, the
// same 500 wording when no database is wired at all.

func newSkillStatsFixture(t *testing.T) (*SkillsHandler, *store.SkillStats, *gin.Engine) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	writeTestFile(t, configPath, "server:\n  port: 0\n")
	db, err := database.NewDB(filepath.Join(dir, "skill-stats.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stats := store.NewSkillStats(db.DB)
	if err := stats.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	// An empty capability table is what makes the page read the skills directory: with units in the
	// process-wide table, only those directories count as installed.
	useTable(t)
	skillsDir := filepath.Join(dir, "skills", "code-audit")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(skillsDir, "SKILL.md"), "---\nname: code-audit\ndescription: 契约\n---\n\n## Steps\n")

	cfg := &config.Config{}
	cfg.SkillsDir = filepath.Join(dir, "skills")
	h := NewSkillsHandler(cfg, configPath, zap.NewNop())
	h.SetDB(db)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/skills/stats", h.GetSkillStats)
	router.DELETE("/skills/stats", h.ClearSkillStats)
	router.DELETE("/skills/:name/stats", h.ClearSkillStatsByName)
	return h, stats, router
}

func doStatsRequest(t *testing.T, router *gin.Engine, method, path string) (int, map[string]interface{}) {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	body := map[string]interface{}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s %s: %v (%s)", method, path, err, rec.Body.String())
	}
	return rec.Code, body
}

func statsEntry(t *testing.T, body map[string]interface{}, skillName string) map[string]interface{} {
	t.Helper()
	list, ok := body["stats"].([]interface{})
	if !ok {
		t.Fatalf("stats is not a list: %T", body["stats"])
	}
	for _, raw := range list {
		item, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("stats entry is not an object: %T", raw)
		}
		if item["skill_name"] == skillName {
			return item
		}
	}
	t.Fatalf("skill %q is not listed: %v", skillName, body["stats"])
	return nil
}

func TestSkillStatsEndpointKeepsItsShape(t *testing.T) {
	_, stats, router := newSkillStatsFixture(t)
	called := time.Date(2026, 4, 1, 9, 30, 0, 0, time.Local)
	if err := stats.Add("code-audit", 3, 2, 1, &called); err != nil {
		t.Fatal(err)
	}

	code, body := doStatsRequest(t, router, http.MethodGet, "/skills/stats")
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	if body["total_skills"] != float64(1) || body["total_calls"] != float64(3) ||
		body["total_success"] != float64(2) || body["total_failed"] != float64(1) {
		t.Fatalf("summary = %v", body)
	}
	if _, ok := body["skills_dir"]; !ok {
		t.Fatalf("skills_dir disappeared from the response: %v", body)
	}
	entry := statsEntry(t, body, "code-audit")
	want := map[string]float64{"total_calls": 3, "success_calls": 2, "failed_calls": 1}
	for key, value := range want {
		if entry[key] != value {
			t.Fatalf("%s = %v, want %v", key, entry[key], value)
		}
	}
	if entry["last_call_time"] != called.Format("2006-01-02 15:04:05") {
		t.Fatalf("last_call_time = %v, want the same formatted string the page already parses", entry["last_call_time"])
	}

	// A skill that was never called is still listed, with zeroed counters and an empty timestamp.
	code, body = doStatsRequest(t, router, http.MethodDelete, "/skills/code-audit/stats")
	if code != http.StatusOK {
		t.Fatalf("clear by name: status %d: %v", code, body)
	}
	_, body = doStatsRequest(t, router, http.MethodGet, "/skills/stats")
	entry = statsEntry(t, body, "code-audit")
	if entry["total_calls"] != float64(0) || entry["last_call_time"] != "" {
		t.Fatalf("after clearing one skill: %v", entry)
	}

	if err := stats.Add("code-audit", 1, 1, 0, &called); err != nil {
		t.Fatal(err)
	}
	code, body = doStatsRequest(t, router, http.MethodDelete, "/skills/stats")
	if code != http.StatusOK || body["message"] != "已清空所有Skills统计信息" {
		t.Fatalf("clear all: status %d, %v", code, body)
	}
	_, body = doStatsRequest(t, router, http.MethodGet, "/skills/stats")
	if body["total_calls"] != float64(0) {
		t.Fatalf("the counters came back: %v", body)
	}
}

// The boundary semantics of a server started without a database: the list still answers (no
// statistics), and a clear is a 500 with the wording the console already shows.
func TestSkillStatsEndpointsWithoutDatabase(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	writeTestFile(t, configPath, "server:\n  port: 0\n")
	cfg := &config.Config{}
	cfg.SkillsDir = filepath.Join(dir, "skills")
	h := NewSkillsHandler(cfg, configPath, zap.NewNop())
	h.SetDB(nil)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/skills/stats", h.GetSkillStats)
	router.DELETE("/skills/stats", h.ClearSkillStats)

	code, body := doStatsRequest(t, router, http.MethodGet, "/skills/stats")
	if code != http.StatusOK || body["total_calls"] != float64(0) {
		t.Fatalf("without a database the list should answer empty, not fail: %d %v", code, body)
	}
	code, body = doStatsRequest(t, router, http.MethodDelete, "/skills/stats")
	if code != http.StatusInternalServerError || body["error"] != "数据库连接未配置" {
		t.Fatalf("status %d: %v", code, body)
	}
}
