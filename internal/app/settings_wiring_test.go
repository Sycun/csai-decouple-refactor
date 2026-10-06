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
	auditLogsSchema := 0
	knowledgeRetrievalSchema := 0
	modelTokenUsageSchema := 0
	robotSessionSchema := 0
	robotIdentitySchema := 0
	c2ArtifactSchema := 0
	auditSchemaOffset := -1
	usageSchemaOffset := -1
	usageSchemaFile := ""
	newDBOffset := -1
	newDBFile := ""
	auditServiceOffset := -1
	pluginCalls := 0
	pluginWithoutToolLayer := 0
	pluginWithoutMCPProvisioner := 0
	switchesApplied := 0
	vulnHandlerCalls := 0
	vulnHandlerWithoutNotifier := 0
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
					case "ensureKnowledgeRetrievalSchema":
						knowledgeRetrievalSchema++
					case "ensureC2PayloadArtifactSchema":
						// The table left the RBAC sweep with its own store; created nowhere, every
						// payload download would be refused as "belongs to nobody".
						c2ArtifactSchema++
					case "ensureRobotIdentitySchema":
						// The two binding tables left the RBAC start-up sweep; created nowhere, a
						// fresh install would fail the first 绑定 command it ever received.
						robotIdentitySchema++
					case "ensureRobotSessionSchema":
						// The table left the data layer's start-up sweep; without this it is only
						// ever created on installations that predate the cut.
						robotSessionSchema++
					case "ensureModelTokenUsageSchema":
						// model_token_usage left the data layer's start-up sweep, and its history
						// carry-over reads process_details: called before the main database exists,
						// it fails with "no such table" and the usage page stays empty forever.
						modelTokenUsageSchema++
						usageSchemaOffset = fset.Position(call.Pos()).Offset
						usageSchemaFile = fset.Position(call.Pos()).Filename
					case "ensureAuditLogsSchema":
						auditLogsSchema++
						auditSchemaOffset = fset.Position(call.Pos()).Offset
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
			case "NewDB":
				// The main connection: every table it used to create itself now gets ensured after
				// this point, so the earliest call is the ordering anchor.
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "database" {
					offset := fset.Position(call.Pos()).Offset
					if newDBOffset < 0 || offset < newDBOffset {
						newDBOffset = offset
						newDBFile = fset.Position(call.Pos()).Filename
					}
				}
			case "NewService":
				// The audit service purges expired records while it is constructed, so the store's
				// own EnsureSchema has to have run first: assembling it earlier is legal Go and shows
				// up as "no such table: audit_logs" on a fresh installation.
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "audit" {
					// Keep the *earliest* construction: there is more than one call site, and the
					// first is what has to find the table already there.
					offset := fset.Position(call.Pos()).Offset
					if auditServiceOffset < 0 || offset < auditServiceOffset {
						auditServiceOffset = offset
					}
				}
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
			case "NewVulnerabilityHandler":
				// Argument 3 is the alert route. A nil there compiles and behaves: the record is
				// stored, the endpoint answers 200, and nobody is ever told - the old design kept
				// this signal on a field of the database connection, where forgetting to set it was
				// invisible in exactly the same way.
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "handler" {
					vulnHandlerCalls++
					if len(call.Args) < 3 {
						t.Errorf("handler.NewVulnerabilityHandler takes %d arguments, want the alert notifier among them", len(call.Args))
					} else if id, isNil := call.Args[2].(*ast.Ident); isNil && id.Name == "nil" {
						vulnHandlerWithoutNotifier++
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
	if !(auditSchemaOffset >= 0 && auditServiceOffset >= 0 && auditSchemaOffset < auditServiceOffset) {
		t.Fatalf("audit_logs' schema is not created before the audit service is assembled "+
			"(ensure at offset %d, audit.NewService at %d): the service purges expired records during "+
			"construction, so on a fresh database it queries a table nobody made yet",
			auditSchemaOffset, auditServiceOffset)
	}
	// Offsets are per-file, so the two calls only order against each other inside one file: an anchor
	// picked up from another file would compare unrelated number spaces and could bless any placement.
	if !(newDBOffset >= 0 && usageSchemaOffset > newDBOffset && usageSchemaFile == newDBFile) {
		t.Fatalf("model_token_usage is not ensured after the main database exists (database.NewDB at "+
			"offset %d, ensureModelTokenUsageSchema at %d): its history carry-over reads process_details, "+
			"so running it earlier fails with \"no such table\" and the usage page stays empty",
			newDBOffset, usageSchemaOffset)
	}
	if c2ArtifactSchema < 1 {
		t.Fatalf("ensureC2PayloadArtifactSchema is never called at boot: c2_payload_artifacts left the " +
			"RBAC sweep, so the payload download gate would find no ownership record and deny everything")
	}
	if robotIdentitySchema < 1 {
		t.Fatalf("ensureRobotIdentitySchema is never called at boot: robot_user_bindings left the RBAC " +
			"sweep, so a fresh base would have nowhere to store a binding and every 绑定 command fails")
	}
	if robotSessionSchema < 1 {
		t.Fatalf("ensureRobotSessionSchema is never called at boot: a robot conversation would not " +
			"survive a restart, because the table mapping a thread to its conversation is created nowhere")
	}
	if modelTokenUsageSchema < 1 {
		t.Fatalf("ensureModelTokenUsageSchema is never called at boot: the table left the data layer's " +
			"start-up sweep, so a fresh installation would write process details all day and lose every " +
			"usage row to \"no such table: model_token_usage\"")
	}
	if vulnHandlerCalls < 1 {
		t.Fatalf("handler.NewVulnerabilityHandler is never called in internal/app: the wiring that lets a " +
			"recorded vulnerability reach the alert robot has to be built here, not hung off the connection")
	}
	if vulnHandlerWithoutNotifier > 0 {
		t.Fatalf("%d call(s) wire the vulnerability handler with a nil notifier: vulnerabilities would be "+
			"recorded and never announced, with nothing on the wire to show why", vulnHandlerWithoutNotifier)
	}
	if knowledgeRetrievalSchema < 1 {
		t.Fatalf("ensureKnowledgeRetrievalSchema is never called at boot: the table left the data " +
			"layer's start-up sweep, and its form is the one with two foreign keys")
	}
	if auditLogsSchema < 1 {
		t.Fatalf("ensureAuditLogsSchema is never called at boot: audit_logs left the start-up sweep, so " +
			"on a fresh installation the first record written would hit a table nobody created")
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
