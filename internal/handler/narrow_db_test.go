package handler

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"

	"go.uber.org/zap"
)

// Each entry below is a handler whose storage field was narrowed from *database.DB to that
// handler's own consumer interface.
//
// The invariant is the nil path, not the happy path. Go converts `var store AssetStore = (*DB)(nil)`
// into a *non-nil* interface, so a plain assignment of a possibly-nil pointer would keep every
// `if h.db == nil` guard - the code path that answers "database unavailable" when a module is
// disabled - permanently wrong. That compiles, and the enabled path never notices. database.Narrow
// is what preserves the distinction; this test is what keeps somebody from "simplifying" it back
// to an assignment.
var narrowedHandlers = []struct {
	name  string
	build func(db *database.DB) interface{}
}{
	{"AssetHandler", func(db *database.DB) interface{} { return NewAssetHandler(db, zap.NewNop()) }},
	{"AgentHandler", func(db *database.DB) interface{} {
		return NewAgentHandler(nil, db, &config.Config{}, zap.NewNop())
	}},
	{"runFinalizer", func(db *database.DB) interface{} { return newRunFinalizer(db, zap.NewNop(), nil, nil) }},
	{"NotificationHandler", func(db *database.DB) interface{} { return NewNotificationHandler(db, nil, zap.NewNop()) }},
	{"OpenAPIHandler", func(db *database.DB) interface{} { return NewOpenAPIHandler(db, zap.NewNop(), nil, nil) }},
	{"RobotHandler", func(db *database.DB) interface{} { return NewRobotHandler(&config.Config{}, db, nil, zap.NewNop()) }},
	{"WebShellHandler", func(db *database.DB) interface{} { return NewWebShellHandler(zap.NewNop(), db) }},
}

// storeOwnedHandlers are the domains that went one step further than a consumer interface: their
// handler holds a per-table store from internal/store, which owns the SQL and the schema of exactly
// one table. The same nil-path invariant applies and is easier to satisfy - a store is a pointer, so
// a nil database cannot turn into a non-nil value holding nil - which is precisely why the guard is
// still worth running: a store built from a live connection must not come back nil either.
var storeOwnedHandlers = []struct {
	name  string
	field string
	build func(db *database.DB) interface{}
}{
	{"SkillsHandler", "stats", func(db *database.DB) interface{} {
		h := NewSkillsHandler(&config.Config{}, "", zap.NewNop())
		h.SetDB(db)
		return h
	}},
	{"AssetHandler", "assets", func(db *database.DB) interface{} { return NewAssetHandler(db, zap.NewNop()) }},
	{"BatchTaskManager", "batch", func(db *database.DB) interface{} {
		m := NewBatchTaskManager(zap.NewNop())
		m.SetDB(db)
		return m
	}},
	// RBAC 域交回 store 之后，这四个 handler 的 RBAC 字段自己就是 *store.RBAC（没有 db 接口字段了）。
	{"RBACHandler", "rbac", func(db *database.DB) interface{} { return NewRBACHandler(db, zap.NewNop()) }},
	{"VulnerabilityHandler", "rbac", func(db *database.DB) interface{} { return NewVulnerabilityHandler(db, zap.NewNop(), nil) }},
	{"ConfigHandler", "rbac", func(db *database.DB) interface{} {
		h := &ConfigHandler{logger: zap.NewNop()}
		h.SetDB(db)
		return h
	}},
	{"MonitorHandler", "executions", func(db *database.DB) interface{} { return NewMonitorHandler(nil, nil, db, zap.NewNop()) }},
	{"AuditHandler", "conversations", func(db *database.DB) interface{} { return NewAuditHandler(db, nil, zap.NewNop()) }},
	{"ChatUploadsHandler", "conversations", func(db *database.DB) interface{} { return NewChatUploadsHandler(zap.NewNop(), db) }},
	{"WorkflowHandler", "projects", func(db *database.DB) interface{} { return NewWorkflowHandler(db, zap.NewNop()) }},
	{"ProjectHandler", "projects", func(db *database.DB) interface{} { return NewProjectHandler(db, zap.NewNop()) }},
	{"ConversationHandler", "conversations", func(db *database.DB) interface{} { return NewConversationHandler(db, zap.NewNop()) }},
	{"AttackChainHandler", "projects", func(db *database.DB) interface{} { return NewAttackChainHandler(db, nil, zap.NewNop()) }},
}

// storeField returns a named field and requires it to be a pointer to a store from internal/store.
func storeField(t *testing.T, built interface{}, name string) reflect.Value {
	t.Helper()
	value := reflect.ValueOf(built)
	if value.Kind() != reflect.Ptr || value.Elem().Kind() != reflect.Struct {
		t.Fatalf("%T is not a pointer to a struct", built)
	}
	field := value.Elem().FieldByName(name)
	if !field.IsValid() {
		t.Fatalf("%T has no %s field: the store moved, update this list", value.Type(), name)
	}
	if field.Kind() != reflect.Ptr || !strings.HasPrefix(field.Type().String(), "*store.") {
		t.Fatalf("%T.%s is %s, not a *store.* pointer: this domain is not owned by a table store",
			value.Type(), name, field.Type())
	}
	return field
}

// storageField returns the handler's `db` field, failing when the field is not an interface -
// which is the same as saying the domain was never narrowed.
func storageField(t *testing.T, built interface{}) reflect.Value {
	t.Helper()
	value := reflect.ValueOf(built)
	if value.Kind() != reflect.Ptr || value.Elem().Kind() != reflect.Struct {
		t.Fatalf("%T is not a pointer to a struct", built)
	}
	field := value.Elem().FieldByName("db")
	if !field.IsValid() {
		t.Fatalf("%T has no db field: the narrowing moved, update this list", value.Type())
	}
	if field.Kind() != reflect.Interface {
		t.Fatalf("%T.db is %s, not an interface: this domain was widened back to *database.DB",
			value.Type(), field.Type())
	}
	return field
}

func TestNarrowedStorageStaysNilWithoutADatabase(t *testing.T) {
	if len(narrowedHandlers)+len(storeOwnedHandlers) < 19 {
		t.Fatalf("only %d narrowed + %d store-owned handlers listed, the inventory is stale (19 domains "+
			"left the god object)", len(narrowedHandlers), len(storeOwnedHandlers))
	}
	for _, entry := range narrowedHandlers {
		field := storageField(t, entry.build(nil))
		if !field.IsNil() {
			t.Errorf("%s built with a nil *database.DB holds a non-nil %s - the typed-nil leak: every "+
				"h.db == nil guard in that handler would take the wrong branch (use database.Narrow)",
				entry.name, field.Type())
		}
	}
	for _, entry := range storeOwnedHandlers {
		field := storeField(t, entry.build(nil), entry.field)
		if !field.IsNil() {
			t.Errorf("%s built with a nil *database.DB holds a non-nil %s", entry.name, field.Type())
		}
	}
}

func TestNarrowedStorageKeepsALiveDatabase(t *testing.T) {
	// A real connection, not `&database.DB{}`: the zero value carries a nil *sql.DB, and
	// NewAgentHandler eagerly loads batch queues - which segfaults on that. The repo's convention
	// is real SQLite files over mocks, so the live path gets a database it can actually query.
	dbPath := filepath.Join(t.TempDir(), "narrow-live.db")
	db, err := database.NewDB(dbPath, zap.NewNop())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close() // 临时目录里的 WAL 文件要能先落下，否则 TempDir 回收会撞见"目录非空"
	for _, entry := range narrowedHandlers {
		field := storageField(t, entry.build(db))
		if field.IsNil() {
			t.Errorf("%s dropped a live *database.DB to nil: the handler would answer 'database unavailable' "+
				"for a working deployment", entry.name)
		}
	}
	for _, entry := range storeOwnedHandlers {
		field := storeField(t, entry.build(db), entry.field)
		if field.IsNil() {
			t.Errorf("%s did not get its table store from a live *database.DB", entry.name)
		}
	}
}

// TestNarrowRejectsAnInterfaceDBDoesNotImplement covers the panic path in database.Narrow: an
// store interface declared without its `var _ XStore = (*DB)(nil)` assertion must fail loudly at
// assembly time instead of producing a handler that panics on the first request.
func TestNarrowRejectsAnInterfaceDBDoesNotImplement(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("database.Narrow returned for an interface *DB does not implement: the assertion in stores.go is missing")
		}
	}()
	_ = database.Narrow[interface {
		NoSuchMethodAnywhere() error
	}](&database.DB{})
}
