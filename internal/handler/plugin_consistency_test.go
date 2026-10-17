package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberstrike-ai/internal/plugin"

	"github.com/gin-gonic/gin"
)

type blockingInstallMemory struct {
	entered, release, forgotten chan struct{}
	mu                          sync.Mutex
	recorded                    bool
}

func (s *blockingInstallMemory) Record(string, string, []string) error {
	close(s.entered)
	<-s.release
	s.mu.Lock()
	s.recorded = true
	s.mu.Unlock()
	return nil
}

func (s *blockingInstallMemory) Forget(string) error {
	s.mu.Lock()
	s.recorded = false
	s.mu.Unlock()
	close(s.forgotten)
	return nil
}

func TestConcurrentUninstallWaitsForInstallPersistence(t *testing.T) {
	env := newPluginTestEnv(t, true)
	memory := &blockingInstallMemory{entered: make(chan struct{}), release: make(chan struct{}), forgotten: make(chan struct{})}
	env.plugins.installs = memory
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/install", env.plugins.Install)
	router.DELETE("/bundles/:id", env.plugins.Uninstall)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	installed := make(chan *httptest.ResponseRecorder, 1)
	go func() { installed <- request(http.MethodPost, "/install", `{"bundle":"reporting-pack"}`) }()
	select {
	case <-memory.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("install did not reach persistence")
	}
	uninstalled := make(chan *httptest.ResponseRecorder, 1)
	go func() { uninstalled <- request(http.MethodDelete, "/bundles/reporting-pack", "") }()
	select {
	case <-memory.forgotten:
		t.Error("uninstall overtook the unfinished install record")
	case <-time.After(100 * time.Millisecond):
	}
	close(memory.release)
	for _, done := range []chan *httptest.ResponseRecorder{installed, uninstalled} {
		select {
		case rec := <-done:
			if rec.Code != http.StatusOK {
				t.Errorf("mutation failed: %d %s", rec.Code, rec.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("mutation deadlocked")
		}
	}
	memory.mu.Lock()
	recorded := memory.recorded
	memory.mu.Unlock()
	if _, present := env.table.Bundle("reporting-pack"); present || recorded {
		t.Fatalf("uninstall left inconsistent state: installed=%v recorded=%v", present, recorded)
	}
}

func TestRejectedRollbackKeepsLiveFiles(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "unknown selection"
		if conflict {
			name = "ownership conflict"
		}
		t.Run(name, func(t *testing.T) {
			env := newPluginTestEnv(t, true)
			pack := filepath.Join(env.bundles, "reporting-pack")
			if _, err := plugin.SnapshotBundle(env.bundles, "reporting-pack", "1.0.0", pack); err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(pack, plugin.ManifestFileName)
			before, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			live := strings.Replace(string(before), "1.0.0", "2.0.0", 1)
			writeTestFile(t, manifest, live)
			selection, status := `"role/missing"`, http.StatusBadRequest
			if conflict {
				unit, err := plugin.NewUnit(plugin.KindRole, "报告撰写", filepath.Join(t.TempDir(), "role.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				if err := env.table.PutLocal(unit); err != nil {
					t.Fatal(err)
				}
				selection, status = `"role/报告撰写"`, http.StatusConflict
			}
			rec := env.do(t, http.MethodPost, "/api/plugins/install", `{"bundle":"reporting-pack","from_version":"1.0.0","units":[`+selection+`]}`)
			if rec.Code != status {
				t.Fatalf("rollback returned %d, want %d: %s", rec.Code, status, rec.Body.String())
			}
			if after, err := os.ReadFile(manifest); err != nil || string(after) != live {
				t.Fatalf("rejected rollback replaced the live manifest: %q err=%v", after, err)
			}
			if _, installed := env.table.Bundle("reporting-pack"); installed {
				t.Fatal("rejected rollback changed the table")
			}
		})
	}
}
