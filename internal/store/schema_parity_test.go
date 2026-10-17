package store

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestSchemaDefinitionsCoverEveryInlineTable pins the two declarations of the same structure to each
// other while both still exist.
//
// Start-up reconciles the database against internal/database/schemas/*.sql, and this package still
// carries its own CREATE TABLE literals; both run, and because the literals go first and the reconcile
// only ever adds what is missing, a fresh install ends up with the union. That is why the duplication
// is harmless today - and why it is easy to grow a hole in. A column that exists only in a literal here
// is a column that silently disappears the day the literals are deleted in favour of the definitions,
// which is the whole point of having one source of truth.
//
// So the claim is directional rather than equality on columns: every table and every index this package
// builds must also be declared in the definitions, and the definitions may declare more (they carry the
// columns that here are still added by an ALTER list). Names are compared, not text - indentation and
// IF NOT EXISTS differ between the two and mean nothing.
func TestSchemaDefinitionsCoverEveryInlineTable(t *testing.T) {
	root := moduleRoot(t)

	created := regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	indexed := regexp.MustCompile(`(?i)\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)\s+ON\s+([a-z_][a-z0-9_]*)`)

	// The inline side: string literals in this package's production files, which is the same traversal
	// the ownership gates use, so a blind walk shows up here the way it does there.
	inlineTables := map[string]bool{}
	inlineIndexes := map[string]string{}
	inlineColumns := map[string]map[string]bool{}
	for path := range productionGoFiles(t, filepath.Join(root, "internal", "store")) {
		for _, lit := range stringLiterals(t, path) {
			for _, m := range created.FindAllStringSubmatch(lit, -1) {
				inlineTables[strings.ToLower(m[1])] = true
			}
			for _, m := range indexed.FindAllStringSubmatch(lit, -1) {
				inlineIndexes[strings.ToLower(m[1])] = strings.ToLower(m[2])
			}
			for table, cols := range literalTableColumns(lit) {
				if inlineColumns[table] == nil {
					inlineColumns[table] = map[string]bool{}
				}
				for c := range cols {
					inlineColumns[table][c] = true
				}
			}
		}
	}

	// The definitions side: the embedded SQL the reconcile engine reads.
	defTables := map[string]bool{}
	defIndexes := map[string]string{}
	defColumns := map[string]map[string]bool{}
	var files int
	for _, name := range []string{"application.sql", "knowledge.sql"} {
		blob, err := os.ReadFile(filepath.Join(root, "internal", "database", "schemas", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files++
		src := string(blob)
		for _, m := range created.FindAllStringSubmatch(src, -1) {
			defTables[strings.ToLower(m[1])] = true
		}
		for _, m := range indexed.FindAllStringSubmatch(src, -1) {
			defIndexes[strings.ToLower(m[1])] = strings.ToLower(m[2])
		}
		for table, cols := range literalTableColumns(src) {
			if defColumns[table] == nil {
				defColumns[table] = map[string]bool{}
			}
			for c := range cols {
				defColumns[table][c] = true
			}
		}
	}

	// Neither side may be empty: an unparsed scan would satisfy every "must be a subset" below and
	// report a clean bill of health for a walk that read nothing.
	if len(inlineTables) < 40 {
		t.Fatalf("the inline scan found %d tables in this package, want at least 40 - the walk has gone blind", len(inlineTables))
	}
	if files != 2 || len(defTables) < 40 {
		t.Fatalf("read %d definition files declaring %d tables, want 2 files and at least 40 tables", files, len(defTables))
	}

	var missingTables, missingIndexes, missingColumns []string
	for table := range inlineTables {
		if !defTables[table] {
			missingTables = append(missingTables, table)
		}
	}
	for index, table := range inlineIndexes {
		if got, ok := defIndexes[index]; !ok {
			missingIndexes = append(missingIndexes, index+" (on "+table+")")
		} else if got != table {
			missingIndexes = append(missingIndexes, index+" (on "+table+" here, on "+got+" in the definitions)")
		}
	}
	for table, cols := range inlineColumns {
		if !defTables[table] {
			continue // already reported above
		}
		for col := range cols {
			if !defColumns[table][col] {
				missingColumns = append(missingColumns, table+"."+col)
			}
		}
	}
	sort.Strings(missingTables)
	sort.Strings(missingIndexes)
	sort.Strings(missingColumns)

	t.Logf("this package builds %d tables / %d indexes inline; the definitions declare %d tables / %d indexes",
		len(inlineTables), len(inlineIndexes), len(defTables), len(defIndexes))

	if len(missingTables)+len(missingIndexes)+len(missingColumns) > 0 {
		t.Fatalf("internal/database/schemas/*.sql is the source start-up reconciles against, so anything built "+
			"only here is lost the day the inline literals go. Missing tables: %v. Missing or differing indexes: %v. "+
			"Missing columns: %v. Add the definition there rather than growing a second shape here.",
			missingTables, missingIndexes, missingColumns)
	}
}

// literalTableColumns maps each CREATE TABLE in a chunk of SQL-ish text to the column names it declares,
// skipping the table-level constraints that also sit in the column list.
func literalTableColumns(src string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	head := regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([a-z_][a-z0-9_]*)"?\s*\(`)
	for _, m := range head.FindAllStringSubmatchIndex(src, -1) {
		table := strings.ToLower(src[m[2]:m[3]])
		open := m[1] - 1
		depth, end := 0, -1
		for i := open; i < len(src); i++ {
			switch src[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			continue
		}
		cols := out[table]
		if cols == nil {
			cols = map[string]bool{}
			out[table] = cols
		}
		for _, part := range splitDDLBody(src[open+1 : end]) {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			up := strings.ToUpper(p)
			if strings.HasPrefix(up, "PRIMARY KEY") || strings.HasPrefix(up, "FOREIGN KEY") ||
				strings.HasPrefix(up, "CHECK") || strings.HasPrefix(up, "CONSTRAINT") ||
				strings.HasPrefix(up, "UNIQUE(") || strings.HasPrefix(up, "UNIQUE (") {
				continue
			}
			if cm := regexp.MustCompile(`^"?([A-Za-z_][A-Za-z0-9_]*)"?`).FindStringSubmatch(p); cm != nil {
				cols[strings.ToLower(cm[1])] = true
			}
		}
	}
	return out
}

// splitDDLBody splits on the commas that separate column definitions, leaving the ones inside a
// parenthesis (a DECIMAL(10,2), a composite key) alone.
func splitDDLBody(body string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, body[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, body[start:])
	return out
}
