package handler

import (
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"

	"go.uber.org/zap"
)

// installModeUnits 把一个装了模式单元的能力表装成进程全局表——模式可用性的新来源：
// 单元在表里且启用，模式才进入目录（"不点不存在"）。
func installModeUnits(t *testing.T, names ...string) {
	t.Helper()
	table := plugin.NewTable()
	for _, name := range names {
		u, err := plugin.NewUnit(plugin.KindMode, name, "/tmp/modes/"+name+".yaml")
		if err != nil {
			t.Fatal(err)
		}
		if err := table.PutLocal(u); err != nil {
			t.Fatal(err)
		}
	}
	plugin.Install(table)
	t.Cleanup(func() { plugin.Install(nil) })
}

func TestRobotModeSwitch(t *testing.T) {
	installModeUnits(t, "deep", "plan_execute", "supervisor")
	h := NewRobotHandler(&config.Config{MultiAgent: config.MultiAgentConfig{Enabled: true}}, nil, nil, zap.NewNop())

	if got := h.cmdSwitchMode("lark", "user-1", "plan-execute"); !strings.Contains(got, "Plan-Execute") {
		t.Fatalf("unexpected switch response: %s", got)
	}
	if got := h.getAgentMode("lark", "user-1"); got != "plan_execute" {
		t.Fatalf("mode = %q, want plan_execute", got)
	}
	if got := h.cmdModes("lark", "user-1"); !strings.Contains(got, "当前模式: Plan-Execute") {
		t.Fatalf("unexpected modes response: %s", got)
	}
}

func TestRobotModeRejectsUninstalledMultiAgent(t *testing.T) {
	// 未装包：deep 不在目录里（"不点不存在"），切换被拒并说明未安装。
	h := NewRobotHandler(&config.Config{MultiAgent: config.MultiAgentConfig{Enabled: true}}, nil, nil, zap.NewNop())

	if got := h.cmdSwitchMode("lark", "user-1", "deep"); !strings.Contains(got, "未安装") {
		t.Fatalf("unexpected rejection: %s", got)
	}
	if got := h.getAgentMode("lark", "user-1"); got != "eino_single" {
		t.Fatalf("mode changed after rejection: %q", got)
	}
	if got := h.cmdModes("lark", "user-1"); strings.Contains(got, "Deep") {
		t.Fatalf("uninstalled mode must not be listed: %s", got)
	}
}

func TestRobotModeRejectsUnavailableMultiAgent(t *testing.T) {
	// 装了包但引擎未启用：拒绝文案指向系统设置，而不是"未安装"。
	installModeUnits(t, "deep")
	h := NewRobotHandler(&config.Config{}, nil, nil, zap.NewNop())

	if got := h.cmdSwitchMode("lark", "user-1", "deep"); !strings.Contains(got, "启用 Eino 多代理") {
		t.Fatalf("unexpected rejection: %s", got)
	}
	if got := h.getAgentMode("lark", "user-1"); got != "eino_single" {
		t.Fatalf("mode changed after rejection: %q", got)
	}
}

func TestParseRobotAgentModeRejectsUnknownMode(t *testing.T) {
	if mode, ok := parseRobotAgentMode("unknown"); ok || mode != "" {
		t.Fatalf("parseRobotAgentMode returned (%q, %v), want empty,false", mode, ok)
	}
}

func TestRobotStatusCommandPermission(t *testing.T) {
	for _, command := range []string{"状态", "status"} {
		permission, recognized := robotCommandPermission(command)
		if !recognized || permission != "chat:read" {
			t.Fatalf("command %q returned permission=%q recognized=%v", command, permission, recognized)
		}
	}
	for _, removed := range []string{"当前", "current"} {
		if _, recognized := robotCommandPermission(removed); recognized {
			t.Fatalf("removed command %q is still recognized", removed)
		}
	}
}

func TestRobotBestPracticeCommandPermissions(t *testing.T) {
	cases := map[string]string{
		"任务":       "chat:read",
		"task":     "chat:read",
		"重命名 新标题":  "chat:write",
		"rename x": "chat:write",
		"诊断":       "config:read",
		"doctor":   "config:read",
	}
	for command, want := range cases {
		permission, recognized := robotCommandPermission(command)
		if !recognized || permission != want {
			t.Fatalf("command %q returned permission=%q recognized=%v, want %q,true", command, permission, recognized, want)
		}
	}
}

func TestRobotConfirmationCanBeCancelled(t *testing.T) {
	h := NewRobotHandler(&config.Config{}, nil, nil, zap.NewNop())
	h.setPendingConfirmation("lark", "user-1", "delete_conversation", "conv-1")
	if got := h.cmdCancelConfirmation("lark", "user-1"); got != "已取消待确认操作。" {
		t.Fatalf("unexpected cancel response: %s", got)
	}
	if got := h.cmdConfirm("lark", "user-1"); !strings.Contains(got, "没有待确认操作") {
		t.Fatalf("confirmation survived cancellation: %s", got)
	}
}

func TestRobotDoctorSeparatesInternalToolsFromHTTPMCP(t *testing.T) {
	h := NewRobotHandler(&config.Config{
		Security: config.SecurityConfig{Tools: []config.ToolConfig{
			{Name: "enabled-tool", Enabled: true},
			{Name: "disabled-tool", Enabled: false},
		}},
		MCP: config.MCPConfig{Enabled: false},
	}, nil, nil, zap.NewNop())

	got := h.cmdDoctor()
	if !strings.Contains(got, "内置 MCP 工具: 1/2 个已启用") {
		t.Fatalf("internal tool status missing: %s", got)
	}
	if !strings.Contains(got, "HTTP MCP 服务: 已关闭") {
		t.Fatalf("HTTP MCP status missing: %s", got)
	}
}
