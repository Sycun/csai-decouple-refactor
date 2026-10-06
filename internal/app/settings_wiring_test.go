package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The HTTP layer resolves its configuration through a snapshot installed at assembly time, and
// falls back to the boot config when none is installed. That fallback exists so unit tests can
// construct a handler directly - it must never be what production runs on, because then the
// role catalog would silently stop updating and every reader would keep serving start-up values.
//
// So: this is a gate, not documentation. It fails if assembly stops installing the store, or
// stops publishing the catalog it just wired.
func TestAssemblyInstallsTheLiveConfigStoreAndPublishesRoles(t *testing.T) {
	root := moduleRootForWiringTest(t)
	dir := filepath.Join(root, "internal/app")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read internal/app: %v", err)
	}
	fset := token.NewFileSet()
	installed := 0
	published := 0
	tableInstalled := 0
	remoteObservers := 0
	scanned := 0
	scannedCalled := 0
	bundlesInstalled := 0
	bootToolRebuilds := 0
	mcpProvisioned := 0
	pluginUnitsDeclared := 0
	skillStatsSchema := 0
	chatUploadSchema := 0
	pluginCalls := 0
	pluginWithoutToolLayer := 0
	pluginWithoutMCPProvisioner := 0
	switchesApplied := 0
	pluginWithoutSwitchStore := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				// A bare call in this package: the capability scan and the boot re-install of the
				// packs on disk, both of which live here.
				if id, isIdent := call.Fun.(*ast.Ident); isIdent {
					switch id.Name {
					case "scanBuiltInCapabilities":
						scannedCalled++
					case "installBundlesFromDisk":
						bundlesInstalled++
					case "applyPersistedSwitches":
						// The console's switch would otherwise live only in this process: the
						// table is rebuilt from disk at start-up, so a role somebody disabled
						// would be serving again after a restart.
						switchesApplied++
					case "provisionDeclaredServers":
						// A pack's MCP server is declared into the live manager, which start-up
						// builds from config.yaml. Without this call the server is in the table and
						// in the console after a restart while nothing connects to it.
						mcpProvisioned++
					case "ensureChatUploadArtifactSchema":
						chatUploadSchema++
					case "ensureSkillStatsSchema":
						// The store owns its schema: without this call the table nobody creates
						// would only appear on installations that predate the cut.
						skillStatsSchema++
					case "declarePackPluginUnits":
						// A pack's plugin unit must come back *declared* rather than enabled: the
						// host holds no domain at start-up, so a manifest that says enabled would
						// otherwise show an enabled unit whose capabilities nobody can call.
						pluginUnitsDeclared++
					}
				}
				return true
			}
			switch sel.Sel.Name {
			case "Rebuild":
				// The boot-time tool-layer rebuild for packs that ship a recipe. Missing it is
				// silent: the recipe sits in the table and stays invisible to every run until
				// somebody presses 应用配置.
				if s, ok := sel.X.(*ast.SelectorExpr); ok {
					if id, isIdent := s.X.(*ast.Ident); isIdent && id.Name == "configHandler" && s.Sel.Name == "Tools" {
						bootToolRebuilds++
					}
				}
			case "InstallSettingsStore":
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "handler" {
					installed++
				}
			case "SetToolInventoryObserver":
				// Without this line the remote capability identities are built by code nobody
				// calls, and every external MCP tool silently falls back to the namespace policy.
				remoteObservers++
			case "Install":
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "plugin" {
					tableInstalled++
				}
			case "NewPluginHandler":
				// Argument 4 is the tool-layer rebuilder and argument 5 the external-MCP
				// provisioner. Both nil are legal Go: a recipe would be recorded in the table but
				// never executable, a server declaration recorded but never written, and either
				// response would still say "installed".
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "handler" {
					pluginCalls++
					if len(call.Args) < 8 {
						t.Errorf("handler.NewPluginHandler takes %d arguments, want the tool layer, the MCP provisioner and the switch store among them", len(call.Args))
					} else if sel, ok := call.Args[3].(*ast.SelectorExpr); !ok || sel.Sel.Name != "Tools" {
						pluginWithoutToolLayer++
					} else if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "configHandler" {
						pluginWithoutToolLayer++
					}
					if len(call.Args) >= 8 {
						if id, ok := call.Args[4].(*ast.Ident); ok && id.Name == "nil" {
							pluginWithoutMCPProvisioner++
						}
						if id, ok := call.Args[5].(*ast.Ident); ok && id.Name == "nil" {
							pluginWithoutSwitchStore++
						}
					}
				}
			case "Reload":
				published++
			}
			return true
		})
	}
	if remoteObservers != 1 {
		t.Fatalf("SetToolInventoryObserver is called %d times in internal/app, want exactly 1: with no "+
			"observer the per-tool remote capabilities are never registered and every external MCP call "+
			"quietly falls back to the namespace-wide policy", remoteObservers)
	}
	if scanned < 10 {
		t.Fatalf("only %d non-test files scanned in internal/app: the walker is not reading the package", scanned)
	}
	if installed != 1 {
		t.Fatalf("handler.InstallSettingsStore is called %d times in internal/app, want exactly 1 "+
			"(0 means every handler silently falls back to the boot config; more than 1 means two "+
			"snapshots can disagree)", installed)
	}
	if tableInstalled != 1 {
		t.Fatalf("plugin.Install is called %d times in internal/app, want exactly 1: without a global "+
			"table the skill middleware silently keeps the single-directory backend, so a bundle's "+
			"skills would look installed while nothing served them", tableInstalled)
	}
	if scannedCalled < 1 {
		t.Fatalf("scanBuiltInCapabilities is never called: the table would be empty, and preferring it " +
			"over Eino's backend would take every shipped skill away from a run")
	}
	if bundlesInstalled < 1 {
		t.Fatalf("installBundlesFromDisk is never called: a pack somebody installed would exist only " +
			"until the next restart, because the table is rebuilt from disk at start-up")
	}
	if bootToolRebuilds < 1 {
		t.Fatalf("configHandler.Tools.Rebuild() is never called at boot: a pack that ships a recipe " +
			"would be in the table but not on the tool surface until POST /config/apply runs")
	}
	if mcpProvisioned < 1 {
		t.Fatalf("provisionDeclaredServers is never called at boot: a pack's MCP server would be in " +
			"the table and in the console but absent from the live manager until the pack is reinstalled")
	}
	if chatUploadSchema < 1 {
		t.Fatalf("ensureChatUploadArtifactSchema is never called at boot: the table used to be created by " +
			"the RBAC initialisation, so nothing else would make it once the store owned it")
	}
	if skillStatsSchema < 1 {
		t.Fatalf("ensureSkillStatsSchema is never called at boot: skill_stats moved out of the data " +
			"layer's start-up sweep, and a store whose table nobody creates fails only on a fresh database")
	}
	if pluginUnitsDeclared < 1 {
		t.Fatalf("declarePackPluginUnits is never called at boot: a pack whose manifest says enabled " +
			"would come back an enabled unit while the host holds no domain, so its capabilities are " +
			"uncallable and the console blames a failed enablement that never happened")
	}
	if switchesApplied < 1 {
		t.Fatalf("applyPersistedSwitches is never called at boot: the table is rebuilt from disk, so " +
			"every switch the operator made in the console would be undone by the next restart")
	}
	if pluginWithoutSwitchStore != 0 {
		t.Fatalf("the plug-in handler was assembled with a nil switch store: the console's switches " +
			"would only last until the next restart, which is what the switch store exists to prevent")
	}
	if published < 1 {
		t.Fatalf("no role catalog publish in assembly: the store would be installed but empty, so "+
			"the roles API and every run path would serve nothing (files scanned: %d)", scanned)
	}
	if pluginCalls != 1 {
		t.Fatalf("handler.NewPluginHandler is called %d times in internal/app, want exactly 1", pluginCalls)
	}
	if pluginWithoutToolLayer != 0 {
		t.Fatalf("the plug-in handler was assembled without the tool-layer rebuilder: a bundle's tool " +
			"recipe would be recorded in the table and reported as installed, while no run path could " +
			"ever execute it (pass the configHandler, which owns RebuildToolLayer)")
	}
	if pluginWithoutMCPProvisioner != 0 {
		t.Fatalf("the plug-in handler was assembled with a nil MCP provisioner: a pack's server " +
			"declaration would be recorded in the table and reported as installed while the live " +
			"external-MCP manager never held it")
	}
	t.Logf("assembly wiring: %d files scanned, 1 live-store install, 1 table install, "+
		"1 remote inventory observer, %d capability scan call(s), %d bundle re-install call(s), "+
		"%d MCP declaration call(s), %d plugin declaration call(s), %d switch overlay call(s), "+
		"%d catalog publish call(s), 1 plug-in handler with its tool layer, MCP provisioner "+
		"and switch store",
		scanned, scannedCalled, bundlesInstalled, mcpProvisioned, pluginUnitsDeclared, switchesApplied, published)
}

func moduleRootForWiringTest(t *testing.T) string {
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
