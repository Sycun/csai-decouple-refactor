// Package layering holds the invariants that outlive any single refactor step: which
// packages are allowed to talk to a vendor SDK, counted over the whole repository.
//
// A per-directory check would be worthless here. The convergence target in
// docs/zh-CN/capability-platform-decoupling-research.md is "one package speaks Eino",
// and it will be reached over many small moves; what makes that tractable is a number that
// can only go down, reported per package, with the file list printed when it moves up.
package layering

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// PackageFiles lists the non-test files of one package that import a prefix.
type PackageFiles struct {
	Package string
	Files   []string
}

// PackagesImporting scans the production tree under root and returns every package whose
// non-test files import something starting with prefix, largest first.
//
// Test files are excluded because a test is allowed to reach into a vendor type to build a
// fixture; the invariant is about the shipped dependency graph. Testdata and generated trees
// are skipped for the same reason: they are not compiled into the binary.
func PackagesImporting(root, prefix string) ([]PackageFiles, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(prefix) == "" {
		return nil, fmt.Errorf("layering: root and prefix are both required")
	}
	byPackage := map[string][]string{}
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch filepath.Base(path) {
				case "testdata", "generated", "node_modules", "venv":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", path, parseErr)
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			for _, imp := range file.Imports {
				value, unquoteErr := strconv.Unquote(imp.Path.Value)
				if unquoteErr != nil {
					return fmt.Errorf("unquote import %s: %w", imp.Path.Value, unquoteErr)
				}
				if strings.HasPrefix(value, prefix) {
					pkg := filepath.ToSlash(filepath.Dir(rel))
					byPackage[pkg] = append(byPackage[pkg], rel)
					break
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	out := make([]PackageFiles, 0, len(byPackage))
	for pkg, files := range byPackage {
		sort.Strings(files)
		out = append(out, PackageFiles{Package: pkg, Files: files})
	}
	sort.Slice(out, func(a, b int) bool {
		if len(out[a].Files) != len(out[b].Files) {
			return len(out[a].Files) > len(out[b].Files)
		}
		return out[a].Package < out[b].Package
	})
	return out, nil
}

// MethodsByReceiver counts methods per receiver type across the non-test files of one
// package, which is what "how big is this god object" actually means: the number of
// declarations a type owns, how many files spread them out, and how many setters the
// wiring has to remember to call.
func MethodsByReceiver(root, pkgDir string) (map[string]int, map[string]map[string]int, error) {
	perType := map[string]int{}
	perTypeFile := map[string]map[string]int{}
	fset := token.NewFileSet()
	dir := filepath.Join(root, pkgDir)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			receiver := receiverName(fn.Recv.List[0].Type)
			if receiver == "" {
				continue
			}
			perType[receiver]++
			if perTypeFile[receiver] == nil {
				perTypeFile[receiver] = map[string]int{}
			}
			perTypeFile[receiver][rel]++
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return perType, perTypeFile, nil
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}

// CountSetters counts exported Set* methods on the given receiver type.
func CountSetters(root, pkgDir, receiver string) (int, []string, error) {
	total := 0
	var names []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, pkgDir), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || fn.Name == nil {
				continue
			}
			if receiverName(fn.Recv.List[0].Type) != receiver {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "Set") {
				total++
				names = append(names, fn.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	sort.Strings(names)
	return total, names, nil
}

// CountAllSetters counts every exported Set* method in one package, keyed by receiver type.
// Counting the package rather than a list of types matters: a new god object would otherwise
// start with a clean slate and grow unchecked.
func CountAllSetters(root, pkgDir string) (int, map[string][]string, error) {
	total := 0
	byReceiver := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, pkgDir), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || fn.Name == nil {
				continue
			}
			if !strings.HasPrefix(fn.Name.Name, "Set") {
				continue
			}
			receiver := receiverName(fn.Recv.List[0].Type)
			if receiver == "" {
				continue
			}
			total++
			byReceiver[receiver] = append(byReceiver[receiver], fn.Name.Name)
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	for receiver := range byReceiver {
		sort.Strings(byReceiver[receiver])
	}
	return total, byReceiver, nil
}

// FieldTypesByFile lists, for each non-test file in one package, how many struct fields carry
// each type text ("*database.DB", "database.AssetStore", ...).
//
// Struct fields only: a function parameter of the same type is not the coupling being measured,
// which is a long-lived transport type holding on to a 361-method god object.
func FieldTypesByFile(root, pkgDir string) (map[string]map[string]int, error) {
	byFile := map[string]map[string]int{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, pkgDir), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		ast.Inspect(file, func(n ast.Node) bool {
			structType, ok := n.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}
			// Struct types only. ast.Field is also the node for a function parameter and for an
			// interface method, so inspecting every *ast.Field would count `NewX(db *database.DB)`
			// as a held reference - it measured 31 where the truth is 9.
			for _, field := range structType.Fields.List {
				if len(field.Names) == 0 {
					continue
				}
				if byFile[rel] == nil {
					byFile[rel] = map[string]int{}
				}
				byFile[rel][typesText(field.Type)] += len(field.Names)
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return byFile, nil
}

// DatabaseInterfaces returns every interface name declared inside internal/database, parsed from
// source. The shape gate used to guess "is this a narrowed surface?" from the name pattern
// `database.*Store`, which silently skipped every surface declared without that suffix. Six handler
// fields were of that kind, so the gate had no view of how they were assigned.
// The truth source is the declaration list, so a future rename cannot hide a field from the gate.
func DatabaseInterfaces(root string) (map[string]bool, error) {
	names := map[string]bool{}
	fset := token.NewFileSet()
	dir := filepath.Join(root, "internal/database")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, parseErr)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if _, isInterface := ts.Type.(*ast.InterfaceType); isInterface {
					names[ts.Name.Name] = true
				}
			}
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no interface declared in %s: the parser is not reading the package", dir)
	}
	return names, nil
}

// NarrowedFieldsByFile lists, per non-test file under pkgDir, the struct fields whose type is one of
// those interfaces. Named fields come back as file -> field names (that is what an assignment key
// matches); embedded ones come back separately because a struct that embeds a store cannot be
// shape-checked by key and would otherwise widen its own method set in silence.
func NarrowedFieldsByFile(root, pkgDir string, interfaces map[string]bool) (map[string][]string, map[string][]string, error) {
	named := map[string][]string{}
	embedded := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, pkgDir), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		ast.Inspect(file, func(n ast.Node) bool {
			structType, ok := n.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}
			for _, field := range structType.Fields.List {
				text := typesText(field.Type)
				name, hit := strings.CutPrefix(text, "database.")
				if !hit || !interfaces[name] {
					continue
				}
				if len(field.Names) == 0 {
					embedded[rel] = append(embedded[rel], text)
					continue
				}
				for _, fieldName := range field.Names {
					named[rel] = append(named[rel], fieldName.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return named, embedded, nil
}

func typesText(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + typesText(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return typesText(t.X) + "." + t.Sel.Name
	case *ast.InterfaceType:
		return "interface"
	default:
		// Maps, slices, channels, funcs: never compared against a dotted name by callers,
		// so one stable bucket is enough and keeps this helper free of a reflect dependency.
		return "other"
	}
}
