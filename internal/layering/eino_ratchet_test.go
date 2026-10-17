package layering

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// einoAdapters are the packages allowed to keep Eino types permanently: the model layer and
// the two Eino-facing adapters. Their file count is deliberately **not** capped - the
// convergence move is to relocate Eino-facing code *into* them, and a gate that punished the
// destination would make the target unreachable (and would be relaxed by the first person to
// hit it).
var einoAdapters = map[string]bool{
	"internal/llm":         true,
	"internal/einomcp":     true,
	"internal/einoobserve": true,
	// The skill adapter earns its place by removing a capability limit, not just by moving code:
	// Eino's own backend takes one BaseDir, so skills living inside a plug-in bundle were
	// unreachable. internal/einoskill implements that backend over the capability table instead,
	// and multiagent now prefers it. Parity with the vendor implementation is pinned by
	// TestBackendMatchesEinoBackend.
	"internal/einoskill": true,
}

// einoDebt is the water mark of the report's P6 acceptance criterion ("one package speaks
// Eino"), measured per non-adapter package over production files. This is the ratchet: a
// package here may only shrink, and a package that is not listed at all is a new leak and
// fails immediately.
//
// internal/multiagent is the biggest block by far because it hosts the Eino ADK runner. That
// is a reason to shrink it deliberately, not a reason to exempt it - the exemptions are
// exactly where a convergence quietly stops.
var einoDebt = map[string]int{
	// One file was absorbed from internal/security when the streaming shell's SDK shim
	// moved next to the ADK wrapper that already lives here. The Eino-facing file count did
	// not grow overall: security lost exactly the file this package gained, and the repository
	// total stayed at 104. Raising a baseline is a deliberate act, so the reason belongs here.
	//
	// 79 -> 82 on 2026-10-17, merging upstream v1.7.24: the context-checkpoint work arrived as
	// three new Eino-facing files here - eino_context_checkpoint.go, eino_generated_context.go
	// and agentic_tool_call_arguments_sanitizer_middleware.go. They are ADK middleware and
	// checkpoint plumbing, which is what this package exists for, so the honest choices were to
	// raise the water mark or to hide them behind an adapter the upstream shape does not have.
	// Raised; the three are named so the next shrink starts from a list instead of a guess.
	"internal/multiagent": 82,
	"internal/knowledge":  12,
	"internal/workflow":   5,
}

// einoDebtPackageFloor and einoDebtFileFloor are the measured totals of the map above:
// 3 packages, 99 files - down from 8 packages / 101 files at the start of the convergence.
// Three packages moved their Eino-facing code into an adapter rather than deleting it: the
// vision model call into llm.DescribeImage, the Claude connection probe into
// llm.PingAgentic, and internal/reasoning behind its own ChatModelTarget interface with the
// SDK mapping in llm/reasoning_target.go. The total Eino-importing file count stayed flat
// while the debt surface shrank, which is what a convergence looks like.
//
// The file total moved 96 -> 99 on 2026-10-17 for the reason recorded on internal/multiagent
// above, the first upward move since the convergence started. The package total did not move,
// and that is the half that tracks the target: the growth landed inside the package that already
// hosts the ADK runner instead of leaking into a new one.
const (
	einoDebtPackageFloor = 3
	einoDebtFileFloor    = 99
)

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

func TestEinoImportsOnlyShrink(t *testing.T) {
	pkgs, err := PackagesImporting(moduleRoot(t), "github.com/cloudwego/eino")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("no package imports Eino at all: either the convergence finished or the scan is broken")
	}
	counts := map[string][]string{}
	for _, p := range pkgs {
		counts[p.Package] = p.Files
	}

	debtPackages, debtFiles := 0, 0
	for pkg, files := range counts {
		if einoAdapters[pkg] {
			continue
		}
		debtPackages++
		debtFiles += len(files)
	}
	t.Logf("Eino importers: %d packages / %d production files; outside the adapters: %d packages / %d files (target: 1 package)",
		len(counts), func() int {
			total := 0
			for _, f := range counts {
				total += len(f)
			}
			return total
		}(),
		debtPackages, debtFiles)

	var regressions, newLeaks []string
	for pkg, files := range counts {
		if einoAdapters[pkg] {
			continue
		}
		baseline, known := einoDebt[pkg]
		if !known {
			newLeaks = append(newLeaks, pkg+" ("+strings.Join(files, ", ")+")")
			continue
		}
		if len(files) > baseline {
			regressions = append(regressions, pkg+": "+strconv.Itoa(len(files))+" > baseline "+strconv.Itoa(baseline)+
				" ["+strings.Join(files, ", ")+"]")
		}
	}
	if len(newLeaks) > 0 {
		t.Fatalf("new packages now speak Eino: %v. The target is one adapter package, so a new importer outside the "+
			"adapters is a regression: put the Eino-facing code in internal/llm or an adapter instead.", newLeaks)
	}
	if len(regressions) > 0 {
		t.Fatalf("Eino imports grew inside existing debt packages: %v", regressions)
	}
	if debtPackages > einoDebtPackageFloor {
		t.Fatalf("%d debt packages import Eino; the floor is %d", debtPackages, einoDebtPackageFloor)
	}
	if debtFiles > einoDebtFileFloor {
		t.Fatalf("%d production files outside the adapters import Eino; the floor is %d", debtFiles, einoDebtFileFloor)
	}
	for pkg, baseline := range einoDebt {
		got := len(counts[pkg])
		if got < baseline {
			// Improvement is reported, never punished.
			t.Logf("%s dropped to %d files (baseline %d): tighten einoDebt", pkg, got, baseline)
		}
		if got == 0 {
			t.Errorf("%s no longer imports Eino at all: remove it from einoDebt and lower the two floors", pkg)
		}
	}
	if debtPackages < einoDebtPackageFloor || debtFiles < einoDebtFileFloor {
		t.Logf("debt fell to %d packages / %d files: tighten einoDebtPackageFloor and einoDebtFileFloor", debtPackages, debtFiles)
	}
}

func intStr(v int) string { return strconv.Itoa(v) }
