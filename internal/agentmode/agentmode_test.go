package agentmode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这张表是「别名 union」的账本：每一条都来自某一份被收掉的既有实现，
// 收口后任何入口（会话、机器人、workflow、前端存储）都必须解析出同一个 id。
func TestCanonicalCoversEveryHistoricalAlias(t *testing.T) {
	cases := map[string]string{
		// config.NormalizeAgentMode / parseRobotAgentMode / workflow("single","chat")
		"eino_single": "eino_single",
		"EINO_SINGLE": "eino_single",
		"eino-single": "eino_single",
		"single":      "eino_single",
		"chat":        "eino_single",
		"单代理":         "eino_single",
		"eino单代理":     "eino_single",
		"eino 单代理":    "eino_single",
		"  单代理  ":     "eino_single",
		// deep 与前端 webshell 历史值 "multi"
		"deep":  "deep",
		"Deep":  "deep",
		"multi": "deep",
		// plan_execute 的四种拼写（config/robot/store/workflow/前端共同集）
		"plan_execute": "plan_execute",
		"plan-execute": "plan_execute",
		"planexecute":  "plan_execute",
		"pe":           "plan_execute",
		"PE":           "plan_execute",
		// supervisor 的三种拼写
		"supervisor": "supervisor",
		"super":      "supervisor",
		"sv":         "supervisor",
	}
	for in, want := range cases {
		got, ok := Canonical(in)
		if !ok || got != want {
			t.Errorf("Canonical(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
}

func TestCanonicalRejectsEmptyAndUnknown(t *testing.T) {
	for _, in := range []string{"", "   ", "bogus", "plan-execution", "supervisor2"} {
		if id, ok := Canonical(in); ok {
			t.Errorf("Canonical(%q) = (%q, true), want not ok", in, id)
		}
	}
}

func TestResolveOrchestrationKeepsSpaceOfThree(t *testing.T) {
	cases := map[string]string{
		"plan_execute": "plan_execute",
		"pe":           "plan_execute",
		"supervisor":   "supervisor",
		"sv":           "supervisor",
		"deep":         "deep",
		// 落入编排空间的旧值/垃圾值一律回落 deep（既有语义：非多代理参数即 deep）
		"":            "deep",
		"eino_single": "deep",
		"bogus":       "deep",
	}
	for in, want := range cases {
		if got := ResolveOrchestration(in); got != want {
			t.Errorf("ResolveOrchestration(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuiltinSingleIsAlwaysInCatalogAndAlone(t *testing.T) {
	got := Build(nil, true)
	if len(got) != 1 {
		t.Fatalf("empty table should yield only the builtin single, got %d entries: %+v", len(got), got)
	}
	if got[0].ID != DefaultID || !got[0].Available || !got[0].Builtin {
		t.Fatalf("builtin entry = %+v, want available builtin %q", got[0], DefaultID)
	}
}

func deepUnit() Unit {
	return Unit{Name: "deep", Path: "/packs/multi-agent-orchestration/modes/deep.yaml", Bundle: "multi-agent-orchestration", Enabled: true}
}

func TestBuildActivatesOnlyEnabledKnownUnits(t *testing.T) {
	got := Build([]Unit{deepUnit()}, true)
	if len(got) != 2 || got[1].ID != "deep" || !got[1].Available || got[1].Bundle != "multi-agent-orchestration" {
		t.Fatalf("activated catalog = %+v, want builtin + available deep", got)
	}

	off := deepUnit()
	off.Enabled = false
	got = Build([]Unit{off}, true)
	if len(got) != 1 {
		t.Fatalf("disabled unit must disappear (不点不存在), got %+v", got)
	}

	override := Unit{Name: "eino_single", Path: "/packs/x/modes/eino_single.yaml", Bundle: "evil", Enabled: true}
	unknown := Unit{Name: "bogus_mode", Path: "/packs/x/modes/bogus_mode.yaml", Bundle: "evil", Enabled: true}
	got = Build([]Unit{override, unknown}, true)
	if len(got) != 1 || got[0].ID != DefaultID {
		t.Fatalf("builtin override / unknown unit must not enter catalog, got %+v", got)
	}
}

func TestBuildReportsEngineDisabled(t *testing.T) {
	got := Build([]Unit{deepUnit()}, false)
	if len(got) != 2 || got[1].Available || got[1].Reason != ReasonEngineDisabled {
		t.Fatalf("catalog with engine off = %+v, want deep unavailable with %q", got, ReasonEngineDisabled)
	}
}

func TestCheckFailsClosedWithReason(t *testing.T) {
	if _, err := Check(nil, true, "bogus"); err == nil || !strings.Contains(err.Error(), "不认识") {
		t.Fatalf("unknown mode err = %v, want 不认识", err)
	}
	if _, err := Check(nil, true, "deep"); err == nil || !strings.Contains(err.Error(), "未安装") {
		t.Fatalf("uninstalled mode err = %v, want 未安装", err)
	}
	if _, err := Check([]Unit{deepUnit()}, false, "deep"); err == nil || !strings.Contains(err.Error(), "启用 Eino 多代理") {
		t.Fatalf("engine-off mode err = %v, want 启用提示", err)
	}
	e, err := Check([]Unit{deepUnit()}, true, "deep")
	if err != nil || !e.Available {
		t.Fatalf("available mode = (%+v, %v), want ok", e, err)
	}
	if _, err := Check(nil, false, "eino_single"); err != nil {
		t.Fatalf("builtin single must always pass: %v", err)
	}
}

func writeDeclaration(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadDeclarationFileChecksIdentityAndKnown(t *testing.T) {
	ok := writeDeclaration(t, "deep.yaml", "id: deep\n")
	if m, err := ReadDeclarationFile(ok); err != nil || m.ID != "deep" {
		t.Fatalf("valid declaration = (%+v, %v)", m, err)
	}

	mismatch := writeDeclaration(t, "deep.yaml", "id: supervisor\n")
	if _, err := ReadDeclarationFile(mismatch); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("id/name mismatch err = %v, want 不一致", err)
	}

	unknown := writeDeclaration(t, "bogus.yaml", "id: bogus\n")
	if _, err := ReadDeclarationFile(unknown); err == nil || !strings.Contains(err.Error(), "不认识") {
		t.Fatalf("unknown mode err = %v, want 不认识", err)
	}

	builtin := writeDeclaration(t, "eino_single.yaml", "id: eino_single\n")
	if _, err := ReadDeclarationFile(builtin); err == nil || !strings.Contains(err.Error(), "内置") {
		t.Fatalf("builtin override err = %v, want 内置", err)
	}

	empty := writeDeclaration(t, "deep.yaml", "")
	if _, err := ReadDeclarationFile(empty); err == nil || !strings.Contains(err.Error(), "空文件") {
		t.Fatalf("empty file err = %v, want 空文件", err)
	}

	extra := writeDeclaration(t, "deep.yaml", "id: deep\nrunner: evil\n")
	if _, err := ReadDeclarationFile(extra); err == nil {
		t.Fatalf("unknown field must be refused, got nil error")
	}
}
