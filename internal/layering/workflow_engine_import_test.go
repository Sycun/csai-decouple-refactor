package layering

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The workflow engine used to receive persistence as a *database.DB behind the interfaces declared in
// internal/database, which meant the package that runs an Eino graph imported the whole data layer:
// every graph node sat one method name away from any table in the application. The run state now has a
// store of its own, so this package must not import internal/database again - the ledger it needs is
// declared at home, and the fact surface it also needs comes through internal/project.
//
// The claim is about imports rather than about method calls because that is the part a compiler cannot
// enforce after the fact: adding one `database.X` reference back would type-check perfectly.
func TestWorkflowEngineDoesNotImportTheDataLayer(t *testing.T) {
	root := moduleRoot(t)
	dir := filepath.Join(root, "internal", "workflow")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the engine directory: %v", err)
	}

	fset := token.NewFileSet()
	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		scanned++
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			if path == "cyberstrike-ai/internal/database" {
				t.Errorf("%s imports %s - the engine's persistence is declared in internal/workflow and answered by store.Workflows; "+
					"importing the connection wrapper's package puts every table back within reach of a graph node", entry.Name(), path)
			}
		}
	}
	if scanned < 10 {
		t.Fatalf("only %d engine files scanned, want at least 10 - a scan that reads no files proves nothing", scanned)
	}
}
