package handler

// OpenAPI 路径定义：mcp 组。为什么必须是函数、为什么合并处对重复路径直接 panic，
// 都写在 openapi_paths.go 里，这里只放数据。

func openAPIPathsMCP() map[string]interface{} {
	return map[string]interface{}{
		"/api/external-mcp": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"外部MCP管理"},
				"summary":     "列出外部MCP",
				"description": "获取所有外部MCP配置和状态",
				"operationId": "getExternalMCPs",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"servers": map[string]interface{}{
											"type":        "object",
											"description": "MCP服务器配置",
											"additionalProperties": map[string]interface{}{
												"$ref": "#/components/schemas/ExternalMCPResponse",
											},
										},
										"stats": map[string]interface{}{
											"type":        "object",
											"description": "统计信息",
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
		"/api/external-mcp/stats": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"外部MCP管理"},
				"summary":     "获取外部MCP统计",
				"description": "获取外部MCP统计信息",
				"operationId": "getExternalMCPStats",
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
		"/api/external-mcp/{name}": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"外部MCP管理"},
				"summary":     "获取外部MCP",
				"description": "获取指定外部MCP的配置和状态",
				"operationId": "getExternalMCP",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "MCP名称",
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
									"$ref": "#/components/schemas/ExternalMCPResponse",
								},
							},
						},
					},
					"404": map[string]interface{}{
						"description": "MCP不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"put": map[string]interface{}{
				"tags":        []string{"外部MCP管理"},
				"summary":     "添加或更新外部MCP",
				"description": "添加新的外部MCP配置或更新现有配置。\n**传输方式**：\n支持两种传输方式：\n**1. stdio（标准输入输出）**：\n```json\n{\n  \"config\": {\n    \"enabled\": true,\n    \"command\": \"node\",\n    \"args\": [\"/path/to/mcp-server.js\"],\n    \"env\": {}\n  }\n}\n```\n**2. sse（Server-Sent Events）**：\n```json\n{\n  \"config\": {\n    \"enabled\": true,\n    \"transport\": \"sse\",\n    \"url\": \"http://127.0.0.1:8082/sse\",\n    \"timeout\": 30\n  }\n}\n```\n**配置参数说明**：\n- `enabled`: 是否启用（boolean，必需）\n- `command`: 命令（stdio模式必需，如：\"node\", \"python\"）\n- `args`: 命令参数数组（stdio模式必需）\n- `env`: 环境变量（object，可选）\n- `transport`: 传输方式（\"stdio\" 或 \"sse\"，sse模式必需）\n- `url`: SSE端点URL（sse模式必需）\n- `timeout`: 超时时间（秒，可选，默认30）\n- `description`: 描述（可选）",
				"operationId": "addOrUpdateExternalMCP",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "MCP名称（唯一标识符）",
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
								"$ref": "#/components/schemas/AddOrUpdateExternalMCPRequest",
							},
							"examples": map[string]interface{}{
								"stdio": map[string]interface{}{
									"summary":     "stdio模式配置",
									"description": "使用标准输入输出方式连接外部MCP服务器",
									"value": map[string]interface{}{
										"config": map[string]interface{}{
											"enabled":     true,
											"command":     "node",
											"args":        []string{"/path/to/mcp-server.js"},
											"env":         map[string]interface{}{},
											"timeout":     30,
											"description": "Node.js MCP服务器",
										},
									},
								},
								"sse": map[string]interface{}{
									"summary":     "SSE模式配置",
									"description": "使用Server-Sent Events方式连接外部MCP服务器",
									"value": map[string]interface{}{
										"config": map[string]interface{}{
											"enabled":     true,
											"transport":   "sse",
											"url":         "http://127.0.0.1:8082/sse",
											"timeout":     30,
											"description": "SSE MCP服务器",
										},
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "操作成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"message": map[string]interface{}{
											"type":    "string",
											"example": "外部MCP配置已保存",
										},
									},
								},
							},
						},
					},
					"400": map[string]interface{}{
						"description": "请求参数错误（如配置格式不正确、缺少必需字段等）",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/Error",
								},
								"example": map[string]interface{}{
									"error": "stdio模式需要提供command和args参数",
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"外部MCP管理"},
				"summary":     "删除外部MCP",
				"description": "删除指定的外部MCP配置",
				"operationId": "deleteExternalMCP",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "MCP名称",
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
						"description": "MCP不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/external-mcp/{name}/start": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"外部MCP管理"},
				"summary":     "启动外部MCP",
				"description": "启动指定的外部MCP服务器",
				"operationId": "startExternalMCP",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "MCP名称",
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
						"description": "MCP不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/external-mcp/{name}/stop": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"外部MCP管理"},
				"summary":     "停止外部MCP",
				"description": "停止指定的外部MCP服务器",
				"operationId": "stopExternalMCP",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "MCP名称",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "停止成功",
					},
					"404": map[string]interface{}{
						"description": "MCP不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/mcp": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"MCP"},
				"summary":     "MCP端点",
				"description": "MCP (Model Context Protocol) 端点，用于处理MCP协议请求。\n**协议说明**：\n本端点遵循 JSON-RPC 2.0 规范，支持以下方法：\n**1. initialize** - 初始化MCP连接\n```json\n{\n  \"jsonrpc\": \"2.0\",\n  \"id\": \"init-1\",\n  \"method\": \"initialize\",\n  \"params\": {\n    \"protocolVersion\": \"2024-11-05\",\n    \"capabilities\": {},\n    \"clientInfo\": {\n      \"name\": \"MyClient\",\n      \"version\": \"1.0.0\"\n    }\n  }\n}\n```\n**2. tools/list** - 列出所有可用工具\n```json\n{\n  \"jsonrpc\": \"2.0\",\n  \"id\": \"list-1\",\n  \"method\": \"tools/list\",\n  \"params\": {}\n}\n```\n**3. tools/call** - 调用工具\n```json\n{\n  \"jsonrpc\": \"2.0\",\n  \"id\": \"call-1\",\n  \"method\": \"tools/call\",\n  \"params\": {\n    \"name\": \"nmap\",\n    \"arguments\": {\n      \"target\": \"192.168.1.1\",\n      \"ports\": \"80,443\"\n    }\n  }\n}\n```\n**4. prompts/list** - 列出所有提示词模板\n```json\n{\n  \"jsonrpc\": \"2.0\",\n  \"id\": \"prompts-list-1\",\n  \"method\": \"prompts/list\",\n  \"params\": {}\n}\n```\n**5. prompts/get** - 获取提示词模板\n```json\n{\n  \"jsonrpc\": \"2.0\",\n  \"id\": \"prompt-get-1\",\n  \"method\": \"prompts/get\",\n  \"params\": {\n    \"name\": \"prompt-name\",\n    \"arguments\": {}\n  }\n}\n```\n**6. resources/list** - 列出所有资源\n```json\n{\n  \"jsonrpc\": \"2.0\",\n  \"id\": \"resources-list-1\",\n  \"method\": \"resources/list\",\n  \"params\": {}\n}\n```\n**7. resources/read** - 读取资源内容\n```json\n{\n  \"jsonrpc\": \"2.0\",\n  \"id\": \"resource-read-1\",\n  \"method\": \"resources/read\",\n  \"params\": {\n    \"uri\": \"resource://example\"\n  }\n}\n```\n**错误代码说明**：\n- `-32700`: Parse error - JSON解析错误\n- `-32600`: Invalid Request - 无效请求\n- `-32601`: Method not found - 方法不存在\n- `-32602`: Invalid params - 参数无效\n- `-32603`: Internal error - 内部错误",
				"operationId": "mcpEndpoint",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/MCPMessage",
							},
							"examples": map[string]interface{}{
								"listTools": map[string]interface{}{
									"summary":     "列出所有工具",
									"description": "获取系统中所有可用的MCP工具列表",
									"value": map[string]interface{}{
										"jsonrpc": "2.0",
										"id":      "list-tools-1",
										"method":  "tools/list",
										"params":  map[string]interface{}{},
									},
								},
								"callTool": map[string]interface{}{
									"summary":     "调用工具",
									"description": "调用指定的MCP工具",
									"value": map[string]interface{}{
										"jsonrpc": "2.0",
										"id":      "call-tool-1",
										"method":  "tools/call",
										"params": map[string]interface{}{
											"name": "nmap",
											"arguments": map[string]interface{}{
												"target": "192.168.1.1",
												"ports":  "80,443",
											},
										},
									},
								},
								"initialize": map[string]interface{}{
									"summary":     "初始化连接",
									"description": "初始化MCP连接，获取服务器能力",
									"value": map[string]interface{}{
										"jsonrpc": "2.0",
										"id":      "init-1",
										"method":  "initialize",
										"params": map[string]interface{}{
											"protocolVersion": "2024-11-05",
											"capabilities":    map[string]interface{}{},
											"clientInfo": map[string]interface{}{
												"name":    "MyClient",
												"version": "1.0.0",
											},
										},
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "MCP响应（JSON-RPC 2.0格式）",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/MCPResponse",
								},
								"examples": map[string]interface{}{
									"success": map[string]interface{}{
										"summary":     "成功响应",
										"description": "工具调用成功的响应示例",
										"value": map[string]interface{}{
											"jsonrpc": "2.0",
											"id":      "call-tool-1",
											"result": map[string]interface{}{
												"content": []map[string]interface{}{
													{
														"type": "text",
														"text": "工具执行结果...",
													},
												},
												"isError": false,
											},
										},
									},
									"error": map[string]interface{}{
										"summary":     "错误响应",
										"description": "工具调用失败的响应示例",
										"value": map[string]interface{}{
											"jsonrpc": "2.0",
											"id":      "call-tool-1",
											"error": map[string]interface{}{
												"code":    -32601,
												"message": "Tool not found",
												"data":    "工具 'unknown-tool' 不存在",
											},
										},
									},
								},
							},
						},
					},
					"400": map[string]interface{}{
						"description": "请求格式错误（JSON解析失败）",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/MCPResponse",
								},
								"example": map[string]interface{}{
									"id": nil,
									"error": map[string]interface{}{
										"code":    -32700,
										"message": "Parse error",
										"data":    "unexpected end of JSON input",
									},
									"jsonrpc": "2.0",
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权，需要有效的Token",
					},
					"405": map[string]interface{}{
						"description": "方法不允许（仅支持POST请求）",
					},
				},
			},
		},
	}
}
