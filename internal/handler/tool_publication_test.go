package handler

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"cyberstrike-ai/internal/mcp"
)

func sortedToolDirectory(server *mcp.Server) []mcp.Tool {
	tools := server.GetAllTools()
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

func TestToolRebuildPublishesOnlyACompleteDirectory(t *testing.T) {
	useTable(t)
	h, _, _ := newToolLayerHandler(t, t.TempDir())
	l := h.Tools
	l.srv.RegisterTool(mcp.Tool{Name: "previous"}, func(context.Context, map[string]interface{}) (*mcp.ToolResult, error) {
		return &mcp.ToolResult{}, nil
	})
	before := sortedToolDirectory(l.srv)
	entered, release := make(chan struct{}), make(chan struct{})
	l.SetVulnerabilityToolRegistrar(func(target *mcp.Server) error {
		target.RegisterTool(mcp.Tool{Name: "prepared"}, func(context.Context, map[string]interface{}) (*mcp.ToolResult, error) {
			return &mcp.ToolResult{}, nil
		})
		close(entered)
		<-release
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- l.Rebuild() }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("registrar did not start")
	}
	if got := sortedToolDirectory(l.srv); !reflect.DeepEqual(got, before) {
		t.Errorf("unfinished rebuild leaked its directory: %v", got)
	}
	if _, _, err := l.srv.CallTool(context.Background(), "previous", nil); err != nil {
		t.Errorf("old tool unavailable during registration: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range l.srv.GetAllTools() {
		names[tool.Name] = true
	}
	if names["previous"] || !names["prepared"] || !names["get_tool_execution"] {
		t.Fatalf("published incomplete directory: %v", names)
	}
}

func TestToolRebuildRegistrationFailureRetainsServedDirectory(t *testing.T) {
	useTable(t)
	h, _, _ := newToolLayerHandler(t, t.TempDir())
	l := h.Tools
	if err := l.Rebuild(); err != nil {
		t.Fatal(err)
	}
	before := sortedToolDirectory(l.srv)
	failure := errors.New("registrar unavailable")
	l.SetWebshellToolRegistrar(func(target *mcp.Server) error {
		target.RegisterTool(mcp.Tool{Name: "partial"}, nil)
		return failure
	})
	err := l.Rebuild()
	var step ErrToolLayerStep
	if !errors.As(err, &step) || step.Step != "registration" || !errors.Is(err, failure) {
		t.Fatalf("registration failure was hidden: %v", err)
	}
	if got := sortedToolDirectory(l.srv); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed registrar replaced live directory: %v", got)
	}
}
