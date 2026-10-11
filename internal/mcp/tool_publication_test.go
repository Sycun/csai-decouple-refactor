package mcp

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestReplaceToolsPreservesRuntimeAndNonToolResources(t *testing.T) {
	live := NewServer(zap.NewNop())
	live.RegisterResource(&Resource{URI: "custom://guide", Name: "guide"})
	live.RegisterTool(Tool{Name: "old"}, nil)
	id := live.BeginToolExecution(context.Background(), "background", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live.RegisterToolExecutionCancel(id, cancel)
	live.AppendToolExecutionPartialOutput(id, "still running")
	prepared := NewServer(zap.NewNop())
	RegisterExecutionControlToolsInto(prepared, live, nil)
	live.ReplaceTools(prepared)
	// Directory maps are copied: subsequent staging changes must not leak into the live server.
	prepared.RegisterTool(Tool{Name: "late"}, nil)
	live.mu.RLock()
	_, guide := live.resources["custom://guide"]
	_, old := live.resources["tool://old"]
	_, control := live.resources["tool://get_tool_execution"]
	_, late := live.tools["late"]
	live.mu.RUnlock()
	if !guide || old || !control || late {
		t.Fatalf("resource/tool replacement: guide=%v old=%v control=%v late=%v", guide, old, control, late)
	}
	result, _, err := live.CallTool(context.Background(), "get_tool_execution", map[string]interface{}{"execution_id": id})
	if err != nil || result.IsError || !strings.Contains(ToolResultPlainText(result), "still running") {
		t.Fatalf("control tool lost the live execution: result=%v err=%v", result, err)
	}
	result, _, err = live.CallTool(context.Background(), "cancel_tool_execution", map[string]interface{}{"execution_id": id})
	if err != nil || result.IsError {
		t.Fatalf("control tool cannot cancel the live execution: result=%v err=%v", result, err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("control tool did not reach the original cancellation handle")
	}
}
