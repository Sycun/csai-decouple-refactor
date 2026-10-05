package handler

// OpenAPI 路径定义：knowledge 组。为什么必须是函数、为什么合并处对重复路径直接 panic，
// 都写在 openapi_paths.go 里，这里只放数据。

func openAPIPathsKnowledge() map[string]interface{} {
	return map[string]interface{}{
		"/api/assets/import": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"资产管理"},
				"summary":     "批量导入资产",
				"description": "新增或按“目标 + 端口 + 协议”去重更新资产。接收 JSON，不直接接收 XLSX/CSV 文件；单次最多 100000 条，需要 asset:write 权限。",
				"operationId": "importAssets",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{"$ref": "#/components/schemas/AssetImportRequest"},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "导入完成",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{"$ref": "#/components/schemas/AssetImportResult"},
							},
						},
					},
					"400": map[string]interface{}{"description": "数量或资产字段校验失败"},
					"401": map[string]interface{}{"description": "未授权"},
					"403": map[string]interface{}{"description": "缺少 asset:write 权限或无权访问指定项目"},
					"500": map[string]interface{}{"description": "导入事务失败"},
				},
			},
		},
		"/api/projects": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"项目管理"},
				"summary":     "列出项目",
				"operationId": "listProjects",
				"parameters": []map[string]interface{}{
					{"name": "status", "in": "query", "schema": map[string]interface{}{"type": "string", "enum": []string{"active", "archived"}}},
					{"name": "limit", "in": "query", "schema": map[string]interface{}{"type": "integer", "default": 200}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "项目列表"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
			"post": map[string]interface{}{
				"tags":        []string{"项目管理"},
				"summary":     "创建项目",
				"operationId": "createProject",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"name":        map[string]interface{}{"type": "string"},
									"description": map[string]interface{}{"type": "string"},
									"scope_json":  map[string]interface{}{"type": "string"},
								},
								"required": []string{"name"},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{"description": "创建成功"},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
		"/api/projects/{id}": map[string]interface{}{
			"get": map[string]interface{}{
				"tags": []string{"项目管理"}, "summary": "获取项目", "operationId": "getProject",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{"200": map[string]interface{}{"description": "项目详情"}},
			},
			"put": map[string]interface{}{
				"tags": []string{"项目管理"}, "summary": "更新项目", "operationId": "updateProject",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{"200": map[string]interface{}{"description": "更新成功"}},
			},
			"delete": map[string]interface{}{
				"tags": []string{"项目管理"}, "summary": "删除项目", "operationId": "deleteProject",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{"200": map[string]interface{}{"description": "删除成功"}},
			},
		},
		"/api/projects/{id}/facts": map[string]interface{}{
			"get": map[string]interface{}{
				"tags": []string{"项目管理"}, "summary": "列出或按 key 获取事实", "operationId": "listProjectFacts",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					{"name": "fact_key", "in": "query", "schema": map[string]interface{}{"type": "string"}},
					{"name": "include_links", "in": "query", "schema": map[string]interface{}{"type": "boolean"}},
					{"name": "include_link_counts", "in": "query", "schema": map[string]interface{}{"type": "boolean"}},
				},
				"responses": map[string]interface{}{"200": map[string]interface{}{"description": "事实列表或单条（可含 link_counts / outgoing_links）"}},
			},
			"post": map[string]interface{}{
				"tags": []string{"项目管理"}, "summary": "创建/更新事实", "operationId": "upsertProjectFactREST",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"fact_key": map[string]interface{}{"type": "string"},
									"summary":  map[string]interface{}{"type": "string"},
									"links": map[string]interface{}{
										"type": "array",
										"items": map[string]interface{}{
											"type": "object",
											"properties": map[string]interface{}{
												"to":   map[string]interface{}{"type": "string"},
												"type": map[string]interface{}{"type": "string"},
											},
										},
									},
									"links_text": map[string]interface{}{"type": "string", "description": "type: fact_key 每行一条"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{"200": map[string]interface{}{"description": "成功"}},
			},
		},
		"/api/projects/{id}/fact-graph": map[string]interface{}{
			"get": map[string]interface{}{
				"tags": []string{"项目管理"}, "summary": "获取项目事实攻击路径图", "operationId": "getProjectFactGraph",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					{"name": "view", "in": "query", "schema": map[string]interface{}{"type": "string", "enum": []string{"path", "full"}, "default": "path"}},
					{"name": "exclude_deprecated", "in": "query", "schema": map[string]interface{}{"type": "boolean", "default": true}},
				},
				"responses": map[string]interface{}{"200": map[string]interface{}{"description": "nodes + edges"}},
			},
		},
		"/api/projects/{id}/fact-edges": map[string]interface{}{
			"get": map[string]interface{}{
				"tags": []string{"项目管理"}, "summary": "列出项目全部事实边", "operationId": "listProjectFactEdges",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{"200": map[string]interface{}{"description": "边列表"}},
			},
			"post": map[string]interface{}{
				"tags": []string{"项目管理"}, "summary": "添加事实边", "operationId": "createProjectFactEdge",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"source_fact_key", "target_fact_key", "edge_type"},
								"properties": map[string]interface{}{
									"source_fact_key": map[string]interface{}{"type": "string"},
									"target_fact_key": map[string]interface{}{"type": "string"},
									"edge_type":       map[string]interface{}{"type": "string"},
									"confidence":      map[string]interface{}{"type": "string"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{"200": map[string]interface{}{"description": "边已创建"}},
			},
		},
		"/api/projects/{id}/fact-edges/{edgeId}": map[string]interface{}{
			"delete": map[string]interface{}{
				"tags": []string{"项目管理"}, "summary": "删除事实边", "operationId": "deleteProjectFactEdge",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					{"name": "edgeId", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{"200": map[string]interface{}{"description": "删除成功"}},
			},
		},
		"/api/projects/{id}/promote-attack-chain/{conversationId}": map[string]interface{}{
			"post": map[string]interface{}{
				"tags": []string{"项目管理"}, "summary": "将对话攻击链沉淀到项目事实图", "operationId": "promoteAttackChainToProject",
				"parameters": []map[string]interface{}{
					{"name": "id", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
					{"name": "conversationId", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{"200": map[string]interface{}{"description": "沉淀结果（facts/edges/graph）"}},
			},
		},
		"/api/vulnerabilities": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"漏洞管理"},
				"summary":     "列出漏洞",
				"description": "获取漏洞列表，支持分页和筛选",
				"operationId": "listVulnerabilities",
				"parameters": []map[string]interface{}{
					{
						"name":        "limit",
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
						"name":        "page",
						"in":          "query",
						"required":    false,
						"description": "页码（与offset二选一）",
						"schema": map[string]interface{}{
							"type":    "integer",
							"minimum": 1,
						},
					},
					{
						"name":        "id",
						"in":          "query",
						"required":    false,
						"description": "漏洞ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					{
						"name":        "conversation_id",
						"in":          "query",
						"required":    false,
						"description": "对话ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					{
						"name":        "project_id",
						"in":          "query",
						"required":    false,
						"description": "项目ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					{
						"name":        "severity",
						"in":          "query",
						"required":    false,
						"description": "严重程度",
						"schema": map[string]interface{}{
							"type": "string",
							"enum": []string{"critical", "high", "medium", "low", "info"},
						},
					},
					{
						"name":        "status",
						"in":          "query",
						"required":    false,
						"description": "状态",
						"schema": map[string]interface{}{
							"type": "string",
							"enum": []string{"open", "closed", "fixed"},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/ListVulnerabilitiesResponse",
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"post": map[string]interface{}{
				"tags":        []string{"漏洞管理"},
				"summary":     "创建漏洞",
				"description": "创建一个新的漏洞记录",
				"operationId": "createVulnerability",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/CreateVulnerabilityRequest",
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
									"$ref": "#/components/schemas/Vulnerability",
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
		"/api/vulnerabilities/stats": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"漏洞管理"},
				"summary":     "获取漏洞统计",
				"description": "获取漏洞统计信息",
				"operationId": "getVulnerabilityStats",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/VulnerabilityStats",
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
		"/api/vulnerabilities/{id}": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"漏洞管理"},
				"summary":     "获取漏洞",
				"description": "获取指定漏洞的详细信息",
				"operationId": "getVulnerability",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "漏洞ID",
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
									"$ref": "#/components/schemas/Vulnerability",
								},
							},
						},
					},
					"404": map[string]interface{}{
						"description": "漏洞不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"put": map[string]interface{}{
				"tags":        []string{"漏洞管理"},
				"summary":     "更新漏洞",
				"description": "更新漏洞信息",
				"operationId": "updateVulnerability",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "漏洞ID",
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
								"$ref": "#/components/schemas/UpdateVulnerabilityRequest",
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
									"$ref": "#/components/schemas/Vulnerability",
								},
							},
						},
					},
					"400": map[string]interface{}{
						"description": "请求参数错误",
					},
					"404": map[string]interface{}{
						"description": "漏洞不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"漏洞管理"},
				"summary":     "删除漏洞",
				"description": "删除指定漏洞",
				"operationId": "deleteVulnerability",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "漏洞ID",
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
						"description": "漏洞不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/attack-chain/{conversationId}": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"攻击链"},
				"summary":     "获取攻击链",
				"description": "获取指定对话的攻击链可视化数据",
				"operationId": "getAttackChain",
				"parameters": []map[string]interface{}{
					{
						"name":        "conversationId",
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
									"$ref": "#/components/schemas/AttackChain",
								},
							},
						},
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
		"/api/attack-chain/{conversationId}/regenerate": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"攻击链"},
				"summary":     "重新生成攻击链",
				"description": "重新生成指定对话的攻击链可视化数据",
				"operationId": "regenerateAttackChain",
				"parameters": []map[string]interface{}{
					{
						"name":        "conversationId",
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
						"description": "重新生成成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/AttackChain",
								},
							},
						},
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
		"/api/knowledge/categories": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "获取分类",
				"description": "获取知识库的所有分类",
				"operationId": "getKnowledgeCategories",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"categories": map[string]interface{}{
											"type":        "array",
											"description": "分类列表",
											"items": map[string]interface{}{
												"type": "string",
											},
										},
										"enabled": map[string]interface{}{
											"type":        "boolean",
											"description": "知识库是否启用",
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
		"/api/knowledge/items": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "列出知识项",
				"description": "获取知识库中的所有知识项",
				"operationId": "getKnowledgeItems",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"items": map[string]interface{}{
											"type":        "array",
											"description": "知识项列表",
										},
										"enabled": map[string]interface{}{
											"type":        "boolean",
											"description": "知识库是否启用",
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
			"post": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "创建知识项",
				"description": "创建新的知识项",
				"operationId": "createKnowledgeItem",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":        "object",
								"description": "知识项数据",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "创建成功",
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
		"/api/knowledge/items/{id}": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "获取知识项",
				"description": "获取指定知识项的详细信息",
				"operationId": "getKnowledgeItem",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "知识项ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
					},
					"404": map[string]interface{}{
						"description": "知识项不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"put": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "更新知识项",
				"description": "更新指定知识项",
				"operationId": "updateKnowledgeItem",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "知识项ID",
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
								"type":        "object",
								"description": "知识项数据",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "更新成功",
					},
					"404": map[string]interface{}{
						"description": "知识项不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "删除知识项",
				"description": "删除指定知识项",
				"operationId": "deleteKnowledgeItem",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "知识项ID",
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
						"description": "知识项不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/knowledge/index-status": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "获取索引状态",
				"description": "获取知识库索引的构建状态",
				"operationId": "getIndexStatus",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"enabled": map[string]interface{}{
											"type":        "boolean",
											"description": "知识库是否启用",
										},
										"total_items": map[string]interface{}{
											"type":        "integer",
											"description": "总知识项数",
										},
										"indexed_items": map[string]interface{}{
											"type":        "integer",
											"description": "已索引知识项数",
										},
										"progress_percent": map[string]interface{}{
											"type":        "number",
											"description": "索引进度百分比",
										},
										"is_complete": map[string]interface{}{
											"type":        "boolean",
											"description": "索引是否完成",
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
		"/api/knowledge/index": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "构建索引",
				"description": "构建知识库向量索引。默认仅处理尚无向量的知识项；mode=full 时全量重建。",
				"operationId": "startKnowledgeIndex",
				"parameters": []map[string]interface{}{
					{
						"name":        "mode",
						"in":          "query",
						"required":    false,
						"description": "索引模式：missing（默认，补齐缺失向量）或 full（全量重建）",
						"schema": map[string]interface{}{
							"type":    "string",
							"enum":    []string{"missing", "full"},
							"default": "missing",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "索引任务已启动",
					},
					"400": map[string]interface{}{
						"description": "无效的 mode 参数",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
					"409": map[string]interface{}{
						"description": "已有索引任务正在进行",
					},
				},
			},
		},
		"/api/knowledge/scan": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "扫描知识库",
				"description": "扫描知识库目录，导入新的知识文件",
				"operationId": "scanKnowledgeBase",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "扫描任务已启动",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/knowledge/search": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "搜索知识库",
				"description": "在知识库中搜索相关内容。基于向量检索，按查询与知识片段的语义相似度（余弦）返回最相关结果。\n**搜索说明**：\n- 语义相似度搜索：嵌入向量 + 余弦相似度，可配置相似度阈值与 TopK\n- 可按风险类型等元数据过滤（如：SQL注入、XSS、文件上传等）\n- 建议先调用 `/api/knowledge/categories` 获取可用的风险类型列表\n**使用示例**：\n```json\n{\n  \"query\": \"SQL注入漏洞的检测方法\",\n  \"riskType\": \"SQL注入\",\n  \"topK\": 5,\n  \"threshold\": 0.7\n}\n```",
				"operationId": "searchKnowledge",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"query"},
								"properties": map[string]interface{}{
									"query": map[string]interface{}{
										"type":        "string",
										"description": "搜索查询内容，描述你想要了解的安全知识主题（必需）",
										"example":     "SQL注入漏洞的检测方法",
									},
									"riskType": map[string]interface{}{
										"type":        "string",
										"description": "可选：指定风险类型（如：SQL注入、XSS、文件上传等）。建议先调用 `/api/knowledge/categories` 获取可用的风险类型列表，然后使用正确的风险类型进行精确搜索，这样可以大幅减少检索时间。如果不指定则搜索所有类型。",
										"example":     "SQL注入",
									},
									"topK": map[string]interface{}{
										"type":        "integer",
										"description": "可选：返回Top-K结果数量，默认5",
										"default":     5,
										"minimum":     1,
										"maximum":     50,
										"example":     5,
									},
									"threshold": map[string]interface{}{
										"type":        "number",
										"format":      "float",
										"description": "可选：相似度阈值（0-1之间），默认0.7。只有相似度大于等于此值的结果才会返回",
										"default":     0.7,
										"minimum":     0,
										"maximum":     1,
										"example":     0.7,
									},
								},
							},
							"examples": map[string]interface{}{
								"basic": map[string]interface{}{
									"summary":     "基础搜索",
									"description": "最简单的搜索，只提供查询内容",
									"value": map[string]interface{}{
										"query": "SQL注入漏洞的检测方法",
									},
								},
								"withRiskType": map[string]interface{}{
									"summary":     "按风险类型搜索",
									"description": "指定风险类型进行精确搜索",
									"value": map[string]interface{}{
										"query":     "SQL注入漏洞的检测方法",
										"riskType":  "SQL注入",
										"topK":      5,
										"threshold": 0.7,
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "搜索成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"results": map[string]interface{}{
											"type":        "array",
											"description": "搜索结果列表，每个结果包含：item（知识项信息）、chunks（匹配的知识片段）、score（相似度分数）",
											"items": map[string]interface{}{
												"type": "object",
												"properties": map[string]interface{}{
													"item": map[string]interface{}{
														"type":        "object",
														"description": "知识项信息",
													},
													"chunks": map[string]interface{}{
														"type":        "array",
														"description": "匹配的知识片段列表",
													},
													"score": map[string]interface{}{
														"type":        "number",
														"description": "相似度分数（0-1之间）",
													},
												},
											},
										},
										"enabled": map[string]interface{}{
											"type":        "boolean",
											"description": "知识库是否启用",
										},
									},
								},
								"example": map[string]interface{}{
									"results": []map[string]interface{}{
										{
											"item": map[string]interface{}{
												"id":       "item-1",
												"title":    "SQL注入漏洞检测",
												"category": "SQL注入",
											},
											"chunks": []map[string]interface{}{
												{
													"text": "SQL注入漏洞的检测方法包括...",
												},
											},
											"score": 0.85,
										},
									},
									"enabled": true,
								},
							},
						},
					},
					"400": map[string]interface{}{
						"description": "请求参数错误（如query为空）",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/Error",
								},
								"example": map[string]interface{}{
									"error": "查询不能为空",
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
					"500": map[string]interface{}{
						"description": "服务器内部错误（如知识库未启用或检索失败）",
					},
				},
			},
		},
		"/api/knowledge/retrieval-logs": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "获取检索日志",
				"description": "获取知识库检索日志",
				"operationId": "getRetrievalLogs",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"logs": map[string]interface{}{
											"type":        "array",
											"description": "检索日志列表",
										},
										"enabled": map[string]interface{}{
											"type":        "boolean",
											"description": "知识库是否启用",
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
		"/api/knowledge/retrieval-logs/{id}": map[string]interface{}{
			"delete": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "删除检索日志",
				"description": "删除指定的检索日志",
				"operationId": "deleteRetrievalLog",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "日志ID",
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
						"description": "日志不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		// ==================== 对话交互 - 缺失端点 ====================,
		"/api/fofa/search": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"FOFA信息收集"},
				"summary":     "FOFA搜索",
				"description": "通过后端代理执行FOFA搜索查询，返回资产信息。",
				"operationId": "fofaSearch",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"query"},
								"properties": map[string]interface{}{
									"query":  map[string]interface{}{"type": "string", "description": "FOFA查询语法", "example": "domain=\"example.com\""},
									"size":   map[string]interface{}{"type": "integer", "description": "返回数量（默认100，最大10000）", "default": 100},
									"page":   map[string]interface{}{"type": "integer", "description": "页码（默认1）", "default": 1},
									"fields": map[string]interface{}{"type": "string", "description": "返回字段，逗号分隔", "example": "host,ip,port,title"},
									"full":   map[string]interface{}{"type": "boolean", "description": "是否查询全部数据", "default": false},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "搜索成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"query":         map[string]interface{}{"type": "string", "description": "实际执行的查询"},
										"size":          map[string]interface{}{"type": "integer"},
										"page":          map[string]interface{}{"type": "integer"},
										"total":         map[string]interface{}{"type": "integer", "description": "总匹配数"},
										"fields":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
										"results_count": map[string]interface{}{"type": "integer"},
										"results":       map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "object"}, "description": "搜索结果列表"},
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
		"/api/fofa/parse": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"FOFA信息收集"},
				"summary":     "自然语言解析为FOFA语法",
				"description": "使用AI将自然语言描述解析为FOFA查询语法，需人工确认后再执行查询。",
				"operationId": "fofaParse",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"text"},
								"properties": map[string]interface{}{
									"text": map[string]interface{}{"type": "string", "description": "自然语言描述", "example": "查找使用WordPress的网站"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "解析成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"query":       map[string]interface{}{"type": "string", "description": "生成的FOFA查询语法"},
										"explanation": map[string]interface{}{"type": "string", "description": "语法解释"},
										"warnings":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "潜在风险或歧义提示"},
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

		// ==================== 配置管理 - 缺失端点 ====================,
		"/api/knowledge/stats": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"知识库"},
				"summary":     "获取知识库统计",
				"description": "获取知识库的总体统计信息，包括分类数和条目数。",
				"operationId": "getKnowledgeStats",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"enabled":          map[string]interface{}{"type": "boolean", "description": "知识库是否启用"},
										"total_categories": map[string]interface{}{"type": "integer", "description": "分类总数"},
										"total_items":      map[string]interface{}{"type": "integer", "description": "条目总数"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},
	}
}
