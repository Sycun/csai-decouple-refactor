package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/routes"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// These are the contract tests for the three surfaces the audit found drifting:
// the i18n dictionaries, the route table, and the hand-written OpenAPI document.
// Each one is a ratchet - it may only become stricter over time.

var i18nPluralSuffix = regexp.MustCompile(`_(zero|one|two|few|many|other)$`)

func flattenKeys(node map[string]any, prefix string, into map[string]bool) {
	for key, value := range node {
		path := prefix + key
		into[path] = true
		if nested, ok := value.(map[string]any); ok {
			flattenKeys(nested, path+".", into)
		}
	}
}

// TestI18nLocaleKeyParity collapses i18next plural variants onto their base key
// before comparing, so legitimate English plural morphology is not reported as
// drift while a genuinely untranslated key is.
func TestI18nLocaleKeyParity(t *testing.T) {
	base := func(name string) map[string]bool {
		path := filepath.Join("..", "..", "web", "static", "i18n", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		flat := map[string]bool{}
		flattenKeys(doc, "", flat)
		out := map[string]bool{}
		for key := range flat {
			out[i18nPluralSuffix.ReplaceAllString(key, "")] = true
		}
		return out
	}

	en := base("en-US.json")
	zh := base("zh-CN.json")

	var onlyZh, onlyEn []string
	for key := range zh {
		if !en[key] {
			onlyZh = append(onlyZh, key)
		}
	}
	for key := range en {
		if !zh[key] {
			onlyEn = append(onlyEn, key)
		}
	}
	sort.Strings(onlyZh)
	sort.Strings(onlyEn)
	if len(onlyZh) > 0 {
		t.Errorf("%d keys exist in zh-CN but are missing from en-US: %v", len(onlyZh), onlyZh)
	}
	if len(onlyEn) > 0 {
		t.Errorf("%d keys exist in en-US but are missing from zh-CN: %v", len(onlyEn), onlyEn)
	}
}

// registeredRoutes resolves the route table with the shared extractor, so this file
// and internal/app agree on what the server serves rather than each carrying a parser.
func registeredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	table, err := routes.Extract(filepath.Join("..", "app"))
	if err != nil {
		t.Fatalf("extract routes: %v", err)
	}
	out := map[string]bool{}
	for _, entry := range table.Lines() {
		out[entry] = true
	}
	return out
}

// ginPathToOpenAPI rewrites :param and *wildcard segments to the OpenAPI
// template form so route strings and spec keys are comparable.

// documentedPaths pulls the path keys out of the hand-written OpenAPI document by
// serving it, which is how the frontend and API consumers see it.
func documentedPaths(t *testing.T) map[string]bool {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil)
	c.Request.Host = "localhost:8080"

	NewOpenAPIHandler(nil, zap.NewNop(), nil, nil).GetOpenAPISpec(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("spec handler returned %d", recorder.Code)
	}
	var spec struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	out := map[string]bool{}
	for path, operations := range spec.Paths {
		for method := range operations {
			out[strings.ToUpper(method)+" "+path] = true
		}
	}
	return out
}

// TestOpenAPIDocumentsOnlyRealRoutes catches the dangerous direction of drift: a
// path in the published spec that the server does not actually serve. That set
// must be empty, forever.
func TestOpenAPIDocumentsOnlyRealRoutes(t *testing.T) {
	routes := registeredRoutes(t)
	if len(routes) < 150 {
		t.Fatalf("route extraction found only %d registrations, the analyzer is broken", len(routes))
	}
	for key := range documentedPaths(t) {
		if routes[key] {
			continue
		}
		// Any is the catch-all registration and satisfies every method.
		method := strings.SplitN(key, " ", 2)[0]
		path := strings.TrimPrefix(key, method+" ")
		if routes["ANY "+path] {
			continue
		}
		t.Errorf("OpenAPI documents %s which no longer exists on the router", key)
	}
}

// TestUndocumentedRouteRatchet is the debt ratchet for the other direction. The
// hand-written spec covers a fraction of the router; that number may only fall.
// Lower the bound as paths get documented - never raise it.
func TestUndocumentedRouteRatchet(t *testing.T) {
	// Measured against the current hand-written spec: 278 registered routes, 132 of
	// them absent from the openapi_paths_*.go groups. Lower this as paths get
	// documented; never raise it.
	const baseline = 132

	routes := registeredRoutes(t)
	documented := documentedPaths(t)

	var missing []string
	for key := range routes {
		if documented[key] {
			continue
		}
		method := strings.SplitN(key, " ", 2)[0]
		path := strings.TrimPrefix(key, method+" ")
		if documented["ANY "+path] || documented[strings.ToUpper("get")+" "+path] {
			continue
		}
		missing = append(missing, key)
	}
	sort.Strings(missing)
	if len(missing) > baseline {
		t.Fatalf("%d registered routes are absent from the OpenAPI document, baseline is %d. "+
			"Document the new ones in the matching internal/handler/openapi_paths_*.go (first 15: %v)", len(missing), baseline, missing[:min(len(missing), 15)])
	}
	if len(missing) < baseline {
		t.Logf("undocumented routes dropped to %d; tighten the baseline in TestUndocumentedRouteRatchet", len(missing))
	}
}

// TestEveryCapabilityKindHasAConsoleLabel is the front-end half of the kind list.
//
// The bundle console renders a unit's kind through plugins.kind.<kind> in both dictionaries, and
// `plugin.Kinds` is the authority on what kinds exist. A kind without a label shows up in the UI as
// the raw key - which is how a new capability kind would ship looking broken to every operator,
// while every Go test stayed green.
func TestEveryCapabilityKindHasAConsoleLabel(t *testing.T) {
	for _, locale := range []string{"zh-CN", "en-US"} {
		path := filepath.Join("..", "..", "web", "static", "i18n", locale+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		pluginsSection, ok := doc["plugins"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no plugins section", locale)
		}
		kinds, ok := pluginsSection["kind"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no plugins.kind block", locale)
		}
		for _, kind := range plugin.Kinds {
			label, ok := kinds[string(kind)].(string)
			if !ok || strings.TrimSpace(label) == "" {
				t.Errorf("%s is missing a console label for capability kind %q", locale, kind)
			}
		}
		// The other direction too: a label for a kind that no longer exists is dead weight that
		// looks like coverage.
		for name := range kinds {
			known := false
			for _, kind := range plugin.Kinds {
				if string(kind) == name {
					known = true
				}
			}
			if !known {
				t.Errorf("%s labels a kind %q that plugin.Kinds does not have", locale, name)
			}
		}
	}
}
