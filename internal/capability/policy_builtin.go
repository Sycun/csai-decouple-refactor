package capability

import "time"

// SourceAgentLocal marks the filesystem and shell tools the agent runtime
// registers itself. They are gated by permission through the registry rather than
// by a name list copied into middleware.
const SourceAgentLocal = "agent-local"

// BuiltinSpecs is the declarative policy table for every capability shipped in
// the Go binary. It replaces the per-tool switch: identity, class, permission,
// mediated grants and approval floor all live here, and the evaluator is the
// only consumer.
//
// Adding a built-in tool means adding one entry to this table. Adding a recipe
// tool means editing only its YAML.
func BuiltinSpecs() []*Spec {
	destructiveWebshell := func(id, name, title string) *Spec {
		return &Spec{
			ID: id, Name: name, Title: title,
			Class: ClassDestructive, Runtime: RuntimeGoBuiltin,
			Permission: "webshell:write", Approval: ApprovalAlways,
			Grants: []CapabilityGrant{{Name: "c2.exec"}},
			Check:  ResourceBoundary("webshell:write", "webshell", "connection_id"),
		}
	}
	batchWriteQueue := Resource("tasks:write", "batch_task", "queue_id")

	return []*Spec{
		// Vulnerability management
		{ID: "core.record_vulnerability", Name: "record_vulnerability", Title: "记录漏洞",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "vulnerability:write",
			Evidence: true, Check: Conversation("vulnerability:write", "conversation_id", false)},
		{ID: "core.list_vulnerabilities", Name: "list_vulnerabilities", Title: "查询漏洞列表",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "vulnerability:read", Approval: ApprovalNever,
			Check: Conversation("vulnerability:read", "", true)},
		{ID: "core.get_vulnerability", Name: "get_vulnerability", Title: "读取漏洞",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "vulnerability:read", Approval: ApprovalNever,
			Check: RetestAwareFinding("vulnerability:read", "id")},
		{ID: "core.update_vulnerability_status", Name: "update_vulnerability_status", Title: "更新漏洞状态与复测结果",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "vulnerability:write",
			Evidence: true, Check: RetestAwareFinding("vulnerability:write", "id")},

		// Asset management
		{ID: "core.create_asset", Name: "create_asset", Title: "创建资产",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "asset:write",
			Check: OptionalProject(Require("asset:write"), "asset:write")},
		{ID: "core.get_asset", Name: "get_asset", Title: "读取资产",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "asset:read", Approval: ApprovalNever,
			Check: ResourceBoundary("asset:read", "asset", "id")},
		{ID: "core.query_assets", Name: "query_assets", Title: "查询资产",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "asset:read", Approval: ApprovalNever,
			Check: Require("asset:read")},
		{ID: "core.update_asset", Name: "update_asset", Title: "更新资产",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "asset:write",
			Check: OptionalProject(ResourceBoundary("asset:write", "asset", "id"), "asset:write")},
		{ID: "core.complete_asset_scan", Name: "complete_asset_scan", Title: "标记资产扫描完成",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "asset:write",
			Check: All(ResourceBoundary("asset:write", "asset", "id"), Conversation("asset:write", "", true))},
		{ID: "core.delete_asset", Name: "delete_asset", Title: "删除资产",
			Class: ClassDestructive, Runtime: RuntimeGoBuiltin, Permission: "asset:delete", Approval: ApprovalAlways,
			Check: ResourceBoundary("asset:delete", "asset", "id")},

		// Project blackboard
		{ID: "core.upsert_project_fact", Name: "upsert_project_fact", Title: "写入项目事实",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "project:write",
			Check: ProjectBound("project:write")},
		{ID: "core.deprecate_project_fact", Name: "deprecate_project_fact", Title: "废弃项目事实",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "project:write",
			Check: ProjectBound("project:write")},
		{ID: "core.restore_project_fact", Name: "restore_project_fact", Title: "恢复项目事实",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "project:write",
			Check: ProjectBound("project:write")},
		{ID: "core.get_project_fact", Name: "get_project_fact", Title: "读取项目事实",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "project:read", Approval: ApprovalNever,
			Check: ProjectBound("project:read")},
		{ID: "core.list_project_facts", Name: "list_project_facts", Title: "列出项目事实",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "project:read", Approval: ApprovalNever,
			Check: ProjectBound("project:read")},
		{ID: "core.search_project_facts", Name: "search_project_facts", Title: "检索项目事实",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "project:read", Approval: ApprovalNever,
			Check: ProjectBound("project:read")},

		// Knowledge base
		{ID: "core.list_knowledge_risk_types", Name: "list_knowledge_risk_types", Title: "列出风险类型",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "knowledge:read", Approval: ApprovalNever,
			Check: Require("knowledge:read")},
		{ID: "core.search_knowledge_base", Name: "search_knowledge_base", Title: "检索知识库",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "knowledge:read", Approval: ApprovalNever,
			Evidence: true, Check: Require("knowledge:read")},

		// Vision
		{ID: "core.analyze_image", Name: "analyze_image", Title: "图像分析",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "agent:execute",
			Grants: []CapabilityGrant{{Name: "fs.read", Target: "workspace"}},
			Check:  Require("agent:execute")},

		// Long-running execution control (this is the mediation control plane,
		// so it must stay in-process).
		{ID: "core.get_tool_execution", Name: "get_tool_execution", Title: "查询执行状态",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "monitor:read", Approval: ApprovalNever,
			Check: Execution("monitor:read")},
		{ID: "core.wait_tool_execution", Name: "wait_tool_execution", Title: "等待执行完成",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "monitor:read",
			Check: Execution("monitor:read")},
		{ID: "core.cancel_tool_execution", Name: "cancel_tool_execution", Title: "取消执行",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "monitor:write",
			Check: Execution("monitor:write")},

		// WebShell assistant
		destructiveWebshell("core.webshell_exec", "webshell_exec", "WebShell 命令执行"),
		{ID: "core.webshell_file_write", Name: "webshell_file_write", Title: "WebShell 写文件",
			Class: ClassDestructive, Runtime: RuntimeGoBuiltin, Permission: "webshell:write", Approval: ApprovalAlways,
			Grants: []CapabilityGrant{{Name: "c2.exec"}},
			Check:  ResourceBoundary("webshell:write", "webshell", "connection_id")},
		{ID: "core.webshell_file_list", Name: "webshell_file_list", Title: "WebShell 列目录",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "webshell:read",
			Check: ResourceBoundary("webshell:read", "webshell", "connection_id")},
		{ID: "core.webshell_file_read", Name: "webshell_file_read", Title: "WebShell 读文件",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "webshell:read",
			Check: ResourceBoundary("webshell:read", "webshell", "connection_id")},

		// WebShell connection management
		{ID: "core.manage_webshell_list", Name: "manage_webshell_list", Title: "列出 WebShell 连接",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "webshell:read", Approval: ApprovalNever,
			Check: Require("webshell:read")},
		{ID: "core.manage_webshell_add", Name: "manage_webshell_add", Title: "新增 WebShell 连接",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "webshell:write",
			Check: Require("webshell:write")},
		{ID: "core.manage_webshell_update", Name: "manage_webshell_update", Title: "更新 WebShell 连接",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "webshell:write",
			Check: ResourceBoundary("webshell:write", "webshell", "connection_id")},
		{ID: "core.manage_webshell_test", Name: "manage_webshell_test", Title: "测试 WebShell 连接",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "webshell:write",
			Grants: []CapabilityGrant{{Name: "net.connect", Target: "target"}},
			Check:  ResourceBoundary("webshell:write", "webshell", "connection_id")},
		{ID: "core.manage_webshell_delete", Name: "manage_webshell_delete", Title: "删除 WebShell 连接",
			Class: ClassDestructive, Runtime: RuntimeGoBuiltin, Permission: "webshell:delete", Approval: ApprovalAlways,
			Check: ResourceBoundary("webshell:delete", "webshell", "connection_id")},

		// Batch task queue
		{ID: "core.batch_task_list", Name: "batch_task_list", Title: "列出批量任务",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "tasks:read", Approval: ApprovalNever,
			Check: Require("tasks:read")},
		{ID: "core.batch_task_get", Name: "batch_task_get", Title: "读取批量任务",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "tasks:read", Approval: ApprovalNever,
			Check: Resource("tasks:read", "batch_task", "queue_id")},
		{ID: "core.batch_task_create", Name: "batch_task_create", Title: "创建批量任务",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "tasks:write",
			Check: OptionalProject(Require("tasks:write"), "tasks:write")},
		{ID: "core.batch_task_start", Name: "batch_task_start", Title: "启动批量任务",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "tasks:write", Approval: ApprovalAlways,
			Check: batchWriteQueue},
		{ID: "core.batch_task_rerun", Name: "batch_task_rerun", Title: "重跑批量任务",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "tasks:write", Approval: ApprovalAlways,
			Check: batchWriteQueue},
		{ID: "core.batch_task_pause", Name: "batch_task_pause", Title: "暂停批量任务",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "tasks:write", Check: batchWriteQueue},
		{ID: "core.batch_task_update_metadata", Name: "batch_task_update_metadata", Title: "更新任务元数据",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "tasks:write", Check: batchWriteQueue},
		{ID: "core.batch_task_update_schedule", Name: "batch_task_update_schedule", Title: "更新任务调度",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "tasks:write", Check: batchWriteQueue},
		{ID: "core.batch_task_schedule_enabled", Name: "batch_task_schedule_enabled", Title: "开关任务调度",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "tasks:write", Check: batchWriteQueue},
		{ID: "core.batch_task_add_task", Name: "batch_task_add_task", Title: "追加队列任务",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "tasks:write", Check: batchWriteQueue},
		{ID: "core.batch_task_update_task", Name: "batch_task_update_task", Title: "更新队列任务",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "tasks:write", Check: batchWriteQueue},
		{ID: "core.batch_task_delete", Name: "batch_task_delete", Title: "删除批量任务",
			Class: ClassDestructive, Runtime: RuntimeGoBuiltin, Permission: "tasks:delete", Approval: ApprovalAlways,
			Check: Resource("tasks:delete", "batch_task", "queue_id")},
		{ID: "core.batch_task_remove_task", Name: "batch_task_remove_task", Title: "移除队列任务",
			Class: ClassDestructive, Runtime: RuntimeGoBuiltin, Permission: "tasks:delete", Approval: ApprovalAlways,
			Check: Resource("tasks:delete", "batch_task", "queue_id")},

		// C2
		{ID: "core.c2_listener", Name: "c2_listener", Title: "监听器管理",
			Class: ClassDestructive, Runtime: RuntimeGoBuiltin, Permission: "c2:write", Approval: ApprovalAlways,
			Grants: []CapabilityGrant{{Name: "net.bind", Target: "listener"}},
			Check:  C2Action("c2_listener", "listener_id")},
		{ID: "core.c2_session", Name: "c2_session", Title: "会话管理",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "c2:write",
			Check: C2Action("c2_session", "session_id")},
		{ID: "core.c2_task", Name: "c2_task", Title: "任务下发",
			Class: ClassDestructive, Runtime: RuntimeGoBuiltin, Permission: "c2:write", Approval: ApprovalAlways,
			Grants: []CapabilityGrant{{Name: "c2.exec"}},
			Check:  C2Action("c2_session", "session_id")},
		{ID: "core.c2_task_manage", Name: "c2_task_manage", Title: "任务管理",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "c2:write",
			Check: C2Action("c2_task", "task_id")},
		{ID: "core.c2_file", Name: "c2_file", Title: "文件管理",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "c2:read",
			Check: ByAction(C2Action("c2_session", "session_id"), map[string]CheckFunc{
				"get_result": C2Action("c2_task", "task_id"),
			})},
		{ID: "core.c2_payload", Name: "c2_payload", Title: "Payload 生成",
			Class: ClassDestructive, Runtime: RuntimeGoBuiltin, Permission: "c2:write", Approval: ApprovalAlways,
			Grants: []CapabilityGrant{{Name: "process.exec", Target: "go build"}},
			Check:  Resource("c2:write", "c2_listener", "listener_id")},
		{ID: "core.c2_event", Name: "c2_event", Title: "事件查询",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "c2:read",
			Check: FirstMatch(
				Branch{When: WhenArgPresent("session_id"), Check: ResourceBoundary("c2:read", "c2_session", "session_id")},
				Branch{When: WhenArgPresent("task_id"), Check: ResourceBoundary("c2:read", "c2_task", "task_id")},
				Branch{When: WhenProjectFilterSet(), Check: Require("c2:read")},
				Branch{Check: GlobalScopeRequired("c2:read")},
			)},
		{ID: "core.c2_profile", Name: "c2_profile", Title: "Malleable Profile 管理",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "c2:write",
			Check: ByAction(GlobalScopeRequired("c2:write"), map[string]CheckFunc{
				"list":   Require("c2:read"),
				"get":    Require("c2:read"),
				"delete": GlobalScopeRequired("c2:delete"),
			})},

		// Internal execution query: registered so the name is never silently
		// unknown; the executor implements it.
		{ID: "core.query_execution_result", Name: "query_execution_result", Title: "查询执行结果",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "monitor:read", Approval: ApprovalNever,
			Timeout: 30 * time.Second, Check: Execution("monitor:read")},

		// Agent-local filesystem and shell tools. They are reachable from the agent
		// runtime rather than the MCP server, and they were gated by a hand-kept list
		// in internal/multiagent; declaring them here makes that middleware ask the
		// registry instead of growing its own policy copy.
		{ID: "core.execute", Name: "execute", Title: "本地命令执行",
			Class: ClassDestructive, Runtime: RuntimeGoBuiltin, Permission: "agent:local-execute",
			Approval: ApprovalInherited, Source: SourceAgentLocal, Virtual: true,
			Grants: []CapabilityGrant{{Name: "process.exec", Target: "*"}}},
		{ID: "core.write_file", Name: "write_file", Title: "写工作区文件",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "agent:local-execute",
			Source: SourceAgentLocal, Virtual: true, Grants: []CapabilityGrant{{Name: "fs.write", Target: "workspace"}}},
		{ID: "core.edit_file", Name: "edit_file", Title: "编辑工作区文件",
			Class: ClassMutating, Runtime: RuntimeGoBuiltin, Permission: "agent:local-execute",
			Source: SourceAgentLocal, Virtual: true, Grants: []CapabilityGrant{{Name: "fs.write", Target: "workspace"}}},
		{ID: "core.read_file", Name: "read_file", Title: "读工作区文件",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "agent:local-execute",
			Approval: ApprovalNever, Source: SourceAgentLocal, Virtual: true,
			Grants: []CapabilityGrant{{Name: "fs.read", Target: "workspace"}}},
		{ID: "core.ls", Name: "ls", Title: "列目录",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "agent:local-execute",
			Approval: ApprovalNever, Source: SourceAgentLocal, Virtual: true, Grants: []CapabilityGrant{{Name: "fs.read", Target: "workspace"}}},
		{ID: "core.glob", Name: "glob", Title: "文件名匹配",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "agent:local-execute",
			Approval: ApprovalNever, Source: SourceAgentLocal, Virtual: true, Grants: []CapabilityGrant{{Name: "fs.read", Target: "workspace"}}},
		{ID: "core.grep", Name: "grep", Title: "内容检索",
			Class: ClassReadonly, Runtime: RuntimeGoBuiltin, Permission: "agent:local-execute",
			Approval: ApprovalNever, Source: SourceAgentLocal, Virtual: true, Grants: []CapabilityGrant{{Name: "fs.read", Target: "workspace"}}},

		// External MCP invocation is a namespace-level policy, not a tool.
		{ID: "core.mcp_external_execute", Name: "mcp:external:execute", Title: "外部 MCP 调用",
			Class: ClassMutating, Runtime: RuntimeMCPRemote, Permission: "mcp:external:execute",
			Virtual: true,
			Check:   GlobalScopeRequired("mcp:external:execute")},
	}
}
