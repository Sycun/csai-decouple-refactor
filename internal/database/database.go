package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/storage"
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
	// dirs is the set of per-conversation directories an ended run leaves on disk. The cleanup itself
	// lives in internal/storage - this type only carries the roots, because the connection object has
	// no business knowing how a plantask board or a reduction cache is laid out.
	dirs               storage.ConversationDirs
	checkpointLoopName string
	checkpointStop     chan struct{}
	checkpointDone     chan struct{}
	closeOnce          sync.Once
	closeErr           error
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
	if err := store.NewMonitor(database.DB, NewRBAC(database).UserCanAccessResource).MigrateLegacyGuardBlocks(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("迁移历史安全拦截记录失败: %w", err)
	}
	database.startPassiveCheckpointLoop("conversations")

	return database, nil
}

// SetConversationDirs configures the best-effort directory cleanup that DeleteConversation and
// DeleteProject perform. It replaces the two setters this used to take (one for the four Eino roots,
// one for the uploads root) with a single decision about where a conversation's files live.
func (db *DB) SetConversationDirs(plantaskBase, checkpointBase, reductionRoot, workspaceRoot, chatUploadsRoot string) {
	if db == nil {
		return
	}
	db.dirs = storage.NewConversationDirs(storage.ConversationDirs{
		Artifacts:   db.conversationArtifactsDir,
		Plantask:    strings.TrimSpace(plantaskBase),
		Checkpoint:  strings.TrimSpace(checkpointBase),
		Reduction:   strings.TrimSpace(reductionRoot),
		Workspace:   strings.TrimSpace(workspaceRoot),
		ChatUploads: strings.TrimSpace(chatUploadsRoot),
	}, db.logger)
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
	// messages 表的 DDL 在 store.Session 里（见 session_schema.go）。

	// 创建过程详情表
	// process_details 表的 DDL 在 store.Session 里（见 session_schema.go）。

	// tool_executions 与 tool_stats 两张表的 DDL、四个后补列与三条索引都在 store.Monitor 里，
	// 由启动那三步按 建表 → 补列 → 建索引 的顺序跑。
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
	// vulnerabilities 表的 DDL、七条索引与七个后补列都在 store.Vulnerabilities 里。

	// assets 表的 DDL、十三个后补列与十条索引都在 store.Assets 的 EnsureSchema 里，按那个顺序自建。
	// 这一段索引不再留在这里：全局建索引的那一步跑在所有补列之后，看起来更晚更安全，
	// 但表的拥有者因此只剩半个，真实安装里有三条索引仍由连接包装创建。

	// 批量任务两张表（队列 + 任务）的 DDL、十三个后补列与三条索引都在 store.BatchTasks 里，
	// 由启动那三步按 建表 → 补列 → 建索引 的顺序跑。

	// WebShell 两张表（连接配置与工作区状态）的 DDL 与三条索引在 store.Webshell 的 EnsureSchema 里。

	// C2 六张表（监听器 / 会话 / 任务 / 文件 / 事件 / Malleable Profile）的 DDL、project_id
	// 补列与十四条索引都在 store.C2 里，由启动那三步按 建表 → 补列 → 建索引 的顺序跑。
	// workflow 五张表的 DDL 与九条索引都在 store.Workflows 的 EnsureSchema 里。

	// 创建索引
	createIndexes := `
	CREATE INDEX IF NOT EXISTS idx_conversations_updated_at ON conversations(updated_at);
	CREATE INDEX IF NOT EXISTS idx_conversations_pinned ON conversations(pinned);
	CREATE INDEX IF NOT EXISTS idx_projects_status ON projects(status);
	CREATE INDEX IF NOT EXISTS idx_projects_updated_at ON projects(updated_at);
	CREATE INDEX IF NOT EXISTS idx_conversations_project_id ON conversations(project_id);
										`

	if _, err := db.Exec(createConversationsTable); err != nil {
		return fmt.Errorf("创建conversations表失败: %w", err)
	}

	// 两张表的 DDL 与三条索引归 store.Session；顺序仍在 conversations 之后（两张表都外键指向它）。
	if err := store.NewSession(db.DB).EnsureSchema(); err != nil {
		return err
	}

	if err := store.NewMonitor(db.DB, NewRBAC(db).UserCanAccessResource).EnsureSchema(); err != nil {
		return err
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

	// 外键指向 conversations，所以这一步不能提前到它之前。
	if err := store.NewVulnerabilities(db.DB, nil).EnsureSchema(); err != nil {
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

	if err := store.NewRBAC(db.DB).EnsureSchema(); err != nil {
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

	// C2 六张表建在它们的主人那里；c2_sessions 指向 c2_listeners、两张子表指向 c2_sessions，
	// 所以六个 CREATE 由 EnsureSchema 自己按外键顺序排。
	if err := store.NewC2(db.DB).EnsureSchema(); err != nil {
		return err
	}

	// 为已有表添加新字段（如果不存在）- 必须在创建索引之前
	if err := db.migrateConversationsTable(); err != nil {
		db.logger.Warn("迁移conversations表失败", zap.Error(err))
		// 不返回错误，允许继续运行
	}

	if err := store.NewSession(db.DB).MigrateMessageColumns(); err != nil {
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

	if err := store.NewVulnerabilities(db.DB, nil).MigrateLateColumns(); err != nil {
		db.logger.Warn("迁移vulnerabilities表失败", zap.Error(err))
		// 不返回错误，允许继续运行
	}
	rebuilt, fkErr := store.NewVulnerabilities(db.DB, nil).MigrateConversationFK()
	if fkErr != nil {
		db.logger.Warn("迁移vulnerabilities会话外键失败", zap.Error(fkErr))
	}
	if rebuilt {
		// 原来这句 Info 由迁移函数自己打；现在由启动这一侧打，内容不变。
		db.logger.Info("vulnerabilities 表已迁移：删除对话时保留漏洞记录")
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
	if err := store.NewC2(db.DB).MigrateListenerColumns(); err != nil {
		db.logger.Warn("迁移c2_listeners表失败", zap.Error(err))
	}
	// c2 十四条索引里有一条坐在 project_id 上；补列跑完之前它建不出来。
	if err := store.NewC2(db.DB).EnsureIndexes(); err != nil {
		return fmt.Errorf("创建C2索引失败: %w", err)
	}
	// 列补写也归表的拥有者；这里保持原来的"记一条 warn 就继续"，建表失败才拦启动。
	if err := store.NewWorkflows(db.DB).MigrateRunsTable(); err != nil {
		db.logger.Warn("迁移 workflow 运行表失败", zap.Error(err))
	}
	if err := store.NewMonitor(db.DB, NewRBAC(db).UserCanAccessResource).MigrateLateColumns(); err != nil {
		db.logger.Warn("迁移tool_executions partial output字段失败", zap.Error(err))
	}
	// 三条 tool_executions 索引仍按原顺序跑在补列之后（全局建索引那一步的位置）。
	if err := store.NewMonitor(db.DB, NewRBAC(db).UserCanAccessResource).EnsureIndexes(); err != nil {
		return fmt.Errorf("创建tool_executions索引失败: %w", err)
	}
	if err := db.migrateLegacyOwnerColumns(); err != nil {
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
// migrateProjectsTable backfills the project stamp on the tables whose schema the data layer still
// owns. `vulnerabilities` is deliberately absent: project_id is in store.Vulnerabilities' own CREATE
// TABLE and in its MigrateLateColumns, and the boot gate refuses an ALTER for a table that has an owner.
func (db *DB) migrateProjectsTable() error {
	for _, col := range []struct {
		table string
		name  string
		stmt  string
	}{
		{"conversations", "project_id", "ALTER TABLE conversations ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE SET NULL"},
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

// migrateLegacyOwnerColumns 是 RBAC 迁移里剩下的两张表的 owner 补列：projects 与 conversations
// 的 DDL 还在本文件里（它们的刀没到），补列先跟着表走；其余四张表（vulnerabilities /
// webshell_connections / batch_task_queues / c2_listeners）的同一列已随各自表主进 store 的迁移。
func (db *DB) migrateLegacyOwnerColumns() error {
	for _, col := range []struct {
		table string
		name  string
		stmt  string
	}{
		{"projects", "owner_user_id", "ALTER TABLE projects ADD COLUMN owner_user_id TEXT"},
		{"conversations", "owner_user_id", "ALTER TABLE conversations ADD COLUMN owner_user_id TEXT"},
	} {
		if err := db.addColumnIfMissing(col.table, col.name, col.stmt); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) addColumnIfMissing(table, name, stmt string) error {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", table, name).Scan(&count)
	if err != nil || count == 0 {
		if _, addErr := db.Exec(stmt); addErr != nil {
			msg := strings.ToLower(addErr.Error())
			if !strings.Contains(msg, "duplicate column") && !strings.Contains(msg, "already exists") {
				return fmt.Errorf("添加%s.%s字段失败: %w", table, name, addErr)
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
