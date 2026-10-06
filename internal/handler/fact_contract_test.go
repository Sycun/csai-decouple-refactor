package handler

import (
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/store"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// These tests pin the wire shape of the project blackboard's HTTP surface before its SQL moves into
// a store of its own. They are characterization tests: where the answer looks odd, the oddity is the
// pre-existing contract and the comment says so.

func openFactContractDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "facts-contract.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newFactContractRouter registers the fact routes exactly as routes_project.go does, with a session
// injected the way the auth middleware would leave it.
func newFactContractRouter(h *ProjectHandler, session *security.Session) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if session != nil {
			c.Set(security.ContextSessionKey, *session)
		}
		c.Next()
	})
	group := router.Group("/api/projects/:id")
	group.GET("/facts", h.ListFacts)
	group.POST("/facts", h.CreateFact)
	group.PUT("/facts/:factId", h.UpdateFact)
	group.DELETE("/facts/:factId", h.DeleteFact)
	group.POST("/facts/deprecate", h.DeprecateFact)
	group.POST("/facts/restore", h.RestoreFact)
	group.GET("/fact-edges", h.ListFactEdges)
	return router
}

func seedFact(t *testing.T, db *database.DB, projectID, key, category, summary, body, confidence string, pinned bool) *store.ProjectFact {
	t.Helper()
	fact, err := database.NewFacts(db).UpsertProjectFact(&store.ProjectFact{
		ProjectID: projectID, FactKey: key, Category: category, Summary: summary, Body: body,
		Confidence: confidence, Pinned: pinned,
	})
	if err != nil {
		t.Fatalf("seed fact %s: %v", key, err)
	}
	return fact
}

func factSummaries(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var items []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode fact list: %v (%s)", err, w.Body.String())
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item["fact_key"].(string))
	}
	return out
}

// factListKeys reads the key set of the first row of a list answer. jsonKeys cannot be used here: the
// listing is a bare array, and a key set taken from an empty array would prove nothing.
func factListKeys(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("fact list is not an array of objects: %v (%s)", err, w.Body.String())
	}
	if len(items) == 0 {
		t.Fatalf("fact list is empty: %s", w.Body.String())
	}
	keys := make([]string, 0, len(items[0]))
	for key := range items[0] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestFactContractListKeysOrderAndFilters pins the bare-array answer, the key set the console reads,
// the pinned-first ordering and every filter the query string can set.
func TestFactContractListKeysOrderAndFilters(t *testing.T) {
	db := openFactContractDB(t)
	project, err := database.NewProjects(db).CreateProject(&database.Project{Name: "board", Status: "active"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	router := newFactContractRouter(NewProjectHandler(db, zap.NewNop()), &security.Session{UserID: "root", Scope: database.RBACScopeAll})
	path := "/api/projects/" + project.ID + "/facts"

	seedFact(t, db, project.ID, "note.pinned", "note", "pinned summary", "", "confirmed", true)
	time.Sleep(5 * time.Millisecond)
	seedFact(t, db, project.ID, "note.old", "note", "old summary", "old body", "tentative", false)
	time.Sleep(5 * time.Millisecond)
	seedFact(t, db, project.ID, "vuln.sql", "finding", "sql injection in login", "recent body", "confirmed", false)
	time.Sleep(5 * time.Millisecond)
	seedFact(t, db, project.ID, "note.gone", "note", "deprecated one", "", "deprecated", false)

	w := doContract(t, router, http.MethodGet, path, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", w.Code, w.Body.String())
	}
	var items []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("list is not a JSON array: %v (%s)", err, w.Body.String())
	}
	if len(items) != 4 {
		t.Fatalf("list length = %d, want 4 (deprecated rows are listed unless excluded): %s", len(items), w.Body.String())
	}
	requireKeys(t, factListKeys(t, w), "id", "project_id", "fact_key", "category", "summary", "body",
		"confidence", "pinned", "created_at", "updated_at")
	if items[0]["fact_key"] != "note.pinned" {
		t.Fatalf("first row = %v, want the pinned fact first even though it is the oldest row", items[0]["fact_key"])
	}
	// body is a plain string here, never null: the read COALESCEs it, and the console concatenates it.
	if _, ok := items[0]["body"].(string); !ok {
		t.Fatalf("body type = %T, want string", items[0]["body"])
	}
	// source_conversation_id / related_vulnerability_id are omitempty, so an unlinked row omits them.
	if _, present := items[0]["related_vulnerability_id"]; present {
		t.Fatalf("unlinked row carries related_vulnerability_id: %v", items[0])
	}

	got := factSummaries(t, w)
	if strings.Join(got, ",") != "note.pinned,note.gone,vuln.sql,note.old" {
		t.Fatalf("order = %v, want the pin first then updated_at DESC", got)
	}

	w = doContract(t, router, http.MethodGet, path+"?exclude_deprecated=1", nil)
	if got := factSummaries(t, w); len(got) != 3 || strings.Join(got, ",") != "note.pinned,vuln.sql,note.old" {
		t.Fatalf("exclude_deprecated = %v", got)
	}
	w = doContract(t, router, http.MethodGet, path+"?category=finding", nil)
	if got := factSummaries(t, w); len(got) != 1 || got[0] != "vuln.sql" {
		t.Fatalf("category filter = %v", got)
	}
	w = doContract(t, router, http.MethodGet, path+"?confidence=tentative", nil)
	if got := factSummaries(t, w); len(got) != 1 || got[0] != "note.old" {
		t.Fatalf("confidence filter = %v", got)
	}
	// Search is one LIKE over three columns, matched case-sensitively for ASCII.
	w = doContract(t, router, http.MethodGet, path+"?search=injection", nil)
	if got := factSummaries(t, w); len(got) != 1 || got[0] != "vuln.sql" {
		t.Fatalf("search filter = %v", got)
	}
	w = doContract(t, router, http.MethodGet, path+"?limit=2&offset=2", nil)
	if got := factSummaries(t, w); len(got) != 2 || strings.Join(got, ",") != "vuln.sql,note.old" {
		t.Fatalf("limit=2&offset=2 = %v, want the third and fourth rows", got)
	}
	// limit<=0 falls back to 100 rather than meaning "no rows".
	w = doContract(t, router, http.MethodGet, path+"?limit=0", nil)
	if got := factSummaries(t, w); len(got) != 4 {
		t.Fatalf("limit=0 = %v, want all four rows", got)
	}
}

// TestFactContractDetailByFactKeyAndLinkViews pins the two detail answers: ?fact_key= is a single
// object (not an array), and the link views change its shape.
func TestFactContractDetailByFactKeyAndLinkViews(t *testing.T) {
	db := openFactContractDB(t)
	project, err := database.NewProjects(db).CreateProject(&database.Project{Name: "board", Status: "active"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	router := newFactContractRouter(NewProjectHandler(db, zap.NewNop()), &security.Session{UserID: "root", Scope: database.RBACScopeAll})
	path := "/api/projects/" + project.ID + "/facts"

	seedFact(t, db, project.ID, "note.source", "note", "source", "", "confirmed", false)
	target := seedFact(t, db, project.ID, "note.target", "note", "target", "", "confirmed", false)
	if _, err := database.NewFacts(db).AddProjectFactEdge(project.ID, store.ProjectFactEdgeInput{To: "note.target", Type: "leads_to", Confidence: "confirmed"}, "note.source", ""); err != nil {
		t.Fatalf("add edge: %v", err)
	}

	w := doContract(t, router, http.MethodGet, path+"?fact_key=note.target", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("detail = %d: %s", w.Code, w.Body.String())
	}
	detail := decodeObject(t, w)
	if detail["id"] != target.ID {
		t.Fatalf("detail id = %v, want %s", detail["id"], target.ID)
	}
	// Without include_links the response is the row itself: no link fields at all.
	requireKeys(t, jsonKeys(t, w), "id", "project_id", "fact_key", "category", "summary", "body",
		"confidence", "pinned", "created_at", "updated_at")

	w = doContract(t, router, http.MethodGet, path+"?fact_key=note.target&include_links=1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("detail with links = %d: %s", w.Code, w.Body.String())
	}
	linked := decodeObject(t, w)
	incoming, ok := linked["incoming_links"].([]any)
	if !ok || len(incoming) != 1 {
		t.Fatalf("incoming_links = %v, want the one edge pointing at this fact", linked["incoming_links"])
	}
	// outgoing_links is omitempty, so a fact with no out-edges omits the key rather than sending [].
	if _, present := linked["outgoing_links"]; present {
		t.Fatalf("fact without out-edges carries outgoing_links: %v", linked["outgoing_links"])
	}

	// A missing key is a 404 whose body is the storage error string, not a structured code.
	w = doContract(t, router, http.MethodGet, path+"?fact_key=note.absent", nil)
	if w.Code != http.StatusNotFound || decodeObject(t, w)["error"] != "事实不存在" {
		t.Fatalf("missing key = %d %s, want 404 事实不存在", w.Code, w.Body.String())
	}

	w = doContract(t, router, http.MethodGet, path+"?include_link_counts=1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("link counts = %d: %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("link count list is not an array: %v (%s)", err, w.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("link count rows = %d, want 2", len(rows))
	}
	// This view wraps every row, so even a fact with no links at all carries the link_counts object.
	if _, ok := rows[0]["link_counts"]; !ok {
		t.Fatalf("row without links lacks link_counts: %v", rows[0])
	}

	w = doContract(t, router, http.MethodGet, "/api/projects/"+project.ID+"/fact-edges", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("edges = %d: %s", w.Code, w.Body.String())
	}
	var edges []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &edges); err != nil {
		t.Fatalf("edges not an array: %v (%s)", err, w.Body.String())
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	for _, key := range []string{"id", "project_id", "source_fact_key", "target_fact_key", "edge_type", "confidence", "created_at", "updated_at"} {
		if _, ok := edges[0][key]; !ok {
			t.Fatalf("edge lacks %s: %v", key, edges[0])
		}
	}
}

// TestFactContractCreateUpdateAndRenameAnswers pins the write answers, including the rename's edge
// cascade and the two shapes of a rejected body.
func TestFactContractCreateUpdateAndRenameAnswers(t *testing.T) {
	db := openFactContractDB(t)
	project, err := database.NewProjects(db).CreateProject(&database.Project{Name: "board", Status: "active"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	router := newFactContractRouter(NewProjectHandler(db, zap.NewNop()), &security.Session{UserID: "root", Scope: database.RBACScopeAll})
	path := "/api/projects/" + project.ID + "/facts"

	// summary and fact_key are both required by binding; the answer is 400 with the validator string.
	if w := doContract(t, router, http.MethodPost, path, []byte(`{"fact_key":"note.a"}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("create without summary = %d, want 400: %s", w.Code, w.Body.String())
	}
	if w := doContract(t, router, http.MethodPost, path, []byte(`{"summary":"x"}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("create without fact_key = %d, want 400: %s", w.Code, w.Body.String())
	}
	// A syntactically invalid key is refused by storage, and storage errors answer 400 here, not 422.
	if w := doContract(t, router, http.MethodPost, path, []byte(`{"fact_key":"_bad","summary":"x"}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid key = %d, want 400: %s", w.Code, w.Body.String())
	}

	seedFact(t, db, project.ID, "note.b", "note", "the dependency", "", "confirmed", false)

	w := doContract(t, router, http.MethodPost, path, []byte(`{"fact_key":"note.a","category":"note","summary":"first","body":"- 依赖事实: note.b","confidence":"confirmed","pinned":true}`))
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	created := decodeObject(t, w)
	if created["fact_key"] != "note.a" || created["pinned"] != true || created["confidence"] != "confirmed" {
		t.Fatalf("create echo = %v", created)
	}
	// With no explicit links, the body's 关联 lines are parsed into incoming edges on create.
	if _, present := created["incoming_links"]; !present {
		t.Fatalf("created fact with a parsed link lacks incoming_links: %v", created)
	}
	factID, _ := created["id"].(string)
	if factID == "" {
		t.Fatalf("created fact has no id: %v", created)
	}

	// Renaming a fact through PUT does not work today, and this pins the broken answer rather than the
	// intended one: UpsertProjectFact looks the row up by (project_id, fact_key), so a changed key
	// falls into the INSERT branch carrying the old primary key and the write dies on the unique index.
	// The handler turns that into a 400 with the raw SQLite text. The edge cascade therefore never runs.
	w = doContract(t, router, http.MethodPut, path+"/"+factID, []byte(`{"fact_key":"note.renamed"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("rename = %d, want the pre-existing 400: %s", w.Code, w.Body.String())
	}
	renamedErr, _ := decodeObject(t, w)["error"].(string)
	if !strings.Contains(renamedErr, "UNIQUE constraint failed: project_facts.id") {
		t.Fatalf("rename error = %q, want the primary-key collision the current code produces", renamedErr)
	}
	if still, err := database.NewFacts(db).GetProjectFactByKey(project.ID, "note.a"); err != nil || still == nil {
		t.Fatalf("fact after the refused rename: %v (%v), want the original key untouched", still, err)
	}

	// Updating a fact of another project is a 404 (the handler compares ProjectID itself).
	other, _ := database.NewProjects(db).CreateProject(&database.Project{Name: "other", Status: "active"})
	if w := doContract(t, router, http.MethodPut, "/api/projects/"+other.ID+"/facts/"+factID, []byte(`{"summary":"x"}`)); w.Code != http.StatusNotFound {
		t.Fatalf("cross-project update = %d, want 404", w.Code)
	}
	if w := doContract(t, router, http.MethodPut, path+"/no-such-fact", []byte(`{"summary":"x"}`)); w.Code != http.StatusNotFound {
		t.Fatalf("unknown fact update = %d, want 404", w.Code)
	}
	if w := doContract(t, router, http.MethodPut, path+"/"+factID, []byte(`{"summary":`)); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed update body = %d, want 400", w.Code)
	}

	// clear_body does not clear anything today, and this pins that: UpdateFact sets the body to "" and
	// UpsertProjectFact's merge keeps the stored body whenever the incoming one is blank. On this fact
	// the body then grows the auto-synced 关联 section, because the blank body still routes through the
	// body-link parser. Recorded as a defect to fix separately, not as the intended contract.
	if w := doContract(t, router, http.MethodPut, path+"/"+factID, []byte(`{"clear_body":true}`)); w.Code != http.StatusOK {
		t.Fatalf("clear body = %d: %s", w.Code, w.Body.String())
	}
	stored, err := database.NewFacts(db).GetProjectFact(factID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(stored.Body, "依赖事实: note.b") {
		t.Fatalf("body after clear_body = %q, want the pinned answer: the original link line survives", stored.Body)
	}

	// On a fact whose body carries no link markup the same request leaves the body untouched at all.
	plain := seedFact(t, db, project.ID, "note.plain", "note", "plain", "just prose", "tentative", false)
	if w := doContract(t, router, http.MethodPut, path+"/"+plain.ID, []byte(`{"clear_body":true}`)); w.Code != http.StatusOK {
		t.Fatalf("clear plain body = %d: %s", w.Code, w.Body.String())
	}
	storedPlain, err := database.NewFacts(db).GetProjectFact(plain.ID)
	if err != nil {
		t.Fatalf("read plain back: %v", err)
	}
	if storedPlain.Body != "just prose" {
		t.Fatalf("plain body after clear_body = %q, want the pinned answer: unchanged", storedPlain.Body)
	}
}

// TestFactContractDeleteDeprecateRestoreAnswers pins the three state-change answers, all of which the
// console buttons depend on: success carries {"success":true} and nothing else.
func TestFactContractDeleteDeprecateRestoreAnswers(t *testing.T) {
	db := openFactContractDB(t)
	project, err := database.NewProjects(db).CreateProject(&database.Project{Name: "board", Status: "active"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	router := newFactContractRouter(NewProjectHandler(db, zap.NewNop()), &security.Session{UserID: "root", Scope: database.RBACScopeAll})
	path := "/api/projects/" + project.ID + "/facts"

	live := seedFact(t, db, project.ID, "note.live", "note", "live", "", "confirmed", false)
	seedFact(t, db, project.ID, "note.linked", "note", "linked", "", "tentative", false)
	if _, err := database.NewFacts(db).AddProjectFactEdge(project.ID, store.ProjectFactEdgeInput{To: "note.linked", Type: "depends_on"}, "note.live", ""); err != nil {
		t.Fatalf("add edge: %v", err)
	}

	w := doContract(t, router, http.MethodPost, path+"/deprecate", []byte(`{"fact_key":"note.live"}`))
	if w.Code != http.StatusOK || decodeObject(t, w)["success"] != true {
		t.Fatalf("deprecate = %d %s, want 200 success", w.Code, w.Body.String())
	}
	deprecateKeys := func() []string {
		w := doContract(t, router, http.MethodGet, path+"?category=note", nil)
		var rows []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("decode after deprecate: %v", err)
		}
		out := []string{}
		for _, row := range rows {
			if row["confidence"] == "deprecated" {
				out = append(out, row["fact_key"].(string))
			}
		}
		return out
	}
	if got := deprecateKeys(); len(got) != 1 || got[0] != "note.live" {
		t.Fatalf("deprecated rows = %v, want only note.live", got)
	}
	// Deprecating a fact marks its edges deprecated too - the graph view filters on that.
	edges, err := database.NewFacts(db).ListProjectFactEdgesByProject(project.ID)
	if err != nil {
		t.Fatalf("list edges: %v", err)
	}
	if len(edges) != 1 || edges[0].Confidence != "deprecated" {
		t.Fatalf("edges after deprecate = %+v, want the one edge deprecated", edges)
	}

	// An unknown key is the storage's "事实不存在", answered as 404.
	if w := doContract(t, router, http.MethodPost, path+"/deprecate", []byte(`{"fact_key":"note.absent"}`)); w.Code != http.StatusNotFound {
		t.Fatalf("deprecate unknown = %d, want 404", w.Code)
	}
	if w := doContract(t, router, http.MethodPost, path+"/deprecate", []byte(`{}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("deprecate without key = %d, want 400", w.Code)
	}

	// Restoring is only legal from deprecated, and every refusal is a 400 (not 404/409).
	if w := doContract(t, router, http.MethodPost, path+"/restore", []byte(`{"fact_key":"note.linked","confidence":"tentative"}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("restore of a live fact = %d, want the pre-existing 400", w.Code)
	}
	if w := doContract(t, router, http.MethodPost, path+"/restore", []byte(`{"fact_key":"note.live","confidence":"urgent"}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("restore with a bad confidence = %d, want 400", w.Code)
	}
	w = doContract(t, router, http.MethodPost, path+"/restore", []byte(`{"fact_key":"note.live","confidence":"confirmed"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("restore = %d: %s", w.Code, w.Body.String())
	}
	restored, err := database.NewFacts(db).GetProjectFactByKey(project.ID, "note.live")
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if restored.Confidence != "confirmed" {
		t.Fatalf("confidence after restore = %q, want confirmed", restored.Confidence)
	}

	w = doContract(t, router, http.MethodDelete, path+"/"+live.ID, nil)
	if w.Code != http.StatusOK || decodeObject(t, w)["success"] != true {
		t.Fatalf("delete = %d %s, want 200 success", w.Code, w.Body.String())
	}
	// Deleting the fact removes its edges as well; nothing is left pointing at the dead key.
	edges, err = database.NewFacts(db).ListProjectFactEdgesByProject(project.ID)
	if err != nil {
		t.Fatalf("list edges after delete: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("edges after delete = %+v, want none", edges)
	}
	if _, err := database.NewFacts(db).GetProjectFact(live.ID); err == nil {
		t.Fatal("fact still readable after delete")
	}
	// The other project's fact id is a 404, and so is an unknown id.
	for _, target := range []string{"no-such-fact", live.ID} {
		if w := doContract(t, router, http.MethodDelete, path+"/"+target, nil); w.Code != http.StatusNotFound {
			t.Fatalf("delete %s = %d, want 404", target, w.Code)
		}
	}
}
