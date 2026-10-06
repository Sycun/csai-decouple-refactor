package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/security"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// DeleteExecution has two very different "the record is not there" cases, and they used to
// be answered the same way: a missing row and a broken database both produced 200
// "执行记录不存在或已被删除". A storage failure reported as a successful delete is worse
// than a 500 - the operator stops looking, while the execution is still in the table and
// still counted in the statistics.

func deleteExecutionRequest(h *MonitorHandler, user *database.RBACUser, id string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/monitor/execution/"+id, nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	// Global scope: this test is about the store outcome, not about ownership (that is
	// TestMonitorExecutionDetailRejectsForeignOwner).
	c.Set(security.ContextSessionKey, security.Session{
		UserID: user.ID, Scope: database.RBACScopeAll,
	})
	h.DeleteExecution(c)
	return w
}

func TestDeleteExecutionOfMissingRecordIsIdempotentSuccess(t *testing.T) {
	db, user := setupConversationRBACTest(t)
	h := NewMonitorHandler(mcp.NewServerWithStorage(zap.NewNop(), database.NewMonitor(db)), nil, db, zap.NewNop())

	w := deleteExecutionRequest(h, user, "never-existed")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a record that is genuinely absent: %s", w.Code, w.Body)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["message"] != "执行记录不存在或已被删除" {
		t.Errorf("body = %v, want the idempotent delete message", body)
	}
}

func TestDeleteExecutionReportsStorageFailureAsFailure(t *testing.T) {
	db, user := setupConversationRBACTest(t)
	h := NewMonitorHandler(mcp.NewServerWithStorage(zap.NewNop(), database.NewMonitor(db)), nil, db, zap.NewNop())

	// A closed store is the real fault shape: the query errors with something that is not
	// sql.ErrNoRows.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	w := deleteExecutionRequest(h, user, "exec-whose-store-is-down")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s, want 500 - a storage failure must not read as a completed delete", w.Code, w.Body)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["message"] != nil {
		t.Errorf("a failed delete must not carry the success message, got %v", body)
	}
	if body["error"] == nil {
		t.Error("the response must say what went wrong")
	}
}
