package app

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/assets"
	"cyberstrike-ai/internal/audit"
	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/c2"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/database"
	"cyberstrike-ai/internal/einoobserve"
	"cyberstrike-ai/internal/handler"
	"cyberstrike-ai/internal/hitl"
	"cyberstrike-ai/internal/knowledge"
	"cyberstrike-ai/internal/logger"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/mcp/builtin"
	"cyberstrike-ai/internal/monitor"
	"cyberstrike-ai/internal/multiagent"
	"cyberstrike-ai/internal/plugin"
	"cyberstrike-ai/internal/robot"
	"cyberstrike-ai/internal/security"
	"cyberstrike-ai/internal/settings"
	"cyberstrike-ai/internal/skillpackage"
	"cyberstrike-ai/internal/storage"
	"cyberstrike-ai/internal/store"
	"cyberstrike-ai/internal/toolguard"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
)

// App 应用
type App struct {
	config             *config.Config
	logger             *logger.Logger
	router             *gin.Engine
	mcpServer          *mcp.Server
	externalMCPMgr     *mcp.ExternalMCPManager
	agent              *agent.Agent
	executor           *security.Executor
	db                 *database.DB
	knowledgeDB        *database.DB // 知识库数据库连接（如果使用独立数据库）
	auth               *security.AuthManager
	knowledgeManager   *knowledge.Manager        // 知识库管理器（用于动态初始化）
	knowledgeRetriever *knowledge.Retriever      // 知识库检索器（用于动态初始化）
	knowledgeIndexer   *knowledge.Indexer        // 知识库索引器（用于动态初始化）
	knowledgeHandler   *handler.KnowledgeHandler // 知识库处理器（用于动态初始化）
	agentHandler       *handler.AgentHandler     // Agent处理器（用于更新知识库管理器）
	robotHandler       *handler.RobotHandler     // 机器人处理器（钉钉/飞书/企业微信等）
	robotMu            sync.Mutex                // 保护机器人长连接的 cancel
	dingCancel         context.CancelFunc        // 钉钉 Stream 取消函数，用于配置变更时重启
	larkCancel         context.CancelFunc        // 飞书长连接取消函数，用于配置变更时重启
	wechatCancel       context.CancelFunc        // 微信 iLink 长轮询取消函数
	telegramCancel     context.CancelFunc        // Telegram 长轮询取消函数
	slackCancel        context.CancelFunc        // Slack Socket Mode 取消函数
	discordCancel      context.CancelFunc        // Discord Gateway 取消函数
	qqCancel           context.CancelFunc        // QQ WebSocket 取消函数
	alertCancel        context.CancelFunc        // 漏洞提醒持久化投递 worker
	c2Manager          *c2.Manager               // C2 管理器（未启用 C2 时为 nil）
	c2Watchdog         *c2.SessionWatchdog       // C2 会话看门狗
	c2WatchdogCancel   context.CancelFunc        // 看门狗取消函数
	c2Handler          *handler.C2Handler        // C2 REST（与 Manager 生命周期同步）
	storageHandler     *handler.StorageHandler   // 运行空间占用统计与垃圾清理
	auditSvc           *audit.Service
}

// New 创建新应用
func New(cfg *config.Config, log *logger.Logger, configPath string) (*App, error) {
	toolGuard, err := toolguard.NewManager(cfg.EffectiveToolGuard())
	if err != nil {
		return nil, fmt.Errorf("初始化调用拦截规则: %w", err)
	}
	if err := multiagent.InitADK(); err != nil {
		return nil, fmt.Errorf("初始化 Eino ADK: %w", err)
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	// CORS中间件
	router.Use(corsMiddleware(cfg.Server.CORSAllowedOrigins))

	// 初始化数据库
	dbPath := cfg.Database.Path
	if dbPath == "" {
		dbPath = "data/conversations.db"
	}

	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}

	db, err := database.NewDB(dbPath, log.Logger)
	if err != nil {
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}
	// Each of these tables is created by the store that owns it, and it happens here rather than
	// wherever the consumer is assembled: the audit service purges expired records while it is being
	// built, the skill page reads its counters as soon as the handlers exist. A store that creates its
	// schema after its first reader would log "no such table" on every fresh installation - which is
	// exactly what the live boot of the audit cut showed.
	if err := ensureAuditLogsSchema(db); err != nil {
		log.Logger.Warn("初始化 audit_logs 表失败", zap.Error(err))
	}
	if err := ensureSkillStatsSchema(db); err != nil {
		log.Logger.Warn("初始化 skill_stats 表失败", zap.Error(err))
	}
	// chat_upload_artifacts carries a foreign key onto conversations, which NewDB has just created.
	if err := ensureChatUploadArtifactSchema(db); err != nil {
		log.Logger.Warn("初始化 chat_upload_artifacts 表失败", zap.Error(err))
	}
	// knowledge_retrieval_logs has foreign keys onto conversations and messages, both just created.
	if err := ensureKnowledgeRetrievalSchema(db); err != nil {
		log.Logger.Warn("初始化 knowledge_retrieval_logs 表失败", zap.Error(err))
	}
	// The alert route exists before anything that will use it: the vulnerability MCP tools are
	// registered further down, while the listener - the robot handler's alert method - is built even
	// later, so the route is what lets both ends be wired in their natural order.
	vulnAlerts := &vulnerabilityAlertRoute{logger: log.Logger}
	// robot_user_bindings / robot_binding_codes have foreign keys onto rbac_users, created just above.
	if err := ensureRobotIdentitySchema(db); err != nil {
		log.Logger.Warn("初始化机器人绑定表失败", zap.Error(err))
	}
	// robot_user_sessions has a foreign key onto conversations, created just above.
	if err := ensureRobotSessionSchema(db); err != nil {
		log.Logger.Warn("初始化 robot_user_sessions 表失败", zap.Error(err))
	}
	// model_token_usage has foreign keys onto process_details/messages/conversations/projects, all
	// just created; its history carry-over reads the timeline, so it runs right after the table.
	if err := ensureModelTokenUsageSchema(db); err != nil {
		log.Logger.Warn("初始化 model_token_usage 表失败", zap.Error(err))
	}

	// 认证管理器（数据库初始化后挂载 RBAC）
	authManager := security.NewAuthManager(cfg.Auth.SessionDurationHours)
	if generatedPassword, err := authManager.AttachRBACStore(db); err != nil {
		return nil, fmt.Errorf("初始化RBAC失败: %w", err)
	} else if generatedPassword != "" {
		config.PrintBootstrapAdminPassword(generatedPassword)
	}
	for platform, userID := range cfg.Robots.ServiceAccountUserIDs() {
		user, userErr := db.GetRBACUserByID(userID)
		if userErr != nil || !user.Enabled {
			return nil, fmt.Errorf("robots.%s.auth.service_user_id 必须指向已启用的 RBAC 用户", platform)
		}
	}

	auditSvc := audit.NewService(db, cfg, log.Logger)
	audit.RegisterConversationCreateHook(auditSvc)
	auditSvc.PurgeExpired()
	audit.StartRetentionLoop(auditSvc, log.Logger)
	if err := db.PurgeWorkflowPackageLifecycle(time.Now().UTC()); err != nil {
		log.Logger.Warn("清理过期工作流包记录失败", zap.Error(err))
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := db.PurgeWorkflowPackageLifecycle(time.Now().UTC()); err != nil {
				log.Logger.Warn("清理过期工作流包记录失败", zap.Error(err))
			}
		}
	}()

	monitorRetention := monitor.NewService(db, cfg, log.Logger)
	monitorRetention.PurgeExpired()
	monitor.StartRetentionLoop(monitorRetention, log.Logger)

	if err := handler.NewHITLManager(db, log.Logger).EnsureSchema(); err != nil {
		log.Logger.Warn("初始化 HITL 表失败", zap.Error(err))
	}
	handler.NewHITLManager(db, log.Logger).SetGlobalWhitelist(cfg.Hitl.ToolWhitelist)
	hitlRetention := hitl.NewService(store.NewHITL(db.DB), cfg, log.Logger)
	hitlRetention.PurgeExpired()
	hitl.StartRetentionLoop(hitlRetention, log.Logger)

	if _, err := InstallCapabilityRegistry(cfg.Security.Tools, log.Logger); err != nil {
		return nil, fmt.Errorf("装配能力策略注册表失败: %w", err)
	}

	// 撤销清单在启动时装载并把命中的能力隔离出注册表；此后每次调用还会再查一次，
	// 所以一台从未再连过 registry 的机器也不会继续执行已被撤销的制品。
	if err := LoadRevocations(cfg.Artifacts.RevocationsPath, log.Logger); err != nil {
		return nil, fmt.Errorf("装载能力撤销清单失败: %w", err)
	}
	if isolated := IsolateRevoked(); len(isolated) > 0 {
		log.Logger.Warn("已撤销的能力被隔离出注册表", zap.Strings("capabilities", isolated))
	}

	// 插件主机进程按空闲时间回收；未启用配置段时这里返回 nil，
	// 声明了插件运行时的能力会在执行处 fail-closed。
	startPluginHostReaper(context.Background(), buildPluginHost(cfg, configPath, log.Logger), log.Logger)

	// 创建MCP服务器（带数据库持久化）
	mcpServer := mcp.NewServerWithStorage(log.Logger, db)
	mcpServer.SetToolAuthorizer(mcpToolAuthorizer(db))
	mcpServer.SetToolGuard(toolGuard)
	mcpServer.ConfigureHTTPToolCallTimeoutFromAgentMinutes(cfg.Agent.ToolTimeoutMinutes)
	mcpServer.ConfigureToolWaitTimeoutSeconds(cfg.Agent.ToolWaitTimeoutSeconds)
	mcpServer.ConfigureToolResultMaxBytes(cfg.MultiAgent.EinoMiddleware.ReductionMaxLengthForTruncEffective())
	mcpServer.ConfigureToolResultSpillRoot(cfg.MultiAgent.EinoMiddleware.ReductionRootDir)

	// 创建安全工具执行器
	executor := security.NewExecutor(&cfg.Security, mcpServer, log.Logger)
	executor.SetShellNoOutputTimeoutSeconds(cfg.Agent.ShellNoOutputTimeoutSeconds)
	executor.SetToolOutputMaxBytes(cfg.MultiAgent.EinoMiddleware.ReductionMaxLengthForTruncEffective())
	executor.SetToolOutputSpillRoot(cfg.MultiAgent.EinoMiddleware.ReductionRootDir)

	// 注册工具
	executor.RegisterTools(mcpServer)

	// 注册漏洞记录工具
	registerVulnerabilityTools(mcpServer, db, log.Logger, vulnAlerts)
	registerAssetTools(mcpServer, db, log.Logger)
	registerProjectFactTools(mcpServer, db, cfg, log.Logger)
	registerVisionTools(mcpServer, cfg, log.Logger)

	// 创建外部MCP管理器（使用与内部MCP服务器相同的存储）
	externalMCPMgr := mcp.NewExternalMCPManagerWithStorage(log.Logger, db)
	externalMCPMgr.SetToolAuthorizer(externalMCPToolAuthorizer())
	// Each server's real tool list becomes a capability identity in LayerRemote, so a rule, an
	// approval prompt or an audit row can name one remote tool instead of "external MCP".
	externalMCPMgr.SetToolInventoryObserver(remoteToolInventoryChanged(log.Logger))
	externalMCPMgr.SetToolGuard(toolGuard)
	externalMCPMgr.ConfigureToolWaitTimeoutSeconds(cfg.Agent.ToolWaitTimeoutSeconds)
	externalMCPMgr.ConfigureToolResultMaxBytes(cfg.MultiAgent.EinoMiddleware.ReductionMaxLengthForTruncEffective())
	externalMCPMgr.ConfigureToolResultSpillRoot(cfg.MultiAgent.EinoMiddleware.ReductionRootDir)
	externalMCPMgr.ConfigureResilience(mcp.ExternalMCPResilienceConfig{
		MaxConcurrentPerServer:  cfg.Agent.ExternalMCPMaxConcurrentPerServer,
		MaxConcurrentTotal:      cfg.Agent.ExternalMCPMaxConcurrentTotal,
		CircuitFailureThreshold: cfg.Agent.ExternalMCPCircuitFailureThreshold,
		CircuitCooldown:         time.Duration(cfg.Agent.ExternalMCPCircuitCooldownSeconds) * time.Second,
	})
	mcp.RegisterExecutionControlTools(mcpServer, externalMCPMgr)
	if cfg.ExternalMCP.Servers != nil {
		externalMCPMgr.LoadConfigs(&cfg.ExternalMCP)
		// 启动所有启用的外部MCP客户端
		externalMCPMgr.StartAllEnabled()
	}

	execReconciler := monitor.NewExecutionReconciler(db, mcpServer, externalMCPMgr, log.Logger)
	execReconciler.ReconcileOnStartup()
	monitor.StartStaleRunningReconcileLoop(execReconciler, log.Logger)

	// 创建Agent
	maxIterations := cfg.Agent.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 30 // 默认值
	}
	agent := agent.NewAgent(&cfg.OpenAI, &cfg.Agent, mcpServer, externalMCPMgr, log.Logger, maxIterations)
	agent.UpdateToolDescriptionMode(cfg.Security.ToolDescriptionMode)

	// 初始化知识库模块（如果启用）
	var knowledgeManager *knowledge.Manager
	var knowledgeRetriever *knowledge.Retriever
	var knowledgeIndexer *knowledge.Indexer
	var knowledgeHandler *handler.KnowledgeHandler

	var knowledgeDBConn *database.DB
	log.Logger.Debug("检查知识库配置", zap.Bool("enabled", cfg.Knowledge.Enabled))
	if cfg.Knowledge.Enabled {
		// 确定知识库数据库路径
		knowledgeDBPath := cfg.Database.KnowledgeDBPath
		var knowledgeDB *sql.DB

		if knowledgeDBPath != "" {
			// 使用独立的知识库数据库
			// 确保目录存在
			if err := os.MkdirAll(filepath.Dir(knowledgeDBPath), 0755); err != nil {
				return nil, fmt.Errorf("创建知识库数据库目录失败: %w", err)
			}

			var err error
			knowledgeDBConn, err = database.NewKnowledgeDB(knowledgeDBPath, log.Logger)
			if err != nil {
				return nil, fmt.Errorf("初始化知识库数据库失败: %w", err)
			}
			knowledgeDB = knowledgeDBConn.DB
			log.Logger.Info("使用独立的知识库数据库", zap.String("path", knowledgeDBPath))
		} else {
			// 向后兼容：使用会话数据库
			knowledgeDB = db.DB
			log.Logger.Info("使用会话数据库存储知识库数据（建议配置knowledge_db_path以分离数据）")
		}

		// 创建知识库管理器
		knowledgeManager = knowledge.NewManager(knowledgeDB, cfg.Knowledge.BasePath, log.Logger)

		// 创建嵌入器
		// 使用OpenAI配置的API Key（如果知识库配置中没有指定）
		if cfg.Knowledge.Embedding.APIKey == "" {
			cfg.Knowledge.Embedding.APIKey = cfg.OpenAI.APIKey
		}
		if cfg.Knowledge.Embedding.BaseURL == "" {
			cfg.Knowledge.Embedding.BaseURL = cfg.OpenAI.BaseURL
		}

		embedder, err := knowledge.NewEmbedder(context.Background(), &cfg.Knowledge, &cfg.OpenAI, log.Logger)
		if err != nil {
			return nil, fmt.Errorf("初始化知识库嵌入器失败: %w", err)
		}

		// 创建检索器（Eino MultiQuery + 重排流水线）
		retrievalConfig := knowledge.RetrievalConfigFromYAML(cfg.Knowledge.Retrieval)
		knowledgeRetriever = knowledge.NewRetriever(knowledgeDB, embedder, retrievalConfig, log.Logger)
		if err := knowledge.WireRetrieverPipeline(context.Background(), knowledgeRetriever, &cfg.OpenAI); err != nil {
			return nil, fmt.Errorf("初始化知识库检索流水线失败: %w", err)
		}

		// 创建索引器（Eino Compose 链）
		knowledgeIndexer, err = knowledge.NewIndexer(context.Background(), knowledgeDB, embedder, log.Logger, &cfg.Knowledge)
		if err != nil {
			return nil, fmt.Errorf("初始化知识库索引器失败: %w", err)
		}

		// 注册知识检索工具到MCP服务器
		knowledge.RegisterKnowledgeTool(mcpServer, knowledgeRetriever, knowledgeManager, log.Logger)

		// 创建知识库API处理器
		knowledgeHandler = handler.NewKnowledgeHandler(knowledgeManager, knowledgeRetriever, knowledgeIndexer, log.Logger)
		bindAudit(knowledgeHandler, auditSvc)
		log.Logger.Info("知识库模块初始化完成", zap.Bool("handler_created", knowledgeHandler != nil))

		// 扫描知识库并建立索引（异步）
		go func() {
			itemsToIndex, err := knowledgeManager.ScanKnowledgeBase()
			if err != nil {
				log.Logger.Warn("扫描知识库失败", zap.Error(err))
				return
			}

			// 检查是否已有索引
			hasIndex, err := knowledgeIndexer.HasIndex()
			if err != nil {
				log.Logger.Warn("检查索引状态失败", zap.Error(err))
				return
			}

			if hasIndex {
				// 如果已有索引，只索引新添加或更新的项
				if len(itemsToIndex) > 0 {
					log.Logger.Info("检测到已有知识库索引，开始增量索引", zap.Int("count", len(itemsToIndex)))
					ctx := context.Background()
					consecutiveFailures := 0
					var firstFailureItemID string
					var firstFailureError error
					failedCount := 0

					for _, itemID := range itemsToIndex {
						if err := knowledgeIndexer.IndexItem(ctx, itemID); err != nil {
							failedCount++
							consecutiveFailures++

							if consecutiveFailures == 1 {
								firstFailureItemID = itemID
								firstFailureError = err
								log.Logger.Warn("索引知识项失败", zap.String("itemId", itemID), zap.Error(err))
							}

							// 如果连续失败2次，立即停止增量索引
							if consecutiveFailures >= 2 {
								log.Logger.Error("连续索引失败次数过多，立即停止增量索引",
									zap.Int("consecutiveFailures", consecutiveFailures),
									zap.Int("totalItems", len(itemsToIndex)),
									zap.String("firstFailureItemId", firstFailureItemID),
									zap.Error(firstFailureError),
								)
								break
							}
							continue
						}

						// 成功时重置连续失败计数
						if consecutiveFailures > 0 {
							consecutiveFailures = 0
							firstFailureItemID = ""
							firstFailureError = nil
						}
					}
					log.Logger.Info("增量索引完成", zap.Int("totalItems", len(itemsToIndex)), zap.Int("failedCount", failedCount))
				} else {
					log.Logger.Info("检测到已有知识库索引，没有需要索引的新项或更新项")
				}
				return
			}

			// 冷启动：仅为尚无向量的知识项构建索引（与 IndexMissing 语义一致）
			log.Logger.Info("未检测到知识库索引，开始自动构建索引")
			ctx := context.Background()
			if err := knowledgeIndexer.IndexMissing(ctx); err != nil {
				log.Logger.Warn("自动构建知识库索引失败", zap.Error(err))
			}
		}()
	}

	// 配置文件路径必须由入口传入（与 flag -config 一致）。勿再用 os.Args[1]，否则 ./cyberstrike-ai --https 会把 --https 当成路径。
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		configPath = "config.yaml"
	}

	skillsDir := skillpackage.SkillsRootFromConfig(cfg.SkillsDir, configPath)
	log.Logger.Debug("Skills 目录（Eino ADK skill 中间件 + Web 管理 API）", zap.String("skillsDir", skillsDir))
	configDir := filepath.Dir(configPath)
	plantaskRel := strings.TrimSpace(cfg.MultiAgent.EinoMiddleware.PlantaskRelDir)
	if plantaskRel == "" {
		plantaskRel = ".eino/plantask"
	}
	plantaskBase := filepath.Join(skillsDir, plantaskRel)
	// Match eino_adk_run_loop: checkpoint_dir is used as configured (relative to process CWD when not absolute).
	checkpointBase := strings.TrimSpace(cfg.MultiAgent.EinoMiddleware.CheckpointDir)
	reductionRoot := strings.TrimSpace(cfg.MultiAgent.EinoMiddleware.ReductionRootDir)
	workspaceRoot := strings.TrimSpace(cfg.Agent.WorkspaceRootDir)
	db.SetEinoConversationDirs(plantaskBase, checkpointBase, reductionRoot, workspaceRoot)

	// 运行空间垃圾清理：根目录一律复用上面已解析好的同一批值，
	// 避免在 storage 包内重新推导导致「清理的目录」与「实际写入的目录」不一致。
	workspaceRootDir := strings.TrimSpace(workspaceRoot)
	if workspaceRootDir == "" {
		workspaceRootDir = filepath.Join("tmp", "workspace")
	}
	reductionRootDir := strings.TrimSpace(reductionRoot)
	if reductionRootDir == "" {
		reductionRootDir = filepath.Join("tmp", "reduction")
	}
	diagnosticLogDir := strings.TrimSpace(cfg.Log.DiagnosticDir)
	if diagnosticLogDir == "" {
		diagnosticLogDir = "log"
	}
	// chat_uploads 与 tmp/c2 目前均为相对进程工作目录的固定路径
	// （见 handler.chatUploadsRootDirName 与 app/c2_lifecycle.go 的 c2.NewManager）。
	chatUploadsRoot := "chat_uploads"
	c2Root := filepath.Join("tmp", "c2")
	// 让 DeleteConversation 一并删除上传附件：其 chat_upload_artifacts 行已由
	// ON DELETE CASCADE 清除，此前磁盘文件会永久残留。
	db.SetChatUploadsDir(chatUploadsRoot)
	storageCleaner := storage.NewCleaner(storage.Options{
		Config: cfg,
		Paths: storage.Paths{
			Workspace:            workspaceRootDir,
			Reduction:            reductionRootDir,
			ConversationArtifact: db.ConversationArtifactsBaseDir(),
			Plantask:             plantaskBase,
			C2:                   c2Root,
			ChatUploads:          chatUploadsRoot,
			WorkflowCheckpoints:  filepath.Join(filepath.Dir(dbPath), "workflow-checkpoints"),
			DiagnosticLogs:       diagnosticLogDir,
		},
		Activity: db,
		Logger:   log.Logger,
	})
	storageService := storage.NewService(storageCleaner, cfg, log.Logger)
	storage.StartRetentionLoop(storageService, log.Logger)
	storageHandler := handler.NewStorageHandler(storageCleaner, cfg, log.Logger)
	bindAudit(storageHandler, auditSvc)

	agent.SetPromptBaseDir(configDir)

	agentsDir := cfg.AgentsDir
	if agentsDir == "" {
		agentsDir = "agents"
	}
	if !filepath.IsAbs(agentsDir) {
		agentsDir = filepath.Join(configDir, agentsDir)
	}
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		log.Logger.Warn("创建 agents 目录失败", zap.String("path", agentsDir), zap.Error(err))
	}
	markdownAgentsHandler := handler.NewMarkdownAgentsHandler(agentsDir, log.Logger)
	bindAudit(markdownAgentsHandler, auditSvc)
	log.Logger.Debug("多代理 Markdown 子 Agent 目录", zap.String("agentsDir", agentsDir))

	// 创建处理器
	// One snapshot store serves every handler that reads or rewrites configuration
	// at runtime. This is what replaces the shared *config.Config pointer that was
	// written from several handlers under different locks (S4).
	settingsStore := settings.New(cfg)

	// The capability table is where installed things live: built-in roles, agents, skills,
	// tool recipes, plus anything a bundle plugs in later. The HTTP layer resolves its
	// configuration through the store installed here, so publishing a snapshot is what makes
	// a plug-in take effect - no restart, and no reader ever sees a half-updated map.
	pluginTable := plugin.NewTable()
	handler.InstallSettingsStore(settingsStore)
	// Publish the table globally only once it actually holds the shipped capabilities: the skill
	// middleware prefers the table over its single-directory backend, so installing an empty one
	// after a failed scan would take skills away from every run instead of leaving the old
	// behaviour in place.
	if err := scanBuiltInCapabilities(pluginTable, cfg, configPath, log.Logger); err != nil {
		log.Logger.Error("内置能力扫描失败，能力表不启用（运行路径退回单目录行为）", zap.Error(err))
	} else {
		plugin.Install(pluginTable)
	}

	agentHandler := handler.NewAgentHandler(agent, db, cfg, log.Logger)
	agentHandler.SetSettings(settingsStore)
	bindAudit(agentHandler, auditSvc)
	agentHandler.SetAgentsMarkdownDir(agentsDir)
	// 如果知识库已启用，设置知识库管理器到AgentHandler以便记录检索日志
	if knowledgeManager != nil {
		agentHandler.SetKnowledgeManager(knowledgeManager)
	}
	monitorHandler := handler.NewMonitorHandler(mcpServer, executor, db, log.Logger)
	bindAudit(monitorHandler, auditSvc)
	monitorHandler.SetMonitorRetention(monitorRetention)
	monitorHandler.SetExternalMCPManager(externalMCPMgr) // 设置外部MCP管理器，以便获取外部MCP执行记录
	monitorHandler.SetTaskManager(agentHandler.TaskManager())
	monitorHandler.SetAgentHandler(agentHandler)
	notificationHandler := handler.NewNotificationHandler(db, agentHandler, log.Logger)
	authHandler := handler.NewAuthHandler(authManager, cfg, configPath, log.Logger)
	bindAudit(authHandler, auditSvc)
	attackChainHandler := handler.NewAttackChainHandler(db, &cfg.OpenAI, log.Logger)
	vulnerabilityHandler := handler.NewVulnerabilityHandler(db, log.Logger, vulnAlerts)
	assetHandler := handler.NewAssetHandler(db, log.Logger)
	projectHandler := handler.NewProjectHandler(db, log.Logger)
	rbacHandler := handler.NewRBACHandler(db, log.Logger)
	bindAudit(rbacHandler, auditSvc)
	rbacHandler.SetAuthManager(authManager)
	workflowHandler := handler.NewWorkflowHandler(db, log.Logger)
	bindAudit(workflowHandler, auditSvc)
	workflowHandler.SetRuntime(agent, cfg)
	bindAudit(vulnerabilityHandler, auditSvc)
	webshellHandler := handler.NewWebShellHandler(log.Logger, db)
	bindAudit(webshellHandler, auditSvc)
	chatUploadsHandler := handler.NewChatUploadsHandler(log.Logger, db)
	bindAudit(chatUploadsHandler, auditSvc)
	registerWebshellTools(mcpServer, db, webshellHandler, log.Logger)
	registerWebshellManagementTools(mcpServer, db, webshellHandler, log.Logger)
	configHandler := handler.NewConfigHandler(configPath, cfg, mcpServer, executor, agent, attackChainHandler, externalMCPMgr, log.Logger)
	configHandler.SetSettings(settingsStore)
	configHandler.SetDB(db)
	configHandler.SetToolGuard(toolGuard)
	bindAudit(configHandler, auditSvc)
	agentHandler.SetHitlConfigSaver(configHandler)
	externalMCPHandler := handler.NewExternalMCPHandler(externalMCPMgr, cfg, configPath, log.Logger)
	bindAudit(externalMCPHandler, auditSvc)
	roleHandler := handler.NewRoleHandler(cfg, configPath, log.Logger, pluginTable)
	bindAudit(roleHandler, auditSvc)
	// Publish the role catalog before anything can serve: built-in files plus any bundle
	// already on disk go through the table, so the first request sees the same answer as the
	// last one after a plug-in.
	if published, err := roleHandler.Reload(); err != nil {
		log.Logger.Warn("角色目录加载失败", zap.Error(err))
	} else {
		log.Logger.Info("角色目录已发布", zap.Int("roles", published))
	}
	// Boot order is the whole argument for doing this here: the shipped capabilities have to be
	// in the table *before* the packs on disk are re-installed, so a pack that shadows a shipped
	// role or skill is refused exactly as the install endpoint refuses it. Installing packs first
	// would let the pack win the identity and the built-in scan would then refuse to put the
	// shipped capability back.
	//
	// Without this step an installed pack lasts until the next restart, which is not what
	// "install" means to whoever clicked it: every run path reads the capability table, and the
	// table is rebuilt from disk at start-up.
	if installed, refused := installBundlesFromDisk(pluginTable, filepath.Join(configDir, "bundles"), log.Logger); installed > 0 || len(refused) > 0 {
		// Republish so the packs' roles reach the served catalog in this same boot, rather than
		// waiting for somebody to install or unplug something.
		if published, err := roleHandler.Reload(); err != nil {
			log.Logger.Warn("能力包装入后刷新角色目录失败", zap.Error(err))
		} else {
			log.Logger.Info("角色目录已按能力表重新发布", zap.Int("roles", published), zap.Int("bundles", installed))
		}
	}
	// The console's unit switch has to survive the restart as well. A bundle's files cannot be
	// edited (the installed content must keep matching the digest taken at install time), so the
	// switch had nowhere durable to go: every unit a pack owns came back enabled and a role
	// somebody switched off was serving again. Only saved "off" is applied here, so a stale row can
	// hide a capability but never widen what may execute.
	unitSwitches := store.NewCapabilitySwitches(db.DB)
	if err := unitSwitches.EnsureSchema(); err != nil {
		log.Logger.Warn("初始化能力单元开关表失败，本次启动的开关不会持久化", zap.Error(err))
	}
	switchesApplied, switchNotes := applyPersistedSwitches(pluginTable, unitSwitches, log.Logger)
	if len(switchNotes) > 0 {
		log.Logger.Warn("部分能力单元开关无法恢复", zap.Strings("notes", switchNotes))
	} else if switchesApplied > 0 {
		// The overlay changed table state, so the served catalog has to be rebuilt from it - a
		// switched-off role would otherwise stay in the snapshot until the next mutation.
		if published, err := roleHandler.Reload(); err != nil {
			log.Logger.Warn("按开关恢复后刷新角色目录失败", zap.Error(err))
		} else {
			log.Logger.Info("角色目录已按能力表重新发布", zap.Int("roles", published), zap.Int("switched_off", switchesApplied))
		}
	}
	// A pack that declares an MCP server has to re-declare it on every boot: the manager's own
	// configuration comes from config.yaml, which a pack does not edit. Declared, never started -
	// the same rule the install endpoint follows.
	if n, msg := provisionDeclaredServers(externalMCPMgr, pluginTable); n > 0 || msg != "" {
		log.Logger.Info("能力包声明的 MCP 服务器已写入管理器（默认停用）",
			zap.Int("declared", n), zap.String("note", msg))
	}
	// The same honesty applies to a pack's plugin binary: the host holds no domain until somebody
	// switches the unit on, so a manifest that says enabled must not leave the table claiming an
	// enabled unit whose capabilities nothing can call.
	if n, msg := declarePackPluginUnits(pluginTable); n > 0 || msg != "" {
		log.Logger.Info("能力包声明的插件单元按停用装入（不启动进程）",
			zap.Int("declared", n), zap.String("note", msg))
	}
	// The one-click extend surface. It is confined to <configDir>/bundles, and it drives the same
	// table the run paths read, so an install here is live on the next request. A pack that
	// contributes tool recipes goes through configHandler's tool-layer rebuild - the same sequence
	// POST /config/apply runs - so the recipe becomes executable without pressing "应用配置".
	// A nil manager has to stay a nil interface: passing the typed nil pointer would make every
	// declaration path call methods on a nil receiver.
	var mcpProvisioner handler.MCPProvisioner
	if externalMCPMgr != nil {
		mcpProvisioner = externalMCPMgr
	}
	pluginHandler := handler.NewPluginHandler(pluginTable, filepath.Join(configDir, "bundles"), roleHandler, configHandler.Tools, mcpProvisioner, newPackPluginProvisioner(log.Logger), unitSwitches, auditSvc, log.Logger)
	skillsHandler := handler.NewSkillsHandler(cfg, configPath, log.Logger)
	bindAudit(skillsHandler, auditSvc)
	fofaHandler := handler.NewFofaHandler(cfg, log.Logger)
	terminalHandler := handler.NewTerminalHandler(log.Logger)
	if db != nil {
		skillsHandler.SetDB(db) // 设置数据库连接以便获取调用统计
	}

	// ============================================================================
	// 初始化 C2 模块（可按配置关闭，节省本机部署资源）
	// ============================================================================
	c2Manager, c2Watchdog, watchdogCancel := setupC2Runtime(cfg, db, agentHandler, log.Logger)
	if c2Manager != nil {
		registerC2Tools(mcpServer, c2Manager, log.Logger, cfg.Server.Port)
	}
	c2Handler := handler.NewC2Handler(c2Manager, log.Logger)
	bindAudit(c2Handler, auditSvc)

	// 创建OpenAPI处理器
	conversationHandler := handler.NewConversationHandler(db, log.Logger)
	bindAudit(conversationHandler, auditSvc)
	conversationHandler.SetTaskStopper(agentHandler)
	conversationHandler.SetTaskStateProvider(agentHandler)
	auditHandler := handler.NewAuditHandler(db, auditSvc, log.Logger)
	robotHandler := handler.NewRobotHandler(cfg, db, agentHandler, log.Logger)
	bindAudit(robotHandler, auditSvc)
	vulnAlerts.attach(robotHandler.NotifyNewVulnerability)
	openAPIHandler := handler.NewOpenAPIHandler(db, log.Logger, conversationHandler, agentHandler)

	// 创建 App 实例（部分字段稍后填充）
	app := &App{
		config:             cfg,
		logger:             log,
		router:             router,
		mcpServer:          mcpServer,
		externalMCPMgr:     externalMCPMgr,
		agent:              agent,
		executor:           executor,
		db:                 db,
		knowledgeDB:        knowledgeDBConn,
		auth:               authManager,
		knowledgeManager:   knowledgeManager,
		knowledgeRetriever: knowledgeRetriever,
		knowledgeIndexer:   knowledgeIndexer,
		knowledgeHandler:   knowledgeHandler,
		agentHandler:       agentHandler,
		robotHandler:       robotHandler,
		c2Manager:          c2Manager,
		c2Watchdog:         c2Watchdog,
		c2WatchdogCancel:   watchdogCancel,
		c2Handler:          c2Handler,
		storageHandler:     storageHandler,
		auditSvc:           auditSvc,
	}
	// 飞书/钉钉长连接（无需公网），启用时在后台启动；后续前端应用配置时会通过 RestartRobotConnections 重启
	app.startRobotConnections()
	alertCtx, alertCancel := context.WithCancel(context.Background())
	app.alertCancel = alertCancel
	go robotHandler.RunVulnerabilityAlertWorker(alertCtx)

	// 设置漏洞工具注册器（内置工具，必须设置）
	vulnerabilityRegistrar := func() error {
		registerVulnerabilityTools(mcpServer, db, log.Logger, vulnAlerts)
		registerAssetTools(mcpServer, db, log.Logger)
		registerProjectFactTools(mcpServer, db, cfg, log.Logger)
		registerVisionTools(mcpServer, cfg, log.Logger)
		return nil
	}
	configHandler.Tools.SetVulnerabilityToolRegistrar(vulnerabilityRegistrar)
	configHandler.Tools.SetCapabilityRefresher(func(tools []config.ToolConfig) error {
		rejections, err := RefreshRecipeLayer(tools)
		for _, r := range rejections {
			log.Logger.Warn("能力配方被拒绝，调用将 fail-closed",
				zap.String("tool", r.ToolName), zap.String("reason", r.Reason))
		}
		return err
	})

	// 设置 WebShell 工具注册器（ApplyConfig 时重新注册）
	webshellRegistrar := func() error {
		registerWebshellTools(mcpServer, db, webshellHandler, log.Logger)
		registerWebshellManagementTools(mcpServer, db, webshellHandler, log.Logger)
		return nil
	}
	configHandler.Tools.SetWebshellToolRegistrar(webshellRegistrar)

	// Skills 由 Eino ADK skill 中间件提供（多代理）；此处不注册 MCP 形态的技能工具
	configHandler.Tools.SetSkillsToolRegistrar(func() error { return nil })

	handler.RegisterBatchTaskMCPTools(mcpServer, agentHandler, log.Logger)
	batchTaskToolRegistrar := func() error {
		handler.RegisterBatchTaskMCPTools(mcpServer, agentHandler, log.Logger)
		return nil
	}
	configHandler.Tools.SetBatchTaskToolRegistrar(batchTaskToolRegistrar)

	// 设置知识库初始化器（用于动态初始化，需要在 App 创建后设置）
	configHandler.SetKnowledgeInitializer(func() (*handler.KnowledgeHandler, error) {
		knowledgeHandler, err := initializeKnowledge(cfg, db, knowledgeDBConn, mcpServer, agentHandler, app, log.Logger)
		if err != nil {
			return nil, err
		}

		// 动态初始化后，设置知识库工具注册器和检索器更新器
		// 这样后续 ApplyConfig 时就能重新注册工具了
		if app.knowledgeRetriever != nil && app.knowledgeManager != nil {
			// 创建闭包，捕获knowledgeRetriever和knowledgeManager的引用
			registrar := func() error {
				knowledge.RegisterKnowledgeTool(mcpServer, app.knowledgeRetriever, app.knowledgeManager, log.Logger)
				return nil
			}
			configHandler.Tools.SetKnowledgeToolRegistrar(registrar)
			// 设置检索器更新器，以便在ApplyConfig时更新检索器配置
			configHandler.SetRetrieverUpdater(app.knowledgeRetriever)
			log.Logger.Info("动态初始化后已设置知识库工具注册器和检索器更新器")
		}

		return knowledgeHandler, nil
	})

	// 如果知识库已启用，设置知识库工具注册器和检索器更新器
	if cfg.Knowledge.Enabled && knowledgeRetriever != nil && knowledgeManager != nil {
		// 创建闭包，捕获knowledgeRetriever和knowledgeManager的引用
		registrar := func() error {
			knowledge.RegisterKnowledgeTool(mcpServer, knowledgeRetriever, knowledgeManager, log.Logger)
			return nil
		}
		configHandler.Tools.SetKnowledgeToolRegistrar(registrar)
		// 设置检索器更新器，以便在ApplyConfig时更新检索器配置
		configHandler.SetRetrieverUpdater(knowledgeRetriever)
	}

	// 设置机器人连接重启器，前端应用配置后无需重启服务即可使钉钉/飞书/微信新配置生效
	configHandler.SetRobotRestarter(app)

	wechatRobotHandler := handler.NewWechatRobotHandler(cfg, configHandler, log.Logger)

	configHandler.SetC2Runtime(app)
	configHandler.Tools.SetC2ToolRegistrar(func() error {
		if app.config.C2.EnabledEffective() && app.c2Manager != nil {
			registerC2Tools(mcpServer, app.c2Manager, log.Logger, app.config.Server.Port)
		}
		return nil
	})

	// The recipe list is read from the capability table, so a pack that ships one has to be
	// rebuilt into it before the first request. Without this the tool unit sits in the table while
	// the tool surface was built from tools_dir at config load, and the pack's recipe only appears
	// after somebody presses 应用配置 - which is "installed" reading as "in effect", the exact
	// thing this layer exists to prevent. It runs here, after every registrar is wired, because
	// rebuilding clears the MCP tool table and refills it.
	//
	// A restored unit switch is the other reason to rebuild: the table can hold a saved "off" for a
	// recipe that came from tools_dir, and the live tool surface was built from that file at config
	// load, so without a rebuild the tool stays registered behind a unit the console shows disabled.
	if bundled := bundleOwnedToolUnits(pluginTable); bundled > 0 || switchesApplied > 0 {
		if err := configHandler.Tools.Rebuild(); err != nil {
			log.Logger.Error("能力包的配方未能装入工具层，POST /config/apply 可重试",
				zap.Int("bundled_recipes", bundled), zap.Error(err))
		} else {
			log.Logger.Info("能力包的配方已随启动装入工具层", zap.Int("bundled_recipes", bundled))
		}
	}

	// The one-click update acts on this installation's own tree, rooted where the live config
	// file is. Its restart hook exits after a graceful shutdown, which is only "a restart" when
	// something supervises the process - so the endpoint reports whether it is standing down or
	// whether somebody has to start it again, instead of promising a boot that may not happen.
	updateHandler := handler.NewUpdateHandler(configDir, log.Logger, auditSvc, func() {
		app.Shutdown()
		os.Exit(0)
	})

	// 设置路由（使用 App 实例以便动态获取 handler）
	setupRoutes(routeDeps{
		app:                   app,
		router:                router,
		authHandler:           authHandler,
		agentHandler:          agentHandler,
		monitorHandler:        monitorHandler,
		notificationHandler:   notificationHandler,
		conversationHandler:   conversationHandler,
		robotHandler:          robotHandler,
		wechatRobotHandler:    wechatRobotHandler,
		configHandler:         configHandler,
		externalMCPHandler:    externalMCPHandler,
		attackChainHandler:    attackChainHandler,
		vulnerabilityHandler:  vulnerabilityHandler,
		assetHandler:          assetHandler,
		projectHandler:        projectHandler,
		workflowHandler:       workflowHandler,
		webshellHandler:       webshellHandler,
		chatUploadsHandler:    chatUploadsHandler,
		roleHandler:           roleHandler,
		pluginHandler:         pluginHandler,
		updateHandler:         updateHandler,
		skillsHandler:         skillsHandler,
		markdownAgentsHandler: markdownAgentsHandler,
		fofaHandler:           fofaHandler,
		terminalHandler:       terminalHandler,
		c2Handler:             app.c2Handler,
		auditHandler:          auditHandler,
		auditSvc:              auditSvc,
		rbacHandler:           rbacHandler,
		mcpServer:             mcpServer,
		authManager:           authManager,
		openAPIHandler:        openAPIHandler,
	})
	return app, nil

}

// mcpHandlerWithAuth 在鉴权通过后转发到 MCP 处理；若配置了 auth_header 则校验请求头，否则直接放行
func (a *App) mcpHandlerWithAuth(w http.ResponseWriter, r *http.Request) {
	cfg := a.config.MCP
	if authHeader := strings.TrimSpace(r.Header.Get("Authorization")); len(authHeader) > 7 && strings.EqualFold(authHeader[:7], "Bearer ") {
		if session, ok := a.auth.ValidateToken(strings.TrimSpace(authHeader[7:])); ok && session.Permissions["mcp:execute"] {
			principal := authctx.NewPrincipalWithScopes(session.UserID, session.Username, session.Scope, session.Permissions, session.PermissionScopes)
			a.mcpServer.HandleHTTP(w, r.WithContext(authctx.WithPrincipal(r.Context(), principal)))
			return
		}
	}
	if !cfg.AllowGlobalAccess || strings.TrimSpace(cfg.AuthHeader) == "" || strings.TrimSpace(cfg.AuthHeaderValue) == "" {
		http.Error(w, "use an authorized user bearer token; global MCP service access is disabled", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(cfg.AuthHeader)), []byte(cfg.AuthHeaderValue)) != 1 {
		a.logger.Logger.Debug("MCP 鉴权失败：header 缺失或值不匹配", zap.String("header", cfg.AuthHeader))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
		return
	}
	permissions := make(map[string]bool, len(security.PermissionCatalog))
	for permission := range security.PermissionCatalog {
		permissions[permission] = true
	}
	principal := authctx.NewPrincipal("service:mcp", "mcp-service", database.RBACScopeAll, permissions)
	r = r.WithContext(authctx.WithPrincipal(r.Context(), principal))
	a.mcpServer.HandleHTTP(w, r)
}

// Run 启动应用（向后兼容，不支持优雅关闭）
func (a *App) Run() error {
	return a.RunWithContext(context.Background())
}

// RunWithContext 启动应用，支持通过 context 取消来优雅关闭
func (a *App) RunWithContext(ctx context.Context) error {
	// 启动MCP服务器（如果启用）
	var mcpServer *http.Server
	if a.config.MCP.Enabled {
		mcpAddr := fmt.Sprintf("%s:%d", a.config.MCP.Host, a.config.MCP.Port)
		a.logger.Info("启动MCP服务器", zap.String("address", mcpAddr))

		mux := http.NewServeMux()
		mux.HandleFunc("/mcp", a.mcpHandlerWithAuth)

		mcpServer = &http.Server{Addr: mcpAddr, Handler: mux}
		go func() {
			if err := mcpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				a.logger.Error("MCP服务器启动失败", zap.Error(err))
			}
		}()
	}

	// 启动主服务器（可选 HTTPS + HTTP/2，见 config server.tls_*）
	addr := fmt.Sprintf("%s:%d", a.config.Server.Host, a.config.Server.Port)
	tlsMode, tlsConf, certFile, keyFile, tlsErr := prepareMainServerTLS(&a.config.Server)
	if tlsErr != nil {
		return tlsErr
	}

	srv := &http.Server{Addr: addr, Handler: a.router}
	var mainMux *mainServerMux
	httpRedirect := config.ServerHTTPRedirectEnabled(&a.config.Server)
	if tlsMode != mainTLSOff {
		srv.TLSConfig = tlsConf
		if err := http2.ConfigureServer(srv, &http2.Server{}); err != nil {
			return fmt.Errorf("主服务 HTTP/2 配置失败: %w", err)
		}
		switch tlsMode {
		case mainTLSFromFiles:
			a.logger.Debug("启动 HTTPS 主服务（已启用 HTTP/2 协商）",
				zap.String("address", addr),
				zap.String("cert", certFile),
			)
		case mainTLSInMemorySelfSigned:
			a.logger.Debug("启动 HTTPS 主服务（内存自签证书，仅测试；已启用 HTTP/2 协商）",
				zap.String("address", addr),
			)
		}
		if httpRedirect {
			a.logger.Debug("已启用 HTTP→HTTPS 自动跳转（同端口嗅探分流）", zap.String("address", addr))
		}
	} else {
		a.logger.Debug("启动 HTTP 主服务", zap.String("address", addr))
	}

	// 监听 context 取消，优雅关闭 HTTP 服务器
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if mainMux != nil {
			if err := mainMux.Shutdown(shutdownCtx); err != nil {
				a.logger.Error("HTTP/HTTPS 分流服务器关闭失败", zap.Error(err))
			}
		} else if err := srv.Shutdown(shutdownCtx); err != nil {
			a.logger.Error("HTTP服务器关闭失败", zap.Error(err))
		}
		if mcpServer != nil {
			if err := mcpServer.Shutdown(shutdownCtx); err != nil {
				a.logger.Error("MCP服务器关闭失败", zap.Error(err))
			}
		}
	}()

	var err error
	switch {
	case tlsMode != mainTLSOff && httpRedirect:
		var tlsConfReady *tls.Config
		tlsConfReady, err = ensureMainTLSConfigCerts(tlsMode, tlsConf, certFile, keyFile)
		if err != nil {
			return fmt.Errorf("加载 TLS 证书: %w", err)
		}
		srv.TLSConfig = tlsConfReady
		var ln net.Listener
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		mainMux = newMainServerMux(ln, srv, portFromListenAddr(addr), a.logger.Logger)
		err = mainMux.Serve()
	case tlsMode == mainTLSOff:
		err = srv.ListenAndServe()
	case tlsMode == mainTLSFromFiles:
		err = srv.ListenAndServeTLS(certFile, keyFile)
	case tlsMode == mainTLSInMemorySelfSigned:
		var ln net.Listener
		ln, err = tls.Listen("tcp", addr, srv.TLSConfig)
		if err == nil {
			err = srv.Serve(ln)
		}
	default:
		err = srv.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown 关闭应用
func (a *App) Shutdown() {
	if a.agentHandler != nil {
		a.agentHandler.ShutdownTasks()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = einoobserve.ShutdownOtel(shutdownCtx)
	shutdownCancel()
	if a.alertCancel != nil {
		a.alertCancel()
		a.alertCancel = nil
	}

	// 停止钉钉/飞书长连接
	a.robotMu.Lock()
	if a.dingCancel != nil {
		a.dingCancel()
		a.dingCancel = nil
	}
	if a.larkCancel != nil {
		a.larkCancel()
		a.larkCancel = nil
	}
	a.robotMu.Unlock()

	a.shutdownC2()

	// 停止所有外部MCP客户端
	if a.externalMCPMgr != nil {
		a.externalMCPMgr.StopAll()
	}

	// 关闭知识库数据库连接（如果使用独立数据库）
	if a.knowledgeDB != nil {
		if err := a.knowledgeDB.Close(); err != nil {
			a.logger.Logger.Warn("关闭知识库数据库连接失败", zap.Error(err))
		}
	}

	// 关闭主数据库连接
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			a.logger.Logger.Warn("关闭主数据库连接失败", zap.Error(err))
		}
	}
}

// startRobotConnections 根据当前配置启动钉钉/飞书长连接（不先关闭已有连接，仅用于首次启动）
func (a *App) startRobotConnections() {
	a.robotMu.Lock()
	defer a.robotMu.Unlock()
	cfg := a.config
	if cfg.Robots.Lark.Enabled && cfg.Robots.Lark.AppID != "" && cfg.Robots.Lark.AppSecret != "" {
		ctx, cancel := context.WithCancel(context.Background())
		a.larkCancel = cancel
		go robot.StartLark(ctx, cfg.Robots, a.robotHandler, a.logger.Logger)
	}
	if cfg.Robots.Dingtalk.Enabled && cfg.Robots.Dingtalk.ClientID != "" && cfg.Robots.Dingtalk.ClientSecret != "" {
		ctx, cancel := context.WithCancel(context.Background())
		a.dingCancel = cancel
		go robot.StartDing(ctx, cfg.Robots, a.robotHandler, a.logger.Logger)
	}
	if cfg.Robots.Wechat.Enabled && cfg.Robots.Wechat.BotToken != "" {
		ctx, cancel := context.WithCancel(context.Background())
		a.wechatCancel = cancel
		go robot.StartWechat(ctx, cfg.Robots, a.robotHandler, cfg.Version, a.logger.Logger)
	}
	if cfg.Robots.Telegram.Enabled && strings.TrimSpace(cfg.Robots.Telegram.BotToken) != "" {
		ctx, cancel := context.WithCancel(context.Background())
		a.telegramCancel = cancel
		go robot.StartTelegram(ctx, cfg.Robots, a.robotHandler, a.logger.Logger)
	}
	if cfg.Robots.Slack.Enabled && strings.TrimSpace(cfg.Robots.Slack.BotToken) != "" && strings.TrimSpace(cfg.Robots.Slack.AppToken) != "" {
		ctx, cancel := context.WithCancel(context.Background())
		a.slackCancel = cancel
		go robot.StartSlack(ctx, cfg.Robots, a.robotHandler, a.logger.Logger)
	}
	if cfg.Robots.Discord.Enabled && strings.TrimSpace(cfg.Robots.Discord.BotToken) != "" {
		ctx, cancel := context.WithCancel(context.Background())
		a.discordCancel = cancel
		go robot.StartDiscord(ctx, cfg.Robots, a.robotHandler, a.logger.Logger)
	}
	if cfg.Robots.QQ.Enabled && strings.TrimSpace(cfg.Robots.QQ.AppID) != "" && strings.TrimSpace(cfg.Robots.QQ.ClientSecret) != "" {
		ctx, cancel := context.WithCancel(context.Background())
		a.qqCancel = cancel
		go robot.StartQQ(ctx, cfg.Robots, a.robotHandler, a.logger.Logger)
	}
}

// RestartRobotConnections 重启钉钉/飞书/微信长连接，使前端应用配置后立即生效（实现 handler.RobotRestarter）
func (a *App) RestartRobotConnections() {
	a.robotMu.Lock()
	if a.dingCancel != nil {
		a.dingCancel()
		a.dingCancel = nil
	}
	if a.larkCancel != nil {
		a.larkCancel()
		a.larkCancel = nil
	}
	if a.wechatCancel != nil {
		a.wechatCancel()
		a.wechatCancel = nil
	}
	if a.telegramCancel != nil {
		a.telegramCancel()
		a.telegramCancel = nil
	}
	if a.slackCancel != nil {
		a.slackCancel()
		a.slackCancel = nil
	}
	if a.discordCancel != nil {
		a.discordCancel()
		a.discordCancel = nil
	}
	if a.qqCancel != nil {
		a.qqCancel()
		a.qqCancel = nil
	}
	a.robotMu.Unlock()
	// 给旧 goroutine 一点时间退出
	time.Sleep(200 * time.Millisecond)
	a.startRobotConnections()
}

// setupRoutes 设置路由
// routeDeps is everything the HTTP wiring needs, assembled once during startup.
// It replaces the 30-positional-parameter signature, where adding a handler meant
// editing both the declaration and the one call site, in order.
type routeDeps struct {
	app                   *App
	router                *gin.Engine
	authHandler           *handler.AuthHandler
	agentHandler          *handler.AgentHandler
	monitorHandler        *handler.MonitorHandler
	notificationHandler   *handler.NotificationHandler
	conversationHandler   *handler.ConversationHandler
	robotHandler          *handler.RobotHandler
	wechatRobotHandler    *handler.WechatRobotHandler
	configHandler         *handler.ConfigHandler
	externalMCPHandler    *handler.ExternalMCPHandler
	attackChainHandler    *handler.AttackChainHandler
	vulnerabilityHandler  *handler.VulnerabilityHandler
	assetHandler          *handler.AssetHandler
	projectHandler        *handler.ProjectHandler
	workflowHandler       *handler.WorkflowHandler
	webshellHandler       *handler.WebShellHandler
	chatUploadsHandler    *handler.ChatUploadsHandler
	roleHandler           *handler.RoleHandler
	pluginHandler         *handler.PluginHandler
	updateHandler         *handler.UpdateHandler
	skillsHandler         *handler.SkillsHandler
	markdownAgentsHandler *handler.MarkdownAgentsHandler
	fofaHandler           *handler.FofaHandler
	terminalHandler       *handler.TerminalHandler
	c2Handler             *handler.C2Handler
	auditHandler          *handler.AuditHandler
	auditSvc              *audit.Service
	rbacHandler           *handler.RBACHandler
	mcpServer             *mcp.Server
	authManager           *security.AuthManager
	openAPIHandler        *handler.OpenAPIHandler
}

func setupRoutes(deps routeDeps) {
	app := deps.app
	router := deps.router
	authHandler := deps.authHandler
	robotHandler := deps.robotHandler
	auditSvc := deps.auditSvc
	authManager := deps.authManager
	openAPIHandler := deps.openAPIHandler

	// API路由
	api := router.Group("/api")

	// 认证相关路由
	authRoutes := api.Group("/auth")
	loginRL := security.NewRateLimiter(10, 1*time.Minute)
	{
		authRoutes.POST("/login", security.RateLimitMiddleware(loginRL), authHandler.Login)
		authRoutes.POST("/logout", security.AuthMiddleware(authManager), authHandler.Logout)
		authRoutes.POST("/change-password", security.AuthMiddleware(authManager), security.RequirePermission("auth:self"), authHandler.ChangePassword)
		authRoutes.GET("/validate", security.AuthMiddleware(authManager), authHandler.Validate)
		authRoutes.POST("/robot-binding-code", security.AuthMiddleware(authManager), security.RequirePermission("auth:self"), robotHandler.CreateRobotBindingCode)
		authRoutes.GET("/robot-bindings", security.AuthMiddleware(authManager), security.RequirePermission("auth:self"), robotHandler.ListMyRobotBindings)
		authRoutes.DELETE("/robot-bindings/:id", security.AuthMiddleware(authManager), security.RequirePermission("auth:self"), robotHandler.DeleteMyRobotBinding)
	}

	protected := api.Group("")
	protected.Use(security.AuthMiddleware(authManager))
	protected.Use(security.RBACMiddlewareWithDenyHook(app.db, func(c *gin.Context, reason, permission string) {
		if auditSvc != nil {
			auditSvc.Record(c, audit.Entry{
				Level: "warn", Category: "rbac", Action: "access_denied", Result: "failure",
				Message: "RBAC 拒绝访问", ResourceType: "route", ResourceID: c.FullPath(),
				Detail: map[string]interface{}{"reason": reason, "permission": permission, "method": c.Request.Method},
			})
		}
	}))
	// Per-domain registrars. The route table they produce is asserted against
	// testdata/routes.golden.txt, so moving a registration cannot silently
	// change which paths exist.

	deps.registerAgentRoutes(protected)
	deps.registerAssetRoutes(protected)
	deps.registerAttackChainRoutes(protected)
	deps.registerAuditRoutes(protected)
	deps.registerC2Routes(protected)
	deps.registerConfigRoutes(protected)
	deps.registerConversationRoutes(protected)
	deps.registerFofaRoutes(protected)
	deps.registerHitlRoutes(protected)
	deps.registerToolGuardRoutes(protected)
	deps.registerStorageRoutes(protected)
	deps.registerExternalMCPRoutes(protected)
	deps.registerChatUploadsRoutes(protected)
	deps.registerKnowledgeRoutes(protected)
	deps.registerMcpRoutes(protected)
	deps.registerMonitorRoutes(protected)
	deps.registerNotificationRoutes(protected)
	deps.registerProjectRoutes(protected)
	deps.registerRbacRoutes(protected)
	deps.registerRobotRoutes(protected)
	deps.registerRobotCallbackRoutes(api)
	deps.registerRoleRoutes(protected)
	deps.registerPluginRoutes(protected)
	deps.registerUpdateRoutes(protected)
	deps.registerSkillRoutes(protected)
	deps.registerTaskRoutes(protected)
	deps.registerTerminalRoutes(protected)
	deps.registerVulnerabilityRoutes(protected)
	deps.registerWebshellRoutes(protected)
	deps.registerWorkflowRoutes(protected)

	// OpenAPI规范（需要认证，避免暴露API结构信息）
	protected.GET("/openapi/spec", openAPIHandler.GetOpenAPISpec)

	// API文档页面（公开访问，但需要登录后才能使用API）
	router.GET("/api-docs", func(c *gin.Context) {
		c.HTML(http.StatusOK, "api-docs.html", nil)
	})

	// 静态文件与前端模板：磁盘上有 web/ 时优先用磁盘（改模板免重编译），
	// 否则回落到内嵌副本，二进制在任意工作目录下都能起。
	webDir := assets.ResolveWebDir("")
	tmpl, err := assets.Templates(webDir)
	if err != nil {
		app.logger.Fatal("前端模板加载失败", err)
	}
	router.SetHTMLTemplate(tmpl)
	router.StaticFS("/static", http.FS(assets.StaticFS(webDir)))

	// 前端页面
	router.GET("/", func(c *gin.Context) {
		version := app.config.Version
		if version == "" {
			version = "v1.0.0"
		}
		c.HTML(http.StatusOK, "index.html", gin.H{"Version": version})
	})
}

// registerWebshellTools 注册 WebShell 相关 MCP 工具，供 AI 助手在指定连接上执行命令与文件操作
func registerWebshellTools(mcpServer *mcp.Server, db *database.DB, webshellHandler *handler.WebShellHandler, logger *zap.Logger) {
	if db == nil || webshellHandler == nil {
		logger.Warn("跳过 WebShell 工具注册：db 或 webshellHandler 为空")
		return
	}

	// webshell_exec
	execTool := mcp.Tool{
		Name:             builtin.ToolWebshellExec,
		Description:      "在指定的 WebShell 连接上执行一条系统命令，返回命令的标准输出。connection_id 由用户在 AI 助手上下文中选定。",
		ShortDescription: "在 WebShell 连接上执行命令",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{
					"type":        "string",
					"description": "WebShell 连接 ID（如 ws_xxx）",
				},
				"command": map[string]interface{}{
					"type":        "string",
					"description": "要执行的系统命令",
				},
			},
			"required": []string{"connection_id", "command"},
		},
	}
	execHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		cid, _ := args["connection_id"].(string)
		cmd, _ := args["command"].(string)
		if cid == "" || cmd == "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "connection_id 和 command 均为必填"}}, IsError: true}, nil
		}
		conn, err := db.GetWebshellConnection(cid)
		if err != nil || conn == nil {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "未找到该 WebShell 连接或查询失败"}}, IsError: true}, nil
		}
		output, ok, errMsg := webshellHandler.ExecWithConnection(conn, cmd)
		if errMsg != "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: errMsg}}, IsError: true}, nil
		}
		if !ok {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "HTTP 非 200，输出:\n" + output}}, IsError: false}, nil
		}
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: output}}, IsError: false}, nil
	}
	mcpServer.RegisterTool(execTool, execHandler)

	// webshell_file_list
	listTool := mcp.Tool{
		Name:             builtin.ToolWebshellFileList,
		Description:      "在指定 WebShell 连接上列出目录内容。path 默认为当前目录（.）。",
		ShortDescription: "在 WebShell 上列出目录",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{"type": "string", "description": "WebShell 连接 ID"},
				"path":          map[string]interface{}{"type": "string", "description": "目录路径，默认 ."},
			},
			"required": []string{"connection_id"},
		},
	}
	listHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		cid, _ := args["connection_id"].(string)
		path, _ := args["path"].(string)
		if cid == "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "connection_id 必填"}}, IsError: true}, nil
		}
		conn, err := db.GetWebshellConnection(cid)
		if err != nil || conn == nil {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "未找到该 WebShell 连接"}}, IsError: true}, nil
		}
		output, ok, errMsg := webshellHandler.FileOpWithConnection(conn, "list", path, "", "")
		if errMsg != "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: errMsg}}, IsError: true}, nil
		}
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: output}}, IsError: !ok}, nil
	}
	mcpServer.RegisterTool(listTool, listHandler)

	// webshell_file_read
	readTool := mcp.Tool{
		Name:             builtin.ToolWebshellFileRead,
		Description:      "在指定 WebShell 连接上读取文件内容。",
		ShortDescription: "在 WebShell 上读取文件",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{"type": "string", "description": "WebShell 连接 ID"},
				"path":          map[string]interface{}{"type": "string", "description": "文件路径"},
			},
			"required": []string{"connection_id", "path"},
		},
	}
	readHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		cid, _ := args["connection_id"].(string)
		path, _ := args["path"].(string)
		if cid == "" || path == "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "connection_id 和 path 必填"}}, IsError: true}, nil
		}
		conn, err := db.GetWebshellConnection(cid)
		if err != nil || conn == nil {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "未找到该 WebShell 连接"}}, IsError: true}, nil
		}
		output, ok, errMsg := webshellHandler.FileOpWithConnection(conn, "read", path, "", "")
		if errMsg != "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: errMsg}}, IsError: true}, nil
		}
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: output}}, IsError: !ok}, nil
	}
	mcpServer.RegisterTool(readTool, readHandler)

	// webshell_file_write
	writeTool := mcp.Tool{
		Name:             builtin.ToolWebshellFileWrite,
		Description:      "在指定 WebShell 连接上写入文件内容（会覆盖已有文件）。",
		ShortDescription: "在 WebShell 上写入文件",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{"type": "string", "description": "WebShell 连接 ID"},
				"path":          map[string]interface{}{"type": "string", "description": "文件路径"},
				"content":       map[string]interface{}{"type": "string", "description": "要写入的内容"},
			},
			"required": []string{"connection_id", "path", "content"},
		},
	}
	writeHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		cid, _ := args["connection_id"].(string)
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		if cid == "" || path == "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "connection_id 和 path 必填"}}, IsError: true}, nil
		}
		conn, err := db.GetWebshellConnection(cid)
		if err != nil || conn == nil {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "未找到该 WebShell 连接"}}, IsError: true}, nil
		}
		output, ok, errMsg := webshellHandler.FileOpWithConnection(conn, "write", path, content, "")
		if errMsg != "" {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: errMsg}}, IsError: true}, nil
		}
		if !ok {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "写入可能失败，输出:\n" + output}}, IsError: false}, nil
		}
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "写入成功\n" + output}}, IsError: false}, nil
	}
	mcpServer.RegisterTool(writeTool, writeHandler)

	logger.Debug("WebShell 工具注册成功")
}

// registerWebshellManagementTools 注册 WebShell 连接管理 MCP 工具
func registerWebshellManagementTools(mcpServer *mcp.Server, db *database.DB, webshellHandler *handler.WebShellHandler, logger *zap.Logger) {
	if db == nil {
		logger.Warn("跳过 WebShell 管理工具注册：db 为空")
		return
	}
	projectIDFromToolArgs := func(ctx context.Context, args map[string]interface{}) string {
		projectID, _ := args["project_id"].(string)
		projectID = strings.TrimSpace(projectID)
		if projectID == "" {
			projectID = strings.TrimSpace(mcp.MCPProjectIDFromContext(ctx))
		}
		return projectID
	}
	explicitProjectIDFromToolArgs := func(args map[string]interface{}) string {
		projectID, _ := args["project_id"].(string)
		return strings.TrimSpace(projectID)
	}
	authorizeWebshellToolProject := func(principal authctx.Principal, permission, projectID string) *mcp.ToolResult {
		projectID = strings.TrimSpace(projectID)
		if projectID == "" {
			return nil
		}
		if projectID == database.ProjectFilterUnbound {
			return nil
		}
		if !db.UserCanAccessResource(principal.UserID, principal.ScopeFor(permission), "project", projectID) {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "无权访问项目: " + projectID}},
				IsError: true,
			}
		}
		return nil
	}

	// manage_webshell_list - 列出所有 webshell 连接
	listTool := mcp.Tool{
		Name:             builtin.ToolManageWebshellList,
		Description:      "列出已保存的 WebShell 连接，返回连接ID、URL、类型、所属项目、备注等信息。默认按当前对话项目边界过滤：项目对话看本项目，未绑定项目的对话看未绑定连接；显式传 project_id 时按指定项目过滤。",
		ShortDescription: "列出所有 WebShell 连接",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"project_id": map[string]interface{}{
					"type":        "string",
					"description": "项目 ID；不填时在项目会话中默认使用当前项目。",
				},
			},
		},
	}
	listHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		connections := []database.WebShellConnection{}
		var err error
		if principal, ok := authctx.PrincipalFromContext(ctx); ok {
			projectID := explicitProjectIDFromToolArgs(args)
			if projectID == "" {
				projectID = mcpEffectiveProjectFilter(ctx, db)
			}
			if result := authorizeWebshellToolProject(principal, "webshell:read", projectID); result != nil {
				return result, nil
			}
			connections, err = db.ListWebshellConnectionsForAccess(principal.UserID, principal.ScopeFor("webshell:read"), projectID)
		} else {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "缺少认证身份"}}, IsError: true}, nil
		}
		if err != nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "获取连接列表失败: " + err.Error()}},
				IsError: true,
			}, nil
		}
		if len(connections) == 0 {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "暂无 WebShell 连接"}},
				IsError: false,
			}, nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("找到 %d 个 WebShell 连接：\n\n", len(connections)))
		for _, conn := range connections {
			sb.WriteString(fmt.Sprintf("ID: %s\n", conn.ID))
			sb.WriteString(fmt.Sprintf("  URL: %s\n", conn.URL))
			sb.WriteString(fmt.Sprintf("  类型: %s\n", conn.Type))
			sb.WriteString(fmt.Sprintf("  请求方式: %s\n", conn.Method))
			sb.WriteString(fmt.Sprintf("  命令参数: %s\n", conn.CmdParam))
			if conn.ProjectID != "" {
				sb.WriteString(fmt.Sprintf("  项目ID: %s\n", conn.ProjectID))
			} else {
				sb.WriteString("  项目: 未绑定\n")
			}
			if conn.Remark != "" {
				sb.WriteString(fmt.Sprintf("  备注: %s\n", conn.Remark))
			}
			sb.WriteString(fmt.Sprintf("  创建时间: %s\n", conn.CreatedAt.Format("2006-01-02 15:04:05")))
			sb.WriteString("\n")
		}
		return &mcp.ToolResult{
			Content: []mcp.Content{{Type: "text", Text: sb.String()}},
			IsError: false,
		}, nil
	}
	mcpServer.RegisterTool(listTool, listHandler)

	// manage_webshell_add - 添加新的 webshell 连接
	addTool := mcp.Tool{
		Name:             builtin.ToolManageWebshellAdd,
		Description:      "添加新的 WebShell 连接到管理系统。支持 PHP、ASP、ASPX、JSP 等类型的一句话木马。",
		ShortDescription: "添加 WebShell 连接",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{
					"type":        "string",
					"description": "Shell 地址，如 http://target.com/shell.php（必填）",
				},
				"password": map[string]interface{}{
					"type":        "string",
					"description": "连接密码/密钥，如冰蝎/蚁剑的连接密码",
				},
				"type": map[string]interface{}{
					"type":        "string",
					"description": "Shell 类型：php、asp、aspx、jsp，默认为 php",
					"enum":        []string{"php", "asp", "aspx", "jsp"},
				},
				"method": map[string]interface{}{
					"type":        "string",
					"description": "请求方式：GET 或 POST，默认为 POST",
					"enum":        []string{"GET", "POST"},
				},
				"cmd_param": map[string]interface{}{
					"type":        "string",
					"description": "命令参数名，不填默认为 cmd",
				},
				"remark": map[string]interface{}{
					"type":        "string",
					"description": "备注，便于识别的备注名",
				},
			},
			"required": []string{"url"},
		},
	}
	addHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		urlStr, _ := args["url"].(string)
		if urlStr == "" {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "错误: url 参数必填"}},
				IsError: true,
			}, nil
		}

		password, _ := args["password"].(string)
		shellType, _ := args["type"].(string)
		if shellType == "" {
			shellType = "php"
		}
		method, _ := args["method"].(string)
		if method == "" {
			method = "post"
		}
		cmdParam, _ := args["cmd_param"].(string)
		if cmdParam == "" {
			cmdParam = "cmd"
		}
		remark, _ := args["remark"].(string)
		principal, ok := authctx.PrincipalFromContext(ctx)
		if !ok {
			return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "缺少认证身份"}}, IsError: true}, nil
		}
		projectID := projectIDFromToolArgs(ctx, args)
		if result := authorizeWebshellToolProject(principal, "webshell:write", projectID); result != nil {
			return result, nil
		}

		// 生成连接ID
		connID := "ws_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
		conn := &database.WebShellConnection{
			ID:        connID,
			URL:       urlStr,
			Password:  password,
			Type:      strings.ToLower(shellType),
			Method:    strings.ToLower(method),
			CmdParam:  cmdParam,
			Remark:    remark,
			ProjectID: projectID,
			CreatedAt: time.Now(),
		}

		if err := db.CreateWebshellConnection(conn); err != nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "添加 WebShell 连接失败: " + err.Error()}},
				IsError: true,
			}, nil
		}
		_ = db.SetResourceOwner("webshell", conn.ID, principal.UserID)
		_ = db.AssignResourceToUser(principal.UserID, "webshell", conn.ID)
		projectLine := "项目: 未绑定"
		if conn.ProjectID != "" {
			projectLine = "项目ID: " + conn.ProjectID
		}

		return &mcp.ToolResult{
			Content: []mcp.Content{{
				Type: "text",
				Text: fmt.Sprintf("WebShell 连接添加成功！\n\n连接ID: %s\nURL: %s\n类型: %s\n请求方式: %s\n命令参数: %s\n%s", conn.ID, conn.URL, conn.Type, conn.Method, conn.CmdParam, projectLine),
			}},
			IsError: false,
		}, nil
	}
	mcpServer.RegisterTool(addTool, addHandler)

	// manage_webshell_update - 更新 webshell 连接
	updateTool := mcp.Tool{
		Name:             builtin.ToolManageWebshellUpdate,
		Description:      "更新已存在的 WebShell 连接信息。",
		ShortDescription: "更新 WebShell 连接",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{
					"type":        "string",
					"description": "要更新的 WebShell 连接 ID（必填）",
				},
				"url": map[string]interface{}{
					"type":        "string",
					"description": "新的 Shell 地址",
				},
				"password": map[string]interface{}{
					"type":        "string",
					"description": "新的连接密码/密钥",
				},
				"type": map[string]interface{}{
					"type":        "string",
					"description": "新的 Shell 类型：php、asp、aspx、jsp",
					"enum":        []string{"php", "asp", "aspx", "jsp"},
				},
				"method": map[string]interface{}{
					"type":        "string",
					"description": "新的请求方式：GET 或 POST",
					"enum":        []string{"GET", "POST"},
				},
				"cmd_param": map[string]interface{}{
					"type":        "string",
					"description": "新的命令参数名",
				},
				"remark": map[string]interface{}{
					"type":        "string",
					"description": "新的备注",
				},
				"project_id": map[string]interface{}{
					"type":        "string",
					"description": "新的所属项目 ID；传空字符串可取消绑定。",
				},
			},
			"required": []string{"connection_id"},
		},
	}
	updateHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		connID, _ := args["connection_id"].(string)
		if connID == "" {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "错误: connection_id 参数必填"}},
				IsError: true,
			}, nil
		}

		// 获取现有连接
		existing, err := db.GetWebshellConnection(connID)
		if err != nil || existing == nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "未找到指定的 WebShell 连接: " + connID}},
				IsError: true,
			}, nil
		}

		// 更新字段（如果提供了新值）
		if urlStr, ok := args["url"].(string); ok && urlStr != "" {
			existing.URL = urlStr
		}
		if password, ok := args["password"].(string); ok {
			existing.Password = password
		}
		if shellType, ok := args["type"].(string); ok && shellType != "" {
			existing.Type = strings.ToLower(shellType)
		}
		if method, ok := args["method"].(string); ok && method != "" {
			existing.Method = strings.ToLower(method)
		}
		if cmdParam, ok := args["cmd_param"].(string); ok && cmdParam != "" {
			existing.CmdParam = cmdParam
		}
		if remark, ok := args["remark"].(string); ok {
			existing.Remark = remark
		}
		if projectID, ok := args["project_id"].(string); ok {
			projectID = strings.TrimSpace(projectID)
			if projectID != "" {
				principal, ok := authctx.PrincipalFromContext(ctx)
				if !ok {
					return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "缺少认证身份"}}, IsError: true}, nil
				}
				if result := authorizeWebshellToolProject(principal, "webshell:write", projectID); result != nil {
					return result, nil
				}
			}
			existing.ProjectID = projectID
		}

		if err := db.UpdateWebshellConnection(existing); err != nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "更新 WebShell 连接失败: " + err.Error()}},
				IsError: true,
			}, nil
		}

		return &mcp.ToolResult{
			Content: []mcp.Content{{
				Type: "text",
				Text: fmt.Sprintf("WebShell 连接更新成功！\n\n连接ID: %s\nURL: %s\n类型: %s\n请求方式: %s\n命令参数: %s\n项目ID: %s\n备注: %s", existing.ID, existing.URL, existing.Type, existing.Method, existing.CmdParam, existing.ProjectID, existing.Remark),
			}},
			IsError: false,
		}, nil
	}
	mcpServer.RegisterTool(updateTool, updateHandler)

	// manage_webshell_delete - 删除 webshell 连接
	deleteTool := mcp.Tool{
		Name:             builtin.ToolManageWebshellDelete,
		Description:      "删除指定的 WebShell 连接。",
		ShortDescription: "删除 WebShell 连接",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{
					"type":        "string",
					"description": "要删除的 WebShell 连接 ID（必填）",
				},
			},
			"required": []string{"connection_id"},
		},
	}
	deleteHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		connID, _ := args["connection_id"].(string)
		if connID == "" {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "错误: connection_id 参数必填"}},
				IsError: true,
			}, nil
		}

		if err := db.DeleteWebshellConnection(connID); err != nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "删除 WebShell 连接失败: " + err.Error()}},
				IsError: true,
			}, nil
		}

		return &mcp.ToolResult{
			Content: []mcp.Content{{
				Type: "text",
				Text: fmt.Sprintf("WebShell 连接 %s 已成功删除", connID),
			}},
			IsError: false,
		}, nil
	}
	mcpServer.RegisterTool(deleteTool, deleteHandler)

	// manage_webshell_test - 测试 webshell 连接
	testTool := mcp.Tool{
		Name:             builtin.ToolManageWebshellTest,
		Description:      "测试指定的 WebShell 连接是否可用，会尝试执行一个简单的命令（如 whoami 或 dir）。",
		ShortDescription: "测试 WebShell 连接",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection_id": map[string]interface{}{
					"type":        "string",
					"description": "要测试的 WebShell 连接 ID（必填）",
				},
				"command": map[string]interface{}{
					"type":        "string",
					"description": "测试命令，默认为 whoami（Linux）或 dir（Windows）",
				},
			},
			"required": []string{"connection_id"},
		},
	}
	testHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
		connID, _ := args["connection_id"].(string)
		if connID == "" {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "错误: connection_id 参数必填"}},
				IsError: true,
			}, nil
		}

		// 获取连接
		conn, err := db.GetWebshellConnection(connID)
		if err != nil || conn == nil {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "未找到指定的 WebShell 连接: " + connID}},
				IsError: true,
			}, nil
		}

		// 确定测试命令
		testCmd, _ := args["command"].(string)
		if testCmd == "" {
			// 根据 shell 类型选择默认命令
			if conn.Type == "asp" || conn.Type == "aspx" {
				testCmd = "dir"
			} else {
				testCmd = "whoami"
			}
		}

		// 执行测试命令
		output, ok, errMsg := webshellHandler.ExecWithConnection(conn, testCmd)
		if errMsg != "" {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: fmt.Sprintf("连接测试失败！\n\n连接ID: %s\nURL: %s\n错误: %s", connID, conn.URL, errMsg)}},
				IsError: true,
			}, nil
		}

		if !ok {
			return &mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: fmt.Sprintf("连接测试失败！HTTP 非 200\n\n连接ID: %s\nURL: %s\n输出: %s", connID, conn.URL, output)}},
				IsError: true,
			}, nil
		}

		return &mcp.ToolResult{
			Content: []mcp.Content{{
				Type: "text",
				Text: fmt.Sprintf("连接测试成功！\n\n连接ID: %s\nURL: %s\n类型: %s\n\n测试命令: %s\n输出结果:\n%s", connID, conn.URL, conn.Type, testCmd, output),
			}},
			IsError: false,
		}, nil
	}
	mcpServer.RegisterTool(testTool, testHandler)

	logger.Debug("WebShell 管理工具注册成功")
}

// initializeKnowledge 初始化知识库组件（用于动态初始化）
func initializeKnowledge(
	cfg *config.Config,
	db *database.DB,
	knowledgeDBConn *database.DB,
	mcpServer *mcp.Server,
	agentHandler *handler.AgentHandler,
	app *App, // 传递 App 引用以便更新知识库组件
	logger *zap.Logger,
) (*handler.KnowledgeHandler, error) {
	// 确定知识库数据库路径
	knowledgeDBPath := cfg.Database.KnowledgeDBPath
	var knowledgeDB *sql.DB

	if knowledgeDBPath != "" {
		// 使用独立的知识库数据库
		// 确保目录存在
		if err := os.MkdirAll(filepath.Dir(knowledgeDBPath), 0755); err != nil {
			return nil, fmt.Errorf("创建知识库数据库目录失败: %w", err)
		}

		var err error
		knowledgeDBConn, err = database.NewKnowledgeDB(knowledgeDBPath, logger)
		if err != nil {
			return nil, fmt.Errorf("初始化知识库数据库失败: %w", err)
		}
		knowledgeDB = knowledgeDBConn.DB
		logger.Info("使用独立的知识库数据库", zap.String("path", knowledgeDBPath))
	} else {
		// 向后兼容：使用会话数据库
		knowledgeDB = db.DB
		logger.Info("使用会话数据库存储知识库数据（建议配置knowledge_db_path以分离数据）")
	}

	// 创建知识库管理器
	knowledgeManager := knowledge.NewManager(knowledgeDB, cfg.Knowledge.BasePath, logger)

	// 创建嵌入器
	// 使用OpenAI配置的API Key（如果知识库配置中没有指定）
	if cfg.Knowledge.Embedding.APIKey == "" {
		cfg.Knowledge.Embedding.APIKey = cfg.OpenAI.APIKey
	}
	if cfg.Knowledge.Embedding.BaseURL == "" {
		cfg.Knowledge.Embedding.BaseURL = cfg.OpenAI.BaseURL
	}

	embedder, err := knowledge.NewEmbedder(context.Background(), &cfg.Knowledge, &cfg.OpenAI, logger)
	if err != nil {
		return nil, fmt.Errorf("初始化知识库嵌入器失败: %w", err)
	}

	// 创建检索器（Eino MultiQuery + 重排流水线）
	retrievalConfig := knowledge.RetrievalConfigFromYAML(cfg.Knowledge.Retrieval)
	knowledgeRetriever := knowledge.NewRetriever(knowledgeDB, embedder, retrievalConfig, logger)
	if err := knowledge.WireRetrieverPipeline(context.Background(), knowledgeRetriever, &cfg.OpenAI); err != nil {
		return nil, fmt.Errorf("初始化知识库检索流水线失败: %w", err)
	}

	// 创建索引器（Eino Compose 链）
	knowledgeIndexer, err := knowledge.NewIndexer(context.Background(), knowledgeDB, embedder, logger, &cfg.Knowledge)
	if err != nil {
		return nil, fmt.Errorf("初始化知识库索引器失败: %w", err)
	}

	// 注册知识检索工具到MCP服务器
	knowledge.RegisterKnowledgeTool(mcpServer, knowledgeRetriever, knowledgeManager, logger)

	// 创建知识库API处理器
	knowledgeHandler := handler.NewKnowledgeHandler(knowledgeManager, knowledgeRetriever, knowledgeIndexer, logger)
	if app != nil && app.auditSvc != nil {
		bindAudit(knowledgeHandler, app.auditSvc)
	}
	logger.Info("知识库模块初始化完成", zap.Bool("handler_created", knowledgeHandler != nil))

	// 设置知识库管理器到AgentHandler以便记录检索日志
	agentHandler.SetKnowledgeManager(knowledgeManager)

	// 更新 App 中的知识库组件（如果 App 不为 nil，说明是动态初始化）
	if app != nil {
		app.knowledgeManager = knowledgeManager
		app.knowledgeRetriever = knowledgeRetriever
		app.knowledgeIndexer = knowledgeIndexer
		app.knowledgeHandler = knowledgeHandler
		// 如果使用独立数据库，更新 knowledgeDB
		if knowledgeDBPath != "" {
			app.knowledgeDB = knowledgeDBConn
		}
		logger.Info("App 中的知识库组件已更新")
	}

	// 扫描知识库并建立索引（异步）
	go func() {
		itemsToIndex, err := knowledgeManager.ScanKnowledgeBase()
		if err != nil {
			logger.Warn("扫描知识库失败", zap.Error(err))
			return
		}

		// 检查是否已有索引
		hasIndex, err := knowledgeIndexer.HasIndex()
		if err != nil {
			logger.Warn("检查索引状态失败", zap.Error(err))
			return
		}

		if hasIndex {
			// 如果已有索引，只索引新添加或更新的项
			if len(itemsToIndex) > 0 {
				logger.Info("检测到已有知识库索引，开始增量索引", zap.Int("count", len(itemsToIndex)))
				ctx := context.Background()
				consecutiveFailures := 0
				var firstFailureItemID string
				var firstFailureError error
				failedCount := 0

				for _, itemID := range itemsToIndex {
					if err := knowledgeIndexer.IndexItem(ctx, itemID); err != nil {
						failedCount++
						consecutiveFailures++

						if consecutiveFailures == 1 {
							firstFailureItemID = itemID
							firstFailureError = err
							logger.Warn("索引知识项失败", zap.String("itemId", itemID), zap.Error(err))
						}

						// 如果连续失败2次，立即停止增量索引
						if consecutiveFailures >= 2 {
							logger.Error("连续索引失败次数过多，立即停止增量索引",
								zap.Int("consecutiveFailures", consecutiveFailures),
								zap.Int("totalItems", len(itemsToIndex)),
								zap.String("firstFailureItemId", firstFailureItemID),
								zap.Error(firstFailureError),
							)
							break
						}
						continue
					}

					// 成功时重置连续失败计数
					if consecutiveFailures > 0 {
						consecutiveFailures = 0
						firstFailureItemID = ""
						firstFailureError = nil
					}
				}
				logger.Info("增量索引完成", zap.Int("totalItems", len(itemsToIndex)), zap.Int("failedCount", failedCount))
			} else {
				logger.Info("检测到已有知识库索引，没有需要索引的新项或更新项")
			}
			return
		}

		// 冷启动：仅为尚无向量的知识项构建索引（与 IndexMissing 语义一致）
		logger.Info("未检测到知识库索引，开始自动构建索引")
		ctx := context.Background()
		if err := knowledgeIndexer.IndexMissing(ctx); err != nil {
			logger.Warn("自动构建知识库索引失败", zap.Error(err))
		}
	}()

	return knowledgeHandler, nil
}

// corsMiddleware allows same-origin requests, valid Chromium extension
// origins, and exact origins explicitly configured by the operator. CORS is
// not an authentication boundary; API access still requires a valid session.
func corsMiddleware(configuredOrigins []string) gin.HandlerFunc {
	allowedOrigins := make(map[string]struct{}, len(configuredOrigins))
	for _, origin := range configuredOrigins {
		if normalized, ok := normalizeCORSOrigin(origin); ok {
			allowedOrigins[normalized] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		origin := strings.TrimSpace(c.GetHeader("Origin"))
		if origin != "" {
			c.Writer.Header().Add("Vary", "Origin")
			normalized, valid := normalizeCORSOrigin(origin)
			_, explicitlyAllowed := allowedOrigins[normalized]
			parsed, _ := url.Parse(origin)
			sameHost := valid && strings.EqualFold(parsed.Host, c.Request.Host)
			browserExtension := valid && isChromiumExtensionOrigin(parsed)
			if !sameHost && !browserExtension && !explicitlyAllowed {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cross-origin request denied"})
				return
			}
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")
		c.Writer.Header().Set("Access-Control-Max-Age", "600")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// isChromiumExtensionOrigin accepts only Chrome's canonical 32-character
// extension IDs (letters a-p). It does not allow arbitrary custom schemes or
// web origins, and the extension must separately obtain host permission.
func isChromiumExtensionOrigin(origin *url.URL) bool {
	if origin == nil || !strings.EqualFold(origin.Scheme, "chrome-extension") || origin.Port() != "" {
		return false
	}
	id := strings.ToLower(origin.Hostname())
	if len(id) != 32 {
		return false
	}
	for _, ch := range id {
		if ch < 'a' || ch > 'p' {
			return false
		}
	}
	return true
}

// normalizeCORSOrigin validates and canonicalizes a serialized origin. CORS
// origins never contain credentials, paths, query strings, or fragments.
func normalizeCORSOrigin(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" || strings.EqualFold(raw, "null") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), true
}
