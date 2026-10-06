package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The usage endpoints' SQL moved to internal/store/model_token_usage.go. The ratchet proves the
// handler stopped reaching for statements; only a wire-level test proves the dashboard still gets the
// same JSON, from the same rows, under the same reachability rules.

const usageContractPayload = `{"source":"agent","orchestration":"react","reason":"final","model":"gpt-test",` +
	`"modelCalls":2,"promptTokens":100,"completionTokens":50,"totalTokens":150,"cachedTokens":7,"reasoningTokens":3}`

func newUsageContractHandler(t *testing.T) (*ConversationHandler, *database.DB) {
	t.Helper()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "usage-contract.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Start-up creates this table through the store that owns it (app.ensureModelTokenUsageSchema);
	// a test that builds the data layer directly has to make the same call.
	if err := store.NewModelTokenUsage(db.DB).EnsureSchema(); err != nil {
		t.Fatalf("ensure model_token_usage: %v", err)
	}
	return NewConversationHandler(db, zap.NewNop()), db
}

// usageContractRouter registers the two paths internal/app/routes_conversation.go puts the handlers on.
func usageContractRouter(h *ConversationHandler, session *security.Session) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if session != nil {
			c.Set(security.ContextSessionKey, *session)
		}
		c.Next()
	})
	router.GET("/api/usage/tokens", h.GetTokenUsageStats)
	router.GET("/api/conversations/:id/token-usage", h.GetConversationTokenUsageStats)
	return router
}

func usageContractUser(t *testing.T, db *database.DB, name string) *database.RBACUser {
	t.Helper()
	user, err := database.NewRBAC(db).CreateRBACUser(name, name, "hash", true, nil)
	if err != nil {
		t.Fatalf("CreateRBACUser %s: %v", name, err)
	}
	return user
}

// recordUsage drives the write path a run uses - a message, then a process detail carrying the
// summary - so the rows the endpoints read came through the store's hook rather than a fixture.
func recordUsage(t *testing.T, db *database.DB, conversationID, payload string) {
	t.Helper()
	message, err := database.NewConversations(db).AddMessage(conversationID, "assistant", "answer", nil)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := database.NewConversations(db).AddProcessDetail(message.ID, conversationID, store.UsageEventType, "usage", payload); err != nil {
		t.Fatalf("AddProcessDetail: %v", err)
	}
}

func ownedConversation(t *testing.T, db *database.DB, user *database.RBACUser, title string) string {
	t.Helper()
	conv, err := database.NewConversations(db).CreateConversation(title, database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if err := database.NewRBAC(db).SetResourceOwner("conversation", conv.ID, user.ID); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}
	return conv.ID
}

func getUsageJSON(t *testing.T, router *gin.Engine, path string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200: %s", path, w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return body
}

func keySet(object map[string]any) []string {
	out := make([]string, 0, len(object))
	for k := range object {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalKeys(got []string, want []string) bool {
	return strings.Join(got, ",") == strings.Join(want, ",")
}

var usageTopLevelKeys = []string{"byDay", "byModel", "byOrchestration", "recent", "summary", "today"}

var usageSummaryKeys = []string{"cachedTokens", "completionTokens", "events", "modelCalls", "promptTokens", "reasoningTokens", "totalTokens"}

var usageRecentItemKeys = []string{
	"cachedTokens", "completionTokens", "conversationId", "createdAt", "id", "messageId", "model", "modelCalls",
	"orchestration", "processDetailId", "promptTokens", "reason", "reasoningTokens", "source", "totalTokens", "updatedAt",
}

// The dashboard reads summary/today/recent and the three groupings, and each recent item by name.
// A renamed or dropped key is a broken page, and only a request answers that.
func TestUsageStatsWireShapeForTheConversationOwner(t *testing.T) {
	h, db := newUsageContractHandler(t)
	owner := usageContractUser(t, db, "usage-owner")
	convID := ownedConversation(t, db, owner, "mine")
	recordUsage(t, db, convID, usageContractPayload)

	body := getUsageJSON(t, usageContractRouter(h, &security.Session{UserID: owner.ID, Scope: database.RBACScopeAssigned}), "/api/usage/tokens")
	if got := keySet(body); !equalKeys(got, usageTopLevelKeys) {
		t.Fatalf("top-level keys = %v, want %v", got, usageTopLevelKeys)
	}
	summary := body["summary"].(map[string]any)
	if got := keySet(summary); !equalKeys(got, usageSummaryKeys) {
		t.Fatalf("summary keys = %v, want %v", got, usageSummaryKeys)
	}
	if summary["events"] != float64(1) || summary["totalTokens"] != float64(150) {
		t.Fatalf("summary = %v", summary)
	}
	recent, ok := body["recent"].([]any)
	if !ok || len(recent) != 1 {
		t.Fatalf("recent = %v, want one row", body["recent"])
	}
	item := recent[0].(map[string]any)
	if got := keySet(item); !equalKeys(got, usageRecentItemKeys) {
		t.Fatalf("recent item keys = %v, want %v", got, usageRecentItemKeys)
	}
	// projectId is omitempty and the conversation has no project: the key must be absent, not null and
	// not empty, because the console distinguishes "no project" from "a project with an empty name".
	if _, present := item["projectId"]; present {
		t.Fatalf("an unprojected row carried projectId: %v", item)
	}
	if item["processDetailId"] == "" || item["conversationId"] != convID {
		t.Fatalf("row identity = %v", item)
	}
	if item["model"] != "gpt-test" || item["source"] != "agent" || item["orchestration"] != "react" {
		t.Fatalf("text columns = %v", item)
	}
}

// Somebody else's conversation is not in the answer - and a request that lost its identity gets
// nothing rather than the whole base, which is what the store layer's single access clause means.
func TestUsageStatsScopesByCaller(t *testing.T) {
	h, db := newUsageContractHandler(t)
	owner := usageContractUser(t, db, "usage-owner")
	stranger := usageContractUser(t, db, "usage-stranger")
	recordUsage(t, db, ownedConversation(t, db, owner, "mine"), usageContractPayload)

	other := getUsageJSON(t, usageContractRouter(h, &security.Session{UserID: stranger.ID, Scope: database.RBACScopeOwn}), "/api/usage/tokens")
	if events := other["summary"].(map[string]any)["events"]; events != float64(0) {
		t.Fatalf("an unrelated caller saw events = %v", events)
	}
	if recent := other["recent"].([]any); len(recent) != 0 {
		t.Fatalf("an unrelated caller saw %d recent rows", len(recent))
	}

	anonymous := getUsageJSON(t, usageContractRouter(h, nil), "/api/usage/tokens")
	if events := anonymous["summary"].(map[string]any)["events"]; events != float64(0) {
		t.Fatalf("a caller with no session saw events = %v, want the fail-closed zero", events)
	}

	admin := getUsageJSON(t, usageContractRouter(h, &security.Session{Scope: database.RBACScopeAll}), "/api/usage/tokens")
	if events := admin["summary"].(map[string]any)["events"]; events != float64(1) {
		t.Fatalf("an unrestricted scope saw events = %v, want 1 without needing a user id", events)
	}

	// Being assigned somebody's conversation reaches its usage too: the clause has four paths, not one.
	second := ownedConversation(t, db, owner, "second")
	if err := database.NewRBAC(db).AssignResourceToUser(stranger.ID, "conversation", second); err != nil {
		t.Fatalf("AssignResourceToUser: %v", err)
	}
	recordUsage(t, db, second, `{"promptTokens":1,"completionTokens":1,"totalTokens":2}`)
	assignedRouter := usageContractRouter(h, &security.Session{UserID: stranger.ID, Scope: database.RBACScopeAssigned})
	assigned := getUsageJSON(t, assignedRouter, "/api/usage/tokens")
	if events := assigned["summary"].(map[string]any)["events"]; events != float64(1) {
		t.Fatalf("an assigned conversation's caller saw events = %v, want 1", events)
	}
	// own and assigned are one clause: what separates them is which permission the caller used to
	// ask, not the SQL. Pinning that keeps a future "narrowing" from being mistaken for a fix.
	ownOnly := getUsageJSON(t, usageContractRouter(h, &security.Session{UserID: stranger.ID, Scope: database.RBACScopeOwn}), "/api/usage/tokens")
	if events := ownOnly["summary"].(map[string]any)["events"]; events != float64(1) {
		t.Fatalf("own scope dropped an assigned conversation: events = %v, want 1", events)
	}
}

// The per-conversation endpoint scopes to the id in the path, and the query parameters keep their
// documented clamps: days 1-365, limit 1-500.
func TestConversationTokenUsageEndpointAndQueryClamps(t *testing.T) {
	h, db := newUsageContractHandler(t)
	owner := usageContractUser(t, db, "usage-owner")
	mine := ownedConversation(t, db, owner, "mine")
	theirs := ownedConversation(t, db, owner, "also mine")
	recordUsage(t, db, mine, usageContractPayload)
	recordUsage(t, db, theirs, `{"model":"gpt-b","totalTokens":9}`)

	router := usageContractRouter(h, &security.Session{UserID: owner.ID, Scope: database.RBACScopeOwn})
	one := getUsageJSON(t, router, "/api/conversations/"+mine+"/token-usage")
	if events := one["summary"].(map[string]any)["events"]; events != float64(1) {
		t.Fatalf("per-conversation usage = %v, want the one row of this conversation", one["summary"])
	}
	if recent := one["recent"].([]any); len(recent) != 1 || recent[0].(map[string]any)["conversationId"] != mine {
		t.Fatalf("per-conversation recent = %v", one["recent"])
	}

	// limit=1 over two rows.
	both := getUsageJSON(t, router, "/api/usage/tokens?limit=1")
	if recent := both["recent"].([]any); len(recent) != 1 {
		t.Fatalf("limit=1 returned %d recent rows", len(recent))
	}
	// A nonsense days value falls back to the seven-day window rather than erroring.
	fallback := getUsageJSON(t, router, "/api/usage/tokens?days=0&limit=-5")
	if events := fallback["summary"].(map[string]any)["events"]; events != float64(2) {
		t.Fatalf("days=0 fell back to a window that missed rows: %v", fallback["summary"])
	}
	// A row's project is the conversation's project at the moment the run happened: the column is a
	// copy taken on write, so linking a conversation afterwards does not re-file its history.
	project, err := database.NewProjects(db).CreateProject(&database.Project{Name: "usage project"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := database.NewConversations(db).SetConversationProjectID(theirs, project.ID); err != nil {
		t.Fatalf("SetConversationProjectID: %v", err)
	}
	recordUsage(t, db, theirs, `{"model":"gpt-c","totalTokens":4}`)
	inProject := getUsageJSON(t, router, "/api/usage/tokens?project_id="+project.ID)
	if events := inProject["summary"].(map[string]any)["events"]; events != float64(1) {
		t.Fatalf("project filter = %v, want the row written while the conversation was in the project", inProject["summary"])
	}
	// Unprojected then answers with two rows - mine's, and theirs' row from before the link - which is
	// the copy semantics visible: one conversation can straddle a project change.
	unbound := getUsageJSON(t, router, "/api/usage/tokens?project_id="+store.ProjectUnbound)
	if events := unbound["summary"].(map[string]any)["events"]; events != float64(2) {
		t.Fatalf("unbound filter = %v, want the two rows written while unprojected", unbound["summary"])
	}
	if events := getUsageJSON(t, router, "/api/usage/tokens")["summary"].(map[string]any)["events"]; events != float64(3) {
		t.Fatalf("unfiltered summary = %v, want the union of the two filters", events)
	}
}

// A handler built without a database keeps a connectionless store, and the endpoint answers with the
// store's error rather than a panic or an empty 200.
func TestUsageStatsFailsLoudlyWithoutADatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewConversationHandler(nil, zap.NewNop())
	router := usageContractRouter(h, &security.Session{UserID: "any", Scope: database.RBACScopeOwn})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/usage/tokens", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "model token usage") {
		t.Fatalf("error body = %v, want the store's own refusal", body)
	}
}
