package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/agentmode"
	"cyberstrike-ai/internal/config"

	"go.uber.org/zap"
)

// The agent node is an execution entry like chat, robot and batch: it must ask the mode catalog
// before dispatching, or a workflow keeps running multi-agent after the orchestration pack was
// unplugged - the exact back door the catalog's fail-closed rule exists to close.
func TestRunAgentNodeRefusesAnUninstalledMode(t *testing.T) {
	node := graphNode{ID: "agent-1", Type: "agent", Config: map[string]any{"agent_mode": "deep"}}
	args := RunArgs{AppCfg: &config.Config{}, Agent: &agent.Agent{}, Logger: zap.NewNop()}

	// A bare assembly has no catalog to ask; only the kernel built-in may pass then.
	_, cont, status, reason := runAgentNode(context.Background(), args, node, newWorkflowLocalState(map[string]interface{}{}, "run-mode-gate"))
	if status != "failed" || cont {
		t.Fatalf("an uninstalled mode ran without a catalog to approve it: status=%q cont=%v", status, cont)
	}
	if !strings.Contains(reason, "未安装") {
		t.Fatalf("the failure does not name why: %q", reason)
	}

	// The wired verdict is honoured verbatim - whatever the catalog says no to, the node refuses.
	boom := errors.New("对话模式 Deep 未安装：多代理编排包未安装，或该模式单元已被停用")
	args.CheckAgentMode = func(id string) error {
		if id != "deep" {
			t.Fatalf("the checker was asked about %q, want the resolved mode id", id)
		}
		return boom
	}
	out, cont, status, reason := runAgentNode(context.Background(), args, node, newWorkflowLocalState(map[string]interface{}{}, "run-mode-gate"))
	if status != "failed" || cont {
		t.Fatalf("a refused mode was dispatched anyway: status=%q cont=%v", status, cont)
	}
	if reason != boom.Error() {
		t.Fatalf("the refusal lost the catalog's reason: %q", reason)
	}
	if m, _ := out["error"].(string); m != boom.Error() {
		t.Fatalf("the node output does not carry the reason: %v", out)
	}
}

// The built-in single-agent mode is the product floor: it passes the check even with no catalog
// wired, so the refusal above is about availability rather than about the node existing.
func TestCheckNodeAgentModePassesTheBuiltinWithoutACatalog(t *testing.T) {
	if err := checkNodeAgentMode(RunArgs{}, agentmode.DefaultID); err != nil {
		t.Fatalf("the built-in mode needs no pack: %v", err)
	}
	if err := checkNodeAgentMode(RunArgs{}, ""); err != nil {
		// An empty id is not the built-in's business here - ResolveWithDefault maps it at the
		// call site. An error is fine; a panic would not be.
		t.Logf("empty mode id refused as expected: %v", err)
	}
}
