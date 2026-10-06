package store

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The data-layer item of the refactor plan is about ownership, not about where a
// file happens to sit: a table written from several places cannot be changed,
// tested or reasoned about as one thing. These are the tables this package owns,
// and the statements that reach them must exist here and nowhere else in
// production code.
//
// Test files in other packages are allowed to create fixtures directly: they set up
// a database, they are not a second write path.
func TestOwnedTablesAreOnlyWrittenFromThisPackage(t *testing.T) {
	root := moduleRoot(t)
	owned := []string{"hitl_interrupts", "hitl_conversation_configs", "notification_reads_by_user", "skill_stats", "chat_upload_artifacts", "audit_logs",
		"knowledge_retrieval_logs", "knowledge_base_items", "knowledge_embeddings", "model_token_usage"}
	pattern := regexp.MustCompile(`(?i)\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM|FROM|JOIN)\s+` + `(` + strings.Join(owned, "|") + `)`)

	var offenders []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "generated" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			inStore := strings.HasPrefix(filepath.ToSlash(rel), "internal/store/")
			if inStore || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if hit := pattern.FindString(string(data)); hit != "" {
				offenders = append(offenders, rel+": "+hit)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", dir, err)
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("statements against tables owned by internal/store leaked into production code: %v. "+
			"Add a method to the owning store instead of writing the table from elsewhere.", offenders)
	}
}

func moduleRoot(t *testing.T) string {
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
