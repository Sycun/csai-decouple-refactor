package multiagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"

	"go.uber.org/zap"
)

// The skill middleware now reads from the capability table when one is installed, which is what
// lets a bundle's skills reach a run. These two tests pin both halves of that decision: the table
// path works, and the "no skills directory configured" behaviour is unchanged rather than
// quietly upgraded to "load skills anyway from a default directory".

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test working directory")
		}
		dir = parent
	}
}

func repoSkillsDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "skills")
}

func repoSkillsTable(t *testing.T) *plugin.Table {
	t.Helper()
	table := plugin.NewTable()
	units, err := plugin.ScanDir(plugin.KindSkill, repoSkillsDir(t), nil)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	if len(units) < 5 {
		t.Fatalf("only %d skills scanned from the factory skills/ directory (measured 5: the kept "+
			"discipline skills plus the format demo)", len(units))
	}
	for _, u := range units {
		if err := table.PutLocal(u); err != nil {
			t.Fatalf("PutLocal: %v", err)
		}
	}
	return table
}

func TestPrepareEinoAgenticSkillsUsesTheCapabilityTable(t *testing.T) {
	table := repoSkillsTable(t)
	plugin.Install(table)
	t.Cleanup(func() { plugin.Install(nil) })

	skillsDir := repoSkillsDir(t)
	loc, mw, fsTools, root, err := prepareEinoAgenticSkills(
		context.Background(), skillsDir, &config.MultiAgentConfig{}, zap.NewNop())
	if err != nil {
		t.Fatalf("prepareEinoAgenticSkills: %v", err)
	}
	if mw == nil {
		t.Fatalf("no skill middleware: the table backend was not taken")
	}
	if loc == nil {
		t.Fatalf("the local backend must still be returned for the filesystem middleware")
	}
	if root != filepath.Clean(skillsDir) {
		t.Fatalf("skillsRoot = %q, expected it to stay the resolved directory %q", root, skillsDir)
	}
	_ = fsTools
}

func TestPrepareEinoAgenticSkillsStillSkipsWhenSkillsDirIsEmpty(t *testing.T) {
	table := repoSkillsTable(t)
	plugin.Install(table)
	t.Cleanup(func() { plugin.Install(nil) })

	// config.Load leaves skills_dir empty for an installation that has none. Serving the
	// shipped directory anyway would hand the model capabilities the operator turned off.
	_, mw, _, root, err := prepareEinoAgenticSkills(
		context.Background(), "", &config.MultiAgentConfig{}, zap.NewNop())
	if err != nil {
		t.Fatalf("prepareEinoAgenticSkills: %v", err)
	}
	if mw != nil {
		t.Fatalf("skills were served with an empty skills_dir")
	}
	if root != "" {
		t.Fatalf("skillsRoot = %q, want empty", root)
	}
}
