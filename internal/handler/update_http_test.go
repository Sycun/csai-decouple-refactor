package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberstrike-ai/internal/update"

	"github.com/gin-gonic/gin"
)

// writeFakeBinary puts an executable-shaped file where update.StampBinary looks, so the
// "the process is running an older build than the disk" state can be produced for real.
func writeFakeBinary(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, update.Binary), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The HTTP contract of "update my own source in one click": status without a network
// round trip, an apply that returns a job handle instead of blocking a request open for
// the length of a compile, and refusals that stay distinguishable from accidents.

func newUpdateRouter(root string, restart func()) (*gin.Engine, *UpdateHandler) {
	return newUpdateRouterWithSource(root, restart, nil, nil)
}

func newUpdateRouterWithSource(root string, restart func(),
	source func() (string, string, string), save func(string, string, string) error) (*gin.Engine, *UpdateHandler) {
	gin.SetMode(gin.TestMode)
	h := NewUpdateHandler(root, nil, nil, restart, source, save)
	router := gin.New()
	router.GET("/api/system/update", h.GetStatus)
	router.GET("/api/system/update/job", h.Job)
	router.POST("/api/system/update/check", h.Check)
	router.POST("/api/system/update/apply", h.Apply)
	router.POST("/api/system/update/restart", h.Restart)
	router.POST("/api/system/update/rollback", h.Rollback)
	router.POST("/api/system/update/adopt", h.Adopt)
	router.POST("/api/system/update/source", h.SaveSource)
	return router, h
}

func doUpdate(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestUpdateStatusIsServedWithoutTouchingTheNetwork(t *testing.T) {
	router, _ := newUpdateRouter(t.TempDir(), nil)

	w := doUpdate(router, http.MethodGet, "/api/system/update", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
	var body struct {
		Status struct {
			Root       string `json:"root"`
			Installed  bool   `json:"installed"`
			CheckError string `json:"checkError"`
			CanBuild   bool   `json:"canBuild"`
		} `json:"status"`
		Job        *struct{ ID string } `json:"job"`
		CanRestart bool                 `json:"canRestart"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status.Root == "" || body.Status.Installed {
		t.Errorf("a plain directory must report its root and that it is not a git tree: %+v", body.Status)
	}
	if body.Status.CheckError == "" {
		t.Error("the reason an update cannot run must be readable on the page, not just a status code")
	}
	if body.Job != nil {
		t.Error("job must be null before anything has been applied")
	}
	if body.CanRestart {
		t.Error("canRestart must be false when no restart hook was wired")
	}
}

func TestUpdateApplyReturnsAJobAndReportsAnHonestFailure(t *testing.T) {
	router, _ := newUpdateRouter(t.TempDir(), nil)

	w := doUpdate(router, http.MethodPost, "/api/system/update/apply", `{"restart":false}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("apply should be accepted and return a handle, got %d %s", w.Code, w.Body)
	}
	var started struct {
		JobID string `json:"job_id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.JobID == "" {
		t.Fatal("apply must return the job id the page polls")
	}

	// The job runs against a non-git directory, so it finishes with a refusal rather
	// than a merge. What matters is that the reported failure is the real reason.
	deadline := time.Now().Add(10 * time.Second)
	var finished struct {
		Job struct {
			State   string `json:"state"`
			Failure struct {
				Reason string `json:"reason"`
			} `json:"failure"`
		} `json:"job"`
	}
	for time.Now().Before(deadline) {
		w = doUpdate(router, http.MethodGet, "/api/system/update/job", "")
		if err := json.Unmarshal(w.Body.Bytes(), &finished); err != nil {
			t.Fatal(err)
		}
		if finished.Job.State != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finished.Job.State != "failed" {
		t.Fatalf("job state = %q, want failed for a non-git install", finished.Job.State)
	}
	if finished.Job.Failure.Reason != "not_a_repo" {
		t.Errorf("failure reason = %q, want not_a_repo", finished.Job.Failure.Reason)
	}
}

func TestUpdateApplyRefusesRestartWhenNothingCanStartTheProcessAgain(t *testing.T) {
	router, _ := newUpdateRouter(t.TempDir(), nil)

	w := doUpdate(router, http.MethodPost, "/api/system/update/apply", `{"restart":true}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: restarting without a supervisor would just stop the platform", w.Code)
	}
	if !strings.Contains(w.Body.String(), "重启") {
		t.Errorf("the refusal must say why: %s", w.Body)
	}
}

func TestUpdateApplyRejectsMalformedBody(t *testing.T) {
	router, _ := newUpdateRouter(t.TempDir(), nil)

	w := doUpdate(router, http.MethodPost, "/api/system/update/apply", `{"restart": "yes please"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400 for a body that is not valid JSON", w.Code, w.Body)
	}
	// A rejected request must not have left a job behind: the page would then poll a
	// phantom update.
	if job := doUpdate(router, http.MethodGet, "/api/system/update/job", ""); !strings.Contains(job.Body.String(), `"job":null`) {
		t.Errorf("a 400 apply must not create a job, got %s", job.Body)
	}
}

func TestUpdateRollbackWithoutARecordedUpdateIsAConflict(t *testing.T) {
	root := t.TempDir()
	router, _ := newUpdateRouter(root, nil)

	w := doUpdate(router, http.MethodPost, "/api/system/update/rollback", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s, want 409", w.Code, w.Body)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != "no_state" {
		t.Errorf("reason = %v, want no_state so the page can say there is nothing to undo", body["reason"])
	}
}

func TestUpdateSourceSaveValidatesBeforeWriting(t *testing.T) {
	var saved []string
	router, _ := newUpdateRouterWithSource(t.TempDir(), nil,
		func() (string, string, string) { return "", "", "" },
		func(r, u, b string) error { saved = append(saved, r+"|"+u+"|"+b); return nil })

	// A name and an address are two ways to say the same thing; guessing which one the
	// operator meant is worse than asking again.
	w := doUpdate(router, http.MethodPost, "/api/system/update/source", `{"remote":"origin","remoteUrl":"https://github.com/x/y.git"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400 for remote+remoteUrl together", w.Code, w.Body)
	}
	// git's ext:: transport runs commands; it must never reach the config.
	w = doUpdate(router, http.MethodPost, "/api/system/update/source", `{"remoteUrl":"ext::sh -c true"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400 for an ext:: address", w.Code, w.Body)
	}
	if len(saved) != 0 {
		t.Fatalf("a refused save must not reach the config layer: %v", saved)
	}

	// A valid save is trimmed and lands in the config layer as three values.
	w = doUpdate(router, http.MethodPost, "/api/system/update/source", `{"remoteUrl":" https://github.com/Sycun/CyberStrikeAI.git ","branch":"main"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", w.Code, w.Body)
	}
	if len(saved) != 1 || saved[0] != "|https://github.com/Sycun/CyberStrikeAI.git|main" {
		t.Fatalf("saved = %v, want the trimmed address and branch", saved)
	}
}

func TestUpdateStatusCarriesTheConfiguredSource(t *testing.T) {
	router, _ := newUpdateRouterWithSource(t.TempDir(), nil,
		func() (string, string, string) { return "", "https://github.com/Sycun/CyberStrikeAI.git", "main" }, nil)

	w := doUpdate(router, http.MethodGet, "/api/system/update", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body struct {
		Source struct {
			Remote     string `json:"remote"`
			RemoteURL  string `json:"remoteUrl"`
			Branch     string `json:"branch"`
			Configured bool   `json:"configured"`
		} `json:"source"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Source.Configured || body.Source.RemoteURL != "https://github.com/Sycun/CyberStrikeAI.git" || body.Source.Branch != "main" {
		t.Fatalf("source = %+v, want the configured address and branch", body.Source)
	}
}

func TestUpdateAdoptPreviewRefusesWithoutASource(t *testing.T) {
	router, _ := newUpdateRouter(t.TempDir(), nil)

	w := doUpdate(router, http.MethodPost, "/api/system/update/adopt", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400", w.Code, w.Body)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != "no_source" {
		t.Fatalf("reason = %v, want no_source so the page can say what is missing", body["reason"])
	}
}

// The "restart to activate" contract: the page must be able to tell that the binary on
// disk is no longer the one answering requests, the supervision hint must come from the
// environment rather than a guess, and the stand-down must never run when it cannot
// change anything.

func TestUpdateStatusReportsAPendingBinaryAndSupervision(t *testing.T) {
	root := t.TempDir()
	writeFakeBinary(t, root, "the build this process is running")
	router, _ := newUpdateRouter(root, func() {})

	type statusBody struct {
		NeedsRestart  bool   `json:"needsRestart"`
		BinaryBuiltAt string `json:"binaryBuiltAt"`
		Supervised    bool   `json:"supervised"`
		Status        struct {
			HasBinary bool `json:"hasBinary"`
		} `json:"status"`
	}
	read := func() statusBody {
		w := doUpdate(router, http.MethodGet, "/api/system/update", "")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", w.Code, w.Body)
		}
		var body statusBody
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	if got := read(); got.NeedsRestart || got.BinaryBuiltAt != "" {
		t.Fatalf("a freshly started process must call the binary it is running current: %+v", got)
	}
	if !read().Status.HasBinary {
		t.Fatal("the fake binary has to be visible as hasBinary for this test to mean anything")
	}

	// A swap (update, rollback or a CLI run) replaces the file; the process is now old and
	// the page has to be able to say so.
	writeFakeBinary(t, root, "a different build that is now on disk")
	after := read()
	if !after.NeedsRestart || after.BinaryBuiltAt == "" {
		t.Fatalf("replacing the binary must be reported as pending, with when it was built: %+v", after)
	}

	// Supervision is read from the markers launchd/systemd leave in the environment.
	t.Setenv("XPC_SERVICE_NAME", "")
	t.Setenv("INVOCATION_ID", "")
	t.Setenv("JOURNAL_STREAM", "")
	if read().Supervised {
		t.Error("with no supervisor markers in the environment, supervised must be false")
	}
	t.Setenv("XPC_SERVICE_NAME", "com.example.thing")
	if !read().Supervised {
		t.Error("XPC_SERVICE_NAME is launchd's own marker; the page's default must follow it")
	}
}

func TestUpdateRestartOnlyStandsDownWhenSomethingIsPending(t *testing.T) {
	root := t.TempDir()
	writeFakeBinary(t, root, "v1")
	fired := make(chan struct{}, 1)
	router, _ := newUpdateRouter(root, func() { fired <- struct{}{} })

	old := updateRestartDelay
	updateRestartDelay = 20 * time.Millisecond
	t.Cleanup(func() { updateRestartDelay = old })

	// Nothing on disk to activate: a bounce would drop the service and change nothing.
	w := doUpdate(router, http.MethodPost, "/api/system/update/restart", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s, want 409 when no binary is pending", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "nothing_pending") {
		t.Errorf("the refusal must carry its reason: %s", w.Body)
	}
	select {
	case <-fired:
		t.Fatal("a refused restart must not stand the process down")
	case <-time.After(80 * time.Millisecond):
	}

	// A new binary is on disk now; the restart is exactly what makes it real.
	writeFakeBinary(t, root, "v2")
	w = doUpdate(router, http.MethodPost, "/api/system/update/restart", "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s, want 202", w.Code, w.Body)
	}
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("the stand-down hook was never called")
	}
}

func TestUpdateRestartRefusesWithoutAHookOrDuringAJob(t *testing.T) {
	root := t.TempDir()
	writeFakeBinary(t, root, "v1")

	// No hook: standing down would just stop the platform.
	router, _ := newUpdateRouter(root, nil)
	writeFakeBinary(t, root, "v2")
	w := doUpdate(router, http.MethodPost, "/api/system/update/restart", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400 without a restart hook", w.Code, w.Body)
	}

	// A running job owns the tree; standing down halfway through a merge is how an
	// installation becomes unrecoverable.
	router2, h := newUpdateRouter(root, func() {})
	h.mu.Lock()
	h.active = "busy"
	h.jobs["busy"] = &updateJob{ID: "busy", Kind: "apply", State: "running"}
	h.mu.Unlock()
	w = doUpdate(router2, http.MethodPost, "/api/system/update/restart", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s, want 409 while a job is running", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "busy") {
		t.Errorf("the conflict must hand back the job that owns the tree: %s", w.Body)
	}
}
