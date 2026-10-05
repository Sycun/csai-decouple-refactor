package handler

// OpenAPI 路径定义：ops 组。为什么必须是函数、为什么合并处对重复路径直接 panic，
// 都写在 openapi_paths.go 里，这里只放数据。

func openAPIPathsOps() map[string]interface{} {
	return map[string]interface{}{
		"/api/terminal/run": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"终端"},
				"summary":     "执行终端命令",
				"description": "在服务器上执行Shell命令并返回结果。",
				"operationId": "terminalRun",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"command"},
								"properties": map[string]interface{}{
									"command": map[string]interface{}{"type": "string", "description": "要执行的命令"},
									"shell":   map[string]interface{}{"type": "string", "description": "Shell类型（默认sh/cmd）"},
									"cwd":     map[string]interface{}{"type": "string", "description": "工作目录（可选）"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "执行完成",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"stdout":    map[string]interface{}{"type": "string", "description": "标准输出"},
										"stderr":    map[string]interface{}{"type": "string", "description": "标准错误"},
										"exit_code": map[string]interface{}{"type": "integer", "description": "退出码"},
										"error":     map[string]interface{}{"type": "string", "description": "执行错误（可选）"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/terminal/run/stream": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"终端"},
				"summary":     "流式执行终端命令",
				"description": "以SSE流式方式执行Shell命令，实时返回输出。每个事件包含 JSON: {\"t\": \"out\"|\"err\"|\"exit\", \"d\": \"数据\", \"c\": 退出码}",
				"operationId": "terminalRunStream",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"command"},
								"properties": map[string]interface{}{
									"command": map[string]interface{}{"type": "string", "description": "要执行的命令"},
									"shell":   map[string]interface{}{"type": "string", "description": "Shell类型（默认sh/cmd）"},
									"cwd":     map[string]interface{}{"type": "string", "description": "工作目录（可选）"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "SSE事件流",
						"content": map[string]interface{}{
							"text/event-stream": map[string]interface{}{
								"schema": map[string]interface{}{
									"type":        "string",
									"description": "Server-Sent Events流，每个事件为JSON: {\"t\":\"out|err|exit\",\"d\":\"data\",\"c\":exitCode}",
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/terminal/ws": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"终端"},
				"summary":     "WebSocket终端",
				"description": "通过WebSocket建立交互式终端连接，支持PTY。客户端发送文本/二进制数据作为命令输入，也可发送JSON: {\"type\":\"resize\",\"cols\":80,\"rows\":24} 调整终端大小。服务端返回二进制PTY输出。",
				"operationId": "terminalWS",
				"responses": map[string]interface{}{
					"101": map[string]interface{}{"description": "WebSocket连接已建立"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},

		// ==================== WebShell管理 ====================,
		"/api/webshell/connections": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"WebShell管理"},
				"summary":     "列出WebShell连接",
				"description": "获取所有已保存的WebShell连接配置列表。",
				"operationId": "listWebshellConnections",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "array",
									"items": map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"id":         map[string]interface{}{"type": "string", "description": "连接ID"},
											"url":        map[string]interface{}{"type": "string", "description": "WebShell URL"},
											"password":   map[string]interface{}{"type": "string", "description": "连接密码"},
											"type":       map[string]interface{}{"type": "string", "description": "Shell类型", "enum": []string{"php", "asp", "aspx", "jsp", "custom"}},
											"method":     map[string]interface{}{"type": "string", "description": "请求方法", "enum": []string{"get", "post"}},
											"cmd_param":  map[string]interface{}{"type": "string", "description": "命令参数名"},
											"remark":     map[string]interface{}{"type": "string", "description": "备注"},
											"created_at": map[string]interface{}{"type": "string", "format": "date-time"},
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
				"tags":        []string{"WebShell管理"},
				"summary":     "创建WebShell连接",
				"description": "保存一个新的WebShell连接配置。",
				"operationId": "createWebshellConnection",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"url"},
								"properties": map[string]interface{}{
									"url":       map[string]interface{}{"type": "string", "description": "WebShell URL"},
									"password":  map[string]interface{}{"type": "string", "description": "连接密码"},
									"type":      map[string]interface{}{"type": "string", "description": "Shell类型", "enum": []string{"php", "asp", "aspx", "jsp", "custom"}},
									"method":    map[string]interface{}{"type": "string", "description": "请求方法", "enum": []string{"get", "post"}},
									"cmd_param": map[string]interface{}{"type": "string", "description": "命令参数名"},
									"remark":    map[string]interface{}{"type": "string", "description": "备注"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "创建成功"},
					"400": map[string]interface{}{"description": "参数错误"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/webshell/connections/{id}": map[string]interface{}{
			"put": map[string]interface{}{
				"tags":        []string{"WebShell管理"},
				"summary":     "更新WebShell连接",
				"description": "更新已有的WebShell连接配置。",
				"operationId": "updateWebshellConnection",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "description": "连接ID", "schema": map[string]interface{}{"type": "string"}},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"url":       map[string]interface{}{"type": "string"},
									"password":  map[string]interface{}{"type": "string"},
									"type":      map[string]interface{}{"type": "string", "enum": []string{"php", "asp", "aspx", "jsp", "custom"}},
									"method":    map[string]interface{}{"type": "string", "enum": []string{"get", "post"}},
									"cmd_param": map[string]interface{}{"type": "string"},
									"remark":    map[string]interface{}{"type": "string"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "更新成功"},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "连接不存在"},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"WebShell管理"},
				"summary":     "删除WebShell连接",
				"description": "删除指定的WebShell连接配置。",
				"operationId": "deleteWebshellConnection",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "description": "连接ID", "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "删除成功"},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "连接不存在"},
				},
			},
		},
		"/api/webshell/connections/{id}/state": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"WebShell管理"},
				"summary":     "获取连接状态",
				"description": "获取WebShell连接的保存状态数据。",
				"operationId": "getWebshellConnectionState",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "description": "连接ID", "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"state": map[string]interface{}{"type": "object", "description": "状态数据（任意JSON）"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
			"put": map[string]interface{}{
				"tags":        []string{"WebShell管理"},
				"summary":     "保存连接状态",
				"description": "保存WebShell连接的状态数据。",
				"operationId": "saveWebshellConnectionState",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "description": "连接ID", "schema": map[string]interface{}{"type": "string"}},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"state": map[string]interface{}{"type": "object", "description": "状态数据（任意JSON）"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "保存成功"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/webshell/connections/{id}/ai-history": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"WebShell管理"},
				"summary":     "获取AI对话历史",
				"description": "获取指定WebShell连接的AI辅助对话历史消息。",
				"operationId": "getWebshellAIHistory",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "description": "连接ID", "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"conversationId": map[string]interface{}{"type": "string"},
										"messages": map[string]interface{}{
											"type": "array",
											"items": map[string]interface{}{
												"type": "object",
												"properties": map[string]interface{}{
													"id":        map[string]interface{}{"type": "string"},
													"role":      map[string]interface{}{"type": "string"},
													"content":   map[string]interface{}{"type": "string"},
													"createdAt": map[string]interface{}{"type": "string", "format": "date-time"},
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
		"/api/webshell/connections/{id}/ai-conversations": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"WebShell管理"},
				"summary":     "列出AI对话",
				"description": "获取指定WebShell连接的所有AI辅助对话列表。",
				"operationId": "listWebshellAIConversations",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "description": "连接ID", "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "array",
									"items": map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"id":        map[string]interface{}{"type": "string"},
											"title":     map[string]interface{}{"type": "string"},
											"createdAt": map[string]interface{}{"type": "string", "format": "date-time"},
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
		"/api/webshell/exec": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"WebShell管理"},
				"summary":     "执行WebShell命令",
				"description": "通过指定的WebShell连接执行远程命令。",
				"operationId": "webshellExec",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"url", "command"},
								"properties": map[string]interface{}{
									"url":       map[string]interface{}{"type": "string", "description": "WebShell URL"},
									"password":  map[string]interface{}{"type": "string"},
									"type":      map[string]interface{}{"type": "string", "enum": []string{"php", "asp", "aspx", "jsp", "custom"}},
									"method":    map[string]interface{}{"type": "string", "enum": []string{"get", "post"}},
									"cmd_param": map[string]interface{}{"type": "string"},
									"command":   map[string]interface{}{"type": "string", "description": "要执行的命令"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "执行结果",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"ok":        map[string]interface{}{"type": "boolean"},
										"output":    map[string]interface{}{"type": "string", "description": "命令输出"},
										"error":     map[string]interface{}{"type": "string", "description": "错误信息"},
										"http_code": map[string]interface{}{"type": "integer", "description": "HTTP响应码"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/webshell/file": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"WebShell管理"},
				"summary":     "WebShell文件操作",
				"description": "通过WebShell执行远程文件操作（列目录、读写文件、创建目录、重命名、删除、上传等）。",
				"operationId": "webshellFileOp",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"url", "action", "path"},
								"properties": map[string]interface{}{
									"url":         map[string]interface{}{"type": "string", "description": "WebShell URL"},
									"password":    map[string]interface{}{"type": "string"},
									"type":        map[string]interface{}{"type": "string", "enum": []string{"php", "asp", "aspx", "jsp", "custom"}},
									"method":      map[string]interface{}{"type": "string", "enum": []string{"get", "post"}},
									"cmd_param":   map[string]interface{}{"type": "string"},
									"action":      map[string]interface{}{"type": "string", "description": "操作类型", "enum": []string{"list", "read", "delete", "write", "mkdir", "rename", "upload", "upload_chunk"}},
									"path":        map[string]interface{}{"type": "string", "description": "目标文件/目录路径"},
									"target_path": map[string]interface{}{"type": "string", "description": "目标路径（rename时使用）"},
									"content":     map[string]interface{}{"type": "string", "description": "文件内容（write/upload时使用）"},
									"chunk_index": map[string]interface{}{"type": "integer", "description": "分块索引（upload_chunk时使用）"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "操作结果",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"ok":     map[string]interface{}{"type": "boolean"},
										"output": map[string]interface{}{"type": "string"},
										"error":  map[string]interface{}{"type": "string"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},

		// ==================== 对话附件 ====================,
		"/api/robot/wecom": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"机器人集成"},
				"summary":     "企业微信回调验证",
				"description": "企业微信服务器URL验证回调（用于配置消息接收地址时的验证）。无需认证。",
				"operationId": "wecomCallbackVerify",
				"security":    []map[string]interface{}{},
				"parameters": []map[string]interface{}{
					{"name": "msg_signature", "in": "query", "required": true, "schema": map[string]interface{}{"type": "string"}},
					{"name": "timestamp", "in": "query", "required": true, "schema": map[string]interface{}{"type": "string"}},
					{"name": "nonce", "in": "query", "required": true, "schema": map[string]interface{}{"type": "string"}},
					{"name": "echostr", "in": "query", "required": true, "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "验证成功，返回解密后的echostr"},
				},
			},
			"post": map[string]interface{}{
				"tags":        []string{"机器人集成"},
				"summary":     "企业微信消息回调",
				"description": "接收企业微信推送的消息事件。无需认证，由企业微信服务器调用。",
				"operationId": "wecomCallbackMessage",
				"security":    []map[string]interface{}{},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "处理成功"},
				},
			},
		},
		"/api/robot/dingtalk": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"机器人集成"},
				"summary":     "钉钉消息回调",
				"description": "接收钉钉推送的消息事件。无需认证，由钉钉服务器调用。",
				"operationId": "dingtalkCallback",
				"security":    []map[string]interface{}{},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "处理成功"},
				},
			},
		},
		"/api/robot/lark": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"机器人集成"},
				"summary":     "飞书消息回调",
				"description": "接收飞书推送的消息事件。无需认证，由飞书服务器调用。",
				"operationId": "larkCallback",
				"security":    []map[string]interface{}{},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "处理成功"},
				},
			},
		},
		"/api/robot/test": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"机器人集成"},
				"summary":     "测试机器人消息处理",
				"description": "模拟机器人消息处理流程，用于调试和验证。需要登录认证。",
				"operationId": "testRobot",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"platform", "text"},
								"properties": map[string]interface{}{
									"platform": map[string]interface{}{"type": "string", "description": "平台类型", "enum": []string{"dingtalk", "lark", "wecom"}},
									"user_id":  map[string]interface{}{"type": "string", "description": "模拟用户ID", "example": "test"},
									"text":     map[string]interface{}{"type": "string", "description": "消息文本", "example": "帮助"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "处理成功"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},

		// ==================== 多代理Markdown ====================,
	}
}
