package handler

// OpenAPI components：文档里所有 $ref 指向的 schema 与 securityScheme。
// 与路径定义一样必须是函数而不是包级变量 —— 见 openapi_paths.go 的说明。

func openAPIComponents() map[string]interface{} {
	return map[string]interface{}{
		"securitySchemes": map[string]interface{}{
			"bearerAuth": map[string]interface{}{
				"type":         "http",
				"scheme":       "bearer",
				"bearerFormat": "JWT",
				"description":  "使用Bearer Token进行认证。Token通过 /api/auth/login 接口获取。",
			},
		},
		"schemas": map[string]interface{}{
			"CreateConversationRequest": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title": map[string]interface{}{
						"type":        "string",
						"description": "对话标题",
						"example":     "Web应用安全测试",
					},
					"projectId": map[string]interface{}{
						"type":        "string",
						"description": "绑定的项目 ID（可选，共享事实黑板）",
					},
				},
			},
			"SetConversationProjectRequest": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"projectId": map[string]interface{}{
						"type":        "string",
						"description": "项目 ID；空字符串表示解除绑定",
					},
				},
				"required": []string{"projectId"},
			},
			"AgentChatResponse": map[string]interface{}{
				"type":        "object",
				"description": "Agent 非流式响应。response 只是交付文本；是否为成功最终回复必须以 finalized/finalizable/status 为准。",
				"properties": map[string]interface{}{
					"response": map[string]interface{}{
						"type":        "string",
						"description": "交付给用户的文本。finalized=false 时为阻断/未完成说明，不是成功结论。",
					},
					"conversationId": map[string]interface{}{
						"type":        "string",
						"description": "对话 ID",
					},
					"assistantMessageId": map[string]interface{}{
						"type":        "string",
						"description": "助手消息 ID（部分接口返回）",
					},
					"mcpExecutionIds": map[string]interface{}{
						"type":        "array",
						"description": "本轮关联的 MCP 工具执行 ID",
						"items":       map[string]interface{}{"type": "string"},
					},
					"agentMode": map[string]interface{}{
						"type":        "string",
						"description": "agent 模式，例如 eino_single、eino_deep、workflow",
					},
					"finalized": map[string]interface{}{
						"type":        "boolean",
						"description": "是否已经通过最终回复检查。只有 true 才能当成功最终回复。",
					},
					"finalizable": map[string]interface{}{
						"type":        "boolean",
						"description": "候选输出是否可提升为最终回复。",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "最终化状态",
						"enum":        []string{"completed", "in_progress", "blocked", "failed", "cancelled", "awaiting_hitl"},
					},
					"completionReason": map[string]interface{}{
						"type":        "string",
						"description": "最终化或阻断原因，例如 verified、pending_tool_executions、missing_execution_evidence",
					},
					"evidenceVerified": map[string]interface{}{
						"type":        "boolean",
						"description": "证据是否满足最终化要求",
					},
					"evidenceRefs": map[string]interface{}{
						"type":        "array",
						"description": "证据引用，例如 mcp_execution:<id>",
						"items":       map[string]interface{}{"type": "string"},
					},
					"pendingExecutionIds": map[string]interface{}{
						"type":        "array",
						"description": "仍处于 queued/running 的工具执行 ID",
						"items":       map[string]interface{}{"type": "string"},
					},
					"missingChecks": map[string]interface{}{
						"type":        "array",
						"description": "未通过最终化检查的原因列表",
						"items":       map[string]interface{}{"type": "string"},
					},
				},
				"required": []string{"response", "conversationId", "finalized", "finalizable", "status", "evidenceVerified"},
			},
			"Conversation": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "对话ID",
						"example":     "550e8400-e29b-41d4-a716-446655440000",
					},
					"title": map[string]interface{}{
						"type":        "string",
						"description": "对话标题",
						"example":     "Web应用安全测试",
					},
					"createdAt": map[string]interface{}{
						"type":        "string",
						"format":      "date-time",
						"description": "创建时间",
					},
					"updatedAt": map[string]interface{}{
						"type":        "string",
						"format":      "date-time",
						"description": "更新时间",
					},
					"projectId": map[string]interface{}{
						"type":        "string",
						"description": "绑定的项目 ID（可选）",
					},
				},
			},
			"ConversationDetail": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "对话ID",
					},
					"title": map[string]interface{}{
						"type":        "string",
						"description": "对话标题",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "对话状态：active（进行中）、completed（已完成）、failed（失败）",
						"enum":        []string{"active", "completed", "failed"},
					},
					"createdAt": map[string]interface{}{
						"type":        "string",
						"format":      "date-time",
						"description": "创建时间",
					},
					"updatedAt": map[string]interface{}{
						"type":        "string",
						"format":      "date-time",
						"description": "更新时间",
					},
					"messages": map[string]interface{}{
						"type":        "array",
						"description": "消息列表",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/Message",
						},
					},
					"messageCount": map[string]interface{}{
						"type":        "integer",
						"description": "消息数量",
					},
				},
			},
			"Message": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "消息ID",
					},
					"conversationId": map[string]interface{}{
						"type":        "string",
						"description": "对话ID",
					},
					"role": map[string]interface{}{
						"type":        "string",
						"description": "消息角色：user（用户）、assistant（助手）",
						"enum":        []string{"user", "assistant"},
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "消息内容",
					},
					"createdAt": map[string]interface{}{
						"type":        "string",
						"format":      "date-time",
						"description": "创建时间",
					},
				},
			},
			"ConversationResults": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"conversationId": map[string]interface{}{
						"type":        "string",
						"description": "对话ID",
					},
					"messages": map[string]interface{}{
						"type":        "array",
						"description": "消息列表",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/Message",
						},
					},
					"vulnerabilities": map[string]interface{}{
						"type":        "array",
						"description": "发现的漏洞列表",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/Vulnerability",
						},
					},
					"executionResults": map[string]interface{}{
						"type":        "array",
						"description": "执行结果列表",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/ExecutionResult",
						},
					},
				},
			},
			"Vulnerability": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "漏洞ID",
					},
					"title": map[string]interface{}{
						"type":        "string",
						"description": "漏洞标题",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "漏洞描述",
					},
					"severity": map[string]interface{}{
						"type":        "string",
						"description": "严重程度",
						"enum":        []string{"critical", "high", "medium", "low", "info"},
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "状态",
						"enum":        []string{"open", "confirmed", "fixed", "false_positive", "ignored"},
					},
					"target": map[string]interface{}{
						"type":        "string",
						"description": "受影响的目标",
					},
				},
			},
			"AssetImportItem": map[string]interface{}{
				"type":        "object",
				"description": "待导入资产；host、ip、domain 至少一项非空",
				"properties": map[string]interface{}{
					"project_id":         map[string]interface{}{"type": "string", "description": "所属项目 ID；调用者必须有权访问"},
					"host":               map[string]interface{}{"type": "string", "maxLength": 500, "example": "https://app.example.com:443"},
					"ip":                 map[string]interface{}{"type": "string", "example": "192.0.2.10"},
					"port":               map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 65535, "example": 443},
					"domain":             map[string]interface{}{"type": "string", "example": "app.example.com"},
					"protocol":           map[string]interface{}{"type": "string", "example": "https"},
					"title":              map[string]interface{}{"type": "string", "maxLength": 500},
					"server":             map[string]interface{}{"type": "string", "maxLength": 255, "example": "nginx"},
					"country":            map[string]interface{}{"type": "string"},
					"province":           map[string]interface{}{"type": "string"},
					"city":               map[string]interface{}{"type": "string"},
					"responsible_person": map[string]interface{}{"type": "string", "maxLength": 255, "description": "资产负责人"},
					"department":         map[string]interface{}{"type": "string", "maxLength": 255, "description": "所属部门"},
					"business_system":    map[string]interface{}{"type": "string", "maxLength": 255, "description": "所属业务系统"},
					"environment":        map[string]interface{}{"type": "string", "enum": []string{"production", "staging", "testing", "development", "other"}},
					"criticality":        map[string]interface{}{"type": "string", "enum": []string{"critical", "high", "medium", "low"}},
					"source":             map[string]interface{}{"type": "string"},
					"source_query":       map[string]interface{}{"type": "string"},
					"status":             map[string]interface{}{"type": "string", "enum": []string{"active", "inactive"}, "default": "active"},
					"tags": map[string]interface{}{
						"type":     "array",
						"maxItems": 30,
						"items":    map[string]interface{}{"type": "string", "maxLength": 64},
					},
				},
			},
			"AssetImportRequest": map[string]interface{}{
				"type":     "object",
				"required": []string{"assets"},
				"properties": map[string]interface{}{
					"assets": map[string]interface{}{
						"type":     "array",
						"minItems": 1,
						"maxItems": 100000,
						"items":    map[string]interface{}{"$ref": "#/components/schemas/AssetImportItem"},
					},
					"source":       map[string]interface{}{"type": "string", "description": "未在资产中填写来源时使用的默认来源"},
					"source_query": map[string]interface{}{"type": "string", "description": "默认来源查询或导入文件名"},
				},
			},
			"AssetImportResult": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"created": map[string]interface{}{"type": "integer", "description": "新建数量", "example": 120},
					"updated": map[string]interface{}{"type": "integer", "description": "去重合并数量", "example": 8},
					"skipped": map[string]interface{}{"type": "integer", "description": "跳过数量", "example": 2},
				},
			},
			"ExecutionResult": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "执行ID",
					},
					"toolName": map[string]interface{}{
						"type":        "string",
						"description": "工具名称",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "执行状态",
						"enum":        []string{"queued", "running", "completed", "failed", "cancelled", "hard_timeout", "orphaned"},
					},
					"result": map[string]interface{}{
						"type":        "string",
						"description": "执行结果",
					},
					"createdAt": map[string]interface{}{
						"type":        "string",
						"format":      "date-time",
						"description": "创建时间",
					},
				},
			},
			"Error": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"error": map[string]interface{}{
						"type":        "string",
						"description": "错误信息",
					},
				},
			},
			"LoginRequest": map[string]interface{}{
				"type":     "object",
				"required": []string{"password"},
				"properties": map[string]interface{}{
					"password": map[string]interface{}{
						"type":        "string",
						"description": "登录密码",
					},
				},
			},
			"LoginResponse": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"token": map[string]interface{}{
						"type":        "string",
						"description": "认证Token",
					},
					"expires_at": map[string]interface{}{
						"type":        "string",
						"format":      "date-time",
						"description": "Token过期时间",
					},
					"session_duration_hr": map[string]interface{}{
						"type":        "integer",
						"description": "会话持续时间（小时）",
					},
				},
			},
			"ChangePasswordRequest": map[string]interface{}{
				"type":     "object",
				"required": []string{"oldPassword", "newPassword"},
				"properties": map[string]interface{}{
					"oldPassword": map[string]interface{}{
						"type":        "string",
						"description": "当前密码",
					},
					"newPassword": map[string]interface{}{
						"type":        "string",
						"description": "新密码（至少8位）",
					},
				},
			},
			"UpdateConversationRequest": map[string]interface{}{
				"type":     "object",
				"required": []string{"title"},
				"properties": map[string]interface{}{
					"title": map[string]interface{}{
						"type":        "string",
						"description": "对话标题",
					},
				},
			},
			"BatchTaskRequest": map[string]interface{}{
				"type":     "object",
				"required": []string{"tasks"},
				"properties": map[string]interface{}{
					"title": map[string]interface{}{
						"type":        "string",
						"description": "任务标题（可选）",
					},
					"tasks": map[string]interface{}{
						"type":        "array",
						"description": "任务列表，每行一个任务",
						"items": map[string]interface{}{
							"type": "string",
						},
					},
					"role": map[string]interface{}{
						"type":        "string",
						"description": "角色名称（可选）",
					},
					"agentMode": map[string]interface{}{
						"type":        "string",
						"description": "代理模式：eino_single（Eino ADK 单代理，默认）| deep | plan_execute | supervisor",
						"enum":        []string{"eino_single", "deep", "plan_execute", "supervisor"},
					},
					"scheduleMode": map[string]interface{}{
						"type":        "string",
						"description": "调度方式（manual | cron）",
						"enum":        []string{"manual", "cron"},
					},
					"cronExpr": map[string]interface{}{
						"type":        "string",
						"description": "Cron 表达式（scheduleMode=cron 时必填）",
					},
					"executeNow": map[string]interface{}{
						"type":        "boolean",
						"description": "是否创建后立即执行（默认 false）",
					},
				},
			},
			"BatchQueue": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "队列ID",
					},
					"title": map[string]interface{}{
						"type":        "string",
						"description": "队列标题",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "队列状态",
						"enum":        []string{"pending", "running", "paused", "completed", "failed"},
					},
					"tasks": map[string]interface{}{
						"type":        "array",
						"description": "任务列表",
						"items": map[string]interface{}{
							"type": "object",
						},
					},
					"createdAt": map[string]interface{}{
						"type":        "string",
						"format":      "date-time",
						"description": "创建时间",
					},
				},
			},
			"CancelAgentLoopRequest": map[string]interface{}{
				"type":     "object",
				"required": []string{"conversationId"},
				"properties": map[string]interface{}{
					"conversationId": map[string]interface{}{
						"type":        "string",
						"description": "对话ID",
					},
					"reason": map[string]interface{}{
						"type":        "string",
						"description": "可选。与 MCP 监控页「终止并说明」一致：非空时合并进当前工具返回给模型的文本（含 USER INTERRUPT NOTE 块）",
					},
					"continueAfter": map[string]interface{}{
						"type":        "boolean",
						"description": "为 true 时仅终止当前进行中的 MCP 工具调用（不取消整轮任务）；须已有工具在执行，否则 400",
					},
				},
			},
			"AgentTask": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"conversationId": map[string]interface{}{
						"type":        "string",
						"description": "对话ID",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "任务状态",
						"enum":        []string{"running", "completed", "failed", "cancelled", "timeout"},
					},
					"startedAt": map[string]interface{}{
						"type":        "string",
						"format":      "date-time",
						"description": "开始时间",
					},
				},
			},
			"CreateVulnerabilityRequest": map[string]interface{}{
				"type":     "object",
				"required": []string{"conversation_id", "title", "description", "severity", "type", "target", "reproduction_steps", "evidence", "impact", "recommendation"},
				"properties": map[string]interface{}{
					"conversation_id": map[string]interface{}{
						"type":        "string",
						"description": "对话ID",
					},
					"title": map[string]interface{}{
						"type":        "string",
						"description": "漏洞标题",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "漏洞描述",
					},
					"severity": map[string]interface{}{
						"type":        "string",
						"description": "严重程度",
						"enum":        []string{"critical", "high", "medium", "low", "info"},
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "状态",
						"enum":        []string{"open", "closed", "fixed"},
					},
					"type": map[string]interface{}{
						"type":        "string",
						"description": "漏洞类型",
					},
					"target": map[string]interface{}{
						"type":        "string",
						"description": "受影响的目标",
					},
					"preconditions":      map[string]interface{}{"type": "string", "description": "前置条件"},
					"reproduction_steps": map[string]interface{}{"type": "string", "description": "复现步骤"},
					"evidence":           map[string]interface{}{"type": "string", "description": "证据/POC，包含请求响应、命令输出、截图说明、日志等"},
					"impact": map[string]interface{}{
						"type":        "string",
						"description": "影响",
					},
					"recommendation": map[string]interface{}{
						"type":        "string",
						"description": "修复建议",
					},
					"retest_notes": map[string]interface{}{"type": "string", "description": "复测方式"},
				},
			},
			"UpdateVulnerabilityRequest": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title": map[string]interface{}{
						"type":        "string",
						"description": "漏洞标题",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "漏洞描述",
					},
					"severity": map[string]interface{}{
						"type":        "string",
						"description": "严重程度",
						"enum":        []string{"critical", "high", "medium", "low", "info"},
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "状态",
						"enum":        []string{"open", "confirmed", "fixed", "false_positive", "ignored"},
					},
					"type": map[string]interface{}{
						"type":        "string",
						"description": "漏洞类型",
					},
					"target": map[string]interface{}{
						"type":        "string",
						"description": "受影响的目标",
					},
					"preconditions":      map[string]interface{}{"type": "string", "description": "前置条件"},
					"reproduction_steps": map[string]interface{}{"type": "string", "description": "复现步骤"},
					"evidence":           map[string]interface{}{"type": "string", "description": "证据/POC，包含请求响应、命令输出、截图说明、日志等"},
					"impact": map[string]interface{}{
						"type":        "string",
						"description": "影响",
					},
					"recommendation": map[string]interface{}{
						"type":        "string",
						"description": "修复建议",
					},
					"retest_notes": map[string]interface{}{"type": "string", "description": "复测方式"},
				},
			},
			"ListVulnerabilitiesResponse": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"vulnerabilities": map[string]interface{}{
						"type":        "array",
						"description": "漏洞列表",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/Vulnerability",
						},
					},
					"total": map[string]interface{}{
						"type":        "integer",
						"description": "总数",
					},
					"page": map[string]interface{}{
						"type":        "integer",
						"description": "当前页",
					},
					"page_size": map[string]interface{}{
						"type":        "integer",
						"description": "每页数量",
					},
					"total_pages": map[string]interface{}{
						"type":        "integer",
						"description": "总页数",
					},
				},
			},
			"VulnerabilityStats": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"total": map[string]interface{}{
						"type":        "integer",
						"description": "总漏洞数",
					},
					"by_severity": map[string]interface{}{
						"type":        "object",
						"description": "按严重程度统计",
					},
					"by_status": map[string]interface{}{
						"type":        "object",
						"description": "按状态统计",
					},
				},
			},
			"RoleConfig": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "角色名称",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "角色描述",
					},
					"enabled": map[string]interface{}{
						"type":        "boolean",
						"description": "是否启用",
					},
					"systemPrompt": map[string]interface{}{
						"type":        "string",
						"description": "系统提示词",
					},
					"userPrompt": map[string]interface{}{
						"type":        "string",
						"description": "用户提示词",
					},
					"tools": map[string]interface{}{
						"type":        "array",
						"description": "工具列表",
						"items": map[string]interface{}{
							"type": "string",
						},
					},
				},
			},
			"Skill": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Skill名称",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "Skill描述",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Skill路径",
					},
				},
			},
			"CreateSkillRequest": map[string]interface{}{
				"type":     "object",
				"required": []string{"name", "description"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Skill名称",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "Skill描述",
					},
				},
			},
			"UpdateSkillRequest": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"description": map[string]interface{}{
						"type":        "string",
						"description": "Skill描述",
					},
				},
			},
			"ToolExecution": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "执行ID",
					},
					"toolName": map[string]interface{}{
						"type":        "string",
						"description": "工具名称",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "执行状态",
						"enum":        []string{"queued", "running", "completed", "failed", "cancelled", "hard_timeout", "orphaned"},
					},
					"createdAt": map[string]interface{}{
						"type":        "string",
						"format":      "date-time",
						"description": "创建时间",
					},
				},
			},
			"MonitorResponse": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"executions": map[string]interface{}{
						"type":        "array",
						"description": "执行记录列表（轻量字段，不含 arguments/result）",
						"items": map[string]interface{}{
							"$ref": "#/components/schemas/ToolExecution",
						},
					},
					"summary": map[string]interface{}{
						"type":        "object",
						"description": "工具调用汇总",
					},
					"topTools": map[string]interface{}{
						"type":        "array",
						"description": "调用量 Top N 工具",
						"items": map[string]interface{}{
							"type": "object",
						},
					},
					"timestamp": map[string]interface{}{
						"type":        "string",
						"format":      "date-time",
						"description": "时间戳",
					},
					"total": map[string]interface{}{
						"type":        "integer",
						"description": "执行记录总数",
					},
					"page": map[string]interface{}{
						"type":        "integer",
						"description": "当前页",
					},
					"pageSize": map[string]interface{}{
						"type":        "integer",
						"description": "每页数量",
					},
					"totalPages": map[string]interface{}{
						"type":        "integer",
						"description": "总页数",
					},
					"retentionDays": map[string]interface{}{
						"type":        "integer",
						"description": "执行记录保留天数",
					},
				},
			},
			"ConfigResponse": map[string]interface{}{
				"type":        "object",
				"description": "配置信息（含 openai、vision、multi_agent 等）",
				"properties": map[string]interface{}{
					"agent": map[string]interface{}{
						"$ref": "#/components/schemas/AgentConfig",
					},
					"vision": map[string]interface{}{
						"$ref": "#/components/schemas/VisionConfig",
					},
				},
			},
			"UpdateConfigRequest": map[string]interface{}{
				"type":        "object",
				"description": "更新配置请求",
				"properties": map[string]interface{}{
					"agent": map[string]interface{}{
						"$ref": "#/components/schemas/AgentConfig",
					},
					"vision": map[string]interface{}{
						"$ref": "#/components/schemas/VisionConfig",
					},
				},
			},
			"AgentConfig": map[string]interface{}{
				"type":        "object",
				"description": "Agent 运行与外部 MCP 防卡死保护配置",
				"properties": map[string]interface{}{
					"max_iterations":                         map[string]interface{}{"type": "integer", "description": "最大迭代次数"},
					"tool_timeout_minutes":                   map[string]interface{}{"type": "integer", "description": "单次工具执行硬超时（分钟）"},
					"tool_wait_timeout_seconds":              map[string]interface{}{"type": "integer", "description": "工具单轮等待秒数；到时返回 execution_id，worker 继续后台执行"},
					"external_mcp_max_concurrent_per_server": map[string]interface{}{"type": "integer", "description": "单个外部 MCP server 并发上限；0=默认2；负数=不限制"},
					"external_mcp_max_concurrent_total":      map[string]interface{}{"type": "integer", "description": "外部 MCP 全局并发上限；0=默认16；负数=不限制"},
					"external_mcp_circuit_failure_threshold": map[string]interface{}{"type": "integer", "description": "连续失败熔断阈值；0=默认3；负数=关闭熔断"},
					"external_mcp_circuit_cooldown_seconds":  map[string]interface{}{"type": "integer", "description": "熔断冷却秒数；0=默认60"},
					"shell_no_output_timeout_seconds":        map[string]interface{}{"type": "integer", "description": "execute/exec 连续无输出终止秒数"},
					"workspace_root_dir":                     map[string]interface{}{"type": "string", "description": "会话工作目录根路径"},
					"system_prompt_path":                     map[string]interface{}{"type": "string", "description": "单代理系统提示文件路径"},
				},
			},
			"VisionConfig": map[string]interface{}{
				"type":        "object",
				"description": "视觉分析（analyze_image MCP 工具）；enabled 且 model 非空时注册工具",
				"properties": map[string]interface{}{
					"enabled":                     map[string]interface{}{"type": "boolean", "description": "是否启用 analyze_image"},
					"model":                       map[string]interface{}{"type": "string", "description": "视觉模型名（必填）", "example": "qwen-vl-max"},
					"api_key":                     map[string]interface{}{"type": "string", "description": "API Key；留空复用 openai.api_key"},
					"base_url":                    map[string]interface{}{"type": "string", "description": "Base URL；留空复用 openai.base_url"},
					"provider":                    map[string]interface{}{"type": "string", "description": "提供商；留空复用 openai.provider"},
					"timeout_seconds":             map[string]interface{}{"type": "integer", "description": "VL 调用超时（秒）"},
					"max_image_bytes":             map[string]interface{}{"type": "integer", "description": "原始文件大小上限（字节）"},
					"max_dimension":               map[string]interface{}{"type": "integer", "description": "长边缩放像素"},
					"jpeg_quality":                map[string]interface{}{"type": "integer", "description": "JPEG 质量 60-100"},
					"max_payload_bytes":           map[string]interface{}{"type": "integer", "description": "送 API 体积上限（字节）"},
					"skip_preprocess_below_bytes": map[string]interface{}{"type": "integer", "description": "低于该字节且尺寸合规时可原图直传；0=始终压缩"},
					"detail":                      map[string]interface{}{"type": "string", "enum": []string{"low", "high", "auto"}, "description": "OpenAI 兼容 image detail"},
				},
			},
			"AnalyzeImageToolCall": map[string]interface{}{
				"type":        "object",
				"description": "内置 MCP 工具 analyze_image：分析服务器本地图片，返回纯文本（验证码/UI/报错等）",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "图片绝对路径或相对于进程工作目录的路径",
					},
					"question": map[string]interface{}{
						"type":        "string",
						"description": "可选：重点问题；验证码建议「只输出验证码字符」",
					},
				},
				"required": []string{"path"},
			},
			"ExternalMCPConfig": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"enabled": map[string]interface{}{
						"type":        "boolean",
						"description": "是否启用",
					},
					"command": map[string]interface{}{
						"type":        "string",
						"description": "命令",
					},
					"args": map[string]interface{}{
						"type":        "array",
						"description": "参数列表",
						"items": map[string]interface{}{
							"type": "string",
						},
					},
				},
			},
			"ExternalMCPResponse": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"config": map[string]interface{}{
						"$ref": "#/components/schemas/ExternalMCPConfig",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "状态",
						"enum":        []string{"connected", "disconnected", "error", "disabled"},
					},
					"toolCount": map[string]interface{}{
						"type":        "integer",
						"description": "工具数量",
					},
					"error": map[string]interface{}{
						"type":        "string",
						"description": "错误信息",
					},
				},
			},
			"AddOrUpdateExternalMCPRequest": map[string]interface{}{
				"type":     "object",
				"required": []string{"config"},
				"properties": map[string]interface{}{
					"config": map[string]interface{}{
						"$ref": "#/components/schemas/ExternalMCPConfig",
					},
				},
			},
			"AttackChain": map[string]interface{}{
				"type":        "object",
				"description": "攻击链数据",
			},
			"MCPMessage": map[string]interface{}{
				"type":        "object",
				"description": "MCP消息（符合JSON-RPC 2.0规范）",
				"required":    []string{"jsonrpc"},
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"description": "消息ID，可以是字符串、数字或null。对于请求，必须提供；对于通知，可以省略",
						"oneOf": []map[string]interface{}{
							{"type": "string"},
							{"type": "number"},
							{"type": "null"},
						},
						"example": "550e8400-e29b-41d4-a716-446655440000",
					},
					"method": map[string]interface{}{
						"type":        "string",
						"description": "方法名。支持的方法：\n- `initialize`: 初始化MCP连接\n- `tools/list`: 列出所有可用工具\n- `tools/call`: 调用工具\n- `prompts/list`: 列出所有提示词模板\n- `prompts/get`: 获取提示词模板\n- `resources/list`: 列出所有资源\n- `resources/read`: 读取资源内容\n- `sampling/request`: 采样请求",
						"enum": []string{
							"initialize",
							"tools/list",
							"tools/call",
							"prompts/list",
							"prompts/get",
							"resources/list",
							"resources/read",
							"sampling/request",
						},
						"example": "tools/list",
					},
					"params": map[string]interface{}{
						"description": "方法参数（JSON对象），根据不同的method有不同的结构",
						"type":        "object",
					},
					"jsonrpc": map[string]interface{}{
						"type":        "string",
						"description": "JSON-RPC版本，固定为\"2.0\"",
						"enum":        []string{"2.0"},
						"example":     "2.0",
					},
				},
			},
			"MCPInitializeParams": map[string]interface{}{
				"type":     "object",
				"required": []string{"protocolVersion", "capabilities", "clientInfo"},
				"properties": map[string]interface{}{
					"protocolVersion": map[string]interface{}{
						"type":        "string",
						"description": "协议版本",
						"example":     "2024-11-05",
					},
					"capabilities": map[string]interface{}{
						"type":        "object",
						"description": "客户端能力",
					},
					"clientInfo": map[string]interface{}{
						"type":     "object",
						"required": []string{"name", "version"},
						"properties": map[string]interface{}{
							"name": map[string]interface{}{
								"type":        "string",
								"description": "客户端名称",
								"example":     "MyClient",
							},
							"version": map[string]interface{}{
								"type":        "string",
								"description": "客户端版本",
								"example":     "1.0.0",
							},
						},
					},
				},
			},
			"MCPCallToolParams": map[string]interface{}{
				"type":     "object",
				"required": []string{"name", "arguments"},
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "工具名称",
						"example":     "nmap",
					},
					"arguments": map[string]interface{}{
						"type":        "object",
						"description": "工具参数（键值对），具体参数取决于工具定义",
						"example": map[string]interface{}{
							"target": "192.168.1.1",
							"ports":  "80,443",
						},
					},
				},
			},
			"MCPResponse": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"description": "消息ID（与请求中的id相同）",
						"oneOf": []map[string]interface{}{
							{"type": "string"},
							{"type": "number"},
							{"type": "null"},
						},
					},
					"result": map[string]interface{}{
						"description": "方法执行结果（JSON对象），结构取决于调用的方法",
						"type":        "object",
					},
					"error": map[string]interface{}{
						"type":        "object",
						"description": "错误信息（如果执行失败）",
						"properties": map[string]interface{}{
							"code": map[string]interface{}{
								"type":        "integer",
								"description": "错误代码",
								"example":     -32600,
							},
							"message": map[string]interface{}{
								"type":        "string",
								"description": "错误消息",
								"example":     "Invalid Request",
							},
							"data": map[string]interface{}{
								"description": "错误详情（可选）",
							},
						},
					},
					"jsonrpc": map[string]interface{}{
						"type":        "string",
						"description": "JSON-RPC版本",
						"example":     "2.0",
					},
				},
			},
		},
	}
}

// openAPIFinalizationRequestSchema 在多处被引用，单独成名。
var openAPIFinalizationRequestSchema = map[string]interface{}{
	"type":        "object",
	"description": "最终回复交付策略。后端不会从自然语言内容推断执行意图；执行入口应显式声明是否要求 completed 工具证据。",
	"properties": map[string]interface{}{
		"requireExecutionEvidence": map[string]interface{}{
			"type":        "boolean",
			"description": "为 true 时，缺少 completed 工具执行记录会触发无注入续跑或最终阻断；普通聊天可省略或设为 false。",
		},
	},
}
