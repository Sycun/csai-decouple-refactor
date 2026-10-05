package handler

// OpenAPI 路径定义：capabilities 组。为什么必须是函数、为什么合并处对重复路径直接 panic，
// 都写在 openapi_paths.go 里，这里只放数据。

func openAPIPathsCapabilities() map[string]interface{} {
	return map[string]interface{}{
		"/api/roles": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"角色管理"},
				"summary":     "列出角色",
				"description": "获取所有安全测试角色",
				"operationId": "getRoles",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"roles": map[string]interface{}{
											"type":        "array",
											"description": "角色列表",
											"items": map[string]interface{}{
												"$ref": "#/components/schemas/RoleConfig",
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
			"post": map[string]interface{}{
				"tags":        []string{"角色管理"},
				"summary":     "创建角色",
				"description": "创建一个新的安全测试角色",
				"operationId": "createRole",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/RoleConfig",
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
		"/api/roles/{name}": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"角色管理"},
				"summary":     "获取角色",
				"description": "获取指定角色的详细信息",
				"operationId": "getRole",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "角色名称",
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
									"type": "object",
									"properties": map[string]interface{}{
										"role": map[string]interface{}{
											"$ref": "#/components/schemas/RoleConfig",
										},
									},
								},
							},
						},
					},
					"404": map[string]interface{}{
						"description": "角色不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"put": map[string]interface{}{
				"tags":        []string{"角色管理"},
				"summary":     "更新角色",
				"description": "更新指定角色的配置",
				"operationId": "updateRole",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "角色名称",
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
								"$ref": "#/components/schemas/RoleConfig",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "更新成功",
					},
					"400": map[string]interface{}{
						"description": "请求参数错误",
					},
					"404": map[string]interface{}{
						"description": "角色不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"角色管理"},
				"summary":     "删除角色",
				"description": "删除指定角色",
				"operationId": "deleteRole",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "角色名称",
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
						"description": "角色不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/skills": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"Skills管理"},
				"summary":     "列出Skills",
				"description": "获取所有Skills列表，支持分页和搜索",
				"operationId": "getSkills",
				"parameters": []map[string]interface{}{
					{
						"name":        "limit",
						"in":          "query",
						"required":    false,
						"description": "每页数量",
						"schema": map[string]interface{}{
							"type":    "integer",
							"default": 20,
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
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"skills": map[string]interface{}{
											"type":        "array",
											"description": "Skills列表",
											"items": map[string]interface{}{
												"$ref": "#/components/schemas/Skill",
											},
										},
										"total": map[string]interface{}{
											"type":        "integer",
											"description": "总数",
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
				"tags":        []string{"Skills管理"},
				"summary":     "创建Skill",
				"description": "创建一个新的Skill",
				"operationId": "createSkill",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/CreateSkillRequest",
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
		"/api/skills/stats": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"Skills管理"},
				"summary":     "获取Skill统计",
				"description": "获取Skill调用统计信息",
				"operationId": "getSkillStats",
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
			"delete": map[string]interface{}{
				"tags":        []string{"Skills管理"},
				"summary":     "清空Skill统计",
				"description": "清空所有Skill的调用统计",
				"operationId": "clearSkillStats",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "清空成功",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/skills/{name}": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"Skills管理"},
				"summary":     "获取Skill",
				"description": "获取指定Skill的详细信息",
				"operationId": "getSkill",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "Skill名称",
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
									"$ref": "#/components/schemas/Skill",
								},
							},
						},
					},
					"404": map[string]interface{}{
						"description": "Skill不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"put": map[string]interface{}{
				"tags":        []string{"Skills管理"},
				"summary":     "更新Skill",
				"description": "更新指定Skill的信息",
				"operationId": "updateSkill",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "Skill名称",
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
								"$ref": "#/components/schemas/UpdateSkillRequest",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "更新成功",
					},
					"400": map[string]interface{}{
						"description": "请求参数错误",
					},
					"404": map[string]interface{}{
						"description": "Skill不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"Skills管理"},
				"summary":     "删除Skill",
				"description": "删除指定Skill",
				"operationId": "deleteSkill",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "Skill名称",
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
						"description": "Skill不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/skills/{name}/bound-roles": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"Skills管理"},
				"summary":     "获取绑定角色",
				"description": "获取使用指定Skill的所有角色",
				"operationId": "getSkillBoundRoles",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "Skill名称",
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
									"type": "object",
									"properties": map[string]interface{}{
										"roles": map[string]interface{}{
											"type":        "array",
											"description": "角色列表",
											"items": map[string]interface{}{
												"type": "string",
											},
										},
									},
								},
							},
						},
					},
					"404": map[string]interface{}{
						"description": "Skill不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/skills/{name}/stats": map[string]interface{}{
			"delete": map[string]interface{}{
				"tags":        []string{"Skills管理"},
				"summary":     "清空Skill统计",
				"description": "清空指定Skill的调用统计",
				"operationId": "clearSkillStatsByName",
				"parameters": []map[string]interface{}{
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "Skill名称",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "清空成功",
					},
					"404": map[string]interface{}{
						"description": "Skill不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/config": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"配置管理"},
				"summary":     "获取配置",
				"description": "获取系统配置信息",
				"operationId": "getConfig",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": "#/components/schemas/ConfigResponse",
								},
							},
						},
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
			"put": map[string]interface{}{
				"tags":        []string{"配置管理"},
				"summary":     "更新配置",
				"description": "更新系统配置",
				"operationId": "updateConfig",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"$ref": "#/components/schemas/UpdateConfigRequest",
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "更新成功",
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
		"/api/storage/meta": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"存储清理"},
				"summary":     "获取存储清理策略",
				"description": "返回自动清理开关、间隔、宽限窗口与全部清理类别的启用状态及保留天数",
				"operationId": "getStorageMeta",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
					"403": map[string]interface{}{
						"description": "缺少 storage:read 权限",
					},
				},
			},
		},
		"/api/storage/status": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"存储清理"},
				"summary":     "获取运行空间占用",
				"description": "返回文件系统容量（含 inode）与各类别的占用、可回收量；结果按短 TTL 缓存",
				"operationId": "getStorageStatus",
				"parameters": []interface{}{
					map[string]interface{}{
						"name":        "refresh",
						"in":          "query",
						"description": "为 true 时强制重新遍历目录，绕过缓存",
						"required":    false,
						"schema":      map[string]interface{}{"type": "boolean"},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
					"403": map[string]interface{}{
						"description": "缺少 storage:read 权限",
					},
				},
			},
		},
		"/api/storage/cleanup": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"存储清理"},
				"summary":     "预览或执行运行空间清理",
				"description": "默认 dry_run=true 只统计不删除；真实删除必须同时传 dry_run=false 与 confirm=true。仅处理已启用的类别，同一时刻只允许一轮执行",
				"operationId": "runStorageCleanup",
				"requestBody": map[string]interface{}{
					"required": false,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"dry_run": map[string]interface{}{
										"type":        "boolean",
										"description": "省略时按 true 处理",
									},
									"confirm": map[string]interface{}{
										"type":        "boolean",
										"description": "dry_run=false 时必须为 true",
									},
									"categories": map[string]interface{}{
										"type":        "array",
										"description": "限定类别；省略表示全部已启用类别",
										"items":       map[string]interface{}{"type": "string"},
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "执行完成（或预览完成）",
					},
					"400": map[string]interface{}{
						"description": "缺少 confirm、或类别键未注册",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
					"403": map[string]interface{}{
						"description": "缺少 storage:write 权限",
					},
					"409": map[string]interface{}{
						"description": "已有一轮清理在执行",
					},
				},
			},
		},
		"/api/config/tools": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"配置管理"},
				"summary":     "获取工具配置",
				"description": "获取所有工具的配置信息",
				"operationId": "getTools",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type":        "array",
									"description": "工具配置列表",
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
		"/api/config/apply": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"配置管理"},
				"summary":     "应用配置",
				"description": "应用配置更改",
				"operationId": "applyConfig",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "应用成功",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/system/update": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"系统更新"},
				"summary":     "查看本安装的源码版本",
				"description": "读取安装目录自己的 git 工作树（分支、远端、当前提交、本地改动、是否有 Go 工具链、是否有可回滚点），不发起任何网络请求",
				"operationId": "getUpdateStatus",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "安装状态与最近一次更新任务",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/system/update/check": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"系统更新"},
				"summary":     "检查本安装自己的远端有无新提交",
				"description": "对安装目录已经跟踪的远端分支执行 fetch，报告落后/领先提交数与新提交列表；取代码的来源永远是本目录自己的远端，不是任何写死的第三方仓库。检查失败返回 200 且带 checkError，不把「查不了」说成「已是最新」",
				"operationId": "checkUpdate",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "检查结果（含 checkError 时为检查未成功）",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/system/update/apply": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"系统更新"},
				"summary":     "一键更新源码并重编译",
				"description": "快进到远端分支、重新编译二进制并原子换入（旧二进制留作 .prev 以便回滚）。本机改过源码文件或分支已分叉时拒绝执行并点名；roles/skills/tools/agents/bundles/knowledge_base/data/config.yaml 等运维者内容在合并前暂存、合并后放回，被保留的文件列在 keptContent。请求立即返回 job_id，进度用 GET /api/system/update/job 轮询；已有任务在跑时返回 409",
				"operationId": "applyUpdate",
				"requestBody": map[string]interface{}{
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"restart": map[string]interface{}{
										"type":        "boolean",
										"description": "更新成功后优雅关闭并退出进程；只在有外部守护时才会被拉起，默认 false",
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"202": map[string]interface{}{
						"description": "任务已受理，返回 job_id",
					},
					"400": map[string]interface{}{
						"description": "请求体不合法，或要求重启但本次启动没有装配重启钩子",
					},
					"409": map[string]interface{}{
						"description": "已有一次更新在进行中",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/system/update/job": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"系统更新"},
				"summary":     "查看更新任务进度",
				"description": "返回正在运行或最近一次更新任务的状态、逐行进度 steps、结果 result 与失败 failure；从未更新过时 job 为 null",
				"operationId": "getUpdateJob",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "任务状态",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/system/update/rollback": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"系统更新"},
				"summary":     "回滚到本次更新之前",
				"description": "撤到上一次更新记录里的旧提交，并换回换入前保留的旧二进制。自那次更新之后 HEAD 已经变化、或本机有源码改动时返回 409 拒绝，而不是丢弃更新之后的工作",
				"operationId": "rollbackUpdate",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "已回滚",
					},
					"409": map[string]interface{}{
						"description": "没有可回滚的记录、更新进行中、或更新之后 HEAD 已被移动",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/plugins": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"能力插件"},
				"summary":     "查看已安装的能力包与能力单元",
				"description": "返回已安装的能力包、由目录扫描登记的独立单元、能力表代数(generation)与源码漂移(drift)；每个单元带 served 标记，标明运行路径是否真的在读它",
				"operationId": "getPluginState",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
					},
					"503": map[string]interface{}{
						"description": "能力表不可用",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/plugins/available": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"能力插件"},
				"summary":     "列出能力包目录里可安装的能力包",
				"description": "只读取能力包根目录下的目录名与 bundle.yaml 清单，返回每个包的 id/名称/版本/所含能力单元及是否已安装；清单损坏以该条目的 error 返回而不是让整个列表失败",
				"operationId": "listAvailablePlugins",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
					},
					"503": map[string]interface{}{
						"description": "能力表不可用",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/plugins/install": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"能力插件"},
				"summary":     "安装能力包（一键扩展）",
				"description": "按 bundles 根目录下的包名安装能力包，安装后立即对后续请求生效，不需要重启；只能引用 <configDir>/bundles 之内的目录，越界路径返回 400；与已存在的身份冲突时返回 409 并指名当前持有者",
				"operationId": "installPluginBundle",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"bundle": map[string]interface{}{
										"type":        "string",
										"description": "bundles 根目录下的能力包名或其路径",
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "安装成功并已生效",
					},
					"400": map[string]interface{}{
						"description": "包名缺失、清单不合法或路径越出 bundles 根目录",
					},
					"409": map[string]interface{}{
						"description": "身份已被其他能力包或内置目录占用",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/plugins/bundles/{id}": map[string]interface{}{
			"delete": map[string]interface{}{
				"tags":        []string{"能力插件"},
				"summary":     "卸载能力包",
				"description": "把该包安装的全部能力单元从能力表摘掉并立刻反映到运行路径；不删除任何文件（源文件本就留在 bundles/<id>/ 内）",
				"operationId": "uninstallPluginBundle",
				"parameters": []map[string]interface{}{
					{
						"name":        "id",
						"in":          "path",
						"required":    true,
						"description": "能力包 ID",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "卸载成功",
					},
					"404": map[string]interface{}{
						"description": "能力包未安装",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/plugins/units/{kind}/{name}": map[string]interface{}{
			"delete": map[string]interface{}{
				"tags":        []string{"能力插件"},
				"summary":     "从能力表摘除一个扫描得到的能力单元",
				"description": "只作用于目录扫描登记的单元；能力包拥有的单元由能力表拒绝并返回 409。不删除文件",
				"operationId": "detachPluginUnit",
				"parameters": []map[string]interface{}{
					{
						"name":        "kind",
						"in":          "path",
						"required":    true,
						"description": "能力类别：role | agent | skill | tool | mcp",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "能力单元名称（身份为 kind/name）",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "已摘除",
					},
					"400": map[string]interface{}{
						"description": "kind 不在已知类别内或 name 为空",
					},
					"404": map[string]interface{}{
						"description": "单元不存在",
					},
					"409": map[string]interface{}{
						"description": "该单元由某个能力包提供，需先卸载该包",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/plugins/units/{kind}/{name}/enabled": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"能力插件"},
				"summary":     "启用或停用单个能力单元",
				"description": "只改能力表里的启停状态，不改任何源文件；因此包内单元的停用不会污染包的源码摘要(digest)。停用后运行路径不再使用该能力，但列表仍能看到它",
				"operationId": "enablePluginUnit",
				"parameters": []map[string]interface{}{
					{
						"name":        "kind",
						"in":          "path",
						"required":    true,
						"description": "能力类别：role | agent | skill | tool | mcp",
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					{
						"name":        "name",
						"in":          "path",
						"required":    true,
						"description": "能力单元名称",
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
									"enabled": map[string]interface{}{
										"type":        "boolean",
										"description": "true 启用，false 停用",
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "已更新",
					},
					"400": map[string]interface{}{
						"description": "kind 非法、name 为空或缺少 enabled",
					},
					"404": map[string]interface{}{
						"description": "单元不存在",
					},
					"401": map[string]interface{}{
						"description": "未授权",
					},
				},
			},
		},
		"/api/config/test-vision": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"配置管理"},
				"summary":     "测试视觉模型连接",
				"description": "测试 Vision 模型 API 是否可用。vision.api_key/base_url 留空时可传 openai 段作回退。",
				"operationId": "testVision",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"vision"},
								"properties": map[string]interface{}{
									"vision": map[string]interface{}{"$ref": "#/components/schemas/VisionConfig"},
									"openai": map[string]interface{}{
										"type":        "object",
										"description": "主 LLM 配置（vision 字段留空时用于 API Key/Base URL 回退）",
									},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "测试结果",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"success":    map[string]interface{}{"type": "boolean"},
										"error":      map[string]interface{}{"type": "string"},
										"model":      map[string]interface{}{"type": "string"},
										"latency_ms": map[string]interface{}{"type": "number"},
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
		"/api/config/test-openai": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"配置管理"},
				"summary":     "测试OpenAI API连接",
				"description": "测试指定的OpenAI/Claude API配置是否可用，发送一个最小请求验证连通性。",
				"operationId": "testOpenAI",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"api_key", "model"},
								"properties": map[string]interface{}{
									"provider": map[string]interface{}{"type": "string", "description": "LLM提供商（openai/claude）", "example": "openai"},
									"base_url": map[string]interface{}{"type": "string", "description": "API基地址（可选，默认根据provider自动选择）"},
									"api_key":  map[string]interface{}{"type": "string", "description": "API密钥"},
									"model":    map[string]interface{}{"type": "string", "description": "模型名称", "example": "gpt-4"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "测试结果",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"success":    map[string]interface{}{"type": "boolean", "description": "是否连接成功"},
										"error":      map[string]interface{}{"type": "string", "description": "失败原因（success=false时）"},
										"model":      map[string]interface{}{"type": "string", "description": "实际使用的模型（success=true时）"},
										"latency_ms": map[string]interface{}{"type": "number", "description": "延迟毫秒数（success=true时）"},
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
		"/api/config/test-typesafe": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"配置管理"},
				"summary":     "测试 TypeSafe Jev 连接",
				"description": "发送一条最小 Noul 请求，验证 TypeSafe System One API Key 是否可用。",
				"operationId": "testTypeSafe",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"api_key"},
								"properties": map[string]interface{}{
									"base_url": map[string]interface{}{"type": "string", "description": "可选，默认 https://api.typesafe.ai"},
									"api_key":  map[string]interface{}{"type": "string", "description": "TypeSafe API Key"},
									"model":    map[string]interface{}{"type": "string", "description": "可选，默认 jev-latest", "example": "jev-latest"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "测试结果",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"success":    map[string]interface{}{"type": "boolean"},
										"error":      map[string]interface{}{"type": "string"},
										"model":      map[string]interface{}{"type": "string"},
										"latency_ms": map[string]interface{}{"type": "number"},
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
		"/api/config/list-models": map[string]interface{}{
			"post": map[string]interface{}{
				"tags":        []string{"配置管理"},
				"summary":     "获取模型列表",
				"description": "代理调用 OpenAI 兼容 GET /models，返回可用模型 id 列表。Claude 不支持。",
				"operationId": "listModels",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"api_key"},
								"properties": map[string]interface{}{
									"provider": map[string]interface{}{"type": "string", "description": "LLM提供商（openai/claude）", "example": "openai"},
									"base_url": map[string]interface{}{"type": "string", "description": "API基地址（可选）"},
									"api_key":  map[string]interface{}{"type": "string", "description": "API密钥"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取结果",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"success":   map[string]interface{}{"type": "boolean"},
										"supported": map[string]interface{}{"type": "boolean"},
										"error":     map[string]interface{}{"type": "string"},
										"models":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
										"count":     map[string]interface{}{"type": "integer"},
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

		// ==================== 终端 ====================,
		"/api/multi-agent/markdown-agents": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"多代理Markdown"},
				"summary":     "列出Markdown代理",
				"description": "获取所有多代理Markdown定义文件列表。",
				"operationId": "listMarkdownAgents",
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"agents": map[string]interface{}{
											"type": "array",
											"items": map[string]interface{}{
												"type": "object",
												"properties": map[string]interface{}{
													"filename":        map[string]interface{}{"type": "string", "description": "文件名"},
													"id":              map[string]interface{}{"type": "string", "description": "代理ID"},
													"name":            map[string]interface{}{"type": "string", "description": "代理名称"},
													"description":     map[string]interface{}{"type": "string", "description": "代理描述"},
													"is_orchestrator": map[string]interface{}{"type": "boolean", "description": "是否为编排器"},
													"kind":            map[string]interface{}{"type": "string", "description": "编排类型"},
												},
											},
										},
										"dir": map[string]interface{}{"type": "string", "description": "代理定义目录路径"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
			"post": map[string]interface{}{
				"tags":        []string{"多代理Markdown"},
				"summary":     "创建Markdown代理",
				"description": "创建新的多代理Markdown定义文件。",
				"operationId": "createMarkdownAgent",
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"name"},
								"properties": map[string]interface{}{
									"filename":       map[string]interface{}{"type": "string", "description": "文件名（可选，自动生成）"},
									"id":             map[string]interface{}{"type": "string", "description": "代理ID"},
									"name":           map[string]interface{}{"type": "string", "description": "代理名称"},
									"description":    map[string]interface{}{"type": "string", "description": "代理描述"},
									"tools":          map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "可用工具列表"},
									"instruction":    map[string]interface{}{"type": "string", "description": "代理指令"},
									"bind_role":      map[string]interface{}{"type": "string", "description": "绑定角色"},
									"max_iterations": map[string]interface{}{"type": "integer", "description": "最大迭代次数"},
									"kind":           map[string]interface{}{"type": "string", "description": "编排类型"},
									"raw":            map[string]interface{}{"type": "string", "description": "原始Markdown内容"},
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
										"filename": map[string]interface{}{"type": "string"},
										"message":  map[string]interface{}{"type": "string", "example": "已创建"},
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
		"/api/multi-agent/markdown-agents/{filename}": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"多代理Markdown"},
				"summary":     "获取Markdown代理详情",
				"description": "获取指定Markdown代理定义文件的详细内容。",
				"operationId": "getMarkdownAgent",
				"parameters": []map[string]interface{}{
					{"name": "filename", "in": "path", "required": true, "description": "文件名", "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"filename":        map[string]interface{}{"type": "string"},
										"raw":             map[string]interface{}{"type": "string", "description": "原始Markdown内容"},
										"id":              map[string]interface{}{"type": "string"},
										"name":            map[string]interface{}{"type": "string"},
										"description":     map[string]interface{}{"type": "string"},
										"tools":           map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
										"instruction":     map[string]interface{}{"type": "string"},
										"bind_role":       map[string]interface{}{"type": "string"},
										"max_iterations":  map[string]interface{}{"type": "integer"},
										"kind":            map[string]interface{}{"type": "string"},
										"is_orchestrator": map[string]interface{}{"type": "boolean"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "代理不存在"},
				},
			},
			"put": map[string]interface{}{
				"tags":        []string{"多代理Markdown"},
				"summary":     "更新Markdown代理",
				"description": "更新指定的Markdown代理定义。",
				"operationId": "updateMarkdownAgent",
				"parameters": []map[string]interface{}{
					{"name": "filename", "in": "path", "required": true, "description": "文件名", "schema": map[string]interface{}{"type": "string"}},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"name":           map[string]interface{}{"type": "string"},
									"description":    map[string]interface{}{"type": "string"},
									"tools":          map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
									"instruction":    map[string]interface{}{"type": "string"},
									"bind_role":      map[string]interface{}{"type": "string"},
									"max_iterations": map[string]interface{}{"type": "integer"},
									"kind":           map[string]interface{}{"type": "string"},
									"raw":            map[string]interface{}{"type": "string"},
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
										"message": map[string]interface{}{"type": "string", "example": "已保存"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "代理不存在"},
				},
			},
			"delete": map[string]interface{}{
				"tags":        []string{"多代理Markdown"},
				"summary":     "删除Markdown代理",
				"description": "删除指定的Markdown代理定义文件。",
				"operationId": "deleteMarkdownAgent",
				"parameters": []map[string]interface{}{
					{"name": "filename", "in": "path", "required": true, "description": "文件名", "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "删除成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"message": map[string]interface{}{"type": "string", "example": "已删除"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "代理不存在"},
				},
			},
		},

		// ==================== Skills管理 - 缺失端点 ====================,
		"/api/skills/{name}/files": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"Skills管理"},
				"summary":     "列出技能包文件",
				"description": "获取指定技能包目录下的所有文件列表。",
				"operationId": "listSkillPackageFiles",
				"parameters": []map[string]interface{}{
					{"name": "name", "in": "path", "required": true, "description": "技能名称/ID", "schema": map[string]interface{}{"type": "string"}},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "获取成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"files": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "文件路径列表"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
					"404": map[string]interface{}{"description": "技能不存在"},
				},
			},
		},
		"/api/skills/{name}/file": map[string]interface{}{
			"get": map[string]interface{}{
				"tags":        []string{"Skills管理"},
				"summary":     "获取技能包文件内容",
				"description": "读取技能包中指定文件的内容。",
				"operationId": "getSkillPackageFile",
				"parameters": []map[string]interface{}{
					{"name": "name", "in": "path", "required": true, "description": "技能名称/ID", "schema": map[string]interface{}{"type": "string"}},
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
										"path":    map[string]interface{}{"type": "string", "description": "文件路径"},
										"content": map[string]interface{}{"type": "string", "description": "文件内容"},
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
				"tags":        []string{"Skills管理"},
				"summary":     "写入技能包文件",
				"description": "写入或更新技能包中的文件内容。",
				"operationId": "putSkillPackageFile",
				"parameters": []map[string]interface{}{
					{"name": "name", "in": "path", "required": true, "description": "技能名称/ID", "schema": map[string]interface{}{"type": "string"}},
				},
				"requestBody": map[string]interface{}{
					"required": true,
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":     "object",
								"required": []string{"path"},
								"properties": map[string]interface{}{
									"path":    map[string]interface{}{"type": "string", "description": "文件相对路径"},
									"content": map[string]interface{}{"type": "string", "description": "文件内容"},
								},
							},
						},
					},
				},
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "保存成功",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"message": map[string]interface{}{"type": "string", "example": "saved"},
										"path":    map[string]interface{}{"type": "string"},
									},
								},
							},
						},
					},
					"401": map[string]interface{}{"description": "未授权"},
				},
			},
		},

		// ==================== 监控 - 缺失端点 ====================,
	}
}
