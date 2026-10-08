package handler

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/agentmode"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"

	"github.com/gin-gonic/gin"
)

// fetchAgentModes 直调 handler（不架服务器）拿到目录，并校验 default 字段。
func fetchAgentModes(t *testing.T, h *AgentModeHandler) []agentmode.Entry {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	h.GetAgentModes(c)
	var body struct {
		Default string            `json:"default"`
		Modes   []agentmode.Entry `json:"modes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /api/agent-modes body %s: %v", rec.Body.String(), err)
	}
	if body.Default != agentmode.DefaultID {
		t.Fatalf("default = %q, want %q", body.Default, agentmode.DefaultID)
	}
	return body.Modes
}

// 装/卸模式单元 → 目录跟随（"不点不存在"）；引擎开关只影响 available，不影响存在性。
func TestAgentModesFollowActivatedUnits(t *testing.T) {
	enabled := &config.Config{MultiAgent: config.MultiAgentConfig{Enabled: true}}

	// 未装任何包：只剩内核内置单代理。
	got := fetchAgentModes(t, NewAgentModeHandler(enabled))
	if len(got) != 1 || got[0].ID != agentmode.DefaultID || !got[0].Available || !got[0].Builtin {
		t.Fatalf("empty table catalog = %+v, want only the builtin single", got)
	}

	// 装三个模式单元（模拟安装「多代理编排包」）：目录出现三条、各自可用。
	installModeUnits(t, "deep", "plan_execute", "supervisor")
	got = fetchAgentModes(t, NewAgentModeHandler(enabled))
	if len(got) != 4 {
		t.Fatalf("activated catalog = %+v, want 4 entries", got)
	}
	byID := map[string]agentmode.Entry{}
	for _, e := range got {
		byID[e.ID] = e
	}
	deep, ok := byID["deep"]
	if !ok || !deep.Available || deep.Runner != agentmode.RunnerMultiAgent {
		t.Fatalf("deep entry = %+v", deep)
	}
	if deep.Label == "" || deep.LabelKey == "" || deep.HintKey == "" {
		t.Fatalf("deep entry must carry display metadata: %+v", deep)
	}

	// 引擎关掉：三条仍在目录里，但 available=false 且指名原因；内置单代理恒可用。
	off := fetchAgentModes(t, NewAgentModeHandler(&config.Config{}))
	if len(off) != 4 {
		t.Fatalf("engine-off catalog = %+v, want still 4 entries", off)
	}
	for _, e := range off {
		if e.ID == agentmode.DefaultID {
			if !e.Available {
				t.Fatalf("builtin single must stay available with engine off: %+v", e)
			}
			continue
		}
		if e.Available || e.Reason != agentmode.ReasonEngineDisabled {
			t.Fatalf("%s with engine off = %+v, want unavailable/%s", e.ID, e, agentmode.ReasonEngineDisabled)
		}
	}

	// 停用 deep 单元：与卸载同义——从目录消失。
	table := plugin.Global()
	if _, err := table.SetEnabled("mode/deep", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	got = fetchAgentModes(t, NewAgentModeHandler(enabled))
	if len(got) != 3 {
		t.Fatalf("catalog after disabling deep = %+v, want 3 entries", got)
	}
	for _, e := range got {
		if e.ID == "deep" {
			t.Fatalf("a disabled unit must disappear from the catalog: %+v", got)
		}
	}
}

// checkModeUnits：装前校验用的是与目录读取同一把尺子（agentmode.ReadDeclaration）——
// id 与文件名不符、内核不认识、试图覆盖内置，三种都整包拒绝。
func TestCheckModeUnitsRefusesBrokenDeclarations(t *testing.T) {
	h := &PluginHandler{}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	modeBundle := func(name, path string) *plugin.Bundle {
		u, err := plugin.NewUnit(plugin.KindMode, name, path)
		if err != nil {
			t.Fatal(err)
		}
		return &plugin.Bundle{ID: "probe", Units: []plugin.Unit{u}}
	}

	good := write("deep.yaml", "id: deep\n")
	if err := h.checkModeUnits(modeBundle("deep", good)); err != nil {
		t.Fatalf("valid declaration refused: %v", err)
	}

	mismatch := write("deep.yaml", "id: supervisor\n")
	if err := h.checkModeUnits(modeBundle("deep", mismatch)); err == nil {
		t.Fatalf("id/file mismatch must be refused")
	}

	unknown := write("bogus.yaml", "id: bogus\n")
	if err := h.checkModeUnits(modeBundle("bogus", unknown)); err == nil {
		t.Fatalf("unknown mode must be refused")
	}

	builtin := write("eino_single.yaml", "id: eino_single\n")
	if err := h.checkModeUnits(modeBundle("eino_single", builtin)); err == nil {
		t.Fatalf("builtin override must be refused")
	}
}
