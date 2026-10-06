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
	dirs storage.ConversationDirs
	// checkpoint is the WAL housekeeping loop; the wrapper only carries it so Close can stop it.
	checkpoint *passiveCheckpointLoop
	closeOnce  sync.Once
	closeErr   error
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
	database.checkpoint = startPassiveCheckpointLoop(db, logger, "conversations")

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
	// 所有表的 DDL 都在各自主人的 EnsureSchema 里，这里只剩调用点与外键顺序：
	//   conversations → store.Conversations（必须最先：下面多数表都外键指向它）
	//   messages / process_details → store.Session；workflow 五张表 → store.Workflows
	//   vulnerabilities → store.Vulnerabilities（外键指向 conversations）
	//   projects → store.Projects；黑板两张表 → store.Facts（外键指向 projects）
	//   assets → store.Assets（外键指向 projects）
	//   tool_executions / tool_stats → store.Monitor；攻击链两张表 → store.AttackChain
	//   批量任务两张表 → store.BatchTasks；WebShell 两张表 → store.Webshell
	//   rbac_* 六张表 → store.RBAC；漏洞提醒两张表 → store.VulnerabilityAlerts
	//   c2_* 六张表 → store.C2；robot_user_sessions → 启动那一次 ensureRobotSessionSchema
	if err := store.NewConversations(db.DB).EnsureSchema(); err != nil {
		return err
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

	if err := store.NewProjects(db.DB).EnsureSchema(); err != nil {
		return err
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
	if err := store.NewConversations(db.DB).MigrateLateColumns(); err != nil {
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

	if err := store.NewFacts(db.DB).DropLegacyTables(); err != nil {
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
	if err := store.NewProjects(db.DB).MigrateLateColumns(); err != nil {
		db.logger.Warn("迁移RBAC资源归属字段失败", zap.Error(err))
	}

	if err := store.NewConversations(db.DB).EnsureIndexes(); err != nil {
		return err
	}
	// projects 的两条索引与 conversations 同一个位置跑：原全局建索引那一步就在补列之后。
	if err := store.NewProjects(db.DB).EnsureIndexes(); err != nil {
		return err
	}

	// model_token_usage 的建表、四个索引与历史回填都归 store.ModelTokenUsage，
	// 由进程启动时那一次 ensureModelTokenUsageSchema 跑（表要先于时间线写入路径存在）。
	db.logger.Debug("数据库表初始化完成")
	return nil
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
	database.checkpoint = startPassiveCheckpointLoop(sqlDB, logger, "knowledge")

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
		db.checkpoint.Stop()
		if db.DB != nil {
			db.closeErr = db.DB.Close()
		}
	})
	return db.closeErr
}
