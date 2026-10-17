package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTesttreeGeneratedDriftReportsActionableError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("testtree requires POSIX shell")
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "testtree.sh"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "check_generated() {")
	end := strings.Index(source, "\ncase \"${1:-verify}\"")
	if start < 0 || end < start {
		t.Fatal("check_generated function not found")
	}
	root := t.TempDir()
	for _, tree := range []string{"dev", "test"} {
		dir := filepath.Join(root, tree, "docs", "zh-CN")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sse-event-catalog.md"), []byte(tree), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", "set -eu\nDEV=$1/dev\nTEST=$1/test\ngen_diff=$(mktemp)\ndie() { echo \"testtree: $*\" >&2; exit 1; }\n"+source[start:end]+"\ncheck_generated", "probe", root)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("drift was accepted")
	}
	if !strings.Contains(string(output), "生成物过期") || !strings.Contains(string(output), "docs/zh-CN/sse-event-catalog.md") || strings.Contains(string(output), "unbound variable") {
		t.Fatalf("drift diagnostic lost: %s", output)
	}
}
