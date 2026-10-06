package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/store"
	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

const (
	// SQLite 在 WAL 模式下建议使用较保守的连接数，降低长读快照导致 checkpoint 饥饿的概率。
	sqliteMaxOpenConns = 25
	sqliteMaxIdleConns = 5
	// 以页为单位的自动 checkpoint 触发阈值（默认 1000 页，约 4MB @ 4KB/page）。
	sqliteWALAutoCheckpointPages = 1000
	// 控制 WAL 目标上限，避免异常场景持续膨胀（256MB）。
	sqliteJournalSizeLimitBytes = 256 * 1024 * 1024
	// 定时执行 PASSIVE checkpoint，平滑推进 WAL 回收。
	sqlitePassiveCheckpointInterval = 300 * time.Second
)

// configureDBPool 设置 SQLite 连接池参数，提升并发稳定性
func configureDBPool(db *sql.DB) {
	// SQLite 同一时间只允许一个写入者；过高连接数会放大锁竞争和 WAL 回收延迟。
	db.SetMaxOpenConns(sqliteMaxOpenConns)
	db.SetMaxIdleConns(sqliteMaxIdleConns)
	db.SetConnMaxLifetime(30 * time.Minute)
}

// configureSQLitePragmas 调整 WAL 回收行为，降低 -wal 文件长期膨胀风险。
func configureSQLitePragmas(db *sql.DB) error {
	if _, err := db.Exec(fmt.Sprintf("PRAGMA wal_autocheckpoint=%d", sqliteWALAutoCheckpointPages)); err != nil {
		return fmt.Errorf("设置 wal_autocheckpoint 失败: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA journal_size_limit=%d", sqliteJournalSizeLimitBytes)); err != nil {
		return fmt.Errorf("设置 journal_size_limit 失败: %w", err)
	}
	return nil
}

// DB 数据库连接
type DB struct {
	*sql.DB
	logger                   *zap.Logger
	conversationArtifactsDir string
	einoPlantaskBaseDir      string // skills_dir + plantask_rel_dir (per-conversation subdirs)
	einoCheckpointBaseDir    string // checkpoint_dir root (per-conversation subdirs)
	einoReductionRootDir     string // reduction_root_dir or default tmp/reduction (conversations/<id> subdirs)
	einoWorkspaceRootDir     string // workspace_root_dir or default tmp/workspace (projects|conversations/<id> subdirs)
	chatUploadsDir           string // chat_uploads root (<date>/<conversationID> subdirs)
	checkpointLoopName       string
	checkpointStop           chan struct{}
	checkpointDone           chan struct{}
	closeOnce                sync.Once
	closeErr                 error
}

// startPassiveCheckpointLoop 启动后台 PASSIVE checkpoint 循环。
func (db *DB) startPassiveCheckpointLoop(name string) {
	if sqlitePassiveCheckpointInterval <= 0 || db == nil || db.DB == nil {
		return
	}
	db.checkpointLoopName = strings.TrimSpace(name)
	db.checkpointStop = make(chan struct{})
	db.checkpointDone = make(chan struct{})

	go func() {
		defer close(db.checkpointDone)
		ticker := time.NewTicker(sqlitePassiveCheckpointInterval)
		defer ticker.Stop()

		// 启动后先尝试一次，尽快回收已有 WAL 堆积。
		db.runPassiveCheckpoint("startup")
		for {
			select {
			case <-db.checkpointStop:
				return
			case <-ticker.C:
				db.runPassiveCheckpoint("ticker")
			}
		}
	}()
}

// runPassiveCheckpoint 执行一次 PRAGMA wal_checkpoint(PASSIVE)。
func (db *DB) runPassiveCheckpoint(trigger string) {
	if db == nil || db.DB == nil {
		return
	}
	startAt := time.Now()
	var busy, logFrames, checkpointed int
	err := db.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointed)
	if db.logger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("db", db.checkpointLoopName),
		zap.String("trigger", trigger),
		zap.Int("busy", busy),
		zap.Int("log_frames", logFrames),
		zap.Int("checkpointed_frames", checkpointed),
		zap.Int64("elapsed_ms", time.Since(startAt).Milliseconds()),
	}
	if err != nil {
		db.logger.Warn("SQLite PASSIVE checkpoint 完成（失败）",
			append(fields, zap.Error(err))...,
		)
		return
	}
	if busy > 0 {
		db.logger.Debug("SQLite PASSIVE checkpoint 完成（部分推进）", fields...)
		return
	}
	db.logger.Debug("SQLite PASSIVE checkpoint 完成（成功）", fields...)
}

// NewDB 创建数据库连接
func NewDB(dbPath string, logger *zap.Logger) (*DB, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=1&_busy_timeout=5000&_synchronous=NORMAL")
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	configureDBPool(db)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	if err := configureSQLitePragmas(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("配置数据库 PRAGMA 失败: %w", err)
	}

	database := &DB{
		DB:     db,
		logger: logger,
	}
	// Keep conversation-scoped artifacts near database files, so cleanup can follow conversation lifecycle.
	baseDir := filepath.Join(filepath.Dir(dbPath), "conversation_artifacts")
	if mkErr := os.MkdirAll(baseDir, 0o755); mkErr == nil {
		database.conversationArtifactsDir = baseDir
	} else if logger != nil {
		logger.Warn("创建 conversation artifacts 目录失败", zap.String("dir", baseDir), zap.Error(mkErr))
	}

	// 初始化表
	if err := database.initTables(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化表失败: %w", err)
	}
	if err := database.migrateLegacyToolGuardBlocks(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("迁移历史安全拦截记录失败: %w", err)
	}
	database.startPassiveCheckpointLoop("conversations")

	return database, nil
}

// SetEinoConversationDirs configures best-effort filesystem cleanup on DeleteConversation.
// plantaskBase is skills_root/plantask_rel (no conversation id); checkpointBase is checkpoint_dir root.
// reductionRoot is reduction_root_dir from config; empty uses tmp/reduction (conversation-scoped subdirs only).
// workspaceRoot is agent.workspace_root_dir from config; empty uses tmp/workspace.
func (db *DB) SetEinoConversationDirs(plantaskBase, checkpointBase, reductionRoot, workspaceRoot string) {
	if db == nil {
		return
	}
	db.einoPlantaskBaseDir = strings.TrimSpace(plantaskBase)
	db.einoCheckpointBaseDir = strings.TrimSpace(checkpointBase)
	db.einoReductionRootDir = strings.TrimSpace(reductionRoot)
	db.einoWorkspaceRootDir = strings.TrimSpace(workspaceRoot)
}

// SetChatUploadsDir configures the chat_uploads root so DeleteConversation can remove
// uploaded attachment files. Their chat_upload_artifacts rows already disappear via
// ON DELETE CASCADE; without this the files themselves would linger forever.
func (db *DB) SetChatUploadsDir(dir string) {
	if db == nil {
		return
	}
	db.chatUploadsDir = strings.TrimSpace(dir)
}

// initTables 初始化数据库表
func (db *DB) initTables() error {
	// 创建对话表（last_react_input / last_react_output 存「代理消息轨迹」JSON 与助手摘要，列名保留以兼容已有库）
	createConversationsTable := `
	CREATE TABLE IF NOT EXISTS conversations (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		role_name TEXT NOT NULL DEFAULT '默认',
		agent_mode TEXT NOT NULL DEFAULT 'eino_single',
		last_react_input TEXT,
		last_react_output TEXT
	);`

	// 创建消息表
	createMessagesTable := `
	CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		conversation_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		mcp_execution_ids TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
	);`

	// 创建过程详情表
	createProcessDetailsTable := `
	CREATE TABLE IF NOT EXISTS process_details (
		id TEXT PRIMARY KEY,
		message_id TEXT NOT NULL,
		conversation_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		message TEXT,
		data TEXT,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
	);`

	// 创建工具执行记录表
	createToolExecutionsTable := `
	CREATE TABLE IF NOT EXISTS tool_executions (
		id TEXT PRIMARY KEY,
		tool_name TEXT NOT NULL,
		arguments TEXT NOT NULL,
		status TEXT NOT NULL,
		result TEXT,
		error TEXT,
		start_time DATETIME NOT NULL,
		end_time DATETIME,
		duration_ms INTEGER,
		partial_output TEXT,
		partial_output_bytes INTEGER NOT NULL DEFAULT 0,
		partial_output_truncated INTEGER NOT NULL DEFAULT 0,
		partial_output_updated_at DATETIME,
		owner_user_id TEXT,
		conversation_id TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`

	// 创建工具统计表
	createToolStatsTable := `
	CREATE TABLE IF NOT EXISTS tool_stats (
		tool_name TEXT PRIMARY KEY,
		total_calls INTEGER NOT NULL DEFAULT 0,
		success_calls INTEGER NOT NULL DEFAULT 0,
		failed_calls INTEGER NOT NULL DEFAULT 0,
		last_call_time DATETIME,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`

	// 攻击链两张表的 DDL 在 store.AttackChain 里（EnsureSchema 一并建两张表与两条会话索引）。

	// 创建项目表
	createProjectsTable := `
	CREATE TABLE IF NOT EXISTS projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		scope_json TEXT,
		status TEXT NOT NULL DEFAULT 'active',
		pinned INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);`

	// 黑板两张表（project_facts 与 project_fact_edges）的 DDL 与六条索引都在 store.Facts 的 EnsureSchema 里。

	// 创建漏洞表
	createVulnerabilitiesTable := `
	CREATE TABLE IF NOT EXISTS vulnerabilities (
		id TEXT PRIMARY KEY,
		conversation_id TEXT,
		conversation_tag TEXT,
		task_tag TEXT,
		title TEXT NOT NULL,
		description TEXT,
		severity TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'open',
		vulnerability_type TEXT,
		target TEXT,
		preconditions TEXT,
		reproduction_steps TEXT,
		evidence TEXT,
		impact TEXT,
		recommendation TEXT,
		retest_notes TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		project_id TEXT,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE SET NULL
	);`

	// assets 表的 DDL、十三个后补列与十条索引都在 store.Assets 的 EnsureSchema 里，按那个顺序自建。
	// 这一段索引不再留在这里：全局建索引的那一步跑在所有补列之后，看起来更晚更安全，
	// 但表的拥有者因此只剩半个，真实安装里有三条索引仍由连接包装创建。

	// 批量任务两张表（队列 + 任务）的 DDL、十三个后补列与三条索引都在 store.BatchTasks 里，
	// 由启动那三步按 建表 → 补列 → 建索引 的顺序跑。

	// WebShell 两张表（连接配置与工作区状态）的 DDL 与三条索引在 store.Webshell 的 EnsureSchema 里。

	// ========================================================================
	// C2 模块（监听器 / 会话 / 任务 / 文件 / 事件 / Malleable Profile）
	// ========================================================================
	createC2ListenersTable := `
	CREATE TABLE IF NOT EXISTS c2_listeners (
		id TEXT PRIMARY KEY,
		project_id TEXT,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		bind_host TEXT NOT NULL DEFAULT '127.0.0.1',
		bind_port INTEGER NOT NULL,
		profile_id TEXT,
		encryption_key TEXT NOT NULL DEFAULT '',
		implant_token TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'stopped',
		config_json TEXT NOT NULL DEFAULT '{}',
		remark TEXT NOT NULL DEFAULT '',
		owner_user_id TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		started_at DATETIME,
		last_error TEXT
	);`

	createC2SessionsTable := `
	CREATE TABLE IF NOT EXISTS c2_sessions (
		id TEXT PRIMARY KEY,
		listener_id TEXT NOT NULL,
		implant_uuid TEXT NOT NULL UNIQUE,
		hostname TEXT,
		username TEXT,
		os TEXT,
		arch TEXT,
		pid INTEGER DEFAULT 0,
		process_name TEXT,
		is_admin INTEGER DEFAULT 0,
		internal_ip TEXT,
		external_ip TEXT,
		user_agent TEXT,
		sleep_seconds INTEGER NOT NULL DEFAULT 5,
		jitter_percent INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'active',
		first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_check_in DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		metadata_json TEXT DEFAULT '{}',
		note TEXT NOT NULL DEFAULT '',
		FOREIGN KEY (listener_id) REFERENCES c2_listeners(id) ON DELETE CASCADE
	);`

	createC2TasksTable := `
	CREATE TABLE IF NOT EXISTS c2_tasks (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		task_type TEXT NOT NULL,
		payload_json TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'queued',
		result_text TEXT,
		result_blob_path TEXT,
		error TEXT,
		source TEXT NOT NULL DEFAULT 'manual',
		conversation_id TEXT,
		approval_status TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		sent_at DATETIME,
		started_at DATETIME,
		completed_at DATETIME,
		duration_ms INTEGER DEFAULT 0,
		FOREIGN KEY (session_id) REFERENCES c2_sessions(id) ON DELETE CASCADE
	);`

	createC2FilesTable := `
	CREATE TABLE IF NOT EXISTS c2_files (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		task_id TEXT,
		direction TEXT NOT NULL,
		remote_path TEXT NOT NULL,
		local_path TEXT NOT NULL,
		size_bytes INTEGER DEFAULT 0,
		sha256 TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (session_id) REFERENCES c2_sessions(id) ON DELETE CASCADE
	);`

	createC2EventsTable := `
	CREATE TABLE IF NOT EXISTS c2_events (
		id TEXT PRIMARY KEY,
		level TEXT NOT NULL DEFAULT 'info',
		category TEXT NOT NULL,
		session_id TEXT,
		task_id TEXT,
		message TEXT NOT NULL,
		data_json TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`

	createC2ProfilesTable := `
	CREATE TABLE IF NOT EXISTS c2_profiles (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		user_agent TEXT,
		uris_json TEXT NOT NULL DEFAULT '[]',
		request_headers_json TEXT,
		response_headers_json TEXT,
		body_template TEXT,
		jitter_min_ms INTEGER DEFAULT 0,
		jitter_max_ms INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`

	// workflow 五张表的 DDL 与九条索引都在 store.Workflows 的 EnsureSchema 里。

	// 创建索引
	createIndexes := `
	CREATE INDEX IF NOT EXISTS idx_messages_conversation_id ON messages(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_conversations_updated_at ON conversations(updated_at);
	CREATE INDEX IF NOT EXISTS idx_process_details_message_id ON process_details(message_id);
	CREATE INDEX IF NOT EXISTS idx_process_details_conversation_id ON process_details(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_tool_executions_tool_name ON tool_executions(tool_name);
	CREATE INDEX IF NOT EXISTS idx_tool_executions_start_time ON tool_executions(start_time);
	CREATE INDEX IF NOT EXISTS idx_tool_executions_status ON tool_executions(status);
	CREATE INDEX IF NOT EXISTS idx_conversations_pinned ON conversations(pinned);
	CREATE INDEX IF NOT EXISTS idx_vulnerabilities_conversation_id ON vulnerabilities(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_vulnerabilities_conversation_tag ON vulnerabilities(conversation_tag);
	CREATE INDEX IF NOT EXISTS idx_vulnerabilities_task_tag ON vulnerabilities(task_tag);
	CREATE INDEX IF NOT EXISTS idx_vulnerabilities_severity ON vulnerabilities(severity);
	CREATE INDEX IF NOT EXISTS idx_vulnerabilities_status ON vulnerabilities(status);
	CREATE INDEX IF NOT EXISTS idx_vulnerabilities_created_at ON vulnerabilities(created_at);
	CREATE INDEX IF NOT EXISTS idx_projects_status ON projects(status);
	CREATE INDEX IF NOT EXISTS idx_projects_updated_at ON projects(updated_at);
	CREATE INDEX IF NOT EXISTS idx_conversations_project_id ON conversations(project_id);
	CREATE INDEX IF NOT EXISTS idx_vulnerabilities_project_id ON vulnerabilities(project_id);
	CREATE INDEX IF NOT EXISTS idx_c2_listeners_created_at ON c2_listeners(created_at);
	CREATE INDEX IF NOT EXISTS idx_c2_listeners_project_id ON c2_listeners(project_id);
	CREATE INDEX IF NOT EXISTS idx_c2_listeners_status ON c2_listeners(status);
	CREATE INDEX IF NOT EXISTS idx_c2_sessions_listener ON c2_sessions(listener_id);
	CREATE INDEX IF NOT EXISTS idx_c2_sessions_status ON c2_sessions(status);
	CREATE INDEX IF NOT EXISTS idx_c2_sessions_last_check_in ON c2_sessions(last_check_in);
	CREATE INDEX IF NOT EXISTS idx_c2_tasks_session ON c2_tasks(session_id);
	CREATE INDEX IF NOT EXISTS idx_c2_tasks_status ON c2_tasks(status);
	CREATE INDEX IF NOT EXISTS idx_c2_tasks_created_at ON c2_tasks(created_at);
	CREATE INDEX IF NOT EXISTS idx_c2_tasks_conversation ON c2_tasks(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_c2_files_session ON c2_files(session_id);
	CREATE INDEX IF NOT EXISTS idx_c2_events_created_at ON c2_events(created_at);
	CREATE INDEX IF NOT EXISTS idx_c2_events_category ON c2_events(category);
	CREATE INDEX IF NOT EXISTS idx_c2_events_session ON c2_events(session_id);
										`

	if _, err := db.Exec(createConversationsTable); err != nil {
		return fmt.Errorf("创建conversations表失败: %w", err)
	}

	if _, err := db.Exec(createMessagesTable); err != nil {
		return fmt.Errorf("创建messages表失败: %w", err)
	}

	if _, err := db.Exec(createProcessDetailsTable); err != nil {
		return fmt.Errorf("创建process_details表失败: %w", err)
	}

	if _, err := db.Exec(createToolExecutionsTable); err != nil {
		return fmt.Errorf("创建tool_executions表失败: %w", err)
	}

	if _, err := db.Exec(createToolStatsTable); err != nil {
		return fmt.Errorf("创建tool_stats表失败: %w", err)
	}

	// 两张表都外键到 conversations，节点表还外键到 tool_executions，所以这一步只能在它们之后。
	if err := store.NewAttackChain(db.DB).EnsureSchema(); err != nil {
		return fmt.Errorf("创建攻击链表失败: %w", err)
	}

	// robot_user_sessions 的建表、索引与 agent_mode 补列归 store.RobotSessions，
	// 由启动那一次 ensureRobotSessionSchema 跑。

	if _, err := db.Exec(createProjectsTable); err != nil {
		return fmt.Errorf("创建projects表失败: %w", err)
	}

	// 顺序不能提前：两张表都对 projects 有外键。
	if err := store.NewFacts(db.DB).EnsureSchema(); err != nil {
		return fmt.Errorf("创建黑板表失败: %w", err)
	}

	if _, err := db.Exec(createVulnerabilitiesTable); err != nil {
		return fmt.Errorf("创建vulnerabilities表失败: %w", err)
	}
	// 外键指向 projects，所以顺序不能提前到它之前。建表、补列、建索引三步的先后由 store 自己保证。
	if err := store.NewAssets(db.DB).EnsureSchema(); err != nil {
		return fmt.Errorf("初始化assets表失败: %w", err)
	}

	// 两张表在这一个 EnsureSchema 里按外键顺序建（任务表外键指向队列表并级联删除）。
	if err := store.NewBatchTasks(db.DB).EnsureSchema(); err != nil {
		return fmt.Errorf("创建批量任务表失败: %w", err)
	}

	// 状态表对连接表有外键，两张表在这一个 EnsureSchema 里按顺序建。
	if err := store.NewWebshell(db.DB).EnsureSchema(); err != nil {
		return fmt.Errorf("创建 WebShell 连接表失败: %w", err)
	}

	if err := db.initRBACTables(); err != nil {
		return fmt.Errorf("创建RBAC表失败: %w", err)
	}
	// 漏洞提醒的两张表由它们的 store 自己建：订阅页与投递 worker 是仅有的两个读者。
	// 顺序不能提前——两张表都对 rbac_users 有外键，投递表还对外键到 vulnerabilities。
	if err := store.NewVulnerabilityAlerts(db.DB).EnsureSchema(); err != nil {
		return fmt.Errorf("创建漏洞提醒表失败: %w", err)
	}

	// 五张表按外键顺序在这一个字符串里建（runs 先于 node_runs、inspections 先于 imports）；
	// 这一步只能在 conversations 之后，因为 workflow_runs.conversation_id 外键指向它。
	if err := store.NewWorkflows(db.DB).EnsureSchema(); err != nil {
		return fmt.Errorf("创建workflow表失败: %w", err)
	}

	for tableName, ddl := range map[string]string{
		"c2_listeners": createC2ListenersTable,
		"c2_sessions":  createC2SessionsTable,
		"c2_tasks":     createC2TasksTable,
		"c2_files":     createC2FilesTable,
		"c2_events":    createC2EventsTable,
		"c2_profiles":  createC2ProfilesTable,
	} {
		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("创建%s表失败: %w", tableName, err)
		}
	}

	// 为已有表添加新字段（如果不存在）- 必须在创建索引之前
	if err := db.migrateConversationsTable(); err != nil {
		db.logger.Warn("迁移conversations表失败", zap.Error(err))
		// 不返回错误，允许继续运行
	}

	if err := db.migrateMessagesTable(); err != nil {
		db.logger.Warn("迁移messages表失败", zap.Error(err))
		// 不返回错误，允许继续运行
	}

	if err := store.NewBatchTasks(db.DB).MigrateQueueColumns(); err != nil {
		db.logger.Warn("迁移batch_task_queues表失败", zap.Error(err))
		// 不返回错误，允许继续运行
	}

	// 三条索引必须排在补列之后：idx_batch_task_queues_title 依赖的 title 只由那一步产生。
	// 原顺序也是这样的——那三条在最后一批全局索引里，而全局索引跑在所有补列之后。
	if err := store.NewBatchTasks(db.DB).EnsureIndexes(); err != nil {
		return fmt.Errorf("创建批量任务索引失败: %w", err)
	}

	if err := db.migrateVulnerabilitiesTable(); err != nil {
		db.logger.Warn("迁移vulnerabilities表失败", zap.Error(err))
		// 不返回错误，允许继续运行
	}
	if err := db.migrateVulnerabilitiesConversationFK(); err != nil {
		db.logger.Warn("迁移vulnerabilities会话外键失败", zap.Error(err))
	}

	if err := db.migrateProjectsTable(); err != nil {
		db.logger.Warn("迁移projects相关表失败", zap.Error(err))
	}
	if err := db.dropProjectFactVersionsTable(); err != nil {
		db.logger.Warn("清理project_fact_versions表失败", zap.Error(err))
	}

	// 列补写归表的拥有者；这里保持原来的"记一条 warn 就继续"。
	if err := store.NewWebshell(db.DB).MigrateConnectionsTable(); err != nil {
		db.logger.Warn("迁移webshell_connections表失败", zap.Error(err))
		// 不返回错误，允许继续运行
	}
	if err := db.migrateC2ListenersTable(); err != nil {
		db.logger.Warn("迁移c2_listeners表失败", zap.Error(err))
	}
	// 列补写也归表的拥有者；这里保持原来的"记一条 warn 就继续"，建表失败才拦启动。
	if err := store.NewWorkflows(db.DB).MigrateRunsTable(); err != nil {
		db.logger.Warn("迁移 workflow 运行表失败", zap.Error(err))
	}
	if err := db.migrateToolExecutionsPartialOutputColumns(); err != nil {
		db.logger.Warn("迁移tool_executions partial output字段失败", zap.Error(err))
	}
	if err := db.migrateRBACOwnershipColumns(); err != nil {
		db.logger.Warn("迁移RBAC资源归属字段失败", zap.Error(err))
	}

	if _, err := db.Exec(createIndexes); err != nil {
		return fmt.Errorf("创建索引失败: %w", err)
	}

	// model_token_usage 的建表、四个索引与历史回填都归 store.ModelTokenUsage，
	// 由进程启动时那一次 ensureModelTokenUsageSchema 跑（表要先于时间线写入路径存在）。
	db.logger.Debug("数据库表初始化完成")
	return nil
}

func (db *DB) migrateToolExecutionsPartialOutputColumns() error {
	for _, col := range []struct {
		name string
		stmt string
	}{
		{"partial_output", "ALTER TABLE tool_executions ADD COLUMN partial_output TEXT"},
		{"partial_output_bytes", "ALTER TABLE tool_executions ADD COLUMN partial_output_bytes INTEGER NOT NULL DEFAULT 0"},
		{"partial_output_truncated", "ALTER TABLE tool_executions ADD COLUMN partial_output_truncated INTEGER NOT NULL DEFAULT 0"},
		{"partial_output_updated_at", "ALTER TABLE tool_executions ADD COLUMN partial_output_updated_at DATETIME"},
	} {
		if err := db.addColumnIfMissing("tool_executions", col.name, col.stmt); err != nil {
			return err
		}
	}
	return nil
}

// migrateMessagesTable 迁移 messages 表，补充 updated_at 字段。
// 语义：updated_at 表示该条消息最后一次被写入/更新的时间（例如助手占位消息在任务结束时更新正文）。
func (db *DB) migrateMessagesTable() error {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name='updated_at'").Scan(&count)
	if err != nil {
		// 如果查询失败，尝试添加字段
		if _, addErr := db.Exec("ALTER TABLE messages ADD COLUMN updated_at DATETIME"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				return fmt.Errorf("添加 messages.updated_at 字段失败: %w", addErr)
			}
		}
	} else if count == 0 {
		if _, err := db.Exec("ALTER TABLE messages ADD COLUMN updated_at DATETIME"); err != nil {
			errMsg := strings.ToLower(err.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				return fmt.Errorf("添加 messages.updated_at 字段失败: %w", err)
			}
		}
	}

	// 回填已有数据：让 updated_at 至少等于 created_at，避免前端出现空/当前时间回退。
	// 这条 UPDATE 归表的拥有者，所以这里只负责在原来的位置调用它，并沿用"失败也继续启动"。
	_ = store.NewSession(db.DB).BackfillMessageUpdatedAt()

	// reasoning_content：DeepSeek 思考模式 + 工具调用续跑；与 last_react_input 互补，供消息表回退路径回放
	var rcColCount int
	errRC := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name='reasoning_content'").Scan(&rcColCount)
	if errRC != nil {
		if _, addErr := db.Exec("ALTER TABLE messages ADD COLUMN reasoning_content TEXT"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				return fmt.Errorf("添加 messages.reasoning_content 字段失败: %w", addErr)
			}
		}
	} else if rcColCount == 0 {
		if _, err := db.Exec("ALTER TABLE messages ADD COLUMN reasoning_content TEXT"); err != nil {
			errMsg := strings.ToLower(err.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				return fmt.Errorf("添加 messages.reasoning_content 字段失败: %w", err)
			}
		}
	}
	return nil
}

// migrateConversationsTable 迁移conversations表，添加新字段
func (db *DB) migrateConversationsTable() error {
	// 检查last_react_input字段是否存在
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('conversations') WHERE name='last_react_input'").Scan(&count)
	if err != nil {
		// 如果查询失败，尝试添加字段
		if _, addErr := db.Exec("ALTER TABLE conversations ADD COLUMN last_react_input TEXT"); addErr != nil {
			// 如果字段已存在，忽略错误（SQLite错误信息可能不同）
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				db.logger.Warn("添加last_react_input字段失败", zap.Error(addErr))
			}
		}
	} else if count == 0 {
		// 字段不存在，添加它
		if _, err := db.Exec("ALTER TABLE conversations ADD COLUMN last_react_input TEXT"); err != nil {
			db.logger.Warn("添加last_react_input字段失败", zap.Error(err))
		}
	}

	// 检查last_react_output字段是否存在
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('conversations') WHERE name='last_react_output'").Scan(&count)
	if err != nil {
		// 如果查询失败，尝试添加字段
		if _, addErr := db.Exec("ALTER TABLE conversations ADD COLUMN last_react_output TEXT"); addErr != nil {
			// 如果字段已存在，忽略错误
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				db.logger.Warn("添加last_react_output字段失败", zap.Error(addErr))
			}
		}
	} else if count == 0 {
		// 字段不存在，添加它
		if _, err := db.Exec("ALTER TABLE conversations ADD COLUMN last_react_output TEXT"); err != nil {
			db.logger.Warn("添加last_react_output字段失败", zap.Error(err))
		}
	}

	// 检查pinned字段是否存在
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('conversations') WHERE name='pinned'").Scan(&count)
	if err != nil {
		// 如果查询失败，尝试添加字段
		if _, addErr := db.Exec("ALTER TABLE conversations ADD COLUMN pinned INTEGER DEFAULT 0"); addErr != nil {
			// 如果字段已存在，忽略错误
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				db.logger.Warn("添加pinned字段失败", zap.Error(addErr))
			}
		}
	} else if count == 0 {
		// 字段不存在，添加它
		if _, err := db.Exec("ALTER TABLE conversations ADD COLUMN pinned INTEGER DEFAULT 0"); err != nil {
			db.logger.Warn("添加pinned字段失败", zap.Error(err))
		}
	}

	// 检查 webshell_connection_id 字段是否存在（WebShell AI 助手对话关联）
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('conversations') WHERE name='webshell_connection_id'").Scan(&count)
	if err != nil {
		if _, addErr := db.Exec("ALTER TABLE conversations ADD COLUMN webshell_connection_id TEXT"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				db.logger.Warn("添加webshell_connection_id字段失败", zap.Error(addErr))
			}
		}
	} else if count == 0 {
		if _, err := db.Exec("ALTER TABLE conversations ADD COLUMN webshell_connection_id TEXT"); err != nil {
			db.logger.Warn("添加webshell_connection_id字段失败", zap.Error(err))
		}
	}

	// 检查 role_name 字段是否存在（对话绑定的业务角色，用于历史任务切换时恢复角色上下文）
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('conversations') WHERE name='role_name'").Scan(&count)
	if err != nil {
		if _, addErr := db.Exec("ALTER TABLE conversations ADD COLUMN role_name TEXT NOT NULL DEFAULT '默认'"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				db.logger.Warn("添加role_name字段失败", zap.Error(addErr))
			}
		}
	} else if count == 0 {
		if _, err := db.Exec("ALTER TABLE conversations ADD COLUMN role_name TEXT NOT NULL DEFAULT '默认'"); err != nil {
			db.logger.Warn("添加role_name字段失败", zap.Error(err))
		}
	}

	// 检查 agent_mode 字段是否存在（对话绑定的执行模式，用于历史任务切换时恢复对话模式）
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('conversations') WHERE name='agent_mode'").Scan(&count)
	if err != nil {
		if _, addErr := db.Exec("ALTER TABLE conversations ADD COLUMN agent_mode TEXT NOT NULL DEFAULT 'eino_single'"); addErr != nil {
			errMsg := strings.ToLower(addErr.Error())
			if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
				db.logger.Warn("添加agent_mode字段失败", zap.Error(addErr))
			}
		}
	} else if count == 0 {
		if _, err := db.Exec("ALTER TABLE conversations ADD COLUMN agent_mode TEXT NOT NULL DEFAULT 'eino_single'"); err != nil {
			db.logger.Warn("添加agent_mode字段失败", zap.Error(err))
		}
	}

	return nil
}

// migrateProjectsTable 迁移 projects / conversations / vulnerabilities 的项目关联字段。
func (db *DB) migrateProjectsTable() error {
	for _, col := range []struct {
		table string
		name  string
		stmt  string
	}{
		{"conversations", "project_id", "ALTER TABLE conversations ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE SET NULL"},
		{"vulnerabilities", "project_id", "ALTER TABLE vulnerabilities ADD COLUMN project_id TEXT"},
	} {
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", col.table, col.name).Scan(&count)
		if err != nil {
			if _, addErr := db.Exec(col.stmt); addErr != nil {
				errMsg := strings.ToLower(addErr.Error())
				if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
					db.logger.Warn("添加字段失败", zap.String("table", col.table), zap.String("field", col.name), zap.Error(addErr))
				}
			}
			continue
		}
		if count == 0 {
			if _, addErr := db.Exec(col.stmt); addErr != nil {
				db.logger.Warn("添加字段失败", zap.String("table", col.table), zap.String("field", col.name), zap.Error(addErr))
			}
		}
	}
	return nil
}

// dropProjectFactVersionsTable 移除已废弃的事实版本归档表。
func (db *DB) dropProjectFactVersionsTable() error {
	_, err := db.Exec(`DROP TABLE IF EXISTS project_fact_versions`)
	return err
}

// migrateVulnerabilitiesConversationFK 将 vulnerabilities.conversation_id 外键改为 ON DELETE SET NULL，删除对话时保留漏洞记录。
func (db *DB) migrateVulnerabilitiesConversationFK() error {
	ok, err := vulnerabilitiesConversationFKOnDeleteSetNull(db.DB)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const createNew = `
	CREATE TABLE vulnerabilities_new (
		id TEXT PRIMARY KEY,
		conversation_id TEXT,
		conversation_tag TEXT,
		task_tag TEXT,
		title TEXT NOT NULL,
		description TEXT,
		severity TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'open',
		vulnerability_type TEXT,
		target TEXT,
		preconditions TEXT,
		reproduction_steps TEXT,
		evidence TEXT,
		impact TEXT,
		recommendation TEXT,
		retest_notes TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		project_id TEXT,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE SET NULL
	);`
	if _, err := tx.Exec(createNew); err != nil {
		return fmt.Errorf("创建 vulnerabilities_new 失败: %w", err)
	}

	const copyRows = `
	INSERT INTO vulnerabilities_new (
		id, conversation_id, conversation_tag, task_tag, title, description,
		severity, status, vulnerability_type, target, preconditions, reproduction_steps,
		evidence, impact, recommendation, retest_notes,
		created_at, updated_at, project_id
	)
	SELECT
		id, conversation_id, conversation_tag, task_tag, title, description,
		severity, status, vulnerability_type, target,
		COALESCE(preconditions, ''), COALESCE(reproduction_steps, ''),
		COALESCE(evidence, ''), impact, recommendation, COALESCE(retest_notes, ''),
		created_at, updated_at, project_id
	FROM vulnerabilities;`
	if _, err := tx.Exec(copyRows); err != nil {
		return fmt.Errorf("复制 vulnerabilities 数据失败: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE vulnerabilities`); err != nil {
		return fmt.Errorf("删除旧 vulnerabilities 表失败: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE vulnerabilities_new RENAME TO vulnerabilities`); err != nil {
		return fmt.Errorf("重命名 vulnerabilities 表失败: %w", err)
	}

	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_vulnerabilities_conversation_id ON vulnerabilities(conversation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_vulnerabilities_conversation_tag ON vulnerabilities(conversation_tag)`,
		`CREATE INDEX IF NOT EXISTS idx_vulnerabilities_task_tag ON vulnerabilities(task_tag)`,
		`CREATE INDEX IF NOT EXISTS idx_vulnerabilities_severity ON vulnerabilities(severity)`,
		`CREATE INDEX IF NOT EXISTS idx_vulnerabilities_status ON vulnerabilities(status)`,
		`CREATE INDEX IF NOT EXISTS idx_vulnerabilities_created_at ON vulnerabilities(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_vulnerabilities_project_id ON vulnerabilities(project_id)`,
	}
	for _, stmt := range indexes {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("重建 vulnerabilities 索引失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 vulnerabilities 外键迁移失败: %w", err)
	}
	db.logger.Info("vulnerabilities 表已迁移：删除对话时保留漏洞记录")
	return nil
}

func vulnerabilitiesConversationFKOnDeleteSetNull(db *sql.DB) (bool, error) {
	rows, err := db.Query(`PRAGMA foreign_key_list(vulnerabilities)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return false, err
		}
		if from == "conversation_id" {
			found = true
			if !strings.EqualFold(onDelete, "SET NULL") {
				return false, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return found, nil
}

// migrateVulnerabilitiesTable 迁移 vulnerabilities 表，补充标签字段
func (db *DB) migrateVulnerabilitiesTable() error {
	columns := []struct {
		name string
		stmt string
	}{
		{name: "conversation_tag", stmt: "ALTER TABLE vulnerabilities ADD COLUMN conversation_tag TEXT"},
		{name: "task_tag", stmt: "ALTER TABLE vulnerabilities ADD COLUMN task_tag TEXT"},
		{name: "project_id", stmt: "ALTER TABLE vulnerabilities ADD COLUMN project_id TEXT"},
		{name: "preconditions", stmt: "ALTER TABLE vulnerabilities ADD COLUMN preconditions TEXT"},
		{name: "reproduction_steps", stmt: "ALTER TABLE vulnerabilities ADD COLUMN reproduction_steps TEXT"},
		{name: "evidence", stmt: "ALTER TABLE vulnerabilities ADD COLUMN evidence TEXT"},
		{name: "retest_notes", stmt: "ALTER TABLE vulnerabilities ADD COLUMN retest_notes TEXT"},
	}

	for _, col := range columns {
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('vulnerabilities') WHERE name=?", col.name).Scan(&count)
		if err != nil {
			if _, addErr := db.Exec(col.stmt); addErr != nil {
				errMsg := strings.ToLower(addErr.Error())
				if !strings.Contains(errMsg, "duplicate column") && !strings.Contains(errMsg, "already exists") {
					db.logger.Warn("添加vulnerabilities字段失败", zap.String("field", col.name), zap.Error(addErr))
				}
			}
			continue
		}
		if count == 0 {
			if _, addErr := db.Exec(col.stmt); addErr != nil {
				db.logger.Warn("添加vulnerabilities字段失败", zap.String("field", col.name), zap.Error(addErr))
			}
		}
	}
	return nil
}

// migrateWebshellConnectionsTable 迁移 webshell_connections 表，补充新字段

func (db *DB) migrateC2ListenersTable() error {
	return db.addColumnIfMissing("c2_listeners", "project_id", "ALTER TABLE c2_listeners ADD COLUMN project_id TEXT")
}

// NewKnowledgeDB 创建知识库数据库连接（只包含知识库相关的表）
func NewKnowledgeDB(dbPath string, logger *zap.Logger) (*DB, error) {
	sqlDB, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=1&_busy_timeout=5000&_synchronous=NORMAL")
	if err != nil {
		return nil, fmt.Errorf("打开知识库数据库失败: %w", err)
	}

	configureDBPool(sqlDB)

	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("连接知识库数据库失败: %w", err)
	}
	if err := configureSQLitePragmas(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("配置知识库数据库 PRAGMA 失败: %w", err)
	}

	database := &DB{
		DB:     sqlDB,
		logger: logger,
	}

	// 初始化知识库表
	if err := database.initKnowledgeTables(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("初始化知识库表失败: %w", err)
	}
	database.startPassiveCheckpointLoop("knowledge")

	return database, nil
}

// initKnowledgeTables 初始化知识库数据库表（只包含知识库相关的表）
func (db *DB) initKnowledgeTables() error {
	// knowledge_base_items 由它的 store 建；必须在向量表之前，后者对外键指向它。
	if err := store.NewKnowledgeItems(db.DB).EnsureSchema(); err != nil {
		return fmt.Errorf("创建knowledge_base_items表失败: %w", err)
	}

	// knowledge_embeddings 的建表、索引与三段列补写都在 store 里：这张表曾经有两份结构定义，
	// 一处在知识库数据库的启动清扫里，一处在知识库包自己的列补写里，两条规则各写三遍 ALTER。
	if err := store.NewKnowledgeEmbeddings(db.DB).EnsureSchema(); err != nil {
		return fmt.Errorf("创建knowledge_embeddings表失败: %w", err)
	}

	// knowledge_retrieval_logs 在独立知识库里不建外键（conversations/messages 可能不在这个库）。
	// 两种拼法都由这张表的主人给出，见 internal/store/knowledge_retrieval.go。
	if err := store.NewKnowledgeRetrieval(db.DB).EnsureStandaloneSchema(); err != nil {
		return fmt.Errorf("创建knowledge_retrieval_logs表失败: %w", err)
	}

	db.logger.Info("知识库数据库表初始化完成")
	return nil
}

// Close 关闭数据库连接
func (db *DB) Close() error {
	if db == nil {
		return nil
	}
	db.closeOnce.Do(func() {
		if db.checkpointStop != nil {
			close(db.checkpointStop)
			if db.checkpointDone != nil {
				<-db.checkpointDone
			}
		}
		if db.DB != nil {
			db.closeErr = db.DB.Close()
		}
	})
	return db.closeErr
}
