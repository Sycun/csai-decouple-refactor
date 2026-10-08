package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/plugin"

	"go.uber.org/zap"
)

// verifyModeUnits 是启动路径唯一能读到声明文件内容的地方（重放走通用 InstallBundle，
// 目录读取只按单元名合并）——坏声明在这里被停用并点名，而不是静默进目录。
func TestVerifyModeUnitsDisablesBadDeclarations(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	table := plugin.NewTable()
	put := func(name, path string) {
		u, err := plugin.NewUnit(plugin.KindMode, name, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := table.PutLocal(u); err != nil {
			t.Fatal(err)
		}
	}

	put("deep", write("deep.yaml", "id: deep\n"))
	// 文件名 supervisor、内容 id: deep——重放能把它带回表里，只有这里能抓住。
	put("supervisor", write("supervisor.yaml", "id: deep\n"))

	disabled, notes := verifyModeUnits(table, zap.NewNop())
	if disabled != 1 {
		t.Fatalf("disabled = %d, want 1 (notes: %v)", disabled, notes)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "mode/supervisor") {
		t.Fatalf("notes = %v, want one naming mode/supervisor", notes)
	}
	if u, ok := table.Unit("mode/deep"); !ok || !u.Enabled {
		t.Fatalf("good unit must stay enabled: %+v (ok=%v)", u, ok)
	}
	if u, ok := table.Unit("mode/supervisor"); !ok || u.Enabled {
		t.Fatalf("bad unit must be switched off: %+v (ok=%v)", u, ok)
	}

	if n, notes := verifyModeUnits(nil, zap.NewNop()); n != 0 || len(notes) != 0 {
		t.Fatalf("nil table = (%d, %v), want (0, nil)", n, notes)
	}
}
