package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// These tests drive the HITL endpoints over HTTP after their SQL moved to
// internal/store: the ratchet proves the handlers stopped writing statements, and
// only a wire-level test proves the frontend still gets the same JSON back.

func newHITLEndpointHandler(t *testing.T) (*AgentHandler, *database.DB) {
	t.Helper()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "hitl-endpoints.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	manager := NewHITLManager(db, zap.NewNop())
	if err := manager.EnsureSchema(); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	h := &AgentHandler{
		db:          db,
		hitlStore:   store.NewHITL(db.DB),
		hitlQueue:   newHITLQueue(database.NewRBAC(db), store.NewHITL(db.DB), &config.Config{}, manager),
		hitlManager: manager,
		config:      &config.Config{},
		logger:      zap.NewNop(),
	}
	return h, db
}

// hitlTestConversation creates the conversation row an assignment or a visibility
// check needs: resource ids are validated against the table, so an interrupt cannot
// be made reachable by naming a conversation that was never created.
func hitlTestConversation(t *testing.T, db *database.DB, title string) string {
	t.Helper()
	conv, err := db.CreateConversation(title, database.ConversationCreateMeta{})
	if err != nil {
		t.Fatalf("create conversation %q: %v", title, err)
	}
	return conv.ID
}

// hitlTestUser creates an RBAC user so resource assignments can point at a real
// row - assignments to an unknown user are refused, not silently ignored.
func hitlTestUser(t *testing.T, db *database.DB, name string) string {
	t.Helper()
	user, err := database.NewRBAC(db).CreateRBACUser(name, name, "hash", true, nil)
	if err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	return user.ID
}

func hitlEndpointRouter(h *AgentHandler, session *security.Session) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if session != nil {
			c.Set(security.ContextSessionKey, *session)
		}
		c.Next()
	})
	router.GET("/api/hitl/pending", h.HITLQueue().ListHITLPending)
	// Same wiring as internal/app/routes_hitl.go: the log surface lives on the extracted
	// collaborator now, so this contract test exercises the object production actually serves.
	router.GET("/api/hitl/logs", h.HITLQueue().ListHITLLogs)
	router.GET("/api/hitl/logs/:id", h.HITLQueue().GetHITLLog)
	router.DELETE("/api/hitl/logs", h.HITLQueue().DeleteHITLLogs)
	router.POST("/api/hitl/dismiss", h.HITLQueue().DismissHITLInterrupt)
	return router
}

func seedEndpointInterrupt(t *testing.T, db *database.DB, id, conversationID, toolName, status, reviewer string, decidedAt *time.Time) {
	t.Helper()
	var decided any
	if decidedAt != nil {
		decided = *decidedAt
	}
	_, err := db.Exec(`INSERT INTO hitl_interrupts
		(id, conversation_id, message_id, mode, tool_name, tool_call_id, payload, status, reviewer, decision, decision_comment, decided_by, created_at, decided_at)
		VALUES (?, ?, '', 'approval', ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?)`,
		id, conversationID, toolName, id+"-call", `{"toolName":"`+toolName+`","argumentsObj":{}}`, status, reviewer,
		decisionFor(status), commentFor(status), reviewer, decided)
	if err != nil {
		t.Fatalf("seed interrupt %s: %v", id, err)
	}
}

func decisionFor(status string) any {
	if status == "pending" {
		return nil
	}
	return "approve"
}

func commentFor(status string) any {
	if status == "pending" {
		return nil
	}
	return "looks fine"
}

type hitlListResponse struct {
	Items []struct {
		ID             string          `json:"id"`
		ConversationID string          `json:"conversationId"`
		ToolName       string          `json:"toolName"`
		Status         string          `json:"status"`
		Reviewer       string          `json:"reviewer"`
		DecidedBy      string          `json:"decidedBy"`
		Payload        string          `json:"payload"`
		Decision       string          `json:"decision"`
		AuditBackend   string          `json:"auditBackend"`
		CreatedAt      time.Time       `json:"createdAt"`
		DecidedAt      json.RawMessage `json:"decidedAt"`
	} `json:"items"`
	Page          *int `json:"page"`
	PageSize      *int `json:"pageSize"`
	Total         int  `json:"total"`
	RetentionDays *int `json:"retentionDays"`
}

func serveHITLEndpoint(t *testing.T, router *gin.Engine, method, target, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func decodeHITLList(t *testing.T, payload []byte) hitlListResponse {
	t.Helper()
	var res hitlListResponse
	if err := json.Unmarshal(payload, &res); err != nil {
		t.Fatalf("decode list response: %v\n%s", err, payload)
	}
	return res
}

func listedIDs(res hitlListResponse) []string {
	out := make([]string, 0, len(res.Items))
	for _, it := range res.Items {
		out = append(out, it.ID)
	}
	return out
}

func TestHITLPendingEndpointServesTheHumanQueue(t *testing.T) {
	h, db := newHITLEndpointHandler(t)
	seedEndpointInterrupt(t, db, "p-human", "conv-a", "nmap_scan", "pending", "human", nil)
	seedEndpointInterrupt(t, db, "p-agent", "conv-a", "http_get", "pending", "audit_agent", nil)
	seedEndpointInterrupt(t, db, "d-old", "conv-a", "exec", "decided", "human", hitlTimePtr(time.Now().Add(-time.Hour)))

	router := hitlEndpointRouter(h, &security.Session{UserID: "owner", Scope: database.RBACScopeAll})
	code, body := serveHITLEndpoint(t, router, http.MethodGet, "/api/hitl/pending?page=1&pageSize=10", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	res := decodeHITLList(t, body)
	if res.Total != 1 || len(res.Items) != 1 || res.Items[0].ID != "p-human" {
		t.Fatalf("pending queue = %v total %d, want only the human-reviewed pending row", listedIDs(res), res.Total)
	}
	item := res.Items[0]
	if item.ConversationID != "conv-a" || item.ToolName != "nmap_scan" || item.Status != "pending" || item.Reviewer != "human" {
		t.Fatalf("row shape changed: %+v", item)
	}
	if string(item.DecidedAt) != "null" {
		t.Fatalf("decidedAt = %s, want JSON null so the UI can tell \"no decision\" from a timestamp", item.DecidedAt)
	}
	if item.Decision != "" {
		t.Fatalf("decision = %q on a pending row, want empty", item.Decision)
	}
	// The keys the sidebar reads must all still be there.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(mustFirstItem(t, body), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "conversationId", "messageId", "mode", "toolName", "toolCallId", "payload", "status", "reviewer", "decision", "comment", "decidedBy", "auditBackend", "auditModel", "createdAt", "decidedAt"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("pending item is missing the %q field", key)
		}
	}
	if res.Page == nil || res.PageSize == nil {
		t.Fatalf("paging fields missing: page=%v pageSize=%v", res.Page, res.PageSize)
	}
}

func mustFirstItem(t *testing.T, body []byte) []byte {
	t.Helper()
	var envelope struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) == 0 {
		t.Fatal("no items to inspect")
	}
	out, err := json.Marshal(envelope.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHITLLogsEndpointPagesFiltersAndReportsRetention(t *testing.T) {
	h, db := newHITLEndpointHandler(t)
	seedEndpointInterrupt(t, db, "d-a", "conv-a", "nmap_scan", "decided", "human", hitlTimePtr(time.Now().Add(-2*time.Hour)))
	seedEndpointInterrupt(t, db, "d-b", "conv-a", "http_get", "decided", "audit_agent", hitlTimePtr(time.Now().Add(-time.Hour)))
	seedEndpointInterrupt(t, db, "p-live", "conv-a", "exec", "pending", "human", nil)

	router := hitlEndpointRouter(h, &security.Session{UserID: "owner", Scope: database.RBACScopeAll})

	code, body := serveHITLEndpoint(t, router, http.MethodGet, "/api/hitl/logs?page=1&pageSize=20", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	res := decodeHITLList(t, body)
	if res.Total != 2 || len(res.Items) != 2 {
		t.Fatalf("logs = %v total %d, want the two decided rows", listedIDs(res), res.Total)
	}
	if res.Items[0].ID != "d-b" {
		t.Fatalf("log order = %v, want newest decision first", listedIDs(res))
	}
	if res.RetentionDays == nil {
		t.Fatal("retentionDays must be reported so the UI can explain why rows disappear")
	}

	code, body = serveHITLEndpoint(t, router, http.MethodGet, "/api/hitl/logs?page=2&pageSize=1", "")
	if code != http.StatusOK {
		t.Fatalf("paged status = %d: %s", code, body)
	}
	res = decodeHITLList(t, body)
	if res.Total != 2 || len(res.Items) != 1 || res.Items[0].ID != "d-a" {
		t.Fatalf("page 2 = %v total %d, want [d-a] of 2", listedIDs(res), res.Total)
	}

	code, body = serveHITLEndpoint(t, router, http.MethodGet, "/api/hitl/logs?decidedBy=agent", "")
	if code != http.StatusOK {
		t.Fatalf("filtered status = %d: %s", code, body)
	}
	res = decodeHITLList(t, body)
	if res.Total != 1 || res.Items[0].ID != "d-b" || res.Items[0].DecidedBy != "audit_agent" {
		t.Fatalf("decidedBy=agent = %v, want [d-b] normalised to audit_agent", listedIDs(res))
	}

	code, body = serveHITLEndpoint(t, router, http.MethodGet, "/api/hitl/logs?q=looks+fine", "")
	res = decodeHITLList(t, body)
	if code != http.StatusOK || res.Total != 2 {
		t.Fatalf("free-text search = %v total %d status %d, want both decided rows", listedIDs(res), res.Total, code)
	}
}

func TestHITLLogDetailEndpointEnforcesConversationAccess(t *testing.T) {
	h, db := newHITLEndpointHandler(t)
	mine := hitlTestConversation(t, db, "mine")
	theirs := hitlTestConversation(t, db, "theirs")
	seedEndpointInterrupt(t, db, "d-mine", mine, "exec", "decided", "human", hitlTimePtr(time.Now()))
	seedEndpointInterrupt(t, db, "d-theirs", theirs, "exec", "decided", "human", hitlTimePtr(time.Now()))
	owner := hitlTestUser(t, db, "hitl-detail-owner")
	if err := database.NewRBAC(db).AssignResourceToUser(owner, "conversation", mine); err != nil {
		t.Fatalf("assign conversation: %v", err)
	}

	router := hitlEndpointRouter(h, &security.Session{UserID: owner, Scope: database.RBACScopeOwn})
	code, body := serveHITLEndpoint(t, router, http.MethodGet, "/api/hitl/logs/d-mine", "")
	if code != http.StatusOK {
		t.Fatalf("own detail status = %d: %s", code, body)
	}
	var detail map[string]any
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("decode detail: %v\n%s", err, body)
	}
	if detail["id"] != "d-mine" || detail["conversationId"] != mine {
		t.Fatalf("detail = %v, want the requested row", detail)
	}

	if code, _ := serveHITLEndpoint(t, router, http.MethodGet, "/api/hitl/logs/d-theirs", ""); code != http.StatusForbidden {
		t.Fatalf("foreign detail status = %d, want 403", code)
	}
	if code, _ := serveHITLEndpoint(t, router, http.MethodGet, "/api/hitl/logs/nope", ""); code != http.StatusNotFound {
		t.Fatalf("unknown detail status = %d, want 404", code)
	}

	// No session at all: the listing is scoped to nothing and the detail is denied,
	// rather than falling back to an unfiltered query.
	anonymous := hitlEndpointRouter(h, nil)
	code, body = serveHITLEndpoint(t, anonymous, http.MethodGet, "/api/hitl/logs", "")
	if res := decodeHITLList(t, body); code != http.StatusOK || res.Total != 0 {
		t.Fatalf("anonymous listing = %d total %d, want an empty page", code, res.Total)
	}
	if code, _ := serveHITLEndpoint(t, anonymous, http.MethodGet, "/api/hitl/logs/d-mine", ""); code != http.StatusForbidden {
		t.Fatalf("anonymous detail status = %d, want 403", code)
	}
}

func TestHITLDismissEndpointCancelsAPendingInterruptOnce(t *testing.T) {
	h, db := newHITLEndpointHandler(t)
	seedEndpointInterrupt(t, db, "p-live", "conv-a", "exec", "pending", "human", nil)
	seedEndpointInterrupt(t, db, "d-done", "conv-a", "exec", "decided", "human", hitlTimePtr(time.Now()))

	router := hitlEndpointRouter(h, &security.Session{UserID: "owner", Scope: database.RBACScopeAll})
	code, body := serveHITLEndpoint(t, router, http.MethodPost, "/api/hitl/dismiss", `{"interruptId":"p-live"}`)
	if code != http.StatusOK {
		t.Fatalf("dismiss status = %d: %s", code, body)
	}
	it, found, err := h.hitlStore.Get("p-live")
	if err != nil || !found {
		t.Fatalf("read back: found=%v err=%v", found, err)
	}
	if it.Status != "cancelled" || it.Decision != "reject" || it.DecidedBy != "human" || it.DecidedAt == nil {
		t.Fatalf("dismissed row = %+v, want a cancelled human decision", it)
	}

	if code, _ := serveHITLEndpoint(t, router, http.MethodPost, "/api/hitl/dismiss", `{"interruptId":"p-live"}`); code != http.StatusNotFound {
		t.Fatalf("second dismiss status = %d, want 404 so the UI stops the countdown", code)
	}
	if code, _ := serveHITLEndpoint(t, router, http.MethodPost, "/api/hitl/dismiss", `{"interruptId":"d-done"}`); code != http.StatusNotFound {
		t.Fatalf("dismissing a decided row = %d, want 404", code)
	}
	if it, _, err := h.hitlStore.Get("d-done"); err != nil || it.Decision != "approve" {
		t.Fatalf("decided row was touched: %+v err=%v", it, err)
	}
	if code, _ := serveHITLEndpoint(t, router, http.MethodPost, "/api/hitl/dismiss", `{}`); code != http.StatusBadRequest {
		t.Fatalf("missing interruptId = %d, want 400", code)
	}
}

// The dismiss endpoint's other job: the tool call blocked in waitDecision must wake with a
// rejection. That step used to reach into HITLManager's lock and pending map from the transport
// layer, and it is HITLManager.DropPending now - which is only checkable from the wire, so this
// test is the behaviour proof of the move. decideCh is buffered, so the wakeup lands whether or
// not the waiter was already blocked; the pending set has to be empty afterwards.
func TestDismissWakesTheWaitingToolCall(t *testing.T) {
	h, db := newHITLEndpointHandler(t)
	conv := hitlTestConversation(t, db, "dismiss-wakes")
	p, err := h.hitlManager.CreatePendingInterrupt(conv, "msg-1", "approval", "exec", "call-1", `{"toolName":"exec"}`, "human")
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}

	router := hitlEndpointRouter(h, &security.Session{UserID: "owner", Scope: database.RBACScopeAll})
	code, body := serveHITLEndpoint(t, router, http.MethodPost, "/api/hitl/dismiss", `{"interruptId":"`+p.InterruptID+`"}`)
	if code != http.StatusOK {
		t.Fatalf("dismiss status = %d: %s", code, body)
	}

	d, err := h.hitlManager.waitDecision(context.Background(), p, 2*time.Second)
	if err != nil {
		t.Fatalf("waitDecision: %v", err)
	}
	if d.Decision != "reject" || d.Comment != "dismissed by user" {
		t.Fatalf("the waiter woke with %+v, want reject / dismissed by user", d)
	}

	h.hitlManager.mu.RLock()
	_, still := h.hitlManager.pending[p.InterruptID]
	h.hitlManager.mu.RUnlock()
	if still {
		t.Fatal("the dismissed interrupt is still in the pending set")
	}
}

func TestHITLDeleteEndpointNeverClearsPendingRows(t *testing.T) {
	h, db := newHITLEndpointHandler(t)
	mine := hitlTestConversation(t, db, "mine")
	theirs := hitlTestConversation(t, db, "theirs")
	seedEndpointInterrupt(t, db, "p-live", mine, "exec", "pending", "human", nil)
	seedEndpointInterrupt(t, db, "d-a", mine, "exec", "decided", "human", hitlTimePtr(time.Now()))
	seedEndpointInterrupt(t, db, "d-b", theirs, "exec", "decided", "human", hitlTimePtr(time.Now()))
	owner := hitlTestUser(t, db, "hitl-delete-owner")
	if err := database.NewRBAC(db).AssignResourceToUser(owner, "conversation", mine); err != nil {
		t.Fatalf("assign conversation: %v", err)
	}

	router := hitlEndpointRouter(h, &security.Session{UserID: owner, Scope: database.RBACScopeOwn})
	code, body := serveHITLEndpoint(t, router, http.MethodDelete, "/api/hitl/logs", `{"ids":["p-live","d-a","d-b","gone"]}`)
	if code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", code, body)
	}
	var res struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("decode delete response: %v\n%s", err, body)
	}
	if res.Deleted != 1 {
		t.Fatalf("deleted = %d, want only the caller's own decided row", res.Deleted)
	}
	for id, want := range map[string]bool{"p-live": true, "d-a": false, "d-b": true} {
		_, found, err := h.hitlStore.Get(id)
		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if found != want {
			t.Errorf("%s present = %v, want %v", id, found, want)
		}
	}

	// "Clear this filtered view" is scoped exactly like the listing is: conv-b is
	// not the caller's, so clearing it must report nothing deleted.
	code, body = serveHITLEndpoint(t, router, http.MethodDelete, "/api/hitl/logs?conversationId="+theirs, `{"all":true}`)
	if code != http.StatusOK {
		t.Fatalf("clear status = %d: %s", code, body)
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("decode clear response: %v\n%s", err, body)
	}
	if res.Deleted != 0 {
		t.Fatalf("cleared %d rows outside the caller's scope, want 0", res.Deleted)
	}
	if _, found, _ := h.hitlStore.Get("d-b"); !found {
		t.Fatal("another conversation's audit row was deleted")
	}
}

func hitlTimePtr(v time.Time) *time.Time { return &v }
