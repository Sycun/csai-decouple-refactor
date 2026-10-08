package handler

// OpenAPI 路径定义：chat 组。为什么必须是函数、为什么合并处对重复路径直接 panic，
// 都写在 openapi_paths.go 里，这里只放数据。

func openAPIPathsChat() map[string]interface{} {
	return map[string]interface{}{
		"/api/auth/login": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"认证"},
				"summary":     "用户登录",
				"description": "使用密码登录获取认证Token",
				"operationId": "login",
				"security":    []map[string]interface{}{},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/LoginRequest",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "登录成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/LoginResponse",
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "密码错误",
					},
				},
			},
		},
		"/api/auth/logout": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"认证"},
				"summary":     "用户登出",
				"description": "登出当前会话，使Token失效",
				"operationId": "logout",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "登出成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"message": map[string]interface{}{
											"type":    "string",
											"example": "已退出登录",
										},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/auth/change-password": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"认证"},
				"summary":     "修改密码",
				"description": "修改登录密码，修改后所有会话将失效",
				"operationId": "changePassword",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/ChangePasswordRequest",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "密码修改成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"message": map[string]interface{}{
											"type":    "string",
											"example": "密码已更新，请使用新密码重新登录",
										},
									},
								},
							},
						},
					},
					"400": map[string]interface{}{
						"description": "请求参数错误",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/auth/validate": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"认证"},
				"summary":     "验证Token",
				"description": "验证当前Token是否有效",
				"operationId": "validateToken",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "Token有效",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"token": map[string]interface{}{
											"type":        "string",
											"description": "Token",
										},
										"expires_at": map[string]interface{}{
											"type":        "string",
											"format":      "date-time",
											"description": "过期时间",
										},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "Token无效或已过期",
					},
				},
			},
		},
		"/api/conversations": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"对话管理"},
				"summary":     "创建对话",
				"description": "创建一个新的安全测试对话。\n**重要说明**：\n- ✅ 创建的对话会**立即保存到数据库**\n- ✅ 前端页面会**自动刷新**显示新对话\n- ✅ 与前端创建的对话**完全一致**\n**创建对话的两种方式**：\n**方式1（推荐）：** 直接使用 `/api/eino-agent` 发送消息，**不提供** `conversationId` 参数，系统会自动创建新对话并发送消息。这是最简单的方式，一步完成创建和发送。\n**方式2：** 先调用此端点创建空对话，然后使用返回的 `conversationId` 调用 `/api/eino-agent` 发送消息。适用于需要先创建对话，稍后再发送消息的场景。\n**示例**：\n```json\n{\n  \"title\": \"Web应用安全测试\"\n}\n```",
				"operationId": "createConversation",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/CreateConversationRequest",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "对话创建成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/Conversation",
								},
							},
						},
					},
					"400": map[string]interface{}{
						"description": "请求参数错误",
					},
					"401": map[string]interface{}{
						"description": "未授权，需要有效的Token",
					},
					"500": map[string]interface{}{
						"description": "服务器内部错误",
					},
				},
			},
			"get": map[string]interface{}{
				"tags":        []string{"对话管理"},
				"summary":     "列出对话",
				"description": "获取对话列表，支持分页和搜索",
				"operationId": "listConversations",
				"parameters": []map[string]interface{}{
					{
						"name":        "limit",
						"in":          "query",
						"required":    false,
						"description": "返回数量限制",
						"schema": map[string]interface{}{
							"type":    "integer",
							"default": 50,
							"minimum": 1,
							"maximum": 100,
						},
					},
					{
						"name":        "offset",
						"in":          "query",
						"required":    false,
						"description": "偏移量",
						"schema": map[string]interface{}{
							"type":    "integer",
							"default": 0,
							"minimum": 0,
						},
					},
					{
						"name":        "search",
						"in":          "query",
						"required":    false,
						"description": "搜索关键词",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					{
						"name":        "project_id",
						"in":          "query",
						"required":    false,
						"description": "按项目筛选；传 __none__ 表示仅未绑定项目的对话",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					{
						"name":        "sort_by",
						"in":          "query",
						"required":    false,
						"description": "排序字段：updated_at（默认）或 created_at",
						"schema": map[string]interface{}{
							"type": "string",
							"enum": []string{"updated_at", "created_at"},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "array",
									"items": map[string]interface{}{
										"$ref": "#/components/schemas/Conversation",
									},
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权，需要有效的Token",
					},
				},
			},
		},
		"/api/conversations/{id}": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"对话管理"},
				"summary":     "查看对话详情",
				"description": "获取指定对话的详细信息，包括对话信息和消息列表",
				"operationId": "getConversation",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "对话ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/ConversationDetail",
								},
							},
						},
					},
					"404": map[string]interface{}{
						"description": "对话不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权，需要有效的Token",
					},
				},
			},
			"put": map[string]interface{}{
				"tags":        []string{"对话管理"},
				"summary":     "更新对话",
				"description": "更新对话标题",
				"operationId": "updateConversation",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "对话ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/UpdateConversationRequest",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "更新成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/Conversation",
								},
							},
						},
					},
					"400": map[string]interface{}{
						"description": "请求参数错误",
					},
					"404": map[string]interface{}{
						"description": "对话不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权，需要有效的Token",
					},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"对话管理"},
				"summary":     "删除对话",
				"description": "删除指定的对话及其会话数据（消息、攻击链等）。**漏洞记录会保留**，仅解除与会话的关联。**此操作不可恢复**。",
				"operationId": "deleteConversation",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "对话ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "删除成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"message": map[string]interface{}{
											"type":        "string",
											"description": "成功消息",
											"example":     "删除成功",
										},
									},
								},
							},
						},
					},
					"404": map[string]interface{}{
						"description": "对话不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权，需要有效的Token",
					},
					"500": map[string]interface{}{
						"description": "服务器内部错误",
					},
				},
			},
		},
		"/api/conversations/{id}/project": map[string]interface{}{
			"put": map[string]interface{}{
				"tags":        []string{"对话管理"},
				"summary":     "设置对话所属项目",
				"description": "绑定或解除对话与项目的关联，用于共享事实黑板",
				"operationId": "setConversationProject",
				"parameters": []map[string]interface{}{
					{
						"name": "id", "in": "path", "required": true,
						"description": "对话ID",
						"schema":      map[string]interface{}{"type": "string"},
					},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/SetConversationProjectRequest",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "设置成功"},
					"400": map[string]interface{}{"description": "项目不存在或参数错误"},
					"404": map[string]interface{}{"description": "对话不存在"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/conversations/{id}/results": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"对话管理"},
				"summary":     "获取对话结果",
				"description": "获取指定对话的执行结果，包括消息、漏洞信息和执行结果",
				"operationId": "getConversationResults",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "对话ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/ConversationResults",
								},
							},
						},
					},
					"404": map[string]interface{}{
						"description": "对话不存在或结果不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权，需要有效的Token",
					},
				},
			},
		},
		"/api/agent-modes": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"对话交互"},
				"summary":     "对话模式目录（内置单代理 + 已激活的多代理模式）",
				"description": "返回此刻的对话模式目录：`eino_single` 是内核内置、恒在且可用；`deep` / `plan_execute` / `supervisor` 随「多代理编排包」的安装进入目录，卸载或停用即从目录消失（不点不存在）。多代理模式在 `multi_agent.enabled=false` 时返回 `available=false`、`reason=engine_disabled`。对话页、WebShell 助手、批量队列与机器人「模式」命令都以此为准。",
				"operationId": "listAgentModes",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"default": map[string]interface{}{"type": "string"},
										"modes": map[string]interface{}{
											"type": "array",
											"items": map[string]interface{}{
												"type": "object",
												"properties": map[string]interface{}{
													"id":        map[string]interface{}{"type": "string"},
													"label":     map[string]interface{}{"type": "string"},
													"labelKey":  map[string]interface{}{"type": "string"},
													"hintKey":   map[string]interface{}{"type": "string"},
													"runner":    map[string]interface{}{"type": "string", "description": "执行器：eino_single（单代理）或 multi_agent（多代理编排）"},
													"available": map[string]interface{}{"type": "boolean"},
													"reason":    map[string]interface{}{"type": "string"},
													"builtin":   map[string]interface{}{"type": "boolean"},
													"bundle":    map[string]interface{}{"type": "string"},
												},
											},
										},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/eino-agent": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"对话交互"},
				"summary":     "发送消息并获取 AI 回复（Eino ADK 单代理，非流式）",
				"description": "向 AI 发送消息并获取回复（非流式）。由 **CloudWeGo Eino** `adk.NewChatModelAgent` + `adk.NewRunner.Run` 执行单代理 MCP 工具链。**不依赖** `multi_agent.enabled`；`multi_agent.eino_skills` / `eino_middleware` 等与多代理主代理一致时可生效。支持 `webshellConnectionId`、角色与附件。",
				"operationId": "sendMessageEinoSingleAgent",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"message":              map[string]interface{}{"type": "string"},
									"conversationId":       map[string]interface{}{"type": "string"},
									"role":                 map[string]interface{}{"type": "string"},
									"webshellConnectionId": map[string]interface{}{"type": "string"},
									"finalization":         openAPIFinalizationRequestSchema,
								},
								"required": []string{"message"},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "成功。只有 finalized=true 表示成功最终回复；finalized=false 时 response 为未完成/阻断说明。",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{"$ref": "#/components/schemas/AgentChatResponse"},
							},
						},
					},
					"400": map[string]interface{}{"description": "参数错误"},
					"401": map[string]interface{}{"description": "未授权"},
					"500": map[string]interface{}{"description": "执行失败"},
				},
			},
		},
		"/api/eino-agent/stream": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"对话交互"},
				"summary":     "发送消息并获取 AI 回复（Eino ADK 单代理，SSE）",
				"description": "向 AI 发送消息并获取流式回复（SSE）。由 Eino **单代理** ADK 执行；事件类型与多代理流式一致（含 `tool_call` / `response_delta` / `thinking` 等）。`response_start` / `response_delta` 仅为候选/过程输出；只有 `type: response` 且 `data.finalized=true` 才表示成功最终回复。缺 completed 执行证据时可能先发送 `finalization_auto_continue`，表示服务端基于已有 trace 无注入续跑。`data.finalized=false` 时 message 为未完成/阻断说明。**不依赖** `multi_agent.enabled`。",
				"operationId": "sendMessageEinoSingleAgentStream",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"message":              map[string]interface{}{"type": "string"},
									"conversationId":       map[string]interface{}{"type": "string"},
									"role":                 map[string]interface{}{"type": "string"},
									"webshellConnectionId": map[string]interface{}{"type": "string"},
									"finalization":         openAPIFinalizationRequestSchema,
								},
								"required": []string{"message"},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "text/event-stream（SSE）",
						"content": map[string]interface{}{
							"text/event-stream": map[string]interface{}{
								"schema": map[string]interface{}{
									"type":        "string",
									"description": "SSE 流。终态 response 事件 data 包含 finalized、finalizable、status、completionReason、evidenceVerified、evidenceRefs、pendingExecutionIds、missingChecks；过程事件可能包含 finalization_auto_continue。",
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/multi-agent": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"对话交互"},
				"summary":     "发送消息并获取 AI 回复（Eino 多代理，非流式）",
				"description": "与 `POST /api/eino-agent` 请求体相同，但由 **CloudWeGo Eino** 多代理执行。编排由请求体 `orchestration`（`deep` | `plan_execute` | `supervisor`）指定，缺省为 `deep`。**前提**：`multi_agent.enabled: true`；未启用时返回 404 JSON。支持 `webshellConnectionId`。",
				"operationId": "sendMessageMultiAgent",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"message": map[string]interface{}{
										"type":        "string",
										"description": "要发送的消息（必需）",
									},
									"conversationId": map[string]interface{}{
										"type":        "string",
										"description": "对话 ID（可选，不提供则新建）",
									},
									"role": map[string]interface{}{
										"type":        "string",
										"description": "角色名称（可选）",
									},
									"webshellConnectionId": map[string]interface{}{
										"type":        "string",
										"description": "WebShell 连接 ID（可选，与 Eino 单/多代理流式行为一致）",
									},
									"finalization": openAPIFinalizationRequestSchema,
									"orchestration": map[string]interface{}{
										"type":        "string",
										"description": "Eino 预置编排：deep | plan_execute | supervisor；缺省 deep",
										"enum":        []string{"deep", "plan_execute", "supervisor"},
									},
								},
								"required": []string{"message"},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "成功。只有 finalized=true 表示成功最终回复；finalized=false 时 response 为未完成/阻断说明。",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{"$ref": "#/components/schemas/AgentChatResponse"},
							},
						},
					},
					"400": map[string]interface{}{"description": "参数错误"},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "多代理未启用或对话不存在"},
					"500": map[string]interface{}{"description": "执行失败"},
				},
			},
		},
		"/api/multi-agent/stream": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"对话交互"},
				"summary":     "发送消息并获取 AI 回复（Eino 多代理，SSE）",
				"description": "与 `POST /api/eino-agent/stream` 类似；由 Eino 多代理执行。`orchestration` 指定 deep / plan_execute / supervisor，缺省 deep。`response_start` / `response_delta` 仅为候选/过程输出；只有 `type: response` 且 `data.finalized=true` 才表示成功最终回复。缺 completed 执行证据时可能先发送 `finalization_auto_continue`，表示服务端基于已有 trace 无注入续跑。**前提**：`multi_agent.enabled: true`；未启用时 SSE 内首条为 `type: error` 后接 `done`。支持 `webshellConnectionId`。",
				"operationId": "sendMessageMultiAgentStream",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"message":              map[string]interface{}{"type": "string"},
									"conversationId":       map[string]interface{}{"type": "string"},
									"role":                 map[string]interface{}{"type": "string"},
									"webshellConnectionId": map[string]interface{}{"type": "string"},
									"finalization":         openAPIFinalizationRequestSchema,
									"orchestration": map[string]interface{}{
										"type":        "string",
										"description": "deep | plan_execute | supervisor；缺省 deep",
										"enum":        []string{"deep", "plan_execute", "supervisor"},
									},
								},
								"required": []string{"message"},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "text/event-stream（SSE）",
						"content": map[string]interface{}{
							"text/event-stream": map[string]interface{}{
								"schema": map[string]interface{}{
									"type":        "string",
									"description": "SSE 流。终态 response 事件 data 包含 finalized、finalizable、status、completionReason、evidenceVerified、evidenceRefs、pendingExecutionIds、missingChecks；过程事件可能包含 finalization_auto_continue。",
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/agent-loop/cancel": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"对话交互"},
				"summary":     "取消任务",
				"description": "取消正在执行的Agent Loop任务",
				"operationId": "cancelAgentLoop",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/CancelAgentLoopRequest",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "取消请求已提交",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"status": map[string]interface{}{
											"type":    "string",
											"example": "cancelling",
										},
										"conversationId": map[string]interface{}{
											"type":        "string",
											"description": "对话ID",
										},
										"message": map[string]interface{}{
											"type":    "string",
											"example": "已提交取消请求，任务将在当前步骤完成后停止。",
										},
									},
								},
							},
						},
					},
					"404": map[string]interface{}{
						"description": "未找到正在执行的任务",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/agent-loop/tasks": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"对话交互"},
				"summary":     "列出运行中的任务",
				"description": "获取所有正在运行的Agent Loop任务",
				"operationId": "listAgentTasks",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"tasks": map[string]interface{}{
											"type":        "array",
											"description": "任务列表",
											"items": map[string]interface{}{
												"$ref": "#/components/schemas/AgentTask",
											},
										},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/agent-loop/tasks/completed": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"对话交互"},
				"summary":     "列出已完成的任务",
				"description": "获取最近完成的Agent Loop任务历史",
				"operationId": "listCompletedTasks",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"tasks": map[string]interface{}{
											"type":        "array",
											"description": "已完成任务列表",
											"items": map[string]interface{}{
												"$ref": "#/components/schemas/AgentTask",
											},
										},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/batch-tasks": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "创建批量任务队列",
				"description": "创建一个批量任务队列，包含多个任务",
				"operationId": "createBatchQueue",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/BatchTaskRequest",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "创建成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"queueId": map[string]interface{}{
											"type":        "string",
											"description": "队列ID",
										},
										"queue": map[string]interface{}{
											"$ref": "#/components/schemas/BatchQueue",
										},
										"started": map[string]interface{}{
											"type":        "boolean",
											"description": "是否已立即启动执行",
										},
									},
								},
							},
						},
					},
					"400": map[string]interface{}{
						"description": "请求参数错误",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"get": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "列出批量任务队列",
				"description": "获取所有批量任务队列",
				"operationId": "listBatchQueues",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"queues": map[string]interface{}{
											"type":        "array",
											"description": "队列列表",
											"items": map[string]interface{}{
												"$ref": "#/components/schemas/BatchQueue",
											},
										},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/batch-tasks/{queueId}": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "获取批量任务队列",
				"description": "获取指定批量任务队列的详细信息",
				"operationId": "getBatchQueue",
				"parameters": []map[string]interface{}{
					{
						"name":        "queueId",
						"in":          "path",
						"required":    true,
						"description": "队列ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/BatchQueue",
								},
							},
						},
					},
					"404": map[string]interface{}{
						"description": "队列不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "删除批量任务队列",
				"description": "删除指定的批量任务队列",
				"operationId": "deleteBatchQueue",
				"parameters": []map[string]interface{}{
					{
						"name":        "queueId",
						"in":          "path",
						"required":    true,
						"description": "队列ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "删除成功",
					},
					"404": map[string]interface{}{
						"description": "队列不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/batch-tasks/{queueId}/start": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "启动批量任务队列",
				"description": "开始执行批量任务队列中的任务",
				"operationId": "startBatchQueue",
				"parameters": []map[string]interface{}{
					{
						"name":        "queueId",
						"in":          "path",
						"required":    true,
						"description": "队列ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "启动成功",
					},
					"404": map[string]interface{}{
						"description": "队列不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/batch-tasks/{queueId}/pause": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "暂停批量任务队列",
				"description": "暂停正在执行的批量任务队列",
				"operationId": "pauseBatchQueue",
				"parameters": []map[string]interface{}{
					{
						"name":        "queueId",
						"in":          "path",
						"required":    true,
						"description": "队列ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "暂停成功",
					},
					"404": map[string]interface{}{
						"description": "队列不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/batch-tasks/{queueId}/tasks": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "添加任务到队列",
				"description": "向批量任务队列添加新任务。任务会添加到队列末尾，按照队列顺序依次执行。每个任务会创建一个独立的对话，支持完整的状态跟踪。\n**任务格式**：\n任务内容是一个字符串，描述要执行的安全测试任务。例如：\n- \"扫描 http://example.com 的SQL注入漏洞\"\n- \"对 192.168.1.1 进行端口扫描\"\n- \"检测 https://target.com 的XSS漏洞\"\n**使用示例**：\n```json\n{\n  \"task\": \"扫描 http://example.com 的SQL注入漏洞\"\n}\n```",
				"operationId": "addBatchTask",
				"parameters": []map[string]interface{}{
					{
						"name":        "queueId",
						"in":          "path",
						"required":    true,
						"description": "队列ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"task"},
								"properties": map[string]interface{}{
									"task": map[string]interface{}{
										"type":        "string",
										"description": "任务内容，描述要执行的安全测试任务（必需）",
										"example":     "扫描 http://example.com 的SQL注入漏洞",
									},
								},
							},
							"examples": map[string]interface{}{
								"sqlInjection": map[string]interface{}{
									"summary":     "SQL注入扫描",
									"description": "扫描目标网站的SQL注入漏洞",
									"value": map[string]interface{}{
										"task": "扫描 http://example.com 的SQL注入漏洞",
									},
								},
								"portScan": map[string]interface{}{
									"summary":     "端口扫描",
									"description": "对目标IP进行端口扫描",
									"value": map[string]interface{}{
										"task": "对 192.168.1.1 进行端口扫描",
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "添加成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"taskId": map[string]interface{}{
											"type":        "string",
											"description": "新添加的任务ID",
										},
										"message": map[string]interface{}{
											"type":        "string",
											"description": "成功消息",
											"example":     "任务已添加到队列",
										},
									},
								},
							},
						},
					},
					"400": map[string]interface{}{
						"description": "请求参数错误（如task为空）",
					},
					"404": map[string]interface{}{
						"description": "队列不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/batch-tasks/{queueId}/tasks/{taskId}": map[string]interface{}{
			"put": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "更新批量任务",
				"description": "更新批量任务队列中的指定任务",
				"operationId": "updateBatchTask",
				"parameters": []map[string]interface{}{
					{
						"name":        "queueId",
						"in":          "path",
						"required":    true,
						"description": "队列ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					{
						"name":        "taskId",
						"in":          "path",
						"required":    true,
						"description": "任务ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"task": map[string]interface{}{
										"type":        "string",
										"description": "任务内容",
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "更新成功",
					},
					"404": map[string]interface{}{
						"description": "任务不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "删除批量任务",
				"description": "从批量任务队列中删除指定任务",
				"operationId": "deleteBatchTask",
				"parameters": []map[string]interface{}{
					{
						"name":        "queueId",
						"in":          "path",
						"required":    true,
						"description": "队列ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					{
						"name":        "taskId",
						"in":          "path",
						"required":    true,
						"description": "任务ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "删除成功",
					},
					"404": map[string]interface{}{
						"description": "任务不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/monitor": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"监控"},
				"summary":     "获取监控信息",
				"description": "获取工具执行监控信息，支持分页和筛选",
				"operationId": "monitor",
				"parameters": []map[string]interface{}{
					{
						"name":        "page",
						"in":          "query",
						"required":    false,
						"description": "页码",
						"schema": map[string]interface{}{
							"type":    "integer",
							"default": 1,
							"minimum": 1,
						},
					},
					{
						"name":        "page_size",
						"in":          "query",
						"required":    false,
						"description": "每页数量",
						"schema": map[string]interface{}{
							"type":    "integer",
							"default": 20,
							"minimum": 1,
							"maximum": 100,
						},
					},
					{
						"name":        "status",
						"in":          "query",
						"required":    false,
						"description": "状态筛选",
						"schema": map[string]interface{}{
							"type": "string",
							"enum": []string{"queued", "running", "completed", "failed", "cancelled", "hard_timeout", "orphaned"},
						},
					},
					{
						"name":        "tool",
						"in":          "query",
						"required":    false,
						"description": "工具名称筛选（支持部分匹配）",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/MonitorResponse",
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/monitor/execution/{id}": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"监控"},
				"summary":     "获取执行记录",
				"description": "获取指定执行记录的详细信息",
				"operationId": "getExecution",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "执行ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/ToolExecution",
								},
							},
						},
					},
					"404": map[string]interface{}{
						"description": "执行记录不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"监控"},
				"summary":     "删除执行记录",
				"description": "删除指定的执行记录",
				"operationId": "deleteExecution",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "执行ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "删除成功",
					},
					"404": map[string]interface{}{
						"description": "执行记录不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/monitor/execution/{id}/cancel": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"监控"},
				"summary":     "取消进行中的工具执行",
				"description": "对当前进程内正在执行的 MCP 工具调用发送 context 取消信号；上层对话/多步任务可继续。若执行已结束或未在本进程内运行则返回 404。",
				"operationId": "cancelExecution",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "执行ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"requestBody": map[string]interface{}{
					"required": false,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"note": map[string]interface{}{
										"type":        "string",
										"description": "可选。非空时与工具已返回输出合并交给大模型，并带有「用户终止说明」标题块以便与命令行原文区分",
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "已发送终止信号",
					},
					"400": map[string]interface{}{
						"description": "请求体不是合法 JSON",
					},
					"404": map[string]interface{}{
						"description": "未找到进行中的工具执行",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/monitor/executions": map[string]interface{}{
			"delete": map[string]interface{}{
				"tags":        []string{"监控"},
				"summary":     "批量删除执行记录",
				"description": "批量删除执行记录",
				"operationId": "deleteExecutions",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "删除成功",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/monitor/stats": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"监控"},
				"summary":     "获取统计信息",
				"description": "获取工具执行统计信息",
				"operationId": "getStats",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type":        "object",
									"description": "统计信息",
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/conversations/{id}/pinned": map[string]interface{}{
			"put": map[string]interface{}{
				"tags":        []string{"对话管理"},
				"summary":     "设置对话置顶",
				"description": "设置或取消对话的置顶状态",
				"operationId": "updateConversationPinned",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "对话ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"pinned"},
								"properties": map[string]interface{}{
									"pinned": map[string]interface{}{
										"type":        "boolean",
										"description": "是否置顶",
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "更新成功",
					},
					"404": map[string]interface{}{
						"description": "对话不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/conversations/{id}/delete-turn": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"对话交互"},
				"summary":     "删除对话轮次",
				"description": "删除指定消息所在的对话轮次（从该轮 user 消息到下一轮 user 消息之前的所有消息），并清空 last_react 状态。",
				"operationId": "deleteConversationTurn",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "对话ID",
						"schema":      map[string]interface{}{"type": "string"},
					},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"messageId"},
								"properties": map[string]interface{}{
									"messageId": map[string]interface{}{
										"type":        "string",
										"description": "锚点消息ID，标识要删除的轮次",
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "删除成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"deletedMessageIds": map[string]interface{}{
											"type":        "array",
											"items":       map[string]interface{}{"type": "string"},
											"description": "被删除的消息ID列表",
										},
										"message": map[string]interface{}{
											"type":    "string",
											"example": "ok",
										},
									},
								},
							},
						},
					},
					"400": map[string]interface{}{"description": "参数错误或删除失败"},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "对话不存在"},
				},
			},
		},
		"/api/messages/{id}/process-details": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"对话交互"},
				"summary":     "获取消息过程详情",
				"description": "按需分页加载指定消息的执行过程详情，包括工具调用、思考过程等事件。默认返回 50 条；导出或旧集成需要全量时可显式传 full=1。",
				"operationId": "getMessageProcessDetails",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "消息ID",
						"schema":      map[string]interface{}{"type": "string"},
					},
					{
						"name":        "summary",
						"in":          "query",
						"required":    false,
						"description": "仅返回过程详情摘要（total / iterationCount / maxIteration）",
						"schema":      map[string]interface{}{"type": "boolean"},
					},
					{
						"name":        "limit",
						"in":          "query",
						"required":    false,
						"description": "分页大小，默认 50，最大 500",
						"schema":      map[string]interface{}{"type": "integer", "default": 50, "maximum": 500},
					},
					{
						"name":        "offset",
						"in":          "query",
						"required":    false,
						"description": "分页偏移量，默认 0",
						"schema":      map[string]interface{}{"type": "integer", "default": 0},
					},
					{
						"name":        "full",
						"in":          "query",
						"required":    false,
						"description": "显式返回全量过程详情；仅建议导出/兼容旧集成使用",
						"schema":      map[string]interface{}{"type": "boolean"},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"processDetails": map[string]interface{}{
											"type": "array",
											"items": map[string]interface{}{
												"type": "object",
												"properties": map[string]interface{}{
													"id":             map[string]interface{}{"type": "string", "description": "详情记录ID"},
													"messageId":      map[string]interface{}{"type": "string", "description": "所属消息ID"},
													"conversationId": map[string]interface{}{"type": "string", "description": "所属对话ID"},
													"eventType":      map[string]interface{}{"type": "string", "description": "事件类型（如tool_call, thinking等）"},
													"message":        map[string]interface{}{"type": "string", "description": "事件消息"},
													"data":           map[string]interface{}{"description": "事件附加数据（JSON对象）"},
													"createdAt":      map[string]interface{}{"type": "string", "format": "date-time", "description": "创建时间"},
												},
											},
										},
										"total":   map[string]interface{}{"type": "integer", "description": "过程详情总数"},
										"offset":  map[string]interface{}{"type": "integer", "description": "当前分页偏移量"},
										"limit":   map[string]interface{}{"type": "integer", "description": "当前分页大小"},
										"hasMore": map[string]interface{}{"type": "boolean", "description": "是否还有更多过程详情"},
										"summary": map[string]interface{}{
											"type":        "object",
											"description": "summary=1 时返回的摘要",
										},
									},
								},
							},
						},
					},
					"400": map[string]interface{}{"description": "参数错误"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},

		// ==================== 批量任务 - 缺失端点 ====================,
		"/api/batch-tasks/{queueId}/rerun": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "重跑批量任务队列",
				"description": "重置已完成或已取消的批量任务队列，重新开始执行所有任务。",
				"operationId": "rerunBatchQueue",
				"parameters": []map[string]interface{}{
					{
						"name":        "queueId",
						"in":          "path",
						"required":    true,
						"description": "队列ID",
						"schema":      map[string]interface{}{"type": "string"},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "重跑成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"message": map[string]interface{}{"type": "string", "example": "批量任务已重新开始执行"},
										"queueId": map[string]interface{}{"type": "string", "description": "队列ID"},
									},
								},
							},
						},
					},
					"400": map[string]interface{}{"description": "仅已完成或已取消的队列可以重跑"},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "队列不存在"},
				},
			},
		},
		"/api/batch-tasks/{queueId}/metadata": map[string]interface{}{
			"put": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "修改队列元数据",
				"description": "修改批量任务队列的标题、角色和代理模式。",
				"operationId": "updateBatchQueueMetadata",
				"parameters": []map[string]interface{}{
					{
						"name":        "queueId",
						"in":          "path",
						"required":    true,
						"description": "队列ID",
						"schema":      map[string]interface{}{"type": "string"},
					},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"title":     map[string]interface{}{"type": "string", "description": "队列标题"},
									"role":      map[string]interface{}{"type": "string", "description": "使用的角色名称"},
									"agentMode": map[string]interface{}{"type": "string", "description": "代理模式", "enum": []string{"eino_single", "deep", "plan_execute", "supervisor"}},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "更新成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"queue": map[string]interface{}{"$ref": "#/components/schemas/BatchQueue"},
									},
								},
							},
						},
					},
					"400": map[string]interface{}{"description": "参数错误"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/batch-tasks/{queueId}/schedule": map[string]interface{}{
			"put": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "修改队列调度配置",
				"description": "修改批量任务队列的调度模式和Cron表达式。队列运行中无法修改。",
				"operationId": "updateBatchQueueSchedule",
				"parameters": []map[string]interface{}{
					{
						"name":        "queueId",
						"in":          "path",
						"required":    true,
						"description": "队列ID",
						"schema":      map[string]interface{}{"type": "string"},
					},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"scheduleMode": map[string]interface{}{"type": "string", "description": "调度模式", "enum": []string{"manual", "cron"}},
									"cronExpr":     map[string]interface{}{"type": "string", "description": "Cron表达式（scheduleMode为cron时必填）", "example": "0 2 * * *"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "更新成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"queue": map[string]interface{}{"$ref": "#/components/schemas/BatchQueue"},
									},
								},
							},
						},
					},
					"400": map[string]interface{}{"description": "参数错误或队列正在运行中"},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "队列不存在"},
				},
			},
		},
		"/api/batch-tasks/{queueId}/schedule-enabled": map[string]interface{}{
			"put": map[string]interface{}{
				"tags":        []string{"批量任务"},
				"summary":     "开关Cron自动调度",
				"description": "开启或关闭批量任务队列的Cron自动调度功能，手工执行不受影响。",
				"operationId": "setBatchQueueScheduleEnabled",
				"parameters": []map[string]interface{}{
					{
						"name":        "queueId",
						"in":          "path",
						"required":    true,
						"description": "队列ID",
						"schema":      map[string]interface{}{"type": "string"},
					},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"scheduleEnabled"},
								"properties": map[string]interface{}{
									"scheduleEnabled": map[string]interface{}{"type": "boolean", "description": "是否启用自动调度"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "设置成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"queue": map[string]interface{}{"$ref": "#/components/schemas/BatchQueue"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "队列不存在"},
				},
			},
		},
		// ==================== FOFA信息收集 ====================,
		"/api/chat-uploads": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"对话附件"},
				"summary":     "列出附件",
				"description": "获取对话文件列表，包含手动上传附件、工具输出和会话产物，可按会话、项目、来源、文件名搜索和分页过滤。",
				"operationId": "listChatUploads",
				"parameters": []map[string]interface{}{
					{"name": "conversation", "in": "query", "required": false, "description": "按对话ID过滤", "schema": map[string]interface{}{"type": "string"}},
					{"name": "project", "in": "query", "required": false, "description": "按项目ID过滤", "schema": map[string]interface{}{"type": "string"}},
					{"name": "source", "in": "query", "required": false, "description": "按来源过滤：upload/reduction/workspace/conversation_artifact/all", "schema": map[string]interface{}{"type": "string", "enum": []string{"all", "upload", "reduction", "workspace", "conversation_artifact"}}},
					{"name": "search", "in": "query", "required": false, "description": "按文件名或子路径搜索", "schema": map[string]interface{}{"type": "string"}},
					{"name": "page", "in": "query", "required": false, "description": "页码，从1开始", "schema": map[string]interface{}{"type": "integer", "default": 1}},
					{"name": "pageSize", "in": "query", "required": false, "description": "每页数量，传 all 返回全部", "schema": map[string]interface{}{"oneOf": []map[string]interface{}{{"type": "integer"}, {"type": "string", "enum": []string{"all"}}}}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"files": map[string]interface{}{
											"type": "array",
											"items": map[string]interface{}{
												"type": "object",
												"properties": map[string]interface{}{
													"relativePath":      map[string]interface{}{"type": "string"},
													"absolutePath":      map[string]interface{}{"type": "string"},
													"name":              map[string]interface{}{"type": "string"},
													"size":              map[string]interface{}{"type": "integer"},
													"modifiedUnix":      map[string]interface{}{"type": "integer"},
													"date":              map[string]interface{}{"type": "string"},
													"conversationId":    map[string]interface{}{"type": "string"},
													"conversationTitle": map[string]interface{}{"type": "string"},
													"projectId":         map[string]interface{}{"type": "string"},
													"projectName":       map[string]interface{}{"type": "string"},
													"subPath":           map[string]interface{}{"type": "string"},
													"source":            map[string]interface{}{"type": "string", "description": "upload/reduction/workspace/conversation_artifact"},
												},
											},
										},
										"folders": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
										"total":   map[string]interface{}{"type": "integer"},
										"page":    map[string]interface{}{"type": "integer"},
										"pageSize": map[string]interface{}{
											"type": "integer",
										},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
			"post": map[string]interface{}{
				"tags":        []string{"对话附件"},
				"summary":     "上传附件",
				"description": "上传文件到对话附件目录（multipart/form-data）。",
				"operationId": "uploadChatFile",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"multipart/form-data": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"file"},
								"properties": map[string]interface{}{
									"file":           map[string]interface{}{"type": "string", "format": "binary", "description": "上传的文件"},
									"conversationId": map[string]interface{}{"type": "string", "description": "关联的对话ID（可选）"},
									"relativeDir":    map[string]interface{}{"type": "string", "description": "目标目录相对路径（可选）"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "上传成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"ok":           map[string]interface{}{"type": "boolean"},
										"relativePath": map[string]interface{}{"type": "string"},
										"absolutePath": map[string]interface{}{"type": "string"},
										"name":         map[string]interface{}{"type": "string"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"对话附件"},
				"summary":     "删除附件",
				"description": "删除指定的对话附件文件。",
				"operationId": "deleteChatUpload",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"path"},
								"properties": map[string]interface{}{
									"path": map[string]interface{}{"type": "string", "description": "文件相对路径"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "删除成功"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/chat-uploads/export": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"对话附件"},
				"summary":     "导出附件",
				"description": "按当前过滤条件导出对话文件 ZIP，包含 manifest.json。",
				"operationId": "exportChatUploads",
				"parameters": []map[string]interface{}{
					{"name": "conversation", "in": "query", "required": false, "description": "按对话ID过滤", "schema": map[string]interface{}{"type": "string"}},
					{"name": "project", "in": "query", "required": false, "description": "按项目ID过滤", "schema": map[string]interface{}{"type": "string"}},
					{"name": "source", "in": "query", "required": false, "description": "按来源过滤：upload/reduction/workspace/conversation_artifact/all", "schema": map[string]interface{}{"type": "string", "enum": []string{"all", "upload", "reduction", "workspace", "conversation_artifact"}}},
					{"name": "search", "in": "query", "required": false, "description": "按文件名或子路径搜索", "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "ZIP文件下载",
						"content": map[string]interface{}{
							"application/zip": map[string]interface{}{
								"schema": map[string]interface{}{"type": "string", "format": "binary"},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/chat-uploads/download": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"对话附件"},
				"summary":     "下载附件",
				"description": "下载指定的对话附件文件。",
				"operationId": "downloadChatUpload",
				"parameters": []map[string]interface{}{
					{"name": "path", "in": "query", "required": true, "description": "文件相对路径", "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "文件下载",
						"content": map[string]interface{}{
							"application/octet-stream": map[string]interface{}{
								"schema": map[string]interface{}{"type": "string", "format": "binary"},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "文件不存在"},
				},
			},
		},
		"/api/chat-uploads/path": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"对话附件"},
				"summary":     "解析附件路径",
				"description": "将文件管理中的相对路径或内部虚拟路径解析为服务器绝对路径，用于复制文件/目录路径。",
				"operationId": "resolveChatUploadPath",
				"parameters": []map[string]interface{}{
					{"name": "path", "in": "query", "required": true, "description": "相对路径或虚拟路径（如 __workspace__/projects/<id>/csv）", "schema": map[string]interface{}{"type": "string"}},
					{"name": "kind", "in": "query", "required": false, "description": "路径类型：file/directory，默认 file", "schema": map[string]interface{}{"type": "string", "enum": []string{"file", "directory"}}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "解析成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"absolutePath": map[string]interface{}{"type": "string"},
										"isDir":        map[string]interface{}{"type": "boolean"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
					"403": map[string]interface{}{"description": "无权访问"},
					"404": map[string]interface{}{"description": "路径不存在"},
				},
			},
		},
		"/api/chat-uploads/content": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"对话附件"},
				"summary":     "获取附件文本内容",
				"description": "读取并返回文本文件的内容。",
				"operationId": "getChatUploadContent",
				"parameters": []map[string]interface{}{
					{"name": "path", "in": "query", "required": true, "description": "文件相对路径", "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"content": map[string]interface{}{"type": "string", "description": "文件文本内容"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "文件不存在"},
				},
			},
			"put": map[string]interface{}{
				"tags":        []string{"对话附件"},
				"summary":     "写入附件文本内容",
				"description": "写入或覆盖文本文件的内容。",
				"operationId": "putChatUploadContent",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"path", "content"},
								"properties": map[string]interface{}{
									"path":    map[string]interface{}{"type": "string", "description": "文件相对路径"},
									"content": map[string]interface{}{"type": "string", "description": "文件文本内容"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "写入成功"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/chat-uploads/mkdir": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"对话附件"},
				"summary":     "创建附件目录",
				"description": "在对话附件目录下创建子目录。",
				"operationId": "mkdirChatUpload",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"name"},
								"properties": map[string]interface{}{
									"parent": map[string]interface{}{"type": "string", "description": "父目录相对路径"},
									"name":   map[string]interface{}{"type": "string", "description": "目录名称"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "创建成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"ok":           map[string]interface{}{"type": "boolean"},
										"relativePath": map[string]interface{}{"type": "string"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/chat-uploads/rename": map[string]interface{}{
			"put": map[string]interface{}{
				"tags":        []string{"对话附件"},
				"summary":     "重命名附件",
				"description": "重命名对话附件文件或目录。",
				"operationId": "renameChatUpload",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"path", "newName"},
								"properties": map[string]interface{}{
									"path":    map[string]interface{}{"type": "string", "description": "当前文件相对路径"},
									"newName": map[string]interface{}{"type": "string", "description": "新名称"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "重命名成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"ok":           map[string]interface{}{"type": "boolean"},
										"relativePath": map[string]interface{}{"type": "string"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},

		// ==================== 机器人集成 ====================,
		"/api/monitor/executions/names": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"监控"},
				"summary":     "批量获取工具名称",
				"description": "根据执行ID列表批量获取对应的工具名称，消除前端N+1请求问题。",
				"operationId": "batchGetToolNames",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"ids"},
								"properties": map[string]interface{}{
									"ids": map[string]interface{}{
										"type":        "array",
										"items":       map[string]interface{}{"type": "string"},
										"description": "执行记录ID列表",
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功，返回ID到工具名称的映射",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type":                 "object",
									"additionalProperties": map[string]interface{}{"type": "string"},
									"description":          "键为执行ID，值为工具名称",
									"example":              map[string]interface{}{"exec-001": "nmap", "exec-002": "sqlmap"},
								},
							},
						},
					},
					"400": map[string]interface{}{"description": "参数错误"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},

		// ==================== 知识库 - 缺失端点 ====================,
	}
}
