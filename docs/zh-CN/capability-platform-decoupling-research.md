# CyberStrikeAI 解耦与能力平台化调研报告

调研日期：2026-09-30 ｜ 基线提交：`470eb5e` ｜ 方式：全量只读实测 + 外部一手资料核查
调研阶段本报告不含代码改动；**P0/P1/P2 已按本报告实施**，实测数字与未完成项见[第十一节 实施状态](#十一实施状态2026-09-30-重构落地)。能力平台契约见 `capability-platform.md`（zh-CN / en-US）。

## 摘要

项目目标是把 CyberStrikeAI 变成**能力平台**：核心只保留引擎与治理，角色/skill/知识库/MCP/提示词/工具由社区提交、平台审核上架、用户按需安装，并像 yakit 一样让用户选择本地模型或远程引擎。

结论：这个方向可行，但当前代码有三个必须先修的结构性问题，且它们**不是代码整洁问题，是安全控制缺失**：

1. **90 个 YAML 工具配方在授权系统里是"无名"的**。授权 switch 只覆盖 51 个内置常量名，其余全部落到 `agent:local-execute` 这条粗粒度兜底；而 `tools/angr.yaml`、`tools/pwntools.yaml` 是**对模型提供的 Python 源码直接 `exec()`**、`tools/exec.yaml` 是 `sh -c`，三者 `enabled: true`。
2. **HITL 的免审批白名单可以由 HTTP 请求体设置、并集合并、立即写入 config.yaml 生效**，且 HITL 是会话级 opt-in。社区提交的角色/skill 一旦能携带工具清单，这就是一条提权通道。
3. **`cmd/mcp-stdio` 是第二套装配路径，不设 authorizer、不接 storage**，同一条 nil 分支在没有 principal 时**不触发 fail-closed**。

另外两处事实更正会影响技术选型：**yakit 并未内置 Python 解释器**（它只内置自研 YakVM；Python 在其代码里是静态分析前端），且 **yakit 是 AGPL-3.0**（你们是 Apache-2.0，只能抄机制不能抄代码）。同时我上一轮建议的 WASM CPython 插件路线**经核实不可用**——它没有 `ssl`、不支持 C/Rust 扩展，`impacket`/`cryptography` 直接跑不了。

---

## 一、现状事实基线（均可用文末命令复现）

| 维度 | 实测值 |
|---|---|
| 规模 | 148,375 行 Go / 664 文件 / 43 个 internal 包 |
| **循环依赖** | **0**（Tarjan SCC 全图分析）→ 问题性质是"边界粒度"，不是"死结" |
| 扇出最大 | `internal/handler` → 29 个内部包；`internal/app` → 22；`internal/multiagent` → 16 |
| 扇入最大 | `internal/config` ← 25；`internal/database` ← 13；`internal/mcp` ← 12；`internal/mcp/builtin` ← 8 |
| 最大包 | `internal/multiagent` 106 文件 / **16,289 行**（全仓 11%），其中 **78 文件直接 import cloudwego/eino** |
| 数据层 | `type DB struct { *sql.DB }`（`database.go:48`）+ **361 个方法**跨 28 文件；**全仓 0 个 Repository 接口**；包外 120 处 `*database.DB` 引用 |
| 越层 | 4 个非数据层包 import `database/sql`；handler 内 **32 处** `h.db.Exec/Query/Begin` 裸 SQL（口径只看 `h.db.`；同包 `HITLManager` 的 `m.db.` 另有 17 处，HTTP 层真实总数 **49**——见 §11 更正） |
| 接线 | `setupRoutes` 558 行 / **30 个参数** / **278 处路由注册**（`app.go:918-1475`）；`App` 31 字段 |
| 工具体寄生 | `internal/app` 内约 2,900 行领域工具实现（webshell 596 / c2 919 / asset 511 / vuln 466 / project_fact 389） |
| 契约 | `handler/openapi.go` **6,503 行手写 map**（107 路径）↔ 278 处路由注册 ↔ 前端 **396 处** `/api/...` 字面量 + **241 处**工具名字面量 |
| 前端 | **0 个 ES 模块**；index.html **46 个 script 标签**；39 个 JS 文件每页全量加载；**446 处** `typeof X === 'function'` 加载顺序探测；SSE 三套线格式 |
| 部署 | `go:embed` 全仓 **0 次**；`web/static`、`web/templates`、`internal/c2/payload_templates` 均 CWD 相对 |
| 安全网 | `go build ./...` 干净；`go test ./...` **32 包全绿 / 0 失败**；247 个测试文件；**37 个测试直接起真实 SQLite、零 mock**；无 golden/testdata；CI 仅 1 个 workflow；**无 golangci-lint / 无 arch-lint / 无 Makefile / 无 `-race`** |
| 变更热度 | 最近 100 次提交中 app.go 11 次、config.go 14 次、handler/agent.go 10 次、monitor.js 10 次（真实 churn 10–14%） |

**项目自身已认可的目标形态**（重要：解耦是"补自己的债"，不是引入外来范式）：
- `docs/zh-CN/agent-finalization-best-practices.md:28-30` 明确以 Claude Code 的 **Hooks / Subagents / Plan Mode** 为参考，结论"验证、审计、阻断应挂在确定的阶段边界上"。
- `internal/agentfinalizer/decision.go`（260 行独立叶子包）已实现文档承诺的 `FinalizationDecision/Finalizable`，被 8 处调用 → **这是全仓最正确的"文档理念 → 叶子包 → 接线层只装配"范本**。
- `internal/multiagent/sub_agent_context.go:26` 注释自陈 "aligned with Claude Code's agent design philosophy"。
- 该文档只有 zh-CN（缺 en-US），且引用的是旧域名 `docs.anthropic.com/en/docs/claude-code/*`（官方已迁 `code.claude.com`）。

---

## 二、P0 安全发现（分级 + 实证）

### S1｜授权对 90 个配方 fail-open-by-omission（最高危）

```go
// internal/app/mcp_authorization.go:188-195
default:
    if builtin.IsBuiltinTool(toolName) {
        return fmt.Errorf("no authorization policy registered for builtin tool %s", toolName) // 内置：fail closed
    }
    if principal.HasPermission("agent:local-execute") {
        return nil                                                                            // 非内置：仅凭粗粒度权限放行
    }
    return fmt.Errorf("missing agent:local-execute")
```

- `IsBuiltinTool` 只认 `builtin/constants.go` 的 **51 个名字**；90 个 YAML 配方**全部不在其中**（grep `angr|pwntools|execute_python` 在该文件 0 命中）。
- `tools/angr.yaml:33` = `exec(script_content, context)`，`script_content` 是**模型传入的 Python 源码字符串**（`:48` 参数声明），`enabled: true`（`:34`）。
- `tools/pwntools.yaml` 同形（`enabled: true` `:29`）；`tools/exec.yaml` = `sh -c`（`enabled: true` `:4`）。
- `tools/http-framework-test.yaml` 是**1,554 行 Python 框架内联在一个 YAML 字符串里**，含裸 `socket`/`ssl.wrap_socket`（`:650-675`）——既无法审，也会绕过任何 HTTP 代理式出网管控。
- 后果：**提示注入只要让模型调 `angr`/`pwntools`/`exec`，就拿到宿主机任意代码执行**，且这三次调用在授权层"不存在"。
- 现状文档承认该权限是兜底（`docs/zh-CN/rbac.md:106` "应仅授予可信操作员"、`:269` "未登记授权策略的内置工具默认拒绝。普通本地/配置工具需要 `agent:local-execute`"），但**未提供逐工具策略**，平台化后不足以支撑社区制品。

### S2｜HITL 免审批白名单可被请求体加宽，且 HITL 是会话级 opt-in

```go
// internal/handler/hitl.go:1056  ToolWhitelist []string `json:"toolWhitelist"`   ← HTTP 请求体字段
// internal/handler/hitl.go:1173  SetHitlToolWhitelist(req.ToolWhitelist)
// internal/handler/hitl.go:1039/:1208  MergeHitlToolWhitelistIntoConfig(...)      ← 合并写入 config.yaml
// internal/config/config.go:1115 // tool_whitelist 可在侧栏「应用」时合并写入 config.yaml 并立即生效
// internal/handler/hitl.go:401-402  _, inWhitelist := cfg.SensitiveTools[toolName]; return cfg, !inWhitelist
// internal/handler/hitl.go:393-395  if !ok || !cfg.Enabled { return cfg, false }   ← 会话未开 HITL 则完全不审批
```

白名单语义是**豁免表**（列入即免审批），且与 config 全局白名单做**并集**。同一时间 `AgentHandler` 无锁写 `cfg.Hitl.*`（`hitl.go:1117-1119`、`hitl_audit_agent.go:373-374`），而 `ConfigHandler` 用自己的 `mu`、`ApplyConfig` 只持 `RLock` → **跨结构体数据竞争**（S4）。

对标警示：Claude Code 官方文档承认 `allowed-tools` "pre-approves listed tools for the current turn, **bypassing standard permission prompts**" 且"Workspace trust doesn't gate this field"——它至少限制在**当前 turn**。你们比它更宽（全局、持久、并集）。Invariant Labs 的 GitHub MCP 事故（issue 正文注入 + "Always Allow" → 私有数据被写进公开 contribution）演示的就是这条路径；Anthropic 对 GTG-1002 的描述（人类选定初始目标后撤出，AI 每秒数个请求）说明**审批在量下会衰减**，不能指望"人一直盯着"。

### S3｜`cmd/mcp-stdio` 第二装配路径无授权、无记录

| | `cmd/server`（经 app.go） | `cmd/mcp-stdio` |
|---|---|---|
| 构造 | `NewServerWithStorage(log, db)` `app.go:155` | `NewServer(log.Logger)` `main.go:36` → 工具执行**不入库** |
| 授权器 | `SetToolAuthorizer(mcpToolAuthorizer(db))` `app.go:156` | **从未调用** → nil |
| 执行控制 | 绑定 db | `RegisterExecutionControlTools(mcpServer, nil)` `main.go:49` |

nil 的语义（`mcp/server.go:535-541` 与 `:944-950`）：`if authorizer != nil {校验} else if authenticated {拒绝}`。**stdio 路径无 principal → else-if 不触发 → 直接执行**。同一 `Server` 类型在两个入口执行不同策略——对以治理为卖点的产品，这是"单一装配点"必须是硬性要求的直接理由。

### S4｜共享可变 Config 的数据竞争

`Config` 30 顶层字段 + 52 嵌套结构，以**同一指针**注入 24 个包，并外借子指针（`&cfg.Security`、`&cfg.OpenAI`）。运行期写入 **59 处**分布在 7 个 handler 文件，两把不同的锁保护同一块内存。

### S5｜CWD / venv 隐式契约

`router.Static("/static","./web/static")`（`app.go:1464`）、`LoadHTMLGlob("web/templates/*")`（`:1465`）、C2 payload 模板读源码树（`payload_builder.go:41` = `"internal/c2/payload_templates"`），全仓 `go:embed` 0 次。Python 同理：`run.sh:178` `source venv/bin/activate` 后 `exec ./cyberstrike-ai`，而 **14 个 `python3` 配方里只有 2 个显式指向 venv** → 按 README:262 直接跑二进制时，14 个工具静默落到系统 python3。

### S6｜`query_execution_result` 已注册可调，但从未实现

`tools/query_execution_result.yaml` 的 `command: "internal:query_execution_result"` 落到 `security/executor.go:1381-1396`，该函数只 `logger.Warn("未知的内部工具")` 并返回错误。模型看得见、能调用、永远失败。

---

## 三、目标形态设计

### 3.1 制品统一：一份 manifest 生成五样东西

**这是本次调研最大的单点收益。** yakit 维护成本低的真实原因是插件头部 `##type:poc` / `##params:root_url` + `cli` 参数声明**自动生成执行界面**——一份声明驱动全部下游。你们现在同一件事有**五份手工副本**：Go 里的 `mcp.Tool{}`、前端表单、`openapi.go` 手写 spec、权限 switch、文档示例。

```go
// 建议的 CapabilitySpec（草案，非最终）
type CapabilitySpec struct {
    ID        string   // "vendor.capability.name" —— 唯一身份，见 3.2
    Version   string   // semver；发布后不可变（禁止重打 tag）
    Title     string
    Class     string   // readonly | mutating | destructive
    Params    json.RawMessage // JSON Schema：驱动 表单 + 校验 + LLM tool schema + OpenAPI
    Authz     AuthzRule       // permission key + 参数级规则（不再是 switch）
    Capabilities []string      // net.connect(host,port) | fs.read(path) | process.exec | db.query | c2.*
    Runtime   string   // "go-builtin" | "plugin-host:python" | "recipe:exec" | "mcp:remote"
    Timeout   time.Duration
    Evidence  bool     // 该能力的输出是否必须进入证据链
}
```

一份 spec 生成：① 前端表单 ② 参数校验 ③ 给 LLM 的 tool schema ④ OpenAPI 片段 ⑤ 权限声明。**同时消灭** `openapi.go` 6,503 行、`setupRoutes` 278 处手工注册、前端 241 处工具名字面量、`mcp_authorization.go` 37-case switch。

### 3.2 身份必须是"处处一致的一种字符串"

Claude Code 的 `mcp__server__tool` 在权限规则、`allowed-tools`、subagent `tools` 里字面完全相同；yakit 用 per-author plugin ID。你们已有雏形：`config.go:2376` 注释里的 `"toolName"` 或 `"mcpName::toolName"`。→ 统一为 `publisher.capability.name`，官方命名空间保留（`core.*`），命名提交时做 typosquat 相似度检查（PyPI 已内建自动 typosquat 标记）。

### 3.3 单一策略管线 + 生命周期拦截点

把 8 处授权实现收敛为**一条有序管线**（Claude Code：deny → ask → allow → mode → callback，且"deny 覆盖包括 bypass 在内的一切"），扩展点用固定事件集（`PreToolUse` / `PostToolUse` / `PermissionRequest` / `Stop`——你们已经在 `agent-finalization-best-practices.md` 里认可了这个方向）。HITL 三套等待机制（内存 chan / 500ms 轮询 / chan+轮询+全局 `sync.Map`）塌缩成**一个** durable pending + resume 原语。

**与 CC 的必要偏离**：评估器必须 **fail-closed**（超时/解析失败=拒绝），CC 的 exit-code 语义（除 2 外继续）不可照抄；不提供 `bypassPermissions`/`dontAsk` 式用户拨盘；每次 `Decide` 落一条不可抵赖的记录（CC 无此要求）。

### 3.4 插件运行时：out-of-process plugin host（不是内置解释器）

各方案实测对比（详见附录 B 的核实标注）：

| 方案 | 崩溃隔离 | 凭据爆炸半径 | 能力强制 | 离线包 | C 扩展(impacket) | 三平台 | 维护风险 |
|---|---|---|---|---|---|---|---|
| cgo + libpython | **差**（段错误带走进程） | 差 | 差 | 差 | 可 | 差 | **高**（主流绑定已停滞） |
| WASM CPython（goccy/go-python） | 可 | 可 | **优**（零 Config 拒绝一切） | 优 | **不可** | 可 | 高（3 个月 / 1 人 / 0 引用者） |
| wazero / Extism + wasm | 可 | 可 | 优（`allowed_hosts`/`max_pages`） | 优 | **不可** | 可 | 可（插件须编译成 wasm） |
| Starlark | 可 | 可 | 优 | 优 | 非 CPython | 优 | 低（Google 活跃） |
| **子进程 + 内嵌 CPython + 能力中介** | **优** | **优**（RPC 不发凭据） | **优**（processguard + netns/seccomp） | **优**（+25–30 MB） | **优**（真 pip wheel） | **优** | **低** |

**推荐最后一条**，理由是它是**唯一同时满足**你们硬约束的方案：`impacket`/`cryptography` 可用、需要 TLS、需要 `subprocess` 调 nmap 等外部 CLI、需要气隙部署、不能引入 cgo。

架构：核心与插件之间是**版本化的 JSON-RPC over stdio**（或 `hashicorp/go-plugin` 的 gRPC-over-stdio）；插件由 `csai-pluginhost` 承载，**按信任域一实例**、崩溃可重启、懒启动（Neovim 的 RPC coprocess 模式：*"can call, be called, and receive events just as if the plugin's code were executed in the main process"*；VS Code 的 Extension Host 明确"从不"在主进程）；子进程**无直接网络**，出网走核心侧 CONNECT 代理白名单（绑定运维者已批准范围）——这条约束与用哪种解释器无关，因此**不形成技术锁定**；凭据绝不经环境变量下发，会话/C2 状态只暴露窄 RPC 方法。

**两个容易被忽略的落点**：
- `goccy/go-python` 虽然不能当插件 ABI，但它恰适合**另一件事**：安全地跑"模型临时生成的纯 Python 片段"（零 Config 默认拒绝一切能力）——正好是 `angr`/`pwntools` 现在用 `exec()` 裸奔要做的事。
- **不要抄 yakit 的插件加密**。能触碰 MITM/C2 权限的代码若不可读，安全审核员就无法审核；IP 保护属于企业版分层，不属于碰运维者凭据的那一层。

### 3.5 引擎侧必须做的两件事

1. **Eino SDK 收口**，验收从"抽象一层"改为可断言：`grep cloudwego/eino` 命中 **≤1 个 adapter 包**（现状 104 文件，其中 78 在 `internal/multiagent`）。参照 pi-agent-core：core 接收注入的 `streamFn`，因此不引用任何厂商 SDK。
2. **Provider 层：dialect ≠ vendor**（pi-ai 模式）：`Model` 记录携带 `api/baseUrl/cost/contextWindow/reasoning`，catalog 为生成物，加厂商是"数据+注册清单"而非代码分支；**每个 provider 跑同一套一致性测试**（abort / context-overflow / tool-call-without-result / unicode-surrogate / cross-provider handoff）。→ 解决 23 个模型构造点、4 处重复重试、`openai/claude_native.go:11` 反向 import；并且**这套测试是判断"20 对孪生函数能否安全合并"的唯一客观判据**。

---

## 四、制品五类现状与差距

| 制品 | 加载路径 | 免重编译生效？ | 阻塞社区提交/安装的点 |
|---|---|---|---|
| skills | `skillpackage/layout.go:42-60` 每次 `os.ReadDir`（无缓存）；`multiagent/eino_skills.go:25-96` 每次 agent 构造时建 backend | **是** | 无结构性阻塞；缺**签名/来源校验**；`handler/skills.go:278 PutSkillPackageFile` 可写包内任意文件（`skillpackage/io.go:116 SafeRelPath` 有防穿越——这条现有控制是对的，要保留） |
| tools/*.yaml | `config.LoadToolsFromDir:1965` → `ReloadSecurityToolsFromDir:1947` → `handler/config.go:1701` + `ClearTools()`+`RegisterTools()` `:1719-1720` | **是** | `ClearTools()` **清空全部工具再重建**，Go 工具靠 12 个 registrar 闭包补注册（`:1724-1740`）——一次 config「应用」就可能丢工具，注册模型必须改为幂等的分层注册 |
| 角色 | `handler/role.go:92/194/242` → `saveConfig:312` → `os.WriteFile:359` | **是** | `role.go:440-446` 把 `role.Tools` **原样写入 YAML，完全不校验**工具是否存在（对比 `GetAllBuiltinTools`）→ 拼错静默存活；角色可携带工具清单 = S2 的利用面 |
| 知识库 | `app.go:2090 NewManager(..., cfg.Knowledge.BasePath)`；`knowledge/manager.go:35 ScanKnowledgeBase`；索引 `app.go:2130` | **是**（扫描可按需触发） | 需重嵌才生效；**但真正的阻塞是设计问题**：社区文本进 RAG 会条件化一个能执行真实攻击动作的 agent，见 §6 |
| 提示词 | 角色 `user_prompt`（`role.go:434`）+ **Go 里硬编码的 prompt 构造器**（`projectprompt/blackboard.go:34-95` 枚举工具名） | 部分 | 文本嵌在 Go 字符串里 = 代码绑定，必须外提为制品 |

---

## 五、工具归位：149 个注册点的最终去向

实测总数：**90 个 YAML 配方 + 51 个 Go 内置 = 141 个可调用能力**；静态注册点 149 处（`RegisterTool` 54 处非测试调用 + 循环注册）。

| 判定 | 数量 | 说明 |
|---|---|---|
| 立即可外提为插件 | **88** | 全部外部二进制配方（nmap/sqlmap/hydra…）+ `exec` + 2 个 python 助手。它们**不闭包任何 DB/C2/agent 状态**（`executor.go:325` 的 handler 只走 `exec.CommandContext`） |
| 引入能力中介后可外提 | **41** | 资产 7 / 漏洞 3 / 项目黑板 6 / webshell 9 / c2 只读与 profile 类 / 知识库 2 / vision 1 / 批量任务 9 ——它们只需要 `db.query`、`c2.list_sessions`、`net.connect(target)`、`progress.emit` 这类中介调用 |
| 必须留在进程内 | **13** | `get/wait/cancel_tool_execution`(3)：它们**就是中介的控制面**，插件不能中介自己的宿主；`batch_task_create/start/rerun/pause`(4)：`startBatchQueueExecution`（`agent.go:2402`）**负责拉起 Eino agent**；`c2_listener`：持有已 bind 的 socket（`c2/manager.go:34 runningListeners`）+ `RestoreRunningListeners:330`；`c2_payload`：进程内 `go build` 交叉编译（`payload_builder.go:199`）并把 `DownloadPath` 交给同一个 web server；`query_execution_result`：**尚未实现** |
| 已损坏 | **1** | `query_execution_result`（S6）——要么实现要么从注册里摘掉，不能让模型调一个必然失败的工具 |

**"增加一个内置工具"今天要改多少地方**：最少 **6 个文件**，典型 **10–12 个文件 / 15 处编辑**——工具体 → `app.go:173-176` **和** `:537-540`（重复注册）→ `constants.go` 的常量 + `IsBuiltinTool` switch + `GetAllBuiltinTools` slice（**三处**）→ `mcp_authorization.go` switch → `rbac.go:15` 权限目录 → `multi_agent_prepare.go:131-145` webshell 模式白名单 → `agent.go:535`、`handler/agent.go:1204/1247` 逐工具参数注入/检索特例 → `projectprompt/blackboard.go:34-95` prompt 枚举 → `builtin-tools.js:9-16` + `chat.js:4397` → `hitl-best-practices.md:30`（zh+en）+ `config.example.yaml:206`（**三处示例**）→ `openapi.go:3568/:902`。**这个数字必须被驱动到 0，平台化才成立。**

---

## 六、社区知识库 = 本产品的独特高危面（Claude Code 与 pi 都没有这个问题）

你们的 RAG 检索结果会进入一个**能执行真实攻击动作**（C2 / webshell / 扫描）的 agent 上下文。已发表证据：

- **PoisonedRAG（USENIX Sec'25）**：向百万级知识库注入 **5 条**恶意文本即达 **~90% ASR**。
- 后续工作：**单个**被污染 KB 文档 → 平均 **87.0%**、多跳 **90.0%**；单条被投毒规则 → **85% LLM 上下文污染**。
- 规模化实测：98,380 个 agent skill 采样，**36.82% 至少一处缺陷**，指令型操纵占被标记缺陷 33.1%；Snyk ToxicSkills 确认 **76 个恶意载荷**（base64 `curl|bash`、读 AWS 凭据、改 systemctl），举报后 30 天移除 93.6% 但**仍有 8 个在线**。
- 机制性缺陷：Datadog 记录 skill 里的 `` `!cmd` `` 动态上下文 **"executes immediately (before Claude sees anything)"**，绕过模型层拒绝（其缓解是 `disableSkillShellExecution`）。
- GPT Store（216 个）：**系统提示提取 97.2%、文件泄露 100%**——"提交即上架、无安全审查"的下场。

**必须做（代码层，不是 prompt 层）**：
1. 知识/persona/提示词文本一律打 `untrusted_advisory` 特权标签，**结构上不得进入工具调用决策路径**，只进最终回答/引用通道。指令层级（Instruction Hierarchy）是**已落地**技术；CaMeL 双模型仍是论文级。
2. **商店永远不能拓宽人批准的范围**：审批绑定运维者的 `(target, port-set, method, time-window)` 元组，制品请求元组外目标 → 执行处 fail-closed；KB 文本入嵌前剥离 IP/URL/CID。
3. 审校 lint 拒绝 `run/execute/scan/ignore previous/you must` 与注入式 URL；类 CC 的尖括号/控制字符转义。
4. **参照 yakit**：AI 只能在 Capability 目录里匹配推荐，**推荐后还要做一次 identifier verification**，并做运行期漂移监控。
5. 渲染期禁止任何执行（`!cmd` 式动态上下文在你们设计里应不可能存在）。

---

## 七、审核流水线：一个开源小团队实际养得起的形态

| 参照 | 可抄 | 教训 |
|---|---|---|
| **Nuclei** | `verified: true` 必须附**可复现 POC + docker-compose 靶场**；反过度匹配（"Test against 3+ non-vulnerable similar applications"、"Version-Only Detection – AVOID"）；**第二人独立复现**才合并；破坏性草稿由员工改写后合并 | 它也没料到模板能 RCE：**CVE-2024-43405** 模板签名校验绕过（Wiz 有完整 writeup）→ 他们硬化了**真实性**，没硬化**执行**。另有两个 2026  advisories（`CVE-2026-76819` Goja→原生代码执行、`CVE-2026-41282` DSL 注入）**我未独立核实，引用前需自行验证** |
| **XSOAR** | 代码类提交必须附测试（"to automatically test the code during the review phase"） | **反面教材**：**playbook 提交完全不要求测试**（只要定义+文档+截图）——风险最高的制品校验最弱。你们的角色/提示词/工作流正是"playbook"这一类 |
| **Elastic** | "verifies the package signature against a public key"，失败 400 verification failed 并阻断 | 它留了 `ignoreUnverified=true` 逃逸阀——**你们不要提供这个阀**（至多租户级策略，绝不请求级豁免） |
| **Chrome Web Store** | 权限在 manifest 声明，**声明本身**驱动安装告警与审核强度；MV3 杀掉 remotely-hosted code 与 `unsafe-eval` | 首版人审、后续算法审 → **能力爬升（capability creep）是 exploitable delta** |
| **GitHub Actions** | 钉**完整 commit SHA**（官方称这是唯一不可变方式）；`permissions:` 最小化；environments + required approvers = **逐次授权**；fork/首次贡献者需人工批准 | Shai-Hulud：往每个可达仓库推一个新 workflow 文件 → **配置写入即管道 RCE**。对应你们：别让 agent 有"写 registry 配置"的能力 |
| **gh CLI** | 扩展 = 仓库里的一个可执行文件（子进程 ABI），并在安装时明确告知 "Extensions are **not verified, signed, or endorsed** by GitHub…you are trusting its publisher" | 诚实的免责文案 + 无沙箱——你们有 processguard，应该做得比它好 |
| **PyPI** | 隔离区（drop from index、保留可见供 triage）、新包 14 天文件截止、注册期 typosquat 标记、抗钓鱼 2FA | 2000+ 恶意报告、66% 在 4h 内处理——**这是运营成本的下限参照** |

**关键设计：把人力花在"能力增量门"上，而不是每次全量人审。**
manifest 与已批准版本相同 → 只做自动重扫；任何**新增**能力（出网/exec/更宽 scope）→ 强制第二人独立复现 + 重新取得用户同意。这是唯一被证明能抓到破坏性制品的机制（Nuclei），也是小团队不被量压垮的唯一办法（Nuclei 的流程是手工作到 1 PR/1 模板）。

**四道门顺序执行**：静态扫描（Go/JS/YAML/secrets/SAST）→ **沙箱引爆**（先无网首启，再带出网标签运行，记录每一次工具调用）→ 人工清单 → 发布。提交走 PR，**不开自助上传按钮**。

**撤销必须客户端强制**：签名摘要黑名单 + publisher 撤销清单，启动时与每次调用前检查，命中即隔离并回报本机哪些 ID 被撤（push-only 已被证明不够）。可执行制品**默认关闭自动更新**；知识/提示词可在 release-age 冷却（≥12–48h）+ 灰度下自动更新。

---

## 八、分阶段执行计划（修订至 v3，含爆炸半径与人日估算）

| 阶段 | 内容 | 爆炸半径（实测） | 人日 | 验收门 |
|---|---|---|---|---|
| **P0 护栏与真缺陷** | Makefile + golangci-lint(v2) + go-arch-lint(warn)；`-race` 入门禁；修 S4 config 竞争（`atomic.Pointer[Snapshot]` + `internal/settings`）；`go:embed` 收 web/ + C2 模板 + 统一解释器解析；实现或摘除 S6 | 59 处 config 写入；2 处 CWD 读盘；1 个坏工具 | **5–7** | `-race` 全绿；从任意目录跑二进制可用 |
| **P1 授权与审批（安全前置）** | 修 S1：给 90 配方**逐个**登记策略（先在 switch 前加"未知工具需显式登记"的 fail-closed）；把 `angr`/`pwntools`/`exec` 收进 `Class=destructive` + 逐次授权；修 S2：白名单改**交集**校验、授权主体为管理员而非请求体；修 S3：stdio 复用同一 authorizer 或显式声明边界 | 8 处 PDP；37-case switch；`hitl_interrupts` SQL 20 处分两份；三套等待机制 | **10–14** | 执行路径上 `grep` 只剩一个决策入口；无 HITL 会话不能碰 destructive |
| **P2 能力身份与 manifest（平台地基）** | `CapabilitySpec` + `publisher.capability.name` 唯一身份；自注册取代 3 份常量表；`go:generate` 生成名字表 + 前端枚举；一份 manifest 驱动 表单/spec/LLM schema/权限 | 51 常量 + 3 处列表；149 注册点；241 JS 字面量；6,503 行 spec；278 路由 | **18–24** | **新增一个工具：改动 0 个 Go 文件**；契约双向测试通过 |
| **P3 契约与前端** | SSE 单写入器 + 事件注册表（从 `terminal.go:183` 的 `streamEvent` 起步，现存 3 套线格式、4 份 `sendEvent` 闭包）；`openapi.yaml` 提交 + 路由↔spec 测试；i18n 键奇偶；逐文件 ES 模块（去重 2,800–3,200 行） | 446 处顺序探测；59 个 JS case vs 35+ Go 事件；en 4549/zh 4537 | **8–12** | 枚举不再手工镜像；漂移即红 |
| **P4 插件运行时** | `csai-pluginhost`（stdio JSON-RPC / go-plugin）+ 子进程内嵌 CPython + `processguard` 强化（netns/seccomp/rlimit）+ 无直连网络 + host 侧 CONNECT 代理白名单；先迁 **2 个** `python3 -c` 配方验证 ABI | 14 个内联 python 配方（7 个在无 C 扩展/无网/无盘下失效） | **12–16** | 插件崩溃不影响主进程；凭据不出现在子进程环境 |
| **P5 审核流水线与商店** | 制品格式 + 签名（registry 侧 keyless，客户端验签无逃逸阀）+ 四道门 + 能力增量门 + 撤销协议 + 离线包与导出/导入 | 5 类制品（现状见 §4） | **20–30** | 提交→审核→上架→安装→逐次授权全链路可演练一次 |
| **P6 常规解耦** | 拆 `*sql.DB` 嵌入（120 引用 / 32 裸 SQL）→ 361 方法按域切 Store → goose+embed；`AgentHandler` 分解（**19 个文件在它上面声明方法，是 handler 的关键路径**）；`setupRoutes` 拆 per-domain；Provider 层 dialect/vendor + 一致性测试；Eino 收口 ≤1 包；session 事件溯源（compaction 作为条目，原文不删） | 558 行/30 参数；26 处 `SetXxx`（**实测 64 处、`AgentHandler` 130 方法/23 文件**，见 §11）；约 22 个测试构造点；78 文件裸 Eino；23 模型构造点；20 对孪生函数 | **30–40** | 每域 Store 独立可测；`grep cloudwego/eino` ≤1 包 |

合计 **≈103–143 人日**（比纯内部解耦的版本增加约 20–27 人日，增量全部来自 P4/P5 平台能力）。P0–P3 每步都可独立上线。

**硬约束**：真实 churn 10–14%，上游 main 活跃且近 5 周有跨热点 feat 提交（`f7882be`、`6ad9ea2` 各改 54 文件）→ **trunk-based 小步（Google ~100 行/CL，重构与功能分开提交），禁止长命重构分支**。整树上传/删目录那种历史问题已在 2026-08-18 结束，之后 blame 可用。

---

## 九、明确不做

- 拆微服务 / 多进程服务化：`architecture.md:62-66` 与 README:202 把"单服务 + SQLite + 一条命令部署"写成产品承诺；HITL/C2/SQLite 是共享事务与单写者关注点。
- 引入 DI 框架：**google/wire 已归档**（README 自陈 no longer maintained）；fx 的运行期图 + lifecycle 会加在你们真正的痛点（启动顺序）上；samber/do 等于再造一门 DI 语言。→ 手工构造子注入 + 消费方窄接口（≤3-4 方法），遵循 Go 官方风格指南。
- 上 ORM / sqlc 重写数据层：你们的 SQL 本身没问题，问题是**所有权**。
- 为 361 方法建一个大 `DB` 接口。
- 进程内 cgo 嵌 Python；把 WASM CPython 当插件 ABI（无 `ssl`/`cryptography`）。
- 抄 yakit 的插件加密、抄 CC 的 `bypassPermissions`/fail-open hook、抄 pi 的 YOLO 默认与"运维者拥有 system prompt"。
- 用 Go `plugin` 包（官方文档自陈仅 Linux/FreeBSD/macOS、工具链版本必须完全一致否则易崩）。
- 动已经正确的东西：`tools/*.yaml` 动态注册、`skillpackage` 校验、`agents/*.md` 数据驱动、`SafeRelPath` 防穿越、`agentfinalizer` 的叶子包范本。

---

## 十、需要你决策（按阻塞程度排序）

1. **`agent:local-execute` 的现状是否作为 P1 阻断项立即处理**——它决定了社区制品的可用攻击面，也决定平台能不能安全上线。
2. **是否授权修改已发布约定**：`developer-guide.md:11` 把 "app wiring, routes, MCP tool registration" 写成 `internal/app` 的职责，`zh:38` 要求路由集中注册；P2/P6 与这两条冲突（`zh:60` 又说要"在合适模块注册"——文档自身矛盾，需要裁决）。
3. **默认只有一个角色**时，仓库自带的 14 roles / 26 skills / 16 agents 是否转为"默认可卸载包"，以及内核三处硬编码编排槽位（`agents/markdown.go:18,21,24`）先改成配置。
4. **社区知识文本是否接受"结构性降级"（只进引用通道、不进决策路径）**——这会让知识库对 agent 的直接帮助变弱，是产品体验与安全之间的真实取舍。
5. **registry 服务与主仓库的组织方式**：registry 作为独立 repo（PR 即提交、CI 即审核）还是你们已有的服务端；以及气隙交付时的离线包格式与刷新流程。
6. **AGPL 边界确认**：可以照 yakit 的机制设计，不可复用其代码；若你们计划引入任何 yaklang 生态件，需法务确认。

7. **通知铃铛里"工具执行失败"这条要不要恢复**（本轮实测发现，非我引入）：
   `internal/handler/notification.go` 里 `loadFailedExecutionItems` **没有任何调用者**，
   而响应把 `"failedExecutions": 0` **写死**；与此同时 `web/static/js/notifications.js:130,152`
   仍在分支 `item.type === 'task_failed'` 并使用 `item.executionId`。
   即"工具执行失败"通知**在服务端已停用、在前端仍待着**。恢复=往摘要里多调一次已迁好的
   `store.Execution.FailedSince` 并用其计数替换写死的 0；不恢复=删掉这个 loader 与页面那两支。
   我没有替你决定（两条路都会改变用户可见行为），改用可达性门禁把它钉住并写进 ratchet 理由。
8. **`POST /api/terminal/run/stream` 是否保留**：前端全树 0 引用（终端面板走 WebSocket `/api/terminal/ws`），
   但该端点在 `internal/app/routes_terminal.go:15` 注册、且已写进 OpenAPI 对外承诺。
   它的线格式我已逐字节钉住，但这条流没有可比对的前端契约。
9. **历史时间线里那两条名字漂移怎么处理**（本轮实测发现，非我引入）：
   `workflow_agent_start` 是页面历史渲染的死分支（后端写的是 `workflow_start`）；
   `finalization_check` 会落库但历史渲染器没有分支，**刷新后就看不见这一行**。
   两个数字目前各钉成基线 1 的 ratchet（`internal/handler/detail_contract_test.go`），
   改法分别是删页面分支/改名字，以及给历史渲染器补一支——都影响用户可见内容，故留给你定。

---


---

## 十一、实施状态（2026-09-30 重构落地）

本节记录**已按本报告实施**的内容与实测后的数字；未列出的阶段（P4/P5/P6）仍未开始。
新增文档：`docs/zh-CN/capability-platform.md` 与 `docs/en-US/capability-platform.md`（能力平台契约），
生成物 `docs/zh-CN/capability-catalog.md`（149 条能力清单）。

### P0 护栏与真缺陷 —— 完成

| 项 | 报告中的现状 | 现在 |
|---|---|---|
| Makefile / golangci-lint / go-arch-lint | 全无 | `Makefile`（`make ci` = gofmt 硬零+vet+`-race`+lint+arch-lint）、`.golangci.yml`(v2，`new-from-rev` 只门禁改动行)、`.go-arch-lint.yml`(warn) |
| ↑ 的自纠 | — | 原来的 `fmt` 目标直接要求 `gofmt -l cmd internal` 为空，而**重构起点上就有 29 个上游遗留文件不合格**，等于 `make ci` 从一开始就是红的。现拆成 `fmt`（真格式化）与 `fmt-check`（ratchet，基线 29，只许降），并已探针验证它会失败（插入一个未格式化函数→30>29）。教训：**新增门禁必须先在"零改动"的树上验一次绿**，否则它第一次响就是无人相信的假警报。终态：ratchet 一路降到 26 后，剩余债务以**一个独立 `style:` 提交**清零（26 个文件逐个证过 `diff <(git show HEAD:f | gofmt) f` 为空 = 纯重排），门禁随即翻回硬零——这不再需要豁免名单，因为名单是空的 |
| `-race` 入门禁 | 无 | `.github/workflows/ci.yml` 全量 `-race`；**并抓出两处真实竞争**（`config.ExpandConfigEnv` 就地改写与 `exec.Command` 拷贝切片共享底层数组；shell 测试自身 `cmd.Process` 与 `Run` 竞争），均已修 |
| S4 config 竞争 | 59 处运行期写入、两把锁同一块内存 | 新增 `internal/settings`：`atomic.Pointer[Snapshot]` + 写者锁；`ConfigHandler`/`AgentHandler` 共用一个存储，HITL 全部读写改走快照（`TestTwoHandlersOneStoreIsRaceFree`） |
| S5 CWD/venv | `go:embed` 0 次 | `go:embed` 3 处（`web/static`+`web/templates`、`internal/c2/payload_templates`）；模板/静态优先磁盘、内嵌兜底；`python3` 由代码统一解析（`CYBERSTRIKE_PYTHON`→`VIRTUAL_ENV`→`<exe>/venv`→系统）；C2 payload 输出目录改为锚定可执行文件而非 CWD。实测：从 `/tmp/…` 无 `web/` 目录启动裸二进制，`/` 返回 200 / 693,883 字节、`/static/js/i18n.js` 200 |
| S6 坏工具 | 注册但只 `logger.Warn` | 已实现 `internal/security/query_execution_result.go`（分页/检索/过滤/spill 文件，按 `monitor:read` + execution 归属校验；spill 路径仅接受本 execution 且在 spill 根内）。7 个测试覆盖含越权与植入路径 |

### P1 授权与审批 —— 完成（S1/S2/S3 三项均已修）

- **新增 `internal/capability`**：`Spec`（身份/级别/权限/审批下限/中介能力上限/证据要求/参数 JSON Schema）+ `Registry`（分层、幂等、按层替换）+ `Evaluator`（**唯一决策入口**，fail-closed，每个决定产出一条带 stage 的记录）。
- **S1**：37 分支授权 switch 撤销（`mcp_authorization.go` 只剩 6 个 case，且都是资源→项目归属查询，不再做授权判断）。`agent:local-execute` **不再是未登记工具的兜底**——未登记即拒绝（`TestUnregisteredCapabilityFailsClosed`）。90 个配方**逐个**声明 `capability:` 清单（destructive 15 / mutating 42 / readonly 33），其中 `exec`/`angr`/`pwntools`/`execute-python-script`/`install-python-package` 等 15 个降为 `destructive`，绑定**新权限 `agent:destructive-execute`**（复用兜底权限会在**加载期**失败）。
- **S2**：免审批白名单由并集改**交集**（会话集 ∩ `config.yaml` 全局集），写白名单的路由加 `config:write`（管理员）；destructive 能力有**不可豁免的审批下限**，由 `ApprovalLedger` 单次使用、60 秒过期、按 (会话, 身份) 释放。
- **S3**：`cmd/mcp-stdio` 不再有第二套装配——`app.InstallStdioPolicy` 复用同一 authorizer，身份来自新增 `mcp_stdio` 配置段；未声明权限则该进程拒绝一切调用。`mcp.Server` 新增 `SetRequestContextDecorator`，入口注入身份而不必复制装配代码。
- **顺带收敛**：`internal/multiagent/rbac_tool_middleware.go` 里手工维护的 7 个本地特权工具名单改为向注册表查询（策略声明在 `Source: agent-local` 的条目里）。

### P2 能力身份与 manifest —— 完成

- 身份统一为 `publisher.capability.name`，`core.*` 保留，注册表内建 typosquat 相似度检查（编辑距离 <2 拒绝）。
- 三份手工名字表塌缩：`builtin.IsBuiltinTool`/`GetAllBuiltinTools` 由注册表派生，`IsBuiltinTool` 已无生产调用方；一致性由**解析 constants.go 字面量**的 `TestDeclaredConstantNamesHavePolicies` 双向强制（不是自比自）。
- 一份 manifest 驱动下游：`make generate` 产出前端枚举（`web/static/js/generated/capability-catalog.js`，149 条）、机器可读 JSON、审核用 Markdown 清单、`testdata/catalog.golden.json` 漂移基线；CI 里 regenerate-and-diff。参数 JSON Schema 同源于 `parameters:`，并由 `capability.ValidateArgs` 用于执行前校验。
- **reload 不再丢工具**：`ClearTools()` 之后只替换 recipe 层，内置策略由层归属保护（`TestRecipeLayerReplacementKeepsBuiltinPolicies`；跨层同名身份保留，修掉我自己在初版实现里引入的驱逐缺陷）。

### P4 插件运行时 —— 完成核心 ABI（含明确边界）

新增 `internal/pluginhost`：**版本化 JSON-RPC over stdio**（`csai-plugin/1`），按信任域一实例（发布者=信任边界），
懒启动、崩溃后下一次调用自动重启、空闲回收；子进程环境**按白名单继承**（名字含 KEY/TOKEN/SECRET/PASSWORD 的键被拒绝转发），
凭据不经环境变量下发；出网只能走主机侧 CONNECT/绝对形式代理，代理策略 = **清单声明 ∩ 运维者批准的
`(host, ports, method, valid_minutes)` 元组**，批准集为空即全拒。子进程接入 `processguard`（cgroup/rlimit 约束）。
执行侧：`capability.Spec.Runtime = plugin-host:*` 的能力在 `security.Executor` 里改道进程外；
**主机未配置时拒绝执行，不退回进程内**——商店不能靠"没配主机"把执行面缩小回本地。

验收门实测（`internal/pluginhost`，全部用真实子进程，不 mock）：
- 插件崩溃不影响主进程：`TestPluginCrashLeavesTheHostAlive`（自杀→本次调用报错→下次调用自动重启并成功）
- 凭据不出现在子进程环境：`TestChildEnvironmentHasNoCredentials`（同时注入 4 个伪造凭据变量，断言子进程看不到且代理变量在）
- 出网 fail-closed：`TestProxyEnforcesTheApprovedTupleSet`（未批准 CONNECT→403；批准元组→200；端口越界→403）
- 能力上限不可自增：`TestPluginGrantCeilingIsIntersected` + `TestGrantCeilingAndMediationRefused`
- ABI 版本不符即拒：`TestProtocolMismatchIsRefused`

参考插件在 `internal/pluginhost/testdata/refplugin`，它同时是社区插件的 ABI 样例。

**边界要说清（没做的不写成分完成）**：代理只约束"走代理的流量"。Go 标准库**从不代理 loopback**，
裸 socket 也可绕过，所以真正的硬网络边界需要每实例 netns/seccomp——`processguard` 目前只有 cgroup/rlimit。
报告推荐的"内嵌 CPython"也没做：本仓库不内置 Python 运行时，参考插件是 Go 写的 ABI 验证体。

### P5 审核流水线与商店 —— 完成判定与客户端强制部分

新增 `internal/artifact`：
- **制品格式与签名**：Ed25519 分离签名，签名覆盖**全部安全字段**（id/version/class/permission/approval/runtime/grants/payload 摘要/publisher）；
  改级别或加能力即自毁签名（`TestSignatureCoversEverySecurityField` 逐字段验证）。
- **无逃逸阀**：`TrustStore.Verify` 对未签名/未知 publisher/密钥不符一律拒绝，**没有 ignoreUnverified 选项**（`TestNoEscapeValveForUnverifiedArtifacts`）。
  可执行制品要求可读：`File.Text` 是审核面，明确不抄参考产品的插件加密。
- **撤销客户端强制**：`Revocations` 按 digest + publisher + 密钥三重匹配，**启动时装载并隔离出注册表、之后每次调用前再查**
  （`internal/app` 的 `revocation` 求值阶段 + `IsolateRevoked`）。撤销永不被 merge 解除；
  清单文件损坏是错误，不会被当作空清单静默放过（`TestMalformedRevocationListIsNotSilentlyIgnored`）。
  发布者被撤销时该发布者**全部**能力失效，且扫描不会误伤内核自带代码（`TestPublisherRevocationCoversEverythingFromOnePublisher`）。
- **来源不可自选**：provenance 由安装器在验签后落账（`StampProvenance` + 独立台账），
  配方 YAML 里**没有** digest 字段可填；台账在 config reload 重建 recipe 层后仍生效
  （`TestConfigReloadKeepsProvenance`，这条是我自己踩出来的缺陷：原先戳在 Spec 上会被重载抹掉）。
- **能力增量门**：`Diff` + `RequiresHumanReReview`——同一清单的重打包只做自动重扫；**新增能力/级别上调/权限变更**一律回到人工，
  且需要第二名独立审核人、作者不能审自己、沙箱引爆未记录不放行（`TestDecideBlocksUntilSandboxAndSecondReviewer`）。
  能力**收窄**不触发重审，保证人力花在增量上。
- **静态扫描（第 1 道门）**：`Lint` 拦截指令覆盖、外传请求、私钥/token 痕迹、`curl|sh`、
  `` !`cmd` `` 动态上下文、role 标签走私、控制字符；社区文本还警告 `you must` / 让 agent 跑命令的祈使句。
- **`SanitizeForIndex`**：入嵌前剥离 URL/IP/CID（§6.2 要求）。
- **隔离区**：`Quarantine` 移走但保留可见，供事后复盘（参照 PyPI 隔离区），不是删除。
- 提交形态按报告要求是 PR + 四道门状态（`Review`/`GateOrder`），**不做自助上传按钮**。

**尚未做**：registry 服务端本身（签名发布、灰度、release-age 冷却）、气隙离线包的导出/导入命令、
沙箱引爆的自动化执行（现在只记录结果并作为放行前置）、`grants` 在 netns 层的硬强制、P6 全部。

### P3 契约与前端 —— 三套线格式归一，事件名注册表强制到发射点

- 已做：i18n 键奇偶测试（**规范化 i18next 复数后缀**后比对，避免把 `dashboard.failedNCalls_one/_other` 误报成漂移；顺手补齐 2 个真实缺口 `webshell.dbProfileName`/`webshell.dirTree`）；路由↔OpenAPI 双向测试（AST 解析出 **278 条**注册路由，与手写 spec 比对：spec 里存在但路由已无 → 硬失败；路由存在但 spec 未记录 → **ratchet 基线 132**，只许降不许升）。
- **SSE 事件名两侧现在有同一份注册表**（本节此前缺的就是这个）：
  - `internal/sse.Scan` 用 go/ast 从发射点提取事件名，**判据来源是源码字面量而非任何派生表**。它把「名字经参数转发」这件事真正算了出来：
    建一张 `函数#参数位 → 名字集合` 的值图（字面量实参做种子、变量实参做边）迭代到不动点。
    此前记录的「AST 只能提取 12 个字面量、事件名多为变量传入」因此被推翻：**实测 21 个名字、0 个无法溯源的发射点**，
    其中 4 个（`eino_empty_response_continue`、`user_interrupt_continue`、`finalization_auto_continue`、`finalization_pending_tools_cancelled`）
    只能通过 `progressCallback(...)` → `sendEvent(...)` 的转发链看到，纯字面量扫描永远漏掉它们。
  - 提取器按信封分域：`agent` 流（`{type,message,data}`）与 `terminal` 流（短键 `{t,d,c}`）各自一套契约、各自校验。
    域也分两半（`internal/sse/contract.go`，生成器与门禁共用同一个入口 `sse.BuildInventory`）：
    **发射包内层** `internal/handler`（帧在这里拼装），**通道层** `internal`（进度回调在这里被*调用*）。
    只扫发射包会得到 14 个名字——那是一份会在运行时拒掉真实事件的注册表，见下。
  - **又被自己的检查抓到一处形状遗漏**：`sse_keepalive.go` 用 `fmt.Fprintf(..., `data: {"type":"heartbeat"}`)` 手拼帧，
    信封扫描看不见它，注册表因此少了传输层唯一一个事件。补了「字符串字面量里出现 `data: {"type":...}` 形态」这一路判据，
    并把心跳改走 `sse.Writer`（新增 `Comment()` 与共享锁 `Options.Lock`，心跳与业务事件仍共用同一把 mutex——
    并发写同一个 ResponseWriter 会破坏 chunked 编码，这是原注释里写明的事故）。
    字节差异只有一处：心跳帧从 `{"type":"heartbeat"}` 变成 `{"type":"heartbeat","message":""}`；
    `monitor.js` 的 `switch (event.type)` 分支不读 message，故无行为影响（已核对消费点）。
  - 生成物三份：`internal/sse/testdata/sse-kinds.golden.json`、`web/static/js/generated/sse-events.js`、`docs/zh-CN/sse-event-catalog.md`（含每个名字的出处文件与行号），`make generate` 重生成，CI 里 regenerate-and-diff。
  - **发射点迁移已完成的部分**：`multi_agent.go`、`eino_single_agent.go` 两条流、`batch_queue_executor.go`/`task_manager.go`/`agent.go`
    三处总线镜像、以及心跳，全部改走 `internal/sse`（新增 `Recorder` 让镜像也过注册表校验，
    `Options.Lock` 让心跳与业务帧共用同一把 mutex）。四处重复的守卫收成一个 `agentStream`
    （取消抑制 / 断连标记 / 镜像先于写 HTTP / 客户端 ctx 已 Done 就停写）。
    手拼帧从 **13 → 1**（只剩 `c2.go`）；`task_event_bus.go` 那处是文档注释，计数器改为跳过注释行，
    否则数字的含义就不是"还剩几个发射点没迁"了。
  - 迁移过程中注册表**抓到我引入的回归**：把闭包换成 `sendEvent := stream.send` 后，提取器一度从 18 个名字掉到 14——
    因为 4 个名字是经「工厂返回的闭包」传入（`progressCallback := h.createProgressCallback(...)`，
    字面量落在返回闭包的第 0 参，而不是工厂函数的第 0 参）。补上方法值（`sendEvent := stream.send`）这一路别名后追踪恢复。
  - **随后发现更严重的一件事：帧级 `type` 只是冰山一角。** 进度回调 `func(string, string, any)` 的第 0 参就是
    前端 `switch` 的那套内层事件名，而这些名字**大部分拼在 `internal/handler` 之外**
    （`internal/multiagent/runner.go` 的 `progress("tool_call", …)`、workflow 引擎、Eino observer）。
    当时按发射包建的 18 项注册表会在运行时**静默拒掉** `tool_call`/`tool_result`/`reasoning_chain`/`thinking`/
    `iteration`/`eino_model_failover`/`workflow_*`——探针实证：`tool_call refused=true bytes=0`，
    即工具输出整片从流里消失，而所有"绿"的门禁对此一无所知。
    新增 `internal/sse/conduit.go`：按**类型形状**（恰好 3 参 `string, string, any|interface{}`）识别进度回调，
    收其调用点的第 0 参字面量（mode `conduit`），并沿该回调形参上的 `switch`/`==` 分支收名（mode `handled`）。
    两次过采教训都写进了实现：**按名字猜**（`progress`/`callback`）会捞进 `dingtalk`、`fs.read`、`页面标题`；
    **按全局变量名匹配**会把别的函数的分支算进来——必须限定在"回调形状的那个函数的第 0 参"作用域内。
    实测：66 个名字（**63 agent + 3 terminal**），其中 **49 个只能由通道层证明**、14 个在发射包内可追。
  - **通道层还漏了一类：名字经局部变量传入回调。** 双侧比对立刻暴露：页面 `switch (event.type)`
    有 `case 'workflow_branch_taken'/_skipped`，服务端清单里没有——而 `internal/workflow/node_exec.go:135-143`
    确实是把它们发给进度回调：
    ```go
    eventType := "workflow_branch_skipped"
    if allowed { eventType = "workflow_branch_taken" }
    args.Progress(eventType, msg, ...)
    ```
    字面量不在调用点上，`conduit`/`handled` 两路规则都看不见，于是这两个事件在运行时被写入器拒掉，
    分支决策整片消失。新增第三路 `assigned`：在同一函数体内收集 `x := "lit"` / `x = "lit"` / `var x = "lit"`
    的字面量，再与「调用进度回调时第 0 参是该变量」求交；形状过滤（`^[a-z][a-z0-9_.]*$`）挡住分支标签
    这类中文文案。实测**恰好新增 2 个名字、0 个误收**，注册表补声明后 63 agent + 3 terminal = 66。
  - 因此「已声明 ⊆ 可追踪」这条从**弱判据升级为硬判据**：此前它容忍"名字只要出现在发射包源码里即可"，
    因为 4 个名字经工厂返回的闭包传入、值图追不到；现在通道层能证明它们，容错分支（含那段"扫全包字面量"的
    helper）整体删除——它本身就是那条自证式空测试的残余。
  - **又抓到一条自证式空测试**：那个「声明了却没人发」的检查最初把 `internal/handler/*.go` 全扫一遍，
    于是它永远能在 `sse_kinds.go` 里找到自己刚声明的名字——恒真。排除注册表文件后重跑探针才真正报红
    （`event "kind_nobody_emits" is declared but the inventory finds no site that emits it`）。
  - 新增 `internal/handler/agent_stream_test.go` 9 个用例钉住迁移后的线上字节：帧形状逐字节、
    未声明事件被 `ErrUnregisteredKind` 拒且一个字节都不写、镜像字节与实发字节完全相同、
    断连后不再写、取消抑制只吞 error 帧不吞 cancelled、心跳两行的确切字节、
    以及两条被迁移端点在错误路径上的真实 HTTP 响应（200 + `text/event-stream` + error/done 两帧）。
  - `TestConduitEventsReachTheWire` 是这次补的**行为**证明（不只计数）：从清单里取出"只有通道层能证明"的名字
    （当前 49 个，下限 40 防提取器退化），逐个经 `agentStream.send` 走一遍，断言逐字节等于
    `data: {"type":<name>,"message":"","data":{...}}` 且总线镜像字节一致——注册表少一个声明，这里立刻红。
  - 门禁（agent/terminal 四条 + C2 三条）全部探针验红（四个方向：多写一帧、声明没人发、发了没声明、golden 过期）：
    `TestSSEInventoryMatchesGolden`（代码↔已提交清单双向；下限取自 `sse.InventoryFloors()` = 下限 55/52/3，实测 66/63/3；
    探针**按域的各一个**：`done`/`heartbeat` 由发射包证明，`tool_call`/`workflow_start` 只有通道层能证明，
    `definitely_not_an_event` 必须不在——否则通道规则失效时清单会一边倒地"通过"）、
    `TestAgentSSEKindsCoverEveryEmittedEvent`（注册表↔实际发射名**双向硬失败**；插一个假 kind → 「declared but the inventory finds no site that emits it」；
    从注册表删掉 `tool_call` → 「the stream can emit "tool_call" but nothing declares it」且行为测试同时红）、
    `TestTerminalSSEKindsCoverEveryEmittedEvent`、
    `TestHandwrittenSSEFramesRatchet`（基线 **0**，从 13 降下来；零总数已蕴含逐文件水位，故水位表已删；
    注册表文件也不再排除——它现在不含任何帧字面量，排除它等于给未来的手拼帧留后门）。
    另验：**把通道域改窄**（`internal` → `internal/database`）→ 下限报 `17 names (14 agent, 3 terminal); floors are 55/52/3`、
    行为测试报「only 0 names as conduit-only」；**通道域指向不存在目录** → `lstat ... no such file` 直接失败，不静默产出短清单。
  - `agentSSE` 注册表在 `internal/handler/sse_kinds.go`，**未登记的名字写不到线上**（`Writer.Send`、`Writer.SendLegacy` 与 `Recorder.Line` 三个入口都验）。
- **C2 事件流也已归一（第三条线）**：它的帧体是裸 `c2.Event`，客户端 switch 在 `category` 上，
  所以 `category` 就是这条流的事件名。`internal/handler/c2_sse.go` 声明 `listener`/`session`/`task` 三类，
  `c2EventSSE` 注册表 + `SendLegacy` 走同一个 writer（gin 的 `c.Stream` 每次给新 w，用一个 `streamSink` 间接层接住，
  仍由 gin 负责 flush）。三道新门：`TestC2CategoriesMatchTheRegistry`（AST 扫 `internal/c2` 的
  `publishEvent`/`PublishCustomEvent` 第 1 参字面量，双向差集 + 基数下限 + 双探针，并**精确钉住那条唯一的
  参数透传点** `manager.go:853`——多一个透传就说明有人新增了不可追踪的类别）、
  `TestC2EventStreamFrameBytes`（逐字节 + 未登记类别零字节 + sink 换连接后不串流）、
  `TestC2EventStreamUsesTheSingleWriter`。三个方向都探针验红（塞回一条手拼帧 / 发一个没声明的类别 / 声明一个没人发的类别）。
- **手拼帧基线现为硬零（0）**：13 → 1 → 0，逐文件水位六个全 0。三套线格式现在只有一个生产者。
  终端流仍走 `SendLegacy`（短键 `{t,d,c}` 字节不变），但那一类帧也只能由 `internal/sse` 产出。
- **双侧比对的"另一侧"现在也被扫出来了**：`internal/sse/consumer.go` 扫 `web/static/js/*.js`
  （排除 `generated/` 与 `*.test.cjs`），按**三层契约**分别记账——页面的三个消费者读的是三个不同生产者，
  混成一层会让"只在历史里出现的名字"冒充实时消费者，测试照样绿而那一帧其实没人渲染。
  - `stream`（**62** 个）：帧的 `type`。判据是结构而非名字：`const X = JSON.parse(...)`、
    `switch (X.type)` 的接收者、以及由它们派生的别名（webshell.js 的 `var _et = eventData.type`，28 个分支）。
    这条规则把事实图谱的 `nodeData.type === 'vulnerability'` 与 C2 任务类型 `switch (type)`（`shell`/`cd`/`upload`…）
    挡在门外——下限（55/25/2）与双探针各自实证。
  - `detail`（**35** 个）：持久化 `process_details.eventType`，刷新后重建时间线读它；**登记但不与 `agent` 比对**。
  - `c2`（**2** 个）：`event.category === 'session'/'task'`。`listener` 没分支是**对的**（走通用渲染 + i18n 标签），
    所以只硬失败"页面分支了注册表里没有的类别"。
  - **两个差集方向都实测钉成 ratchet、只许降**：服务端可发而页面不分支 **2**
    （`model_output_rejected`、`eino_context_overflow_retry`）；页面分支而服务端从不发 **1**
    （`warning`：monitor.js 的 `case` 与 webshell.js 的 `_et ===` 两支死代码——写入器拒绝未登记 kind，
    那一支永远走不到。正解是删分支**或**真的声明并发出该事件；我没有替你决定，门禁注释里写明了两条出路）。
  - 探针实证（各注入一次、报出名字后撤销）：页面加 `case 'probe_ghost_event'` →
    `the page branches on 2 names the server never emits (baseline 1): [probe_ghost_event warning]`
    且 golden 同步报"清单里没有它，请重生成"；服务端经回调新增 `probe_new_event` 并声明 →
    `the server can emit 3 names the page ignores (baseline 2): [... probe_new_event]`；
    `event.category === 'probe_category'` → C2 门禁红；从枚举删一个名字 →
    `agent enum has 62 names, the registry emits 63 - regenerate the enum`；摘掉 `index.html` 的 script 标签 →
    `index.html does not load the generated SSE enum`。
  - 提取器**又被自己的下限抓出一次误收**：`typeof event.type === 'object'` 左边确是帧字段，
    右边却是 JS 类型名，凭空造出 `object` 事件。加了回溯 `typeof` 的排除规则，stream 63 → 62；
    基数同时从"行数"改为"去重名字数"（c2 由 3 行修正为 2 个名字）。
  - 生成物现在同时含两份清单（`consumed`/`detailConsumed`/`c2Consumed`），
    `docs/zh-CN/sse-event-catalog.md` 逐事件加**前端消费点**列（精确到 `file:line`）并单列双向差集、
    持久化契约、C2 三节；`TestSSEPageConsumesTheCommittedInventory` 让前端半边同样受 golden 约束。
  - **生成的枚举不再躺在 `generated/` 里当死产物**：`index.html:6916` 在 `monitor.js` **之前**引入，
    `monitor.js` 分发入口调用 `CSAI.isSSEEvent(event.type)`，未识别事件在控制台告警。
    `TestGeneratedSSEEnumMatchesTheRegistry` + `TestPageLoadsTheGeneratedEnum`（产物必须被加载、
    且必须早于调用者——只测内容不测接线，产物再正确也没人读）。
  - **顺带查出的事实（不是回归，但要请你决策）**：终端流 `{"t","d","c"}` 在前端**没有任何消费者**——
    `web/` 全树搜 `terminal/run`、`run/stream` 均 0 命中，`terminal.js` 走 WebSocket `/api/terminal/ws`。
    即 `POST /api/terminal/run/stream`（`internal/app/routes_terminal.go:15`，且已写进 OpenAPI）
    是只有外部调用方的端点。它的字节我仍逐条钉住，但这条流的"前端契约"无从比对；是否保留请进 §10 决策项。
- **第三套契约也已双侧比对**（`process_details.event_type`：刷新后重建时间线读的就是它）：
  - 做法不是再写一个分析器，而是把 `internal/sse/inventory.go` 的**值图分析器参数化**：
    `analysis{sinks, envelopes, label, handwrittenFrames}` 一份配置 = 一套契约，
    `Scan` 用流式 sinks（`sendEvent`/`emit`/`Send`… 第 0 参 + `StreamEvent.Type` 等信封字段），
    `ScanPersistedDetails` 用持久化 sinks（`AddProcessDetail`/`AddProcessDetailWithID` **第 2 参**
    + `InterruptedUpdate.EventType`）。字面量、局部赋值、工厂别名、参数透传到不动点这套难逻辑
    **只存在一份**，改一处两边同时受益。流式一侧的产物**逐字节未变**（`git diff` 干净、
    全部 SSE 门禁照绿），这既是重构正确性的证据，也是我敢动它的前提。
  - 实测：服务端可持久化 **8** 个（`cancelled`/`eino_agent_reply`/`error`/`finalization_check`/
    `knowledge_retrieval`/`planning`/`thinking`/`timeout`），页面历史分支 **35** 个。
    golden 新增 `persisted`/`persistedSources`，目录新增"服务端可写入的行 vs 页面历史分支"一节，
    CI regenerate-and-diff 自动覆盖。
  - **又抓出两条真漂移（都只登记、未擅自修）**：
    ①`chat.js` 历史分支里有 `workflow_agent_start`，而**两个生产者都给不出这个名字**
    （工作流引擎写的是 `workflow_start`）——死分支；
    ②`finalization_check` 会被写进 `process_details`，但历史渲染器没有对应分支——
    **行落库、刷新后就看不见**（实时流里是可见的，所以特别容易被漏）。
  - 门禁 `TestPersistedDetailTierMatchesThePage` 双向都钉成只许降的 ratchet
    （死分支基线 1、未渲染行基线 1），外加 5 个**双向探针**（`knowledge_retrieval` 只在持久化侧、
    `tool_call` 只在流式侧、`finalization_check` 两侧都有但页面没有、`workflow_agent_start` 只有页面有、
    `definitely_not_a_detail_type` 三边都必须没有）；下限 persisted ≥ 8 / page ≥ 25。
    三个漂移方向各自注入探针验红：注入页面死分支 → `2 history branches nothing can produce (baseline 1)`；
    注入一条 `h.db.AddProcessDetail(…, "probe_persisted_type", …)` → `the store can persist 2 event types
    the history renderer ignores` 且 golden 同步报"清单里没有它，请重生成"。
  - **这条门禁的已知边界写进了代码注释与目录**：经**进度回调变量**传入的持久化不在精确集合里
    （值图按声明过的函数名记参数位，回调是变量而非函数声明）。
    因此"服务端可持久化而页面不渲染"这一方向**可能少报**；反向用「持久化 ∪ 流式」作上界，
    **只会多报不会误判**。要收紧就得让值图认识"回调变量持有函数体"这一层，已列进待办。
- **未开始**：逐文件 ES 模块改造（去重 2,800–3,200 行）——本轮只给 `monitor.js` 加了枚举调用这一处，
  其余 5 个仍是被复制两遍的独立巨型文件。

### P6 Eino 收敛 —— 有门禁、有三种可复制手法、债面 8 包→5 包

报告的验收判据是 `grep cloudwego/eino` **≤1 包**。起点实测 **11 个包 / 104 个非测试文件**
（`internal/multiagent` 一家 78 个），这不是一轮能收完的量，所以先做能站住的事：把判据变成门禁，再搬第一片。

- **`internal/layering`**：`PackagesImporting(root, prefix)` 扫 `internal`/`cmd`/`pkg` 的**非测试**文件
  （跳 `testdata`/`generated`/`node_modules`/`venv`），按包返回文件清单。
  `TestEinoImportsOnlyShrink` 三个方向分别处置：①**适配包之外的新引入包 = 立即硬失败**
  （否则收敛会在没人注意时开新口子）；②债包内数量超基线 = 硬失败并**打印具体文件**；
  ③债包数与债文件数各有一个总数上限。下降只 `Logf` 要求收紧——改进不该被罚，
  否则第一个撞到的人直接把基线放宽。
  **适配包（`internal/llm`/`internal/einomcp`/`internal/einoobserve`）刻意不设上限**：
  收敛的本质是把债包的 Eino 代码**加到适配包里**，第一版给所有包都设上限等于用门禁堵死自己的落点；
  探针 F2 专门验"适配包变胖不报红"，这条设计约束才立得住。
- **可复制手法：接口留在规则包，SDK 映射进适配包。** `internal/reasoning` 的职责是
  "把配置与用户意图翻译成 chat model 的 reasoning 字段"，原本 10 个函数签名都吃
  `*einoopenai.ChatModelConfig`，但实际只碰**两个字段**（`ExtraFields` 20 处、`ReasoningEffort` 4 处）。
  现在它声明自己需要的 `ChatModelTarget{ExtraFields()/SetExtraFields/SetEffort(level)}`，
  Eino 侧映射搬到 `internal/llm/reasoning_target.go`（`einoChatModelTarget` + 两个入口）。
  该包 515 行行为测试改用 `fakeChatModel` 双，**逐条照旧通过**；
  唯一一条需要真实 SDK 的**线上载荷测试**（`mode=off` 后请求 JSON 里不得出现
  `thinking`/`reasoning_effort`/`output_config`/`reasoning`）随适配器一起进 `internal/llm`，
  另补两条适配层测试：三个 effort 名与清空的映射；`ExtraFields()` 必须返回**同一个 map**
  （否则规则往里写的字段会被静默丢掉）。nil 语义也保住——接口上的 `cfg == nil` 守卫不再可靠，
  所以适配入口在包装前先判 nil 返回。
- **`internal/security` 不再 import Eino**：流式 shell 的进程规则（并发读 stdout/stderr、
  定长块、后台会话、取消时终止进程组）本来就该是 security 的职责，但它原先直接吃
  `*schema.StreamWriter[*filesystem.ExecuteResponse]`——安全包被厂商流类型绑住了。
  现在 security 只讲自己的 `ShellEvent{Output,ExitCode,Err}` / `ShellSink{Send;Close}`，
  入口是 `RunShellStreaming(ctx, command, runInBackendGround, sink)`；
  SDK 侧薄壳 `streamingShell` 落在 `internal/multiagent/eino_execute_streaming_shell.go`，
  **紧挨着本来就在包装它的 `einoStreamingShellWrap`**（shim 之前跨两个包被包了两层）。
  `Send` 的 bool 返回值就是框架的"消费者已断开"信号，调用方据此杀进程组——这条语义在接口上原样保留。
  security 的 5 条流式测试与 1 条进程组测试改跑 `streamCollector` 双，断言语义逐条对应：
  stderr 必须在 stdout 阻塞时**先到**（旧实现在此点是坏的）、sudo 快速失败且退出码 1、
  后台启动**不得等待作业结束**、空命令必须报错。`multiagent` 基线 78→79 的**理由写在数字旁边**
  （它吸收了 security 的那个文件；**该轮**仓库总数 104 不变），提高基线必须是显式可审的动作。
- **`internal/handler` 不再 import Eino**：连接测试原本在传输层手搭
  `[]*schema.AgenticMessage{schema.UserAgenticMessage("Hi")}`，现在走 `llm.PingAgentic`。
- **`internal/vision` 不再 import Eino**：原本自己 `einoopenai.NewChatModel` 并手搭两条通道的
  消息；现在走 `llm.DescribeImage(ctx, llm.VisionRequest{...})`，**请求结构体不含厂商类型**，
  detail 映射与四条错误文案逐字保留。`CompatibleHTTPClient` 由调用方传入，因为那个 transport
  helper 在 `internal/openai` 而 `openai` 依赖 `llm`——注释写明了这条环怎么绕开，不是随手加的字段。
- **结果：总引入包 11 → 6；适配包之外的债面 8 包/100 文件 → 3 包/96 文件；import Eino 的文件
  总数 104 → 105。** 前四项收敛的形状是"代码被搬走，没被删掉，也没长出来"；最后那 +1 是
  **唯一一次真的长出来**：`internal/einoskill` 是新增的适配器文件，不是为了搬家省事——
  Eino 自带的 skill backend 只接受一个 `BaseDir`，所以能力包里的 skill 根本无法被读到。
  它替换的是一条能力上限，而债面（判据真正盯的那一侧）一步没退。
- 探针三向验红/验绿后撤销：①债包 `internal/security` 加一个 eino 文件 →
  `Eino imports grew inside existing debt packages: [internal/security: 2 > baseline 1 [shell_execute_stream.go, zz_eino_probe.go]]`；
  ②**适配包** `internal/llm` 加一个 eino 文件 → 不报红（设计意图，必须能验）；
  ③全新包 `internal/hitl` 加 eino import →
  `new packages now speak Eino: [internal/hitl (internal/hitl/zz_eino_probe.go)]`。
- 已进 `make ci`（`layering-check`）与 `.github/workflows/ci.yml`。**剩余债面**：
  `workflow` 5、`knowledge` 12、`multiagent` 79（托管 ADK runner，最大的一块）。
  `openai` 的原生 Claude 通道已整体搬进 `internal/llm/claude_native.go`（本就在依赖 llm，方向不成环），
  openai 侧只剩三个方法的薄委托；生成器逼出的另一处修正：`Resolve(vendor, "")` 过去交出
  `BaseURL` 为空、要到发请求才失败的方言，现在回落行内 `DefaultBaseURL`。
  判据与手法都已就位，后面每一片都是同一套动作：接口进规则包、映射进适配包、降基线、复测探针。

### P6 Provider 方言目录代码生成 —— 完成（此前是"表在代码里、没人能读"）

`internal/provider` 的方言表一直是**纯 Go 字面量**，报告把它列为 P6 待办（"provider catalog 代码生成"）。
现在它有两份已提交产物，且**逐字节**受门禁约束：

- `internal/provider/publish.go`：`Publish()/PublishJSON()/PublishMarkdown()` 是**唯一渲染处**，
  生成命令 `internal/provider/gen` 只负责写文件与基数下限（实测 6 个 vendor，下限 5）。
  渲染逻辑放在包内而不是命令里，是为了让测试能逐字段比对——
  **第一版我把比对写成"手挑几个字段"，把某个 vendor 的 context window 从 128k 改成 512k 门禁照样绿**；
  改成 `json.Marshal(整行)` 比对后，改一个位（`StrictJSONSchema`）、改一个数字、改文档一个字符，三种探针全部报红。
- 产物：`docs/zh-CN/provider-catalog.md`（vendor/api/endpoint/默认 base_url/context/max_tokens/能力位/重试/溢出标记
  + 派生答案表 + 成本表）与 `internal/provider/testdata/provider-catalog.golden.json`；
  `make generate` 覆盖，CI 新增 regenerate-and-diff 步骤。
- **顺带补了一处真实的可用性缺陷**：`Catalog.Resolve(vendor, "")` 过去会把 `BaseURL` 留空，
  于是 `EndpointURL()` 到**发请求那一刻**才报 "no base URL configured"——错误里连 vendor 都没有。
  现在 `Resolve` 回落到该行的 `DefaultBaseURL`（没有才用 chat 默认），
  `TestResolveAlwaysProducesAUsableDialect` 对**每个 vendor + 一个未知 vendor** 都断言能解析出 https 端点。
- **还抓出一次测试污染生产状态的通病**：`TestAddingAVendorIsDataNotCode` 往 `Default()` 里
  `Register` 了 `acme-gateway` 又不还原，于是同包后面的"发布一致性"测试看到 7 个 vendor 而报红。
  已改为在 `NewCatalog()` 副本上注册。**全局单例被测试改写**这类问题只有"产物必须等于表"这种
  严格断言才会暴露——宽松的手挑字段版本会永远绿。
- 文档如实记下一处**有意的行为差异**：旧代码只对字面量 `"claude"` 特判 Anthropic 域名，
  因此 `provider: anthropic` 且 base_url 为空时会被指向 api.openai.com（然后失败）；
  方言表让 `anthropic` 与 `claude` 都落到 `https://api.anthropic.com`。
  `TestAnthropicChannelDefaultIsNotTheChatDefault` 钉住这一点并注明是修正而非回归。

### P6 `AgentHandler` 分解 —— 先把水位量出来，再把"忘了注入"变成 CI 判据

报告 §4 给的线索是"26 处 `SetXxx`"和"19 个文件在 `AgentHandler` 上声明方法"。动手前按
refactor-ratchet 的规矩先实测，**两个数字都比报告写的多**（遍历域：`internal/handler` 全部非测试文件，
接收者=所有带方法接收者的类型，判据来源=`go/ast` 的 `FuncDecl`，不是 grep）：

- `AgentHandler`：**130 个方法 / 23 个文件**（起点；两刀之后 112/21，见下一小节）。
- `internal/handler` 里 `Set*` 注入方法：**64 个，分布在 21 个接收者类型上**（报告记 26，低估）。
  其中 **`SetAudit` 有 18 份逐字相同的副本**——`h.audit` 引用 198 处，`if h.audit != nil` 手写守卫 **89 处**。
- 其余大接收者：`RobotHandler` 68 方法/2 文件、`ConfigHandler` 45/3、`BatchTaskManager` 40/1、
  `C2Handler` 39/1、`AgentTaskManager` 34/2、`ChatUploadsHandler` 34/1、`WorkflowHandler` 25/3。

**门禁**（`internal/layering/handler_size_ratchet_test.go`，`make layering-check` 覆盖）：
`agentHandlerMethodCeiling = 130`、`agentHandlerFileCeiling = 23`、`setterCeiling = 64`（整包计数）、
`receiverMethodCeilings` 对上面 8 个类型分别封顶。降了只 `Logf` 提醒收紧，涨了**报错并打印文件分布**。
两处探针都已验证会红后精确撤销：
`handler types grew past their measured ceilings: [AgentHandler: 131 methods > ceiling 130]`、
`65 Set* injection methods in internal/handler (ceiling 64): [AgentHandler=8[…SetProbeThing…]]`。
`setterCeiling` 这个数在同一个探针上暴露了我自己的口径错误：先按 4 个接收者数出 31、
再按 8 个数出 42，**按整包才数出 64**——基线一旦取自"我正在改的那几个类型"，
新增一个 god object 就会带着干净配额开始长。

**落地的一处内聚塌陷**：HITL 的三个配置保存器（白名单/策略/默认审批人）原本是
`AgentHandler` 上的三个字段 + 三个 setter + `internal/app` 三行装配，现收成一个
`hitlConfigSavers` 构件与一个 `SetHitlConfigSaver`（`AgentHandler` 的 setter 9 → 7，
方法/文件水位不变，所以门禁数字仍钉 130/23——收紧要跟着真实测量走）。
判据是"装配必须记得调的 setter 数量"，因为**记得调的装配才是半成品对象**的反面。

**审计注入的完整性门禁**（本轮新增，报告里没有要求，但它是上面那 18 份副本唯一的真风险）：
18 个 handler 各自声明 `SetAudit`，`New()` 必须记得逐个调用；**漏掉一个不会报错**——
那个 handler 照常服务特权端点，只是一条审计记录都不写。这正是审计系统最不该有的失败形态。
- `handler.Auditable` 接口（`internal/handler/audit_wiring.go`）把"可注入审计"变成类型；
- `internal/app` 的 19 处 `xHandler.SetAudit(auditSvc)` 全部改成 `bindAudit(xHandler, auditSvc)`
  （`internal/app/audit_wiring.go`），**调用位置与语义逐处对应，零行为改动**；
  `bindAudit` 不做 nil 保护：这里传 nil 是编程错误，启动即 panic 才是对的，
  静默跳过恰恰就是被防的那个缺陷。
- 门禁 `TestEveryAuditableHandlerIsAuditBound`：真相源是**解析 `internal/handler` 源码**得到的
  "声明了 `SetAudit` 的类型集合"+"返回这些类型的 `New*` 构造器集合"；然后扫 `internal/app`
  每个函数体，要求`x := handler.NewYHandler(...)` 之后同函数内必须有 `bindAudit(x, …)`。
  反空跑：类型集合 `< 18`、构造器集合 `< 18`、app 内构造 `< 18` 都直接判扫描器坏掉。
- 三个方向的探针（均已撤销、复验绿、`grep` 确认无残留）：
  ① 删掉 `bindAudit(robotHandler, auditSvc)` →
  `app.go: NewRobotHandler is assigned to robotHandler but that variable is never passed to bindAudit - its endpoints would write no audit records`；
  ② 新增一个带 `SetAudit` 的 handler 类型并在 `New()` 里构造而不注入 → 同一个测试报出 `NewProbeHandler`，
  且水位打印从 18 变 19，证明**新 handler 会自动进入判据**，不需要有人记得更新清单；
  ③ 给它补上 `bindAudit` → 绿（修复方式与成本匹配）。
- **这一步顺带修掉了一个诊断质量问题**：①最初报的是
  `found 18 bindAudit calls for 19 constructions: the call scan misses a binding form`——
  我加的 `bindings >= constructions` 这条**代理不变量抢在真判据之前报错，把别人的漏注入说成扫描器的错**。
  删掉代理条件后（调用扫描退化会让每个构造点都变成 violation，比计数更响也更好定位），
  报错才真正点名那个 handler。
- **明确不做的合并**：把那 18 个 `SetAudit` 收成一个内嵌构件（`type audit struct{ *auditsvc.Service }`
  嵌入 18 个 handler）在文本上只多一行，但它会把 89 处 `if h.audit != nil` 守卫的语义换掉——
  嵌一层包装后守卫变成"包装非 nil"而**内层 service 仍是 nil**，静默反转成"审计已开"，
  到 `Record` 时才 panic；这类改动**编译器和现有测试都不会报**。省 18 个 setter 不值得拿 89 处
  安全守卫的语义去换，故记录为决策而非遗漏。真正的收敛路径是先把守卫改成一处具名方法
  （`func (h *X) auditWriter() *audit.Service`），那需要逐域动调用面，排在 `AgentHandler` 分解之后。

**复现**：`make wiring-check`（或 `go test -count=1 -run 'TestEveryAuditableHandlerIsAuditBound|TestBindAuditReachesTheSetter' ./internal/app/`）、
`make layering-check`。

### P6 `AgentHandler` 分解第一刀 —— 中断队列读面 9 个方法搬进 `HITLQueue`

上一节把水位钉成 130 方法/23 文件的门禁；这一刀是门禁之后**第一次真的把方法搬出去**：

- 搬的是 `hitl_logs.go` 里那 9 个方法（`ListHITLLogs`/`DeleteHITLLogs`/`GetHITLLog` 三个端点 +
  `hitlStoreOrErr`/`listHitlInterrupts`/`hitlRetentionDays`/`filterAllowedHitlInterruptIDs`/
  `hitlInterruptAllowed`/`hitlConversationAllowed` 六个内部构件），新文件 `hitl_queue.go` 上的
  `HITLQueue` 只带**四样状态**：HITL 域存储、一个 `UserCanAccessResource` 的会话可见性判断
  （单方法接口 `conversationAccessSource`，不是 `*database.DB`——不然就是刚归零的裸句柄又搬家）、
  保留期配置、审计服务。留在 `hitl_logs.go` 的是请求↔store 的翻译函数（包级，无状态）。
- **为什么这一族该走**：`hitl.go` 那 20 个方法是决策/终止/超时这条运行链路，跟 agent run loop 的
  任务、会话、SSE 状态绑在一起；而读队列/权限/日志这一族只碰上面四样东西。
  判据不是"文件多大"，是"共享了多少状态"。
- **setter 计数不升**：`HITLQueue` 没有 `SetAudit` 方法，`AgentHandler.SetAudit` 直接给它同包的字段赋值。
  整包 `Set*` 仍是 64（18 个 handler 一份 `SetAudit` 的重复这次没有变成 19 份）。
  新协作对象的访问面是 `agentHandler.HITLQueue()`，`routes_hitl.go` 三条路由改挂在它身上——
  **方法名与路径都没动，所以 278 条路由 golden 仍逐条相同**（`TestRouteTableMatchesGolden` 当场过）。
- 水位：`AgentHandler` **130 → 122 方法、23 → 22 文件**，两条上限当场收紧；
  探针：给 `AgentHandler` 再加一个方法 → `AgentHandler: 123 methods > ceiling 122`，撤销后复验绿。
- 过程里被测试抓到一个**真行为差异**：`hitl_endpoints_test.go` 用结构体字面量手搓 `AgentHandler`，
  搬走之后 `hitlQueue` 是 nil → `DELETE /hitl/logs` 回 500 `hitl store unavailable`。
  修法是把测试改成按生产路径构造（`newHITLQueue(...)`），**没有**改成"访问器里惰性构造"——
  惰性初始化会在并发请求下写同一个字段，是把一个可见的失败换成一个偶发的数据竞争。
- 我自己还犯了一次值得写下来的错：撤销那个探针时用了 `git checkout HEAD -- internal/handler/agent.go`，
  而这个文件本轮**本来就有合法改动**，于是三处编辑一起被冲掉；靠"撤销后立刻 `grep` 关键标识 +
  `git diff` 对照本轮意图"才当场发现。**撤销注入物必须用精确编辑删掉那几行**，
  或者事先 `git diff > /tmp/patch`；对已经改过的文件执行任何 `git checkout` 都是破坏性操作。

- **第二刀：收尾链路 10 个方法 → `runFinalizer`（`finalization_helpers.go`）**。判定终态 / 落库 /
  取消悬挂的工具执行 / 等待工具离开 pending，全在 `finalizeAgentRunForDeliveryWithPolicy` /
  `decideAgentRunForDeliveryWithPolicy` / `persistFinalizationDecision` /
  `cleanupPendingToolExecutionsAfterIteration` 等 10 个方法里，依赖只有四样：窄化的存储面、logger、
  `agent.CancelMCPToolExecutionWithNote` 一个方法、一次消息内容写回。
  后两样各用一个**单方法接口**接进来（`cancellableToolExecution`、`messageContentWriter`），
  `messageContentWriter` 由 `AgentHandler` 自己满足（同包，不需要导出）——
  于是"写回消息"这一条不需要把整个 handler 交出去。
  **`tryAutoContinueAfterFinalization` 刻意留在 `AgentHandler`**：它拿着 `progressCallback`/
  `curHistory`/`attempt` 驱动 Runner 续跑，是运行链路的胶水而不是收尾记账；
  为了搬它而再造两层接口，就是把"分解"做成"搬运"。
  水位 **122 → 112 方法、22 → 21 文件**，两条上限再收紧，探针 `113 methods > ceiling 112` 验红。
- 这两刀让**形状门禁自己抓到过一次真问题**（值得记）：`newRunFinalizer` 最初直接把"已经窄化过的接口参数"
  赋给字段，文本规则看不见上游有没有窄化，于是报
  `finalization_helpers.go assigns a narrowed storage field via db`。
  规则没错、代码不够稳：改成让构造函数**自己收 `*database.DB` 并在边界处 `Narrow`**，
  与其余 19 个持有者同一个形状。顺带修了上一轮我自己加的一条过头条件——
  "每个声明窄接口的文件必须扫到 ≥1 条赋值"对"只经构造参数注入"的协作对象是误报，
  换成"文件必须读得到 + 全包赋值总数 ≥19"，两条一起保住"不许空集通过"。

**复现**：`make layering-check`（三条只降门禁 + Eino + 裸句柄硬零 + 形状门禁）、
`go test -count=1 -run 'TestRouteTableMatchesGolden' ./internal/app/`、
`go test -count=1 -run 'TestHITL' ./internal/handler/`。

### 门禁复现命令

```bash
make ci                                   # gofmt 硬零 + vet + go test -race ./... + lint + arch-lint + layering-check + wiring-check
make fmt-check                            # gofmt：**硬零**（起点 29 → ratchet 26 → 0）。清零以独立
                                          # `style:` 提交出现，26 个文件逐个用
                                          # `diff <(git show HEAD:f | gofmt) f` 证过纯重排；
                                          # 因此 `make fmt` 现在随时可跑且不会带出无关改动
go test -count=1 ./internal/app/ -run 'Shipped|Declared|Catalog|Capability|Recipe|Approval'
go test -count=1 ./internal/handler/ -run 'I18n|Route|OpenAPI|Undocumented|TwoHandlers|HITLExemption'
go test -count=1 ./internal/handler/ -run 'StorePackageHoldsNoHTTPConcerns'                       # store 包不掺 HTTP 关注点
go test -count=1 -v -run TestRawSQLIsOnlyWrittenByTheLayersThatOwnIt ./internal/layering/ 2>&1 | grep 'raw SQL outside'   # 分层裸 SQL **0 条 / 0 个文件**（起点 49），扫描覆盖 508 个生产文件；空清单由 8 条正向对照 + 遍历域下限自证
go test -count=1 -v -run TestStoredInstantIsReadInOnePlace ./internal/layering/ 2>&1 | grep 'stored-instant'   # 「一个 DATETIME 列怎么读回时间」自有层外 **0 个文件**（起点 24 处读者），覆盖 552 个生产文件
go test -count=1 -v -run 'TestDatabaseSurface' ./internal/layering/ 2>&1 | grep -E 'database surface|dropped'  # *DB 方法 **316 上限 / 277 个导出面**，5 个不可达者逐条列名
go test -count=1 ./internal/store/ -run 'TestModelTokenUsage|TestRobotSessions|TestKnowledgeItems|TestOwnedTables'   # 第十一~十四片新增：真库 + 差分 + 归属
go test -count=1 ./internal/handler/ -run 'TestUsageStats|TestConversationTokenUsage'   # 用量两端点的 HTTP 契约：键集合逐字钉住 + 四种可达性
go test -count=1 ./internal/app/ -run TestAssemblyInstalls   # 装配接线：每个 store 的建表/回填/声明在启动里被调，且顺序对
go test -count=1 ./internal/handler/ -run 'TestNotificationProducers|TestNotificationTypes|TestDigest'   # 通知契约：可达性 + 两侧名字集
go test -count=1 ./internal/handler/ -run 'TestPersistedDetailTier|TestGoldenRecords'                     # 持久化 tier：8 vs 35，双向 ratchet 1/1
go test -count=1 ./internal/provider/ -run 'TestPublished|TestResolve|TestAnthropic'                       # 方言目录产物逐字节一致
go run ./internal/provider/gen -root .                                                                    # 重生成 provider 目录
make layering-check                                                                                       # Eino 收敛：逐包基线，新引入包直接红
make wiring-check                                                                                         # 审计注入完整性：18 个可注入类型必须逐个 bind
go test -count=1 -run 'TestNarrowedStorage|TestNarrowRejects' ./internal/handler/ ./internal/database/   # typed-nil 不泄漏（窄接口字段）
go test -count=1 -v -run TestHandlerLayerHoldsNoGodObject ./internal/layering/ 2>&1 | grep 'transport layer' # 硬零水位：0 个裸句柄字段 / 18 个消费者接口 / 990 字段扫描
go test -count=1 -run TestNarrowedFieldsAreOnlyAssignedThroughNarrow ./internal/layering/                      # 形状门禁：窄接口字段只能经 database.Narrow 赋值
go test -count=1 -run 'TestHandler' ./internal/layering/                                                  # AgentHandler/接收者/setter 三条只降门禁
go test -count=1 -v -run TestEinoImportsOnlyShrink ./internal/layering/ 2>&1 | grep imported              # 当前水位打印
go test -count=1 ./internal/handler/ -run 'TestHITL.*Endpoint'                              # HITL 5 条接口 JSON 契约
go test -count=1 ./internal/store/ ./internal/hitl/                                          # 域存储真 SQLite + 保留策略
go test -count=1 ./internal/sse/ -run TestScanWebTiers                                       # 前端三层扫描的下限与探针
go test -count=1 ./internal/handler/ -run 'SSEPage|GeneratedSSEEnum|PageLoads|C2PageBranches'  # 双侧差集 ratchet 2/1 + 枚举接线
go run ./internal/sse/gen -root .                                                            # 重生成 golden/JS/目录（CI regenerate-and-diff）
go test -count=1 ./internal/handler/ -run 'SSEInventoryMatchesGolden|AgentSSEKinds|TerminalSSEKinds|HandwrittenSSEFramesRatchet'   # SSE 事件契约四门
go test -count=1 ./internal/sse/                                                              # 线格式字节 / 锁 / 心跳
go test -count=1 ./internal/handler/ -run 'TestC2CategoriesMatchTheRegistry|TestC2EventStream'  # C2 事件流：类别注册表 + 字节
go test -count=1 -run TestOwnedTablesAreOnlyWrittenFromThisPackage ./internal/store/          # 全仓：一张表一个写入者
go run ./internal/capability/gen -root .  # 重新生成能力目录
grep -cE '^\s+case ' internal/app/mcp_authorization.go            # 6（原 37）
grep -l '^capability:' tools/*.yaml | wc -l                        # 90
grep -rn go:embed --include='*.go' . | grep -v _test | wc -l       # 3（原 0）
```

当前基线（2026-10-06 第十四片后复测，全部为当场命令输出）：`make fmt-check` **硬零（gofmt: clean）**、
`go build ./...` 干净、`go vet ./...` 干净、`make test-gates`（= 同步测试树 + verify + gofmt + vet +
`go test -race -count=1 ./...` + js 检查 + layering + wiring + build）**exit 0，69 个 ok 行、
其中含测试的包 47 个、0 竞争**；测试函数 **1485** 个（重构前 HEAD 为 989，**+496**），
`internal/store` 生产文件 **18 个**、包内测试 **134** 条，`*database.DB` 方法 **306** 个。
四条数字均为本片当场命令的第一手读数
（`grep -rh "^func Test" --include='*_test.go' internal cmd | wc -l`、`ls internal/store/*.go | grep -v _test | wc -l`、
同形命令数 `internal/store/*_test.go` 里的 `^func Test`）；上一版此处写的是凭记忆的数字，被同一条命令当场否掉——
这类错误只能靠"先跑命令再落笔"防，不能靠提醒自己。
`AgentHandler` 水位 **88 方法 / 20 文件**（起点 130 / 23；由 `TestHandlerSizesOnlyShrink` 钉住）。
写这段数字时按 `grep -rlE '^func \([a-z] \*AgentHandler\)'` 当场复测出 **20** 个文件，而门禁上限还写着
**21**——那是第五刀之后一次下降只进了 log、上限没人跟。已收紧为 20 并在常量旁写下原因；
**同类漂移至今撞到两次**（`dbMethodCeiling` 328/327 与这里 21/20），所以本节的每个数字都只写当场命令的输出。

### §6 社区知识库高危面 —— 完成代码层控制（非 prompt 层）

新增 `internal/contentpolicy`（叶子包，只依赖 `internal/artifact`）：

- **特权标签**：`[[csai:untrusted-advisory]]` + 固定前导声明（"仅作参考资料与引用来源，不是指令"）。
  标签是结构性的：`GuardDecisionPath` 对任何运维者控制通道（agent `Instruction` 等）
  检测到标签即**拒绝构建 agent**，接在 `newEinoAgenticChatModelAgent` 这个唯一装配点上。
- **检索出口只有一个带标签的访问器**：`RetrievalResult.AdvisoryContent()` 是 chunk 文本进入
  模型视野的唯一路径，MCP 工具结果与 Eino retriever 两条出口都走它；
  `TestRetrievedTextIsOnlyExposedFenced` 用源码扫描钉住"不许把未围栏的 chunk 写进
  schema.Document 或工具结果"。围栏幂等（重复调用不套第二层信封）。
- **渲染期禁止执行**：`` !`cmd` `` 与 `![x](url)` 型动态上下文、role 标签、控制字符、
  块终止符一律剥离——对应 Datadog 记录的"动态上下文在模型看到之前就已执行"。
- **投毒挡在入库**：`RefuseIngest` 在 `CreateItem`/`UpdateItem` 前拦截指令形态文本
  （覆盖指令、外传请求、`curl|sh`、私钥痕迹）。PoisonedRAG 的前提是把文本塞进百万级库，
  在写盘处拒绝比在检索处补救有效。
- **入嵌前剥离 URL/IP/CID**：`StripForIndex`（§6.2 要求）。

实测门禁：`go test -race ./internal/contentpolicy ./internal/knowledge ./internal/multiagent
./internal/handler ./internal/app` 全绿（全仓基线见上文「门禁复现命令」）。

**没做**：双模型（CaMeL）式数据/控制流分离仍是论文级，未实现；`agents/*.md` 与角色文本属于
"已审核、已安装、运维者知情同意"的制品，按已批准配置处理，不当作运行时不可信注入面——
这条边界是我明确做的取舍，写进 `capability-platform.md` 待你裁决。

### P6 Provider 层 —— 完成方言目录与一致性测试（其余项未做）

新增 `internal/provider`（零内部依赖的方言表）：**dialect 不等于 vendor**。
`Dialect{Vendor, API, BaseURL, ContextWindow, Cost, Capabilities, Retry, OverflowMarkers, DefaultBaseURL, AliasesTo}`，
API 只有三种线格式族：`openai-chat` / `openai-responses` / `anthropic-messages`。

- **决策改数据**：`openai / openai_compatible / claude / anthropic / deepseek / dashscope` 六行；
  加厂商=加一行（`Catalog.Register`），不改调用点。`Resolve()` 对未知名字退化到 chat 方言而非拒绝启动。
- **原先散落的 provider 字符串比较从 22 处降到 4 处**，且剩下 4 处经核对都不是 LLM 厂商判断：
  `handler/fofa.go`×2 是搜索测绘 provider 的空值默认；`eino_model_resilience.go:457` 是两个 channel
  的身份相等比较（failover 去重）；`config.go:2433` 是 **rerank 的独立 provider 命名空间**
  （已在代码注释写明不得与模型方言混用）。迁移点：`llm.IsClaudeProvider`、`reasoning/eino.go`×3、
  `multiagent` 的 agentic backend 判定、`handler/config.go` 的默认 base URL 与"能否列模型"。
- **迁移行为被测试钉住**：`TestAgenticBackendSetMatchesTheLegacyRule` 断言
  `""/openai/openai_compatible/claude/anthropic` 仍可用、`deepseek/dashscope/未知` 仍不可用；
  `TestVendorDefaultsAreData` 断言默认 URL 与列表探测范围与迁移前一致。
- **一致性测试套件**（12 个测试，报告要求的 5 个场景全部覆盖）：abort/499、context-overflow
  按各家 marker 归一到同一类且**不可重试**、tool-call-without-result 按方言声明判定、
  unicode 代理对经 JSON 编解码不变、跨厂商交接（openai↔claude↔anthropic 双向）保留
  工具调用 id/顺序/system 通道；另有 endpoint+auth 形状、流式增量合并、加厂商无需改码。

**套件立刻抓到两个我自己引入的真实缺陷**（这正是它存在的意义）：
1. `ParseRequest` 只接受 `[]map[string]any`，而经 `json.Marshal/Unmarshal` 的真实载荷解出的是 `[]any`
   → 跨厂商交接静默解出空对话；已改为 `asMapSlice` 兼容两种形态。
2. 把 `ListsModels` 建成 per-vendor 布尔后，`deepseek` 行忘填该字段=**静默剥夺了它原有的模型列表探测**；
   已改为由线格式族推导（`ListsModels = !IsAnthropicMessagesVendor`），零值陷阱消失。

**P6 其余项未做**：`*sql.DB` 嵌入拆除与按域切 Store、`AgentHandler` 分解、`setupRoutes` 拆分、
Eino 收口至 ≤1 包、session 事件溯源、20 对孪生函数合并（现在至少有了判据：那套一致性测试）。

### P6 `setupRoutes` 拆分 —— 完成

分域注册器落地：`setupRoutes` 从 **558 行 / 30 个位置参数** 降到 **107 行**，只建组、挂中间件、逐个调用
`internal/app/routes_<domain>.go` 的 25 个 `register<Domain>Routes`（app.go 2353 → 1928 行）。
`routeDeps` 结构体取代位置参数表，加一个 handler 不再需要同时改声明与唯一调用点的顺序。

**证明而不是相信**：新增 `internal/routes`（跨文件解析 group 绑定并迭代到不动点的 AST 提取器，
因为 `protected` 组在装配函数里创建、在注册器里作为参数使用），拆分前先把 278 条路由存成
`internal/app/testdata/routes.golden.txt`，拆分后 `TestRouteTableMatchesGolden` 断言**逐条相同**
（实测输出 `IDENTICAL: 278 routes`）；`TestEveryDomainRegistrarIsWired` 反向断言每个注册器都真被调用。

过程里被抓到两处：
- `internal/security/route_inventory_test.go`（每个受保护路由必须映射到权限目录，原有测试）只解析 `app.go`，
  拆完立刻覆盖率跌到 14 条而报错——这正是分域后必须共享提取器的理由。已改用它，审计范围反而从
  "app.go 里的 protected" 扩到全部 278 条中挂在鉴权组下的路由。
- 拆分工具的一次性脚本把 `misc` 当兜底桶（188 行），已再切成 tool-guard / storage / external-mcp /
  chat-uploads 四个域，`vulnerability-alerts` 与 `workflow-package*` 归入各自域，不留杂项文件。

**门禁自纠（后来发现，值得写下来）**：拆分时留下的**一次性取数探针**
`internal/routes/zz_dump_test.go` 一直**没有环境变量门控**，于是 `go test ./...` 每次跑都按"当前树的路由"
重写 `routes.golden.txt`——golden 门禁被它自己喂的那条测试**原地刷新**。这次是审计注入门禁交付时
例行 `ls internal/*/zz_*` 撞见的。已删除（`TestWriteGolden` 才是长期写法：`CSAI_WRITE_ROUTE_GOLDEN=1` 显式开关），
删除后复跑 `TestRouteTableMatchesGolden` 仍绿，说明它历次刷出来的内容与工作区里那份 golden
逐字节相同、**没有回归被固化进基线**——但这是运气：`internal/app` 在 `internal/routes` 之前跑，比对先于重写。
教训两条：①一次性探针文件交付前必须删或明确标注保留原因，`zz_` 前缀不是免责凭证；
②**"基线由被测方自己写"的门禁要在交付时检查写入路径是否门控**，否则它测的是"现在的代码等于现在的代码"。

`routes` 包同时被 `internal/handler/contract_test.go` 使用，删掉了它自己那份 AST 遍历（两处解析同一事实必然漂移）。

**运行时也验过，不只靠静态比对**：用拆分后的二进制在无关 CWD 起服务（无 `web/` 目录），
把 golden 里 75 条无参数 GET 路由逐条打过去——全部非 404（401/回调 404 由 handler 自身策略返回），
`/` 200、登录接口 401。即 `GET /api/robot/wecom` 的 404 来自 `HandleWecomGET` 在机器人未启用时自己返回的
`http.StatusNotFound`，路由本身在册。

### P6 数据层所有权 —— `hitl_interrupts` 真正只有一个主人

- `internal/database/stores.go`：按**消费者实际调用面**生成 19 个窄接口（`var _ XStore = (*DB)(nil)` 编译期断言），
  把"361 方法 + 内嵌 `*sql.DB` 被到处传"这件事变成有明确边界的契约。**注意**：我试过直接把 handler 字段类型
  换成这些接口，编译失败暴露出真实耦合面——handler 会把 `db` 再传给 `BatchTaskManager`/`HITLManager` 等
  仍要求 `*database.DB` 的结构，因此窄接口目前只是**契约与迁移目标**，尚未替换字段类型。这一步要连着
  改构造图，不是改类型名。HITL 迁移后 `AgentStore` 少了两个方法（`DeleteHitlInterruptLogs*`），
  `HITLStore` 起初保留原样（它的四条成员就是 `Begin/Exec/Query/QueryRow`，等于把「HITL 的消费面 = 裸 SQL」写进契约），
  第二片做完后已删除，见下。
- `internal/store`：两个域存储。
  1. `NotificationReads`（拥有 `notification_reads_by_user`：建表、读状态、幂等 upsert 事务、按用户保留 150 行）；
  2. `HITL`（拥有 `hitl_interrupts` 的**读、列、批删、按筛选清、保留期裁剪、payload 合并写、Agent 决策落库、dismiss**）。
     `internal/database/hitl_logs.go` 与其测试已删除——同一张表的 SQL 现在只有一份。
     `MutatePayload` 把原先"读 payload→合并→写回"两条独立语句收进**一个事务**：并行审批的结果写不再互相覆盖丢失。
- `internal/store/access.go`：会话可见性子句（owner / 会话直授权 / project owner / project 直授权 四条 OR）
  从 `notification.go` 的 handler 私有 helper 提成**一份共享、被独立测试的构件**。它原先被三个域的 SQL 拼接复用；
  `internal/store` 不 import `internal/database`，所以这里重述了 2 字段的 `Access`，并由
  `TestStorePackageHoldsNoHTTPConcerns` + `.go-arch-lint.yml`（`store.dependsOn: []`、`denies: database`）双向钉住：
  域存储只要一个连接就能独立测试，这不是说法而是编译期事实。`internal/hitl` 也不再 import `internal/database`。
- **我自己的测试抓到的两个真缺陷**（不是"顺手改进"，是原来就会错）：
  1. **裸 SQL 基线一直少算**：报告的 32 只数了 `h.db.`，而 HEAD 上同包 `HITLManager` 用 `m.db.` 又写了 **17 条**
     没有任何计数覆盖过它——原始 HTTP 层裸 SQL 其实是 **49** 条。本轮之前的计数是 47（前几轮把 `h.db` 降到 30），
     我此前写的 30/32 都是只看了一个接收者的局部事实。现 `TestHandlerRawSQLRatchet` 对**两个接收者**分别设基线，
     并用一次性探针分别验证两者**都会失败**（`h.db`：注入 1 条→报 20>19 并列出分布；`m.db`：注入 1 条→报 18>17）。
  2. **NULL payload 让审计条目静默消失**：`payload`/`message_id`/`tool_call_id`/`decision`/`decision_comment` 是可空列，
     而旧代码把它们 scan 进 `string`，scan 报错后 `for rows.Next() { if err != nil { continue } }` **静默跳过该行**
     ——一条 payload 为 NULL 的审批记录不会出现在审计日志列表里，`GET /api/hitl/logs/{id}` 则直接 500。
     新 `scanInterrupt` 对可空列用 `NullString`，`scanInterrupts` 改为**返回错误而不是跳行**：宁可报错也不让合规记录凭空蒸发。
- **HTTP 契约有端到端回归了**：新增 `internal/handler/hitl_endpoints_test.go`（真 DB + 真 gin 路由 + 真 RBAC 用户/授权，
  5 个用例）钉住侧栏读的 16 个 JSON 键、`decidedAt` 必须是 `null` 而非零值时间戳、两个视图各自的排序
  （日志按 `COALESCE(decided_at, created_at)`，人工队列按 `created_at`）、分页与 `retentionDays`、
  `decidedBy=agent` 归一化、自由文本搜索、越权 403 / 未知 404 / 无会话=空列表且详情 403、
  dismiss 只能成功一次、批删永不删 pending、"清空当前筛选"同样受可见性约束。
  这些是**迁移前没有覆盖**的路径——只靠 ratchet 只能证明 handler 不再写 SQL，证明不了前端还能拿到同样的 JSON。
- `internal/hitl/retention_test.go` 重写：原先它只测 `retention_days=0` 一条且要拖整个数据层进来；
  现在用 1 方法消费侧接口测 4 件事（0 不调用存储、按天数算 cutoff、存储报错不 panic、无存储不 panic）。

**第二片：把 `HITLManager` 的 17 条与 `internal/app` 的 4 条也收进存储**（这一片做完，
`hitl_interrupts` / `hitl_conversation_configs` 在**生产代码**里只剩 `internal/store` 一个写入者）：

- `internal/store/hitl_lifecycle.go`：`EnsureSchema`（两张表的建表 + `reviewer`/`decided_by` 迁移回填 +
  重启时把上一进程遗留的 pending 置为 `cancelled/system`，返回条数）、`CreateInterrupt`、
  `Resolve` / `ResolvePending`（后者把 `WHERE id=? AND status='pending'` 的竞态守卫放进语句里，
  超时/取消**不可能**覆盖已经落库的人工决策）、`Decision`（轮询面，区分「无此行」与「尚无决定」）、
  `LatestPendingMode`、`ConversationConfig` 三件套（upsert、`timeout<0` 归一为 0、空 reviewer 归一为 human）。
- `internal/store/session.go`：新增**会话域**存储，接管重启对账那条跨 `messages`/`process_details`/`hitl_interrupts`
  的取证查询与回写。分工是判据式的：*证据规则与事务*在存储（哪些占位符算已结束、`interrupted_at` 的取值优先级、
  内容守卫防覆盖、终态事件去重），*告诉用户的话术*留在 handler——持久层不该决定用户看到什么文案。
- `internal/handler/hitl.go`：`HITLManager` 的 `db *database.DB` 字段**删除**，改为 `interrupts *store.HITL` +
  `sessions *store.Session`；构造签名不变（各调用点仍传 `*database.DB`，内部取连接），
  于是 manager 只剩内存态（谁在等、谁已批准），所有持久读写都问存储。
  无连接时不再 panic 而是返回错误：审批**fail-closed**（`CreatePendingInterrupt` 直接失败而不是放行）。
- `internal/database/stores.go`：删掉 `HITLStore`——它的四条成员就是 `Begin/Exec/Query/QueryRow`，
  等于把「HITL 的消费面 = 裸 SQL」写进契约；现在这张表的契约是 `internal/store.HITL` 的方法集。
- `internal/app/c2_hitl_bridge.go`：**第四处写入者**（在我宣布「只有一个主人」之前查出来的）。
  C2 危险任务桥原本自己 `INSERT`/两条带 `status='pending'` 守卫的 `UPDATE`/一条轮询 `SELECT`，
  现改走 `CreateInterrupt`/`ResolvePending`/`Decision`；语义保持（行被撤回=允许，人工拒绝=拒绝，超时=系统拒绝）。
- `internal/store/ownership_test.go`：**全仓归属测试**——三张由 `internal/store` 拥有的表
  （`hitl_interrupts`/`hitl_conversation_configs`/`notification_reads_by_user`）在任何
  `internal/store` 之外的非测试 .go 文件里都不许出现语句（正则覆盖 `INSERT INTO|UPDATE|DELETE FROM|FROM|JOIN`，
  跳过 `testdata`/`generated`）。探针验证会红：临时加一条 `UPDATE hitl_interrupts` 到 `internal/app` →
  报出文件名与匹配片段。这条测试才是「一个主人」的真正判据——它扫的是**整个仓库**，
  而 handler 的 ratchet 只看得到 `internal/handler`，`internal/app` 那 4 条正是从这个盲区里挖出来的。

**第三片：`messages` 内容写回——一条语句被抄了 15 遍**

按文件数完剩下的 19 条之后才发现，其中 **15 条是逐字符相同的同一条语句**
`UPDATE messages SET content = ?, updated_at = ? WHERE id = ?`，抄在 6 个文件里
（`workflow_integration` 4、`multi_agent` 4、`eino_single_agent` 3、`finalization_helpers` 2、
`agent` 1、`batch_queue_executor` 1），参数顺序还各不相同——这正是报告说的孪生复制，只是复制的是 SQL。
- `internal/store/session.go` 新增 `SetMessageContent(id, content) (int64, error)`：返回受影响行数，
  让"消息行不存在"与"写成功"可区分；15 个调用点原本 `_, _ =` 全弃错，现在失败至少进一条 Warn
  （原先失败就是用户看到的"处理中..."永远不掉）。
- 另两条 `h.db.Exec` 是**近邻孪生**：`appendAssistantMessageNotice` 与
  `mergeAssistantMessagePartialOnCancel` 的 CASE 语句只差一个分支
  （`OR TRIM(content) = '处理中...'`）。合并为 `AppendNotice` / `AppendPartialOnCancel` 两个具名方法
  共享一条 SQL 模板，差异写成一个具名常量 `emptyWhenPlaceholder`；
  `TestAppendNoticeVersusAppendPartialOnCancel` 专门钉这条差异
  ——**合并错方向不会编译失败也不会让任何既有测试变红**（占位符会被追加而不是替换）。
  测试同时按原样断言分隔符是**字面四字符 `\n\n`**：SQLite 字符串字面量不做反斜杠转义，
  改动前后逐字节一致，我没有顺手"修成真的换行"。
- `AgentHandler` 新增 `sessions *store.Session`（与 `hitlStore` 同一个构造模式，
  `sessionStore()` 惰性取），handler 侧 `setMessageContent` 保留"空 id 直接跳过"的守卫语义。
- 逐字节核对方式：迁移后用 `git diff` 只应看到语句消失、换成一次方法调用；
  `grep -nE '\bh\.db\.(Exec|Query|QueryRow|Begin|Prepare)\(' internal/handler/*.go` 从 19 行降到 4 行。

**第四片：通知摘要的两处跨域读，以及它藏着的两个真缺陷**

`notification.go` 剩下的 4 条读的是**别人的表**，按域搬进 `store.Vulnerability.RecentFindings`
与 `store.Execution.FailedSince`（可见性子句随查询一起搬走，成为 `store.ConstrainFinding`：
owner / 资源指派 / 项目 / 会话四条路径，`all` 全放行、空主体 `AND 1=0`）。搬的过程中查出两条：

- **无会话的漏洞从铃铛里凭空消失**（与我此前修掉的审计行丢数据同类）：
  `conversation_id` 可空且 `CreateVulnerability` 用 `nullIfEmpty` 写 NULL，而旧代码 scan 进 `string`
  后对 Scan 错误回答 `continue`——于是项目级/手工录入的漏洞**既不条目也不计数**，
  而漏洞列表页正常显示它们（那里的查询 COALESCE 过）。现在查询里 `COALESCE(conversation_id,'')`，
  且 scan 失败是错误不是跳行。两条测试钉住：store 层 `TestRecentFindingsKeepsRowsWithoutAConversation`
  与端到端 `TestDigestListsFindingsWithoutAConversation`（真库真路由真权限，断言条目数与 `newHighVulns` 计数）。
- **`task_failed` 通知在服务端已停用**：见 §10 决策项 7。它的发现过程本身就是教训——
  两侧的**名字集合完全相同**（5 个全对上），只看名字的比对会判定"契约一致"；
  是加上**从 gin 入口点出发的可达性**才暴露那条生产者根本无人调用。
  新增 `TestNotificationProducersAreReachableFromTheDigest`（可达性孤儿必须 ⊆ 已登记集合，登记项含理由文本；
  把它接回去后测试会反过来要求你同步清单）与 `TestNotificationTypesMatchThePage`
  （页面分支而服务端发不出 = ratchet 1；服务端发得出而页面不分支 = 硬失败）。

**当前水位**：handler 包裸 SQL **0 条**（`h.db` **0** + `m.db` **0**；重构起点 49），
`m.db` 基线归零意味着 HITL 相关的两个类型都不再自己写 SQL；已分别用一次性探针验证**两个指标都会红**。
硬零意味着传输层不再拼任何 SQL：`rawSQLFloors` 把八个文件逐个钉 0，`h.db` 基线 0 已用探针验证会红
（注入一条 `h.db.Query` → 报 `grew to 1 (baseline 0): [notification.go=1]`）。
可达性门禁自身的两个洞也是探针逼出来的：
①注入一个返回 `[]NotificationSummaryItem{{Type: "probe_orphan_type"}}` 的孤儿生产者，**最初没报**——
省略了类型的复合字面量要靠容器才解析得出，补上 `[]T{…}` 元素解析后立刻报 `2 > 1` 并点名函数；
②把页面已分支的 `c2_session_online` 改名 → "发得出而页面不分支"方向如期硬失败。
反过来，最初用**文本模式**抓 `Type:` 会捞进 MCP 内容块与机器人消息的同名字段
（凭空多出 `user`/`conversation`/`external_mcp` 等 7 个"事件类型"），改成只认
`NotificationSummaryItem` 字面量后集合归位到 5 个——又是一次"判据口径必须先于数字"。
`internal/store` 共 **52** 个用例（除「全仓归属扫描」与「无连接必须拒绝写入」两条外，
其余每一条都开真实 SQLite 文件库，零 mock），
覆盖包括「NULL 可空列不得让审计行静默消失」
「证据规则不许把还可能恢复的占位符改掉」「超时不得覆盖已落库的人工决策」。
外加 `internal/handler/hitl_endpoints_test.go` 的 5 个端到端用例。

**下一步（同一模式的延续，不是新设计）**：`notification`(2，跨域读 vulnerabilities/tool_executions，
应并入漏洞与执行存储) 是本指标里最后的存量；`workflow_integration`/`multi_agent`/`eino_single_agent`/
`agent`/`finalization_helpers`/`batch_queue_executor` 已在第三片清空并逐个钉成水位 0。
报告 §3 里"handler 还剩 12 处跨域表访问"（`agent.go` 20 处跨包调用）不在本指标口径内——那是**方法调用面**
而非裸 SQL，要在窄接口替换那一轮一起收口；
域存储齐了之后再回头把 handler 字段换成 `internal/database/stores.go` 的窄接口并收口构造图。

### P6 数据层第五片 —— 窄接口从"纸面契约"变成真实字段类型

`internal/database/stores.go` 那 19 个接口此前**只是契约**：字段类型仍是 `*database.DB`，
所以任何 handler 依然能顺着字段摸到全部 361 个方法。这一片把 **18 个域**的字段换成各自的消费者接口，
外加删掉一个**死字段**（`KnowledgeHandler.db`：该类型只有 knowledge.go 一个文件、全文没有一处读它，
诚实的处置是连构造参数一起删，而不是为它凭空造一个 store）：
`AgentStore / AssetStore / AttackChainStore / AuditStore / BatchTaskStore / ChatUploadsStore /
ConfigStore / ConversationStore / MonitorStore / NotificationStore / OpenAPIStore / ProjectStore /
RBACStore / RobotStore / SkillsStore / VulnerabilityStore / WebShellStore / WorkflowStore`
（实测口径：`internal/handler` 非测试文件里 `*database.DB` 结构体字段 **19 → 0**，窄接口字段 0 → 18；
990 个结构体字段全扫，无一例外，`*sql.DB` 字段也是 0）。

**最后三个域是靠"把接口声明在链条另一端"拿下的**，这一层的做法值得复制：
`multiagent` 自己**一个数据库方法都不调**，它只是把 handle 转发给 `internal/project`——
所以真正要声明接口的是 `project`（13 个方法：项目行 + 事实与事实边账本），`multiagent` 收 `project.Store`；
`agentfinalizer` 只要 2 个方法（读/写一次工具执行）→ `database.ToolExecutionLedger`；
`attackchain` 要链的节点边 + 会话证据 + 提升为项目时的事实账本 → `database.AttackChainLedger`；
workflow 引擎要自己的 run/node-run 账本 + 项目事实 → `workflow.Store`（组合两者）。
多个包共用的面**声明在 `internal/database/surfaces.go`**（消费者都 import 它，声明在消费者侧会成环），
再由消费包用**类型别名**指回去（`project.Store` / `agentfinalizer.Store` / `attackchain.Store`）——
一份清单、一处 `var _ X = (*DB)(nil)` 编译期断言，不把 13 个签名抄三遍。
`Surfaces 里刻意不放 Close`：消费共享句柄的一方不该能关掉它。

**`database.Narrow` 不是可有可无的包装**，它是这一片唯一的安全性来源：
`var store AssetStore = (*DB)(nil)` 得到的是**非 nil 接口**，于是传输层里所有
`if h.db == nil` 守卫（实测 **64 处**生产代码，"模块未启用数据库时回答 database unavailable 而不是 panic"
的那条路径）会**永久走错分支**——而这一切编译通过、启用路径的测试也全绿。`Narrow` 把 nil 指针映射成 nil 接口。

**这个陷阱是被真实测试抓到的，不是被论证出来的**：我最初的替换正则只认 `db: db,`（单个空格），
而 `robot.go` / `conversation.go` / `notification.go` / `openapi.go` / `vulnerability.go`
五处是对齐写法 `db:                   db,`，于是字段换成了接口、赋值却没走 `Narrow`。
`go test ./internal/handler/` 立刻 panic：
`database.(*DB).GetRobotSessionBinding(0x0, …)` ← `TestRobotModeRejectsUnavailableMultiAgent`
用 `NewRobotHandler(cfg, nil, nil, logger)` 构造，正是"无数据库"那条分支。
**编译器在这件事上完全无能为力**（`*DB` 隐式实现接口），所以补了三条永久断言：
- `TestNarrowedStorageStaysNilWithoutADatabase`：对清单里每个 handler 用 `nil` 构造，反射取 `db` 字段，
  要求它**既是接口又是 nil**；字段类型不是接口就直接判"这个域没被窄化/清单过期"。
- `TestNarrowedStorageKeepsALiveDatabase`：反方向——活 `*DB` 不得被降成 nil（否则好端端的部署会答"数据库不可用"）。
- `TestNarrowRejectsAnInterfaceDBDoesNotImplement`：`Narrow` 的 panic 分支不是死代码。
探针验红：把 `robot.go` 的赋值改回 `db: db,` →
`RobotHandler built with a nil *database.DB holds a non-nil database.RobotStore - the typed-nil leak …`；
把某个字段改宽回 `*database.DB` → **编译失败**（比反射断言更早）。均已撤销、复验绿、`grep` 确认无残留。

**门禁已从 ratchet 翻成硬零不变量**（`internal/layering/concrete_db_field_ratchet_test.go`，
现名 `TestHandlerLayerHoldsNoGodObject`）：判据不再是"比昨天少"，而是**HTTP 层任何结构体都不得持有
`*database.DB`（也不得持有 `*sql.DB`）**；配两条反空跑下限——扫到的结构体字段总数 `>= 500`（实测 990），
窄接口字段数 `>= 18` 只升不降。新增持有者**硬失败并告知去处**
（探针：给 `skills.go` 加一个 `probeRaw *database.DB` 字段 →
`1 handler struct fields still hold the whole database handle: internal/handler/skills.go: 1 field(s) of type *database.DB`）。

第二条门禁 `TestNarrowedFieldsAreOnlyAssignedThroughNarrow` 管**形状**：凡声明了窄接口的文件，
其对 `db` 字段的每一处赋值都必须经 `database.Narrow`。它交付时**第一次探针没变红**，原因是我把
模块相对路径又拼了一次目录前缀，读文件失败就 `continue`——于是集合为空、门禁永远绿；这正是本仓库
"空集合等于通过"的老坑。现在每条判据都带**非空硬失败**：清单里的文件若一条赋值都没扫到就直接
`t.Fatalf`，并且总赋值数 `< 18` 也失败。修好后探针才如期报
`internal/handler/asset.go assigns a narrowed storage field via db`。
同一轮还纠出一个假阳性：`setterRe` 把 10 处 `if h.db == nil` **比较**当成了赋值（RE2 没有负向先行断言，
只能显式要求 `= ` 后一个字符不是 `=`），干净树上就红了。两条经验：
**每条新门禁都要跑"注入违规必须红 + 干净树必须绿"两次**，缺一次它就不可信；
**文本判据必须防"读到空"**，空集永远满足"没有违规"。

**最后三个域是怎么打通的**（它们此前不是"没轮到"，而是 `h.db` 流进了别的包的签名）：
`agent.go` → `multiagent.RunDeepAgent` / `RunEinoSingleChatModelAgent` 与 `agentfinalizer.FromRunResult`
（另有 `batch_queue_executor.go`/`eino_single_agent.go`/`finalization_helpers.go` 四个调用点）；
`project.go` → `internal/project` 与 `internal/attackchain` 的 6 处；
`workflow.go` → `workflowrunner.RunArgs.DB` 结构体字面量。
解法就是上一段说的"接口声明在链条另一端"。**这一层一共走通过五次**（区别只在于要改签名的是本包共享函数还是别包入口）：
`conversation.go` —— 两个共享历史渲染器 `processDetailsToJSON` /
`enrichEmptyToolCallArgumentsFromExecution` 原本收 `*database.DB`，实测量是**它们只调一个方法**
（`FindNearestToolExecutionArguments`），于是改成单方法接口 `toolExecutionArgumentSource`，字段随即窄化成功。
`audit.go` —— 唯一逃逸点是 `audit.ApplyResourceAvailability(h.db, row)`，它顺着"这条审计记录指向的资源还在吗"
需要 **8 个存在性查询**。给 `internal/audit` 声明 `ResourceExistenceSource`（8 个方法，消费者自己写），
把 `ApplyResourceAvailability` / `resourceStillExists` 的参数从 `*database.DB` 换成它，`AuditStore` 补齐那 8 个成员，
`AuditHandler.db` 就成了接口——**债面 8 → 7**。这条也顺带证明：`ApplyResourceAvailability` 里那句
`if db == nil` 只有在调用方传的是 `database.Narrow` 产物时才仍然成立，因此注释里写明了这一点，
并把 `AuditHandler` 加进 `narrowedHandlers` 清单（探针：把赋值改回裸 `db` →
`AuditHandler built with a nil *database.DB holds a non-nil database.AuditStore - the typed-nil leak`，已撤销复验绿）。
`monitor.go` —— 4 处逃逸全打在**本包**两个可见性辅助函数上
（`filterToolExecutionsForAccess` / `toolExecutionVisible`），实测它们只需要 **1 个方法**
`UserCanAccessResource`，于是改成单方法接口 `conversationAccessLookup`；`MonitorStore` 同样缺这一成员
（第三个暴露"按直接调用面生成"盲区的例子），补齐后债面 **7 → 6**，
探针同样报出 `MonitorHandler … the typed-nil leak` 后撤销。
`attackchain.go` —— 唯一别包逃逸是 `attackchain.NewBuilder(h.db, openAIConfig, h.logger)`；
实测 Builder 自己的调用面是 **9 个方法**，于是在 `internal/attackchain` 声明 `Store`、把 `Builder.db`
字段与构造参数都换成它（**这个包本身也不再持有 361 方法对象**），`AttackChainStore` 补成它的超集，
handler 字段随即窄化，债面 **6 → 3**（`knowledge.go` 的死字段与 `batch_task_manager.go` 先一步降到 4/3）。
`batch_task_manager.go` —— 反例很有参考价值：它的 `m.db` 有 22 个方法调用面却**零逃逸**，
所以换字段类型后编译一次通过，说明 `BatchTaskStore` 当初就是按这个消费者的真实调用面生成的。
`knowledge.go` —— 第三个反例：字段**根本没人读**。删除字段与构造参数（两处 `internal/app` 调用点跟着改），
比给它编一个接口更诚实，也不会留下"看起来已窄化"的假账。

**顺带修了一条生成器口径缺陷**：`ConversationStore` 里**本来没有** `FindNearestToolExecutionArguments`，
因为那份接口是按"`h.db.X` 直接调用面"生成的，而调用发生在**经参数传递的接收者**上。
漏项的表现是"字段一换类型就编译不过"——正好是编译器能帮忙的方向，故当场补齐并在 `stores.go` 注明原因。

**还纠了一次我自己的计数口径**：新增的 `FieldTypesByFile` 第一版把 `go/ast` 里所有 `*ast.Field`
都当结构体字段，于是 `func NewX(db *database.DB)` 这种**函数参数**也被算进债面——
测出 31，真实 8（那一轮的值，此后又降到 7）。**基线若按 31 钉，上线即松了近四倍**，这正是"先声明遍历域再数"的意义所在；
现在只遍历 `*ast.StructType` 的字段列表。

### P6 分解第 3–5 刀 —— 按"为什么改"切，并把 6,816 行手写 OpenAPI 拆到域

**边界先定义，再动刀**（上一轮留下的阻塞点正是"没定边界就切"）。判据是三个互斥的问题：
规则*是什么*——在没有任何东西运行的时候被编辑 → 归 `HitlPolicy`；
已经挂起的审批——谁在等、等什么 → 归 `HITLQueue`（第一刀已落）；
工具调用里等一个决定 → **留在 run loop**，因为它和 task、会话历史、SSE 写入器同呼吸，
搬走它就得为搬运再造两层接口。

| 面 | 数字 | 门禁 |
|---|---|---|
| `AgentHandler` 方法 / 文件 | 130（起点）→ 112（两刀）→ **91 / 21** | `agentHandlerMethodCeiling = 91`、`agentHandlerFileCeiling = 21`，只准降；扫描下限 85 防"读到一个空表" |
| 第三刀 `a8b2105` | 11 个审批配置端点搬进 `HitlPolicy`（它自己持有写通道、manager、queue），112 → 102 | 路由表必须把这 11 条按 (path, method) 逐条挂在 `HitlPolicy()` 上；`AgentHandler` 不许长回旧名字（探针：加一个同名孪生方法即红） |
| 第四刀 `0a81f48` | 第三刀只搬端点、留下 10 个方法的接口回读 agent —— 挂着边界名字的访问器袋。改成策略自己持有已加载配置与共享快照：有效默认值、豁免名单合并、审计引擎解析、"这个会话跑在哪条规则下"全部内聚。102 → 91，handler 包净减 131 行 | `SetSettings` 必须转发给策略：构造期捕获快照 = 运维者改默认值只落进 `config.yaml`、不落进运行规则，正是活快照存在的理由（真机点验 PUT 后回读） |
| 第五刀 `73d00f7` | 企业微信传输面（签名校验、AES 信封、被动回复体、主动发送）从 `RobotHandler` 抽进 `WecomGateway`；replay guard **留在** handler（Lark 回调共用），网关经 3 方法 inbound 接口问它自己答不了的问题。68 → 63（原先 64 个方法挤在 1,979 行单文件） | 逐 (path, method) 断言网关注册；注册器门禁从只认 `(protected)` 扩到接受 `api` 组（把参数名收窄去凑检查是撒谎）；探针：把任一线协议方法长回 `RobotHandler` 即红 |
| 第六刀 | 挂起审批的**应答面**（`ListHITLPending`/`DecideHITLInterrupt`/`DismissHITLInterrupt`）从 `AgentHandler` 归 `HITLQueue` —— 按已定的边界，"谁在等人、等什么"这件事本来就归它。顺带修掉一处真实的私有状态泄漏：`DismissHITLInterrupt` 原先在传输层里自己锁 manager、从 `pending` map 删除、往 `decideCh` 塞拒绝 —— 一个类型的加锁规则内联进另一个类型的 HTTP 代码；现由 `HITLManager.DropPending` 承担。`q.manager` 经构造函数注入，不新增 setter（整包 64 未动）。91 → **88** | `TestPendingInterruptRoutesAnswerFromTheQueue` + `TestPendingInterruptMethodsAreNotOnAgentHandler`（agent 长回即红、queue 丢掉即红）；`TestHITLManagerPrivateStateStaysPrivate` 走 AST 禁止 manager 方法之外经字段句柄触碰 `mu/pending/runtime/approvedExec/globalWhitelist/decideCh`；`TestDismissWakesTheWaitingToolCall` 从线上验证"关掉审批会叫醒等待中的工具调用" |

**第六刀的三条私有状态门禁踩过的三个坑**（都记在这里，因为它们对任何"文本式门禁"都成立）：
① 用 regexp 扫行会把注释当代码 —— `batch_task_manager.go` 里两句"必须在持有
`BatchTaskManager.mu` 下调用"被报成泄漏；改用 `go/ast` 后注释天然不在场。
② `*ast.Field` 同时覆盖**结构体字段、函数参数、接收者**：按"类型为 `*HITLManager` 的 Field 名字"
收句柄，收到 manager 自己的接收者名 `m`，于是全包每一处 `m.mu`（含 `AgentTaskManager` 的）都红了。
③ 修正后只认**结构体声明的字段**且只看两跳形状（`h.hitlManager.mu`、`q.manager.pending`），
接收者/参数不算句柄；已知盲区是"先 `local := h.hitlManager` 再读 `local.mu`"，
用可读性（`DropPending` 是显而易见的调用）与 review 兜，而不是把门禁做成类型推断。
④ 探针三连：新增一处 `len(h.hitlManager.pending)` → 隐私门禁红；把 `ListHITLPending` 作为孪生方法长回
agent → 接线门禁红；把 `DropPending` 改成空函数 → 唤醒断言按"超时自动拒绝"失败（等待方确实被叫不醒）。

**手写 OpenAPI 文档按域拆（`6d7e905`）**：`handler/openapi.go` 6,816 行、3 个函数，内联
map 字面量独占第 31–6,774 行 —— 报告 §1 记的"契约面最陡的一处"。现按域分成
`openapi_paths_{chat,knowledge,capabilities,mcp,ops}.go`（41/25/32/6/14 条路径）+
`openapi_components.go`，handler 只剩组装（110 行）。数据保持**函数而非常量**：
`enrichSpecWithI18nKeys` 会就地往每个 operation 写 `x-i18n-tags`，提成包级共享 map 就是两个
并发 `GET /api/openapi/spec` 同时写同一块内存。

验收按"最硬的那条"来：拆前拆后各取一份**服务出来的**文档，`cmp` 无差异
（155,278 B、118 路径、157 操作）。留下的永久门禁三条，都跑过探针确认会红：

| 门禁 | 探针 | 现象 |
|---|---|---|
| `TestOpenAPIGroupsDoNotOverlap`（分组不相交 + 部分之和 = 合并总数） | 把 ops 组一条 path 改成 chat 组已有的 | `path /api/agent-loop/cancel is declared by both the chat and the ops group`，并在合并器 panic 之前先报出两个文件 |
| `TestOpenAPIGroupFloors`（每组路径数下限，只准升） | 删掉 `/api/robot/lark` 整块 | `the ops group documents 13 paths, floor is 14` |
| `TestOpenAPIOperationsGolden`（`METHOD /path\|operationId\|tag` 157 行 golden） | 改一个 path 名 | dropped/added 两个方向各打印该行；golden 空文件本身判失败 |

复现：

```sh
go test -count=1 -run 'TestOpenAPI' ./internal/handler/                  # 拆分的三条门禁
go test -count=1 -run 'TestHandlerSizesOnlyShrink|TestHandlerSettersOnlyShrink|TestHandlerLayerScanIsSane' ./internal/layering/
go test -count=1 -run 'TestPendingInterrupt' ./internal/app/             # 应答面的接线与"不许长回 agent"
go test -count=1 -run 'TestHITLManagerPrivateStateStaysPrivate|TestDismissWakes' ./internal/handler/
CSAI_WRITE_OPENAPI_GOLDEN=1 go test ./internal/handler -run TestOpenAPIOperationsGolden   # 故意改文档时才重生成
```

### P3 前端 —— `_t` 一族收口（同名不同义，比"重复"更危险）

`_t` 此前写在 **7 个**脚本里，而且是**三种不同实现**：5 个"i18next 有就用、没有就给 key"；
`workflows.js` 把"答案等于 key"也算没译、并吞异常；`roles.js` 在此基础上还内置三条中文兜底文案。
名字相同、语义不同、且都在同一页的全局作用域里互相覆盖 —— 一个页面在缺词条时显示 key 还是显示中文，
**取决于脚本加载顺序**。这与 §11 转义器那一条是同一类缺陷，只是危害小一级。

收口成 `web/static/js/i18n-tag.js` 的**两个具名行为**（不做合并，因为两者不等价：
`tOrKey` 把"故意译成空串"照原样传出去，`tFallback` 把它判为未译）：

| 落点 | 改法 |
|---|---|
| 5 个纯实现 + `monitor.js` 内 4 处函数级 `_t` | 一行委托 `CSAI.tOrKey`（**保持函数声明**以免改动提升语义） |
| `workflows.js` | `CSAI.tFallback(key, opts)` |
| `roles.js` | `CSAI.tFallback(key, opts, ROLE_COPY_FALLBACK)` —— 本页文案回到本页，通用取值器不再携带某页词汇 |

门禁 `web/static/js/i18n-tag.test.cjs`（6 条，`make js-check` 自动带上，前端测试 212 → **218**）：
两种语义各自的行为表（空串 / 非字符串 / 抛异常 / `constructor` 不许经 `Object.prototype` 应答）、
"不许有脚本自己实现查找"（按名字 `_t` + 按 body）、**与名字无关**的 ratchet（数还写着
`typeof window.t === 'function'` 的文件数，基线 **23**，只准降）、本页文案必须留在本页、
每个模板都必须先加载 `i18n-tag.js`（控制台与 `/api-docs` 两个模板分别判）。
四条探针：改回一份 `_t` 手写实现即红；**换一个不在名单里的新名字** `tr()` 时按名字的测试不红、
按残留的 ratchet 红（23→24）；模板里删掉一行 `<script>` 即红；往通用文件里粘一张文案表即红。

**这条 ratchet 就是"重构不彻底"的诚实记账**：`_t` 这一族收口了，但 23 个脚本仍在调用点旁边
手写同一个判断（`roles.js`、`tasks.js` 都在两处），所以不能写成硬零；逐文件 ES 模块改造完成后
它才有机会归零。

真机点验（headless Chrome + CDP，两个页面各自的真实 `window`）：`CSAI` / `tOrKey` / `tFallback`
在全局可解析、`typeof _t === 'function'`、`_t('roles.noDescription')` 返回「暂无描述」而不是 key、
未知 key 仍返回 key、`i18next.language = zh-CN`、**零未捕获异常**。Node 侧那处要额外
`ctx.CSAI = ctx.window.CSAI` 才跑得动，因为浏览器里 `window` 就是全局对象、vm 里它只是一个属性
—— 这条差异本身也是"必须有真浏览器点验"的理由。

### 能力包携带可执行代码（2026-10-05 夜，插件化最后一公里）

**此前插件化的边界在哪**：包能带角色 / 子代理 / 技能 / 配方 / MCP 声明五类**内容**，
`internal/pluginhost` 也能在进程外跑代码——但两者从未接上：ABI 里 `capabilities/list`
从写下那天起就**没有任何调用方**（只有测试固件自己答一遍），要跑一个包里的二进制，
必须有人在 `config.yaml` 手写一条配方 + 一个信任域。所以"装个包就多个能力"这句话，
对可执行代码一直不成立。

| 落地 | 内容 |
|---|---|
| 第六类 kind `plugin` | `plugins/<name>.yaml` + 包内 `bin/<name>`；`pluginId` == 单元名 == 信任域三处必须同名（能力 id 首段就是路由目标，不同名会把调用送到别的进程） |
| 宿主侧发现 | `Instance.Capabilities` / `Service.ListCapabilities` 真的去调 `capabilities/list`；只接受字符串数组（形状不对即拒绝，不能悄悄解成空表——空表的意思是"什么都不提供"） |
| **双向交叉核对** | 清单里有、插件没报 → 该入口调用必失败；插件报了、清单里没有 → 它的 `class/permission/grants` 没人审阅过。**两种都拒绝整单元**，并回收刚声明的信任域、把开关退回停用，409 里指名差在哪 |
| 权限来源 | `class` / `permission` / `grants` **只来自声明文件**（人写的、跟包一起签的），永不来自插件自我描述——能自我描述的组件就能把自己描述成 `destructive` |
| 归属与优先级 | 与包声明的 MCP 服务器同源：`config.yaml` 已有的信任域优先，包不能覆盖；卸载包也不能删它；一个域只有一个主人；**换声明必关旧进程**（旧 grant 不能继续跑） |
| 同意边界 | 安装只进表：不声明、不启动；开关才声明 + 启动 + 核对；开关的"开"**不落库**，每次启动都要重新核对（否则升级后的包就在跑没人重新批准过的代码） |
| 运行时可见 | `GET /api/plugins` 带 `pluginHost`（域 / 属于哪个包 / 是否在跑 / 重启次数 / grants / 出网代理地址），控制台在单元行上直接显示；否则崩溃重启循环看起来是"健康"的 |
| 能力身份 | 登记进 `capability.LayerPlugin`，按单元成组装卸；runtime = `plugin-host:abi`（前缀命中既有的进程外路由，**不加新分支**——那个前缀判断就是防不可信代码进本进程的唯一屏障） |

**门禁与探针**（都跑过"注入即红 / 撤销即绿"）：
`internal/pluginhost/discovery_test.go`（真子进程：发现集合、越界发布者、对象形状、空白 id、
发现与调用共用一个实例）；`pack_domains_test.go`（不启进程、文件优先双向、单主人、
换声明关掉旧实例、端到端发现+调用）；`internal/handler/plugin_unit_test.go`（声明装载、
包外二进制含**软链逃逸**、十种不可审阅的清单写法、装完即停用、核对失败回滚、开关与卸载都清空）；
`internal/app/plugin_capabilities_test.go`（真固件端到端 + 双向不一致各拒绝 + 无宿主时明确拒绝）；
`TestEveryCapabilityKindHasAConsoleLabel`（六类 kind 在两份字典都要有标签，且不许有僵尸标签）；
`plugins-ui.test.cjs`（单元行上的运行态、以及 `pluginsT` 用到的键两份字典都要有）。

**顺手修掉的一个反向错误**：`plugin_host.enabled: true` 但域列表为空时，装配**不装宿主**，
于是包永远无法声明第一个信任域——那等于要求运维者先手写一个域，别人的插件才能跑。
现在"启用但没有域"是一个空的可用的宿主；只有 `enabled` 为假才没有宿主。
宿主未配置时启用被明确拒绝，**不退化成"在本进程里试着跑一下"**。

**同日补上的 provenance**：`plugin` 单元的指纹是「声明 + 它点名的二进制」合成的一份
（`plugin.DigestPaths`；安装与 `Drifted()` 同一算法，所以**装完之后换掉可执行文件会被报成漂移**，
而不是继续当没动过）。 登记的每个插件能力带 `Publisher` + `ArtifactDigest`（后者只含二进制：
撤销针对一次构建，改一行说明文字不该让黑名单指向别的东西），执行路径的 `revocation` stage 每次调用都查
——`TestRevokedPackPluginBuildIsNotCallable` 走真 authorizer：按摘要撤销该 build 下一次调用即拒、
按发布者撤销整包即拒、内置代码不受影响。两条都有反向探针。
**同晚把最后两段接上**：① 包内插件能力进 MCP 工具面（`ToolLayer.registerPackPluginTools`，排在内置
注册之后、同名保留内置——包能新增入口，不能顶掉产品已有的名字），并补上此前**全仓零测试**的执行路径那一段：
`internal/security/plugin_routing_test.go` 两面钉住「plugin runtime 绝不退回进程内执行」与非插件 runtime
仍走配方路径（探针把 fail-closed 换成 fallthrough，`/bin/echo --text hi` 真就跑起来了，测试即红）；
② 启动过程把包里 `plugin` 单元置为停用（`declarePackPluginUnits`），因为那一刻宿主并没有它的信任域，
界面却显示「已启用」并把原因写成一次没发生过的启用失败——这是 MCP kind 早已做对、plugin kind 漏掉的
另一半，只迁一半比不迁更有害。

**下一刀的前置条件已量出来（2026-10-06）**：`audit_logs` 那 5 个方法之所以不能直接搬进 store，
是因为它依赖的两个 SQLite 时间工具（`formatSQLiteUTC`、`sqliteEpochGE`）**住在 `internal/database`**，
而 store 侧现在用**另一种写法**表达同一个谓词——实测 `CAST(strftime('%s', <列>) AS INTEGER) > ?`
出现在 `store/hitl.go:440`、`store/execution.go:44,47`、`store/vulnerability.go:51,53`，
而 `strftime('%s', <列>) > strftime('%s', ?)` 出现在 `database/sqltime.go:15-16`。
**同一件事两个入口**，且参数编码不同（绑定整数秒 vs 绑定 UTC 字符串）。
**这一条已经做完（2026-10-06 同晚）**：新建 `internal/sqltime`，四个入口
`UTC` / `Compare` / `Seconds` / `SecondsOrNull`，两处旧写法都从它发出——
`database/sqltime.go` 的两个私有工具退化成一行委托（11 个调用点不动、编码不变），
`store/{execution,vulnerability,hitl}.go` 的五处字面量改成拼接。
参数编码为什么**故意保留两种**：一个调用点绑什么由它已经存进占位符的东西决定，
统一它要改的是数据写法而不是表达式写法，那是另一件事、另一刀。
漂移怎么证的：`internal/sqltime/sqltime_test.go` 把四条**逐字节等于搬迁前**的 SQL 字面量钉住
（改动一个字节就红），加上 `UTC` 的时区无关用例；防回潮用
`TestSQLiteInstantSpellingHasOneHome`——`strftime('%s'` 只许出现在 `internal/sqltime` 里
（探针：在 `store/access.go` 抄一份即红），并已进 `make layering-check`。
剩下两个前置条件还在：`AuditStore` 里混着的**存在性查询**要先按表拆开，
`database.AuditLog` / `ListAuditLogsFilter` 两个类型有 5 个文件在用要一起搬。

**仍然没有做的**：包内二进制的**制品签名链**（现在靠声明+双向核对+摘要撤销，没有 Ed25519 覆盖可执行文件）；
netns/seccomp 级硬出网边界（插件仍走宿主侧 CONNECT 代理 + `StrictEgress`，这是代理白名单不是内核边界）；
registry 服务端与气隙包导出。

### P6 数据层第六片 —— `skill_stats` 回到它自己的 store（2026-10-06）

`internal/database/skill_stats.go` 是 5 个挂在 `*DB` 上的方法 + 1 个**没有任何调用方**的
`SaveSkillStats`。表的写入方有两个（skills 页面读、agent 运行循环累加），主人没有。
本片按前五片的同一形状落地：

- `internal/store/skill_stats.go`：`SkillStats` 只吃 `*sql.DB`；**DDL 一起搬过来**
  （`EnsureSchema()` 幂等），因为「查一张别人建表」的 store 只算搬了一半。
  启动由 `internal/app` 的 `ensureSkillStatsSchema(db)` 调用，与 `capability_unit_switches`
  同一位置、同一条理由。
- 两个 handler 各持 `*store.SkillStats`：`SkillsHandler.SetDB` 改为构造 store（nil `*database.DB`
  仍是 nil store，方法答错误而不 panic）；`AgentHandler` 在构造里 `stats: newSkillStatsStore(db)`。
  `database.SkillsStore` 整个接口与 `AgentStore` 里那一行随之删除。
- **两处刻意偏离，都写进注释**：① 行扫描失败原来 `logger.Warn` + `continue`（那一行统计就静默消失），
  现在 `return err` + `rows.Err()`——与漏洞域那一刀同一判据；② store 不带 logger，
  原先「Exec 失败记 Error + 调用方再记一次」的重复日志少一条，错误仍原样回给调用方。
  HTTP 侧的字面不变：500 的 `"数据库连接未配置"`、`"清空统计信息失败: "` 前缀、
  `stats[].last_call_time` 的 `2006-01-02 15:04:05` 格式、无库时列表照样 200 且计数为 0。
- 数字：`*database.DB` 方法 **358 → 353**；新增 `TestDatabaseSurfaceOnlyShrinks`
  （只降门禁，基线钉在 353，遍历域 = `internal/database` 全部非测试文件，实测 27 个文件有方法），
  归属门禁 `TestOwnedTablesAreOnlyWrittenFromThisPackage` 的表清单加 `skill_stats`；
  两个门禁都双向探针验红（长一个 `*DB` 方法即红、在 handler 里写一条 `UPDATE skill_stats` 即红）。
- 测试两层：store 侧 5 条真库用例（建表幂等、`+=` 累加、`COALESCE` 不擦时间戳、
  NULL 时间戳回 `nil`、清空的作用域、无连接被拒）；handler 侧 2 条走真实 gin 路由的契约用例
  （响应键集合与格式、按名清空后归零、全清文案、无库边界）。
  **契约用例差点假装通过**：技能目录名不符合 skill 规范时，`ListSkillSummaries` 会静默跳过它，
  于是 `total_skills: 0`、断言"看不见就等于没发生"——第一版就在这里红了一次。

### 数据层的反面：`*DB` 上**没人调用**的方法（2026-10-06，与第六片同晚）

切走一个域之前先量"这张表还有没有人用"。用 AST 把 `*database.DB` 的导出方法全数出来，
再在 `internal/` + `cmd/` 的**非测试**代码里找调用点（含别处的接口成员行），实测：
**导出方法 296 个里 16 个在生产代码里一个调用方都没有**，另有 5 个只被数据层测试自己调用。
16 个直接删（连同 `internal/mcp` 里那条谁都不调用的 `MonitorStorage.SaveToolStats` 接口成员一起删）：
`*DB` 方法 **353 → 337**。5 个只被测试调用的先列进**有名字、有理由、只准缩短**的清单
（都是 RBAC 化之前的旧查询，`...ForAccess`/`...Page` 兄弟才是活路径；删它们要先把测试搬到兄弟上）。

- 新门禁 `TestDatabaseSurfaceHasNoUnreachableMethods`：再出现"生产代码没人调"的导出方法即红；
  清单里的名字若已不再死（方法被删了或被调用了）也红——**清单不能变成化石**。
  四个分支都双向探针验红（新增死方法、从清单删一行、把方法删掉、恢复）。
- 判据的误差方向写进注释：同名方法（`GetVulnerabilityStats` 既是 handler 又是 `*DB` 的）
  只会让它**少报**、不会误报，所以门禁可信；编译器管另一半——
  外部接口真需要的成员删掉就编译不过（`SaveToolStats` 就是这么暴露出"接口成员本身是死的"）。
- 反面教训（写进开发树规矩）：**探针只在测试树里做，恢复一律回开发树 `sync`**。
  我在测试树用 `git checkout --` 恢复探针文件，测试树的 HEAD 落后于开发树未提交的改动，
  于是恢复成了"旧版本"，`scripts/testtree.sh verify` 立刻抓到 `plantask.go` 两树不一致。

### P6 数据层第七片 —— `chat_upload_artifacts`（连它的建表语句一起从 RBAC 里搬出来）

上传附件的授权表有四条 SQL 挂在 `*DB` 上，被一个 handler 用；而**它的 `CREATE TABLE` 与两条索引写在
`initRBACTables` 里**——也就是说"访问控制初始化"决定了上传附件这张表存不存在。这一片把 SQL 与 DDL
一起搬进 `internal/store/chat_upload.go`（`Record/OwnerOf/Forget/Rename` + `EnsureSchema`，
外键指向 conversations，所以建表必须在基础表之后），`ChatUploadsStore` 随之少掉那四个成员
（其余成员是会话标题/项目名/基础目录/可见性谓词，属于别的表，留在窄接口里按表归属继续切）。
方法数 **337 → 333**；归属门禁加 `chat_upload_artifacts`；启动调用进 AST 门禁；
store 侧 6 条真库用例（前缀级联删除、子树改名带同一套 `LIKE` 转义、身份不全不写行、无连接被拒、
建表幂等）。原来测这四条 SQL 的 `TestRBACUploadOwnership` 搬进 store 测试，RBAC 那个文件里删掉——
**不是把断言删了**，前缀级联与子树改名这两条规则在新地方各有一条用例盯着。

**顺手抓到一个自己工具链上的真缺陷**（值得单独记）：`scripts/testtree.sh` 的清单来自
`git ls-files`，于是**"工作区已删但尚未 stage"的文件仍然在清单里**——sync 照旧打印"sync 完成"，
而那份过期副本一直留在测试树里（这次是 `internal/database/chat_upload.go`），
`verify` 只在比内容时才报不一致。修法是清单里再要求路径**存在或本身是符号链接**，
删除项才进入删除列表；修完 sync 立刻把那个副本删掉并复验一致。
教训：**门禁的"来源"也必须被验**——`ls-files` 说的是索引里有什么，不是工作区里有什么。

**同类扫描的收口（2026-10-06，与第七片同晚）**：`SaveToolStats` 那一次暴露的不是一个方法，
而是一类——**声明出来却没人调用**的接口成员。把 `internal/database/stores.go` + `surfaces.go`
里全部窄接口的成员用 AST 数出来（实测 **286 个成员**），逐个在生产代码里找调用点：
**0 个没人调用**（唯一那一例已随 `SaveToolStats` 一起删）。为了让它不再长回来，
新门禁 `TestConsumerSurfacesDeclareOnlyCalledMethods` 进 `make layering-check`：
接口里出现"生产代码没人调"的成员即红（探针：往 `AuditStore` 塞一个
`NothingEverCallsThisMethod` 立刻红，同时 `var _ AuditStore = (*DB)(nil)` 也在编译期拦住）；
成员数下限只防"解析器什么都没读到"，**不惩罚删成员**——删成员是进展。
判据的误差方向与 `TestDatabaseSurfaceHasNoUnreachableMethods` 同：别处同名方法只会让它**少报**。

### P6 数据层第八片 —— `audit_logs`（第八个交回主人的表，附带一个启动顺序缺陷）

`AuditStore` 这个接口本身是错的：它把 `audit_logs` 的三条查询和**另外七张表的八条存在性查询**
混在同一个 surface 里（因为审计页面要把自己的 storage 交给 `audit.ApplyResourceAvailability`）。
这一刀把它拆开：八条存在性查询单独成为 `database.ResourceExistence`（消费者仍是 `internal/audit`
自己声明的 `ResourceExistenceSource`），`audit_logs` 的五条 SQL + **建表与四条索引**
进 `internal/store/audit_logs.go`；`database.AuditLog` / `ListAuditLogsFilter` 两个类型
一起变成 `store.AuditLog` / `store.AuditListFilter`（5 个文件的引用同批改掉）。
`*DB` 方法 **333 → 328**，`AuditStore` 这个接口整体消失。

**启动顺序缺陷（真机点验抓出来的，不是测试）**：`audit.NewService(...)` 在构造之后立刻
`PurgeExpired()`，而我最初把 `ensureAuditLogsSchema` 放在后面（原先表是 `NewDB` 开库时建的，
顺序无所谓）。空库启动就出现一条
`warn audit/service.go:138 清理过期审计日志失败 error="no such table: audit_logs"`。
修法是把三个 store 的建表统一提到 `database.NewDB` 之后、任何消费者构造之前
（`chat_upload_artifacts` 的外键指向 conversations，而 conversations 正是 `NewDB` 里建的，
所以这个位置同时满足两边的约束）。
并加了一条**顺序门禁**：`ensureAuditLogsSchema` 的位置必须在 `audit.NewService` 之前（按 AST 偏移比较）。
探针教训值得记：我两次把探针写成"把 block 插在 anchor **前面**"（`replace(anchor, block+anchor)`），
于是门禁"绿"了两轮——**探针必须先证明它真的改了现场**（这次是打印两份代码的行号），
再谈它有没有让门禁变红；改对成 `replace(anchor, anchor+block)` 后立刻红，恢复后绿。

空库真机复验（端口 18104、独立 db、按记录下来的 PID 收尾）：`no such table` 与建表失败行数 **0**；
`/api/audit/logs`、`/api/audit/summary`、`/api/skills/stats`、`/api/chat-uploads` 全部 200；
`sqlite_master` 里 `audit_logs` + 四条 `idx_audit_logs_*` 都在。

### 上帝对象的"最后一层可达"被量出来了（2026-10-06，第八片之后）

`internal/database.DB` **仍然是 `struct { *sql.DB; ... }`**（`database.go:48-49`）——
嵌入意味着"任何持有 `*database.DB` 的包都能对任意表写 SQL"。前七片靠窄接口 + `database.Narrow`
把 handler 层的可达收掉了（`h.db.Exec/Query/...` 在 `internal/handler` 已归零），
但嵌入本身还在。用**接收者形状**重新量了一遍（正则 `\b(db|d|conn|sqlDB)\.(Exec|Query|QueryRow|Begin|Prepare|MustExec)\(`，
遍历域 = `internal/` + `cmd/` 全部非测试文件，**排除** `internal/database/` 与 `internal/store/`
——这两层写 SQL 是设计在起作用而不是泄漏）：

**30 条，集中在 3 个文件，全在 `internal/knowledge`**：`manager.go` 24、`schema_migrate.go` 3、`indexer.go` 3。
（`internal/security/process_scope.go:84` 的 `scope.guard.Prepare(cmd)` 是同类正则的误报，
用接收者名单把它排除，而不是放宽判据。）

新门禁 `TestRawSQLOutsideDataAndStoreLayersOnlyShrinks` 把这三行数字钉成**每文件上限、只准降**，
并且**没在清单里的新文件一旦出现这种调用就红**（探针：在 `handler/audit.go` 加一条
`db.QueryRow(...)` → 报 "a file not reviewed for this list"）。已进 `make layering-check`。

**下一片因此是明确的**：把 `internal/knowledge` 那 30 条抽进 `internal/store/knowledge.go`
（knowledge 自己的表：`knowledge_items` / `knowledge_embeddings` / schema 迁移），
数字降到 0 之后，才能真正谈"把 `*sql.DB` 从 `DB` 里拆出来不再嵌入"——
那需要先把**没有别的句柄可拿**的包全部转成窄接口或 store，否则拆嵌入就是编译灾难。
门禁改动的纪律：**改完 Makefile 必须跑那个 target 本身**（这行引号我拼错过两次，
`go test` 直接跑是发现不了的，只有 `make test-gates` 会撞）。

### P6 数据层第九片 —— `knowledge_retrieval_logs`，顺带挖出"一张表两套 schema、两个数据库"

抽这张表的 SQL 时发现它**被建了两次，而且语句不一样**：
- `NewDB`（会话主库 `data/conversations.db`）里的 `knowledge_retrieval_logs` **带两个外键**
  （`conversation_id → conversations`、`message_id → messages`，都是 `ON DELETE SET NULL`）+ 3 条索引；
- `NewKnowledgeDB`（独立知识库文件）里的同名表**故意不带外键**，注释写着
  "因为 conversations 和 messages 表可能不在这个数据库中"。

于是这两套拼法连同索引一起进 `internal/store/knowledge_retrieval.go`
（`EnsureSchema()` 与 `EnsureStandaloneSchema()`，各自带索引），
**差别留在同一个地方并被说清楚**，而不是散在数据层两处；
两条测试分别断言"主库那版必须有两个外键"与"独立库那版必须为 0"，
这样谁把其中一版"顺手统一"掉就会红。
写入方也从两处变成一个：`Manager.LogRetrieval/GetRetrievalLogs/DeleteRetrievalLog`
和**会话删除路径**里那句手动的 `DELETE FROM knowledge_retrieval_logs WHERE conversation_id = ?`
（原本是第二个写入者）现在都经这个 store。
读取侧的解释逻辑（7 种历史时间格式、items JSON、解析失败退回 now 并 warn）**留在 knowledge**，
store 只按原样把 `created_at` 文本和 JSON 传回去——绑定 `time.Now()` 的写法一字未改，
换成 `sqltime.UTC` 就会改变已存行的文本形态。

数字：`internal/knowledge` 的裸 SQL **24 → 19**（`manager.go`），
`raw_sql_ratchet_test.go` 的上限随之下调；`knowledge_retrieval_logs` 进归属门禁表清单。
**两道门禁各自探针验红**：把某文件上限调低一格 → 报 "19 raw statements, ceiling 18"；
在 `handler/knowledge.go` 塞一条 `DELETE FROM knowledge_retrieval_logs` → 报"泄漏"。
（顺带纠一处我自己犯的错：第一次跑归属探针是**绿的**，因为表名根本没进清单——
我上一段脚本在更早的一处断言上抛异常就退出了，后面的清单编辑没执行。
**"改了门禁"必须包含"门禁里那一项真的存在"**：这次是把 `grep -n owned :=` 的输出与红/绿一起看。）

启动侧：主库的建表在 `ensureKnowledgeRetrievalSchema(db)`（紧跟 conversations/messages 之后，
因为那两个外键），并被 AST 门禁钉住（探针：删掉那三行 → `never called at boot` 即红）；
独立库的建表在 `NewKnowledgeDB → initKnowledgeTables` 里经 `EnsureStandaloneSchema()` 完成。

**空库真机复验（一个干净实例，全部四张 store 拥有的表）**：
`knowledge_retrieval_logs=1 / 外键=2 / 索引=3`，`audit_logs=1`，`skill_stats=1`，
`chat_upload_artifacts=1`，日志里 `no such table` 与建表失败 **0 行**。
这一轮真机点验还抓出两件事，都不是代码问题而是"验错了东西"：
① `ensureKnowledgeRetrievalSchema` 最初**根本没进 app.go**（我那段脚本在更早一处断言就抛异常退出，
后面的编辑没执行），而第一次查库看到"表不存在"时先怀疑的是代码不是二进制——
`make test-gates` 之后我又改了 app.go 却没重编二进制，测的是**旧产物**；重编后一切正常。
**结论：真机点验之前必须重编，且"表没建出来"要按顺序排除 二进制新旧 → 门禁是否真在跑 → 代码**。
② 上一段"表存在但外键=0"其实查的是**空文件**（sqlite3 打不开目标文件时会新建一个空的），
配置里的库路径替换没命中（那行早已是 `data/livecheck.db`）。
所以现在固定用一个 python 脚本按 `*.db` 全列举并打印每个计数，而不是 `sqlite3 单文件` 加一堆引号。

**下一片照旧**：`knowledge_embeddings`（含 `schema_migrate.go` 的三条列迁移）与
`knowledge_base_items`（`manager.go` 剩下的 19 条里的大部分）——它们**同时存在于两个数据库文件**，
所以 store 侧要先决定"每表 × 每库"的形状，别再制造第二套拼法。

### P6 数据层第十片 —— `knowledge_embeddings` 的**同一件事两处实现**合成一处

抽这张表之前先按类扫了一遍，结果不是"少一处 SQL"而是**一条规则两份实现**：
- `internal/database.(*DB).migrateKnowledgeEmbeddingsColumns()`：给老库补 `sub_indexes` /
  `embedding_model` / `embedding_dim`，`pragma_table_info` 守卫；
- `internal/knowledge/schema_migrate.go: EnsureKnowledgeEmbeddingsSchema()`：**同样的三句 ALTER、
  同样的守卫**，被 `indexer.go` 两处调用；旁边还挂着一个零调用方的
  `ensureKnowledgeEmbeddingsSubIndexesColumn`（删）。
谁先跑都不影响结果（都有守卫），但两份拼写意味着以后只改一处。
现在 `internal/store/knowledge_embeddings.go` 是唯一入口：`EnsureSchema()`（建表 + 索引 + 补列）
与 `EnsureColumns()`（只补列，表不存在时**按原样静默返回**，因为 indexer 构造早于建表），
两条调用路径都换成它；`schema_migrate.go` 与 `migrateKnowledgeEmbeddingsColumns` 删除。

真机复验（开 `knowledge.enabled`、独立 `data/knowledge.db`）：
`knowledge_embeddings` 有 **9 列**（含补出来的三列）与 `idx_knowledge_embeddings_item_id`，
`knowledge_base_items` / `knowledge_retrieval_logs` 同在，主库 `livecheck.db` 只有那张带外键的
`knowledge_retrieval_logs`（正确：向量表不属于主库），日志里 `no such table`/建表失败/结构迁移
**0 行**，索引补齐流程正常跑完。
测试 4 条：建表幂等 + 索引存在、老表（6 列）补成新表形状且第二次是 no-op、表缺失时静默、无连接被拒。
`internal/knowledge` 裸 SQL 计数：**manager.go 19 / indexer.go 3**（`schema_migrate.go` 那 3 条随文件消失，
已从上限清单删除）；下一片就是把这 22 条按表搬进 store（向量读、相似度 JOIN、条目 CRUD）。

**另一条纪律**（编译器帮我抓到的）：测试包里的 helper 名字必须全局唯一——
我新写的 `columnExists` 与 `hitl_lifecycle_test.go` 里已有的同名函数冲突，
`go vet` 立刻报 `redeclared in this block`；改成 `embeddingColumnExists` 才对。

### P6 数据层第十一片 —— 分层裸 SQL **归零**，以及"债还挂着时门禁自证有效"这件事怎么收场

这片把 `internal/knowledge` 剩下的 22 条语句按表搬完：**`knowledge_base_items` 17 个方法进
`store.KnowledgeItems`**（第十一片的主体，条目行 CRUD + 分页 + 四列搜索 + 重建顺序），
**向量侧 5 条进 `store.KnowledgeEmbeddings`**（批量写入、按条目删除、两个计数、检索用的那张
`JOIN knowledge_base_items` 的相似度候选读）。两张表被一个 JOIN 绑在一起，所以一片里同时交回两个主人，
否则归属门禁会在"只搬一半"时红。

`Retriever`/`Indexer`/`Manager`/`SQLiteIndexer` 不再持有 `*sql.DB` 字段（构造签名不变，
参数只用来绑 store），`buildKnowledgeIndexChain`/`NewSQLiteIndexer` 改收 store。
eino 索引器原来在事务里逐条校验 meta，现在先算完整批再一次性写：**回滚语义没变**（任一条不合格
整批不落库），错误串从 `insert chunk %d` 合成 `write %d chunks`（全仓无测试断言该串，已核）。
检索侧只把 SQL 与扫描搬走，`json.Unmarshal`、维度/模型一致性判断、余弦、阈值截断留在原处——
那些是检索的领域逻辑，不是行的形状。

**store 真库测试抓出一个存量缺陷**：`knowledge_base_items.content` 是可空列，而 `GetByID` 与
`ExistingAt` 把它扫进 `string`。老库里有 NULL 正文的行时，`convertAssign` 直接报
`converting NULL to string is unsupported`，目录扫描以 `查询知识项失败` **整体失败**（不是"这一行不见了"，
是整次扫描中断）。两处改 `sql.NullString`，NULL 读回 `""`。测试
`TestKnowledgeItemsContentNullStaysReadable` 就是这条的回归钉子——它先红后绿，红的那次给的正是驱动错误串。

**归零之后，门禁怎么继续证明自己？** 这一层的判据原本是"每个文件的条数 ≤ 上限"，而债现在是 0：
空清单既可能是"干净"，也可能是"扫描器坏了"，两者输出一模一样。三件事补上这个空洞：
1. **正向对照**（`rawSQLControls`，8 条样本语句）：每条必须被两个扫描器之一命中，否则测试直接失败并报
   "this gate cannot see the SQL it claims to prevent"。
2. **遍历域下限**（`rawSQLWalkFloor = 500`）：`internal/` + `cmd/` 下非测试 .go 共 552 个，
   减去两个自有层里的 45 个，必须真扫到 507 个；扫不到就说"扫描器没在读目录"。
3. **判据换成与名字无关的一条**：光按接收者名（`db|d|conn|sqlDB`）匹配会瞎。新增
   `sqlTextAlone`——引号或反引号后面直接跟 `SELECT|INSERT INTO|UPDATE|DELETE FROM` 的**字符串字面量**
   也算，无论它在哪个变量上执行。

探针（每条都"注入即红、撤销即绿"）：
- 往 `internal/knowledge` 塞一个生产文件，一条走 `db.Query("SELECT id FROM knowledge_base_items ...")`、
  一条走**门禁原本不认识的名字** `sqlite.ExecContext(ctx, "UPDATE knowledge_embeddings ...")`
  → `TestRawSQLIsOnlyWrittenTheLayersThatOwnIt` 报 `3 raw statement(s), a file not reviewed`，
  `TestOwnedTablesAreOnlyWrittenFromThisPackage` 报 `FROM knowledge_base_items` 泄漏；两条都红，删文件都绿。
- 把一条对照样本换成两边都不命中的 `mgr.sqlite.DoThing(1)` → 门禁立刻自杀式变红（对照组活着）。
- 把遍历下限临时调到 999999 → 报 `the walk considered 507 production files`（覆盖断言活着）。

**水位复测**：分层裸 SQL **0 条 / 0 个文件**（扫描过 507 个生产文件）；同一条 SQL 文本判据在
`internal/database` 数到 **429** 处、`internal/store` **87** 处（第十二片复测的两个数字；这两层按定义
就是 SQL 的主人，所以"归零"指的是**它们之外**，不代表数据层已无 SQL）。本片交完时 `*database.DB` 是
**327**（上限从 328 收紧，见下），下一片之后是 320；`internal/store` 生产文件 **14 个**、
包内测试 **99 条**（本次新增 11 + 补强 7）。

**顺带抓到 ratchet 自己的一个口径漏洞**：收紧上限时我按上一次记录写 328，AST 计数器说 327。
查两棵树的方法名集合差，发现少的是 `migrateKnowledgeEmbeddingsColumns`——它在**上一个已提交**的切片里
被删，而"只降不升"的门禁对下降**只打 log 不失败**，所以一处已经落后的水位被带了进来。
结论写进门禁注释：**下降只出现在日志里，所以每次写数字必须当场复测，不能抄上一条 commit 的**。

### P6 数据层第十二片 —— `model_token_usage`：把授权子句收成一条，顺手抓到两个存量缺陷

这片把用量表整域交回主人：`internal/store/model_token_usage.go` 现在持有 **建表 + 四个索引 +
写（timeline 钩子与幂等 upsert）+ 回填 + 三个统计读 + 明细读**，`internal/database/model_token_usage.go`
整个删除，`*database.DB` **327 → 320**（少 7 个：4 个公开方法、1 个私有钩子、2 个私有查询 helper——
计数器数的是所有 `*DB` 接收者，不只导出的，所以下降比方法清单看起来多）。
写路径从 `db.maybeRecordModelTokenUsage(...)` 改成 `store.NewModelTokenUsage(db.DB).RecordFromProcessDetail(...)`
加同一行 Warn 日志；建表与回填从 `initTables` 里挪进进程启动的 `ensureModelTokenUsageSchema`，
配 AST 顺序门禁（`database.NewDB` 之后再执行，回填要读 `process_details`）。
handler 侧：`ConversationHandler` 加 `usage *store.ModelTokenUsage` 字段，构造函数签名不变，
`database.ConversationStore` 少一个成员（窄接口 283 → 282 个成员，全部仍有生产调用点）。

**授权子句收成一条**：这一域原来用数据层那份 `appendConversationAccessFilter`（四条 OR 路径），
store 层另有一份 `ConstrainConversation`（四条 EXISTS 路径）。抽它的时候没有再抄第三份，
而是让 store 用它自己那条，并补一个**差分测试**：`legacyAccessClause` 把被替换掉的旧拼写原文钉在
测试文件里，同一份种子数据、五种身份 × 两种 scope，两侧返回的 `process_detail_id` 集合逐个相等。
唯一**故意不同**的一处被单独断言：旧拼写在 `userID == ""` 时整条子句不加（无身份的请求读到全库），
store 那条给 `1=0`。路由本身有鉴权中间件，所以这条差异在线上不可达；它是把 fail-open 的默认
挪成 fail-closed，而不是新增限制。
差分测试当场抓到**我自己写错的地方**：子句最初传的是 JOIN 出来的 `c.id`，而它内部相关子查询也把这个表
起名为 `c`，`WHERE c.id = c.id` 恒真 → 任何人看到全部行；改成本表的 `mtu.conversation_id`（JOIN 留着，
它才是"会话删了就不再统计"的那道筛子）。

**第二个存量缺陷（真机语义，不是风格）**：行上的文本列原来这样取
`strings.TrimSpace(fmt.Sprint(m["model"]))`——payload 里没有这个键时 `fmt.Sprint(nil)` 得到
**四个字母 `<nil>` 并写进库**，于是 `COALESCE(NULLIF(TRIM(model),''),'unknown')` 的 unknown 兜底
永远不会命中，用量页出现一个叫 `<nil>` 的分组。改成 `textField`：缺键或 JSON null 都写空串。
**注意**：这只管新写入；已有库里那些 `<nil>` 文本仍在（不打算在读侧为它开特例，也不让 store 悄悄改写历史）。

测试：store 侧 **10 条**（建表幂等 + 四索引、钩子只吃 usage 事件、按 `process_detail_id` 幂等重写、
total 兜底、project 列的可空读、会话删除后其用量隐身、回填一次性且保留原时刻、分组与 today 窗口、
排序/limit/会话/项目/`__none__` 过滤、四条授权路径 + 差分 + 无连接被拒）；
HTTP 契约 **4 条**（顶层与 recent 的**键集合**逐字钉住、`projectId` 的 omitempty 语义、
无会话 = 全零、scope all 无需 user、按 `:id` 归一、`days/limit` 兜底、项目过滤与"写入时刻拷贝"语义、
无库 handler 返回 500 且带 store 自己的错误串）。写数据全部走 `db.AddMessage → db.AddProcessDetail`
真实链路，所以钩子本身也在被测范围内。

探针（全部「注入即红、撤销即绿」，其中两条第一次注入是无效的，记下形状）：
- 删掉启动那次 ensure 调用 → 接线门禁红。
- 把子句列改回 `c.id` → 授权路径与差分测试同时红（这就是抓到我写错的那条）。
- `textField` 换回 `fmt.Sprint(m[key])` → `<nil>` 分组钉子红。
  （第一次探针只替换了 return 那一行，`v == nil` 的守卫还在，于是"注入即绿"= 探针无效；换掉整个函数体才红。）
- 改一个 json tag（`reasoningTokens` → `reasoningToken`）→ 契约测试键集合红。
- 在**另一个文件**里插一处偏移更小的 `database.NewDB(` → 顺序门禁红：偏移按文件计，
  跨文件比较会认可任何摆放位置，所以断言现在要求两个调用同一文件。
- 往 `internal/handler` 塞一条 `SELECT COUNT(*) FROM model_token_usage` → 归属门禁红并报出表名。

**一处口径收窄值得写明**：`model_token_usage` 的建表现在只由服务端启动路径执行；
`cmd/server` 里那条改密码 CLI 仍会 `database.NewDB`，但不再顺手建这张它永远不写的表。

### P6 数据层第十三片 —— 「一个 DATETIME 列怎么读回时间」原来有 24 个读者、24 套写法

这不是搬表，是**收一条被抄了 24 遍的规则**。`sqltime` 上一片只管"怎么写/怎么比"，
这次把它不管的第三半——**怎么读回来**——收进同一个家：`sqltime.Parse` / `ParseOK`，认 12 种历史写法
（RFC3339 两式、空格分隔与 `T` 分隔 × 有/无偏移 × 有无小数秒、以及 `Z07:00` 变体）。

改之前的真实分布（按"函数级"数，不是按文件）：`internal/database/conversation.go` **14 处**
（会话/消息/过程详情各自拼一条 `if t,e := time.Parse(...)` 链）、`batch_task.go` 3 处、
`asset.go` `parseAssetScanTime`、`monitor.go` `LoadToolStatsSummary`、`robot_session.go`、
`project.go` `parseDBTime`（唯一那份 10 写法"超集"）、`knowledge/manager.go` 2 处（一处内联 7 写法表 +
`itemTimeLayouts`）、`store/model_token_usage.go` 1 处。**同一个列值能否读出一个真实时间，取决于哪段代码读到它**
——这就是"一行时间显示成 0001 年 / 空白 / 少一天"这类只有真机才复现的杂症的根。

改完：**`time.Parse` 一个存下来的 DATETIME 的地方只剩 `internal/sqltime`**（新门禁实测 0 个文件，扫描覆盖 551 个生产文件——这条判据的遍历域是除 `internal/sqltime` 外的全部生产码，比裸 SQL 那条更宽）。
顺带清掉 6 个只为那些链存在的 `var err error` / `var parseErr error` 声明与 3 条注释。

**判据为什么按"调用形状"而不是按表名**：`readingStoredInstant` 只认 `time.Parse("2006-01-02[ T]15:04:05…" …)`，
并**故意放过**两类同形但不同决定的写法——`x.Format("2006-01-02 15:04:05")` 是展示层的输出格式（现存 12 处，
CSV/Markdown/工具回执，归显示代码管），`time.ParseInLocation(…, time.Local)` 是监控图表按本机时区打桶。
门禁对这两类各有一条**反向对照**：把它们也抓就算红（探针验过：往 allowed 清单里塞一条真解析调用，
立刻报 `fired on an allowed spelling`）。

**三条探针**（注入即红 / 撤销即绿）：
- 在 `internal/database/project.go` 塞一个 `time.Parse("2006-01-02 15:04:05", x)` → 门禁红并报文件名；
- 往正向对照清单里加一条两边都不命中的 `whatever.DoThing(x)` → 门禁自杀式红（`cannot see the parse it claims to prevent`）；
- 往 allowed 清单里放一条真解析 → 放过检查红。
另有两条**一开始无效的探针**，值得写下来防再犯：① 只删 `layouts` 里任意一条，15 个样本仍全过——
Go 的 `Parse` 里布局的空格也匹配 `T`、`.999999999` 也匹配短小数，于是**这 12 条互相冗余**（12 种单删都试过，全绿）。
结论是"保留这份宽松清单"而不是"求极小集"：读老库的容错就是要宽，多试一次的成本是一次失败比较，
这句话现在写在 `sqltime.layouts` 的注释里，不是留成读者的猜测。② 我第一版最小化脚本把红/绿判反了，
打印出的 `load-bearing` 全是反义——发现得靠"最终清单没变但每条都'不可删'"这种自相矛盾。

测试：`TestParseAcceptsEveryFormTheReplacedReadersUsed`（15 个样本 = 各被替换读者原来认的写法集合的代表值，
逐一断言解析出的**同一瞬间**，含带偏移、无偏移、3/6/9 位小数、空格与 `T`）与
`TestParseOKSeparatesUnparseableFromZero`（空串/垃圾 → false 且零值；库里真存了 `0001-01-01 00:00:00` → true 且零值，
这条是 asset/monitor 那两个"要区分没读到 vs 读到零"的调用点需要的形状）。

### P6 数据层第十四片 —— `robot_user_sessions`：一条"重启后还认得这个聊天线程"的映射

3 个方法 + 一条私有补列迁移（`migrateRobotUserSessionsTable`）进 `store.RobotSessions`，
`internal/database/robot_session.go` 删除，`*database.DB` **320 → 316**。
建表、索引与 `agent_mode` 补列从 `initTables` 挪进启动的 `ensureRobotSessionSchema`（配 AST 接线门禁）。
`RobotHandler` 加 `threadBindings *store.RobotSessions` 字段（构造函数签名不变），
三处调用点改指 store；`RobotStore` 少 3 个成员。字段第一次叫 `sessions` 时与结构体里已有的
`sessions map[string]string`（内存里的线程→会话缓存）**撞名**，编译立刻红——
这两个名字讲的是同一件事的两份状态，改名 `threadBindings` 之后边界反而更清楚。

`store.RobotSessions` 刻意**不解释** session key 的形状（那是 handler 按平台拼的），也不做鉴权；
读回来的行把两个默认值补好（`默认` / `eino_single`），使读侧不必知道哪一列是空的——
这套默认原来在读写两侧各有一份，现在写侧补完、读侧只为老行兜底。

测试 8 条真库：建表幂等 + 索引存在、**老表（无 `agent_mode`）被补出新形状**、
未知 key 与空 key 都返回 `nil, nil`（"没写过"和"没身份"都不是错误）、
写入读出逐字段一致、空 role/mode 落默认值、同 key 重写**不新增行**且 `updated_at` 前进、
"没会话可记"的四种入参静默不写、删会话**级联删掉指针**（外键开着，这正是留孤儿的那类 bug）、
无连接四处被拒。

门禁复测：`*database.DB` 上限 320→316 并写下轨迹；归属清单加 `robot_user_sessions`；
死面扫描的**地板**从 280 教到 270（实测 277）——注意这条断言的**文字**里还写着旧数字，
改判据时把消息一起改，否则将来报出的数字没人信。探针两条：删掉启动那次 ensure → 接线门禁红
（消息直接说"机器人会话重启后不认线程"）；往 handler 塞一条 `SELECT session_key FROM robot_user_sessions`
→ 归属门禁红并报表名。两条都撤销即绿。

### P6 数据层第十五片 —— 机器人身份绑定：把"哪个账号"和"这个账号能干什么"彻底分开

`robot_user_bindings` + `robot_binding_codes` 两张表进 `store.RobotIdentity`，
`internal/database/robot_identity.go` 删除，`*database.DB` **316 → 310**
（发码 / 消费 / 解析 / 列表 / 两种删除，加上它们共用的私有 `normalizeRobotIdentity`）。
建表与两条索引原来在 `rbac.go: initRBACTables` 的语句清单里，一并搬进 store 的 `EnsureSchema`，
由启动的 `ensureRobotIdentitySchema` 执行（配 AST 接线门禁；`robot_user_bindings` 的外键指向刚建好的 `rbac_users`）。

**这一刀真正修的是耦合方向**：原来 `ConsumeRobotBindingCode` 直接返回 `*database.RBACUser`、
`ResolveRobotRBACAccess` 直接返回 `*database.RBACAccess`——身份表的主人顺手把权限域的
类型与解析函数（`GetRBACUserByID` / `ResolveRBACAccess`）也拿在手里。现在 store 只回答
**"这个码属于哪个账号"/"这个外部身份绑到哪个账号"**，`handler/robot.go` 再拿这个 user id 去问 RBAC
（鉴权处与绑定处各一次显式组合，错误文案与原来逐字一致）。JOIN 里那句 `u.enabled = 1` **留在 SQL**
（禁用账号的发码不可消费、已有绑定不可解析，这是行的谓词，不是权限计算）。

**归属清单为什么先不放 `robot_user_bindings`**：`internal/database/vulnerability_alert.go` 的
告警收件人查询 JOIN 了它（"这个漏洞要发到哪些平台账号"），那条查询按表归属属于**漏洞告警域**，
要连同 `vulnerability_alert_subscriptions` 一起搬。先只把 `robot_binding_codes` 列入清单，
并在 `ownership_test.go` 上方写清"另一张表等告警那一片一起认领，避免先半个声明就报泄漏"——
这是"永远不为没做完的活写 0"那条纪律的用法。

测试：**store 侧 10 条**真库（幂等建表与两索引、残缺身份/过期码/无主码一律拒、
消费即单次使用且第二次既不成功也不回账号、**8 个 goroutine 抢一个码只有一个赢**、
发码在同一事务里清掉本账号旧码且不动别人的码、禁用账号不可消费不可解析但**绑定行不删**、
重绑替换指向、删账号级联带走绑定与未用码、历史时间写法读回（`2026-01-02T03:04:05Z` 与
`…+08:00` 两种都过上一片的 `sqltime.Parse`）、无连接七处被拒）；
**原有两条端到端鉴权测试改写为走组合路径**（`internal/database/robot_identity_test.go` 与
`internal/handler/robot_rbac_test.go`、`vulnerability_alert_test.go` 三处），
它们仍用真 RBAC bootstrap，钉住"权限来自实时角色而非绑定那一刻的快照"。

**一处诚实的负结果**：我把 `RowsAffected != 1` 那道"已被使用"的守卫删掉后，
8 路并发测试**仍然绿**——在这条连接上输者是被前面那条 SELECT（要求 `used_at IS NULL`）拒掉的，
守卫在这套测试里不可达。它覆盖的是"两边都先 SELECT 成功"的交错，
所以**留着**（成本是一行判断，省不掉的正确性代价是一次重复绑定），
但注释与测试说明都改成"这里证明不了它"，不把它写成被测试保护的东西。

探针三条：删启动那次 ensure → 接线门禁红；把 `u.enabled = 1` 摘掉 → 禁用账号那条测试红；
（第三条：删单次使用的 UPDATE 守卫 → 如上所述**没有变红**，已作为负结果写进上文而不是隐藏。）

### P6 第十六刀 —— 数据库连接包装不再当应用回调的注册表

`*database.DB` 上原来挂着一个字段 `vulnerabilityCreatedHook`，由装配 `db.SetVulnerabilityCreatedHook(robotHandler.NotifyNewVulnerability)`
设置，两个写入点（HTTP 创建漏洞、MCP 记录漏洞工具）通过 `db.NotifyVulnerabilityCreated(created)` 触发。
**连接包装因此成了应用关注点的回调注册表**：任何拿到 `*DB` 的包都能读写字段，忘了设置时
"漏洞记下了但永远不提醒"在协议上毫无痕迹。现在改成装配层一个具名类型
`app.vulnerabilityAlertRoute`（`internal/app/vulnerability_alert_route.go`），
`VulnerabilityHandler` 通过构造函数**必须**收到 `VulnerabilityNotifier`，工具注册也显式收一条路由；
`*database.DB` **310 → 308**（两个方法 + 那个字段）。

保留这个间接层是有理由的，而且理由写在类型注释里而不是留在记忆里：
漏洞 MCP 工具在 `app.New()` 里比 robotHandler **先注册**，所以注册那一刻拿不到具体的监听者。
变化只是这层间接从"连接对象的字段"挪到"装配层一个有名字、被测过的东西"。
`attach(nil)` 会打一条 warn（提醒未接线），旧写法在同样情形下是彻底沉默。

行为逐条对齐旧实现：**通知前 `created := *vulnerability` 拷贝再起 goroutine**（写路径随后继续改自己那条记录，
不能改变在途提醒读到的东西）；`nil` 记录直接返回；无监听者时静默跳过而不是 panic；
处理端仍只在自己的调用点等一次握手。

测试：**装配层 3 条**（拷贝语义——通知后立即改原记录，断言监听者拿到的仍是记录当时的值；
无监听者 / nil 记录 / attach(nil) 都不炸且不改写入路径；16 写 16 改挂接并发下每条通知都到达一个监听者，
`-race` 过），**handler 层 2 条**（注入的 notifier 收到**刚存库那一条**的 id/标题/严重级，
并回查 `vulnerabilities` 表确认不是另一条；notifier 为 nil 时端点仍 2xx 且库里真有 1 行——
"有没有提醒"是装配决定，不是这条路径的前提）。

门禁：接线测试新增一条 **AST 断言**——`internal/app` 里每个 `handler.NewVulnerabilityHandler(...)` 必须 3 参，
且第 3 参不能是字面量 `nil`；探针注入 `nil` 立刻红（消息直说"记录得下、永不播报、协议上看不出原因"）。
另两条探针：删掉 handler 那次 notify 调用 → 注入测试红；把拷贝换成共享指针 → 拷贝语义那条红。
死面扫描的地板这次从 270 降到 **260**（实测 269）并改写它的注释：
地板的职责是"扫描器还在读整个目录"，不是把每个切片都会下降的数字冻在原地——
上一片把它教得太贴实测值，这一片两方法一删就撞线了。

### P6 第十七刀 —— `c2_payload_artifacts`：payload 下载门改成"两个问题"

最后一个自带表的小文件（2 个方法）交回 `store.C2PayloadArtifacts`，`internal/database/c2_payload.go` 整文件删除，
`*database.DB` **306**。原来的 `UserCanAccessC2Payload(userID, scope, filename)` 把两件事写在一条方法里：
查这张表的归属，以及调 RBAC 层问"这个用户能不能碰那个 listener"。现在拆开——store 只答
`Lookup(filename) → {owner, listener, found}`，下载门 `userMayFetchPayloadArtifact` 依次问
"是不是你建的"与"你能不能访问那个 listener"；建表与索引照例从 `rbac.go` 的启动清单搬进
`EnsureSchema` + 启动的 `ensureC2PayloadArtifactSchema`（AST 接线门禁盯住）。归属清单加上这张表。

测试：store 侧 6 条（幂等建表与索引、写入读出逐字段、按文件名重建只留一条且换掉归属、
**残缺归属静默不写**——build 路径忽略返回的 error，写一条没人能认领的记录比不写更糟、
未记录文件 `found=false` 且不是错误、trim 后再查、无连接被拒）；
下载门 1 条 7 例走真库真 RBAC：owner 命中、陌生人拒、**被分到那个 listener 的人**命中、
分配不跨 listener、scope=all 命中、未记录文件对受限调用者拒、未记录文件对 scope=all **仍然放行**
（这是被原样保下来的旧行为： unrestricted 那条路径从来没查过记录，文件在不在磁盘上才是下一道）。

**两条负结果，都不假装被测到**：
① `!found` 那道守卫**测试区分不出来**——记录缺失时 owner 与 listener 都是空串，后面两条检查照样拒。
删掉守卫，7 例仍全绿。守卫作为"规则的字面陈述"保留，注释里写明没有测试证明它必要。
② 我第一版的并发测试**断言写错了**：路由是异步派发的，只 `wg.Wait()` 等调用方并不等投递，
`-race` 下 16 条只收到 15 条。改成等 `delivered` 通道收满 16 次，连跑 6 轮稳定。
教训：异步边界的测试必须等**副作用发生**，不是等"提交副作用的那次调用"返回——
本地不带 `-race` 单跑时它是绿的，只有全套 race 才暴露。

### 全新库启动的表清点（第十七刀之后当场复跑，验收用）

一条命令能复验"每张表确实有一个创建者、而且那个创建者在启动里被调到"：
在一个**空 data 目录**里起服务（独立端口、`knowledge.enabled: true`、独立 `knowledge.db`），
然后用 `sqlite3`/`python3 -c` 比对 store 里所有 `CREATE TABLE IF NOT EXISTS` 的表名与实际存在的表。

当场结果（15 张 store 所有的表）：
- **主库 12 张全部就位**：`audit_logs`、`c2_payload_artifacts`、`capability_unit_switches`、
  `chat_upload_artifacts`、`hitl_conversation_configs`、`hitl_interrupts`、`model_token_usage`、
  `robot_binding_codes`、`robot_user_bindings`、`robot_user_sessions`、`skill_stats`、
  （`notification_reads_by_user` 见下）；
- **知识库 3 张在 `data/knowledge.db`**：`knowledge_base_items`、`knowledge_embeddings`、
  `knowledge_retrieval_logs`；`knowledge_retrieval_logs` 在主库里也有一张**不带外键**的版本
  （一张表两种拼写，两种都由它的主人给出，见 §11「第九片」）；
- **唯一不在启动时创建的是 `notification_reads_by_user`**：它由通知自己的读路径 `EnsureSchema`。
  实测 `GET /api/notifications/summary` 首条请求 **200**、返回完整键集合，调用之后表就存在，
  整段日志 `no such table` **0 条**——惰性创建在这里是成立的，不是被漏掉的主人；
- 启动日志里 `建表失败` / `结构迁移` / `表失败` **0 条**。

这条清点同时是第八、九、十一~十七刀的**共同回归门**：那些切片都把 `CREATE TABLE` 从
`initTables` 搬进 store + 启动的那一次 ensure；少接一次 ensure，这里就会少一张表。
（同一形状的缺陷在第八片真机点验时抓到过一次：audit purge 在 `audit.NewService` 构造里跑，
建表必须在那之前——见 §11「第八片」。）

### P6 第十八刀 —— 授权词汇收成一处：`RBACListAccess` 与 `Access` 是同两个字段的两份声明

漏洞域搬不动的三块铺垫先拆掉两块（都各自是一个绿色提交）：

1. **`store.Vulnerability` 改名 `store.Vulnerabilities`**：单数名腾出来给行结构体。
   行结构体现在还在 `internal/database`，而投递与告警的读要**带着整条记录**走，
   store 不能反向引用数据层的类型。
2. **`database.RBACListAccess` 消失，`store.Access` 成为唯一声明**：两份都是
   `{UserID, Scope}`，作用域三个字 `"all"/"own"/"assigned"` 也各有一份常量，值当场对过一致。
   同一件事两处声明的实际风险是**改一边编译通过、可见性范围就变了**——数据层那五个 handler 字段
   与 store 那批查询用的是同名不同源的两套词汇。改动 103 处引用（含 16 个文件），纯类型改名、
   由编译器逐项验收；窄接口/字段归属三条门禁的数字一片没动（18 窄接口字段、13 store 字段、1143 字段扫描、21 个接口）。

**一块必须写下来的自伤**：这两步都用了一次跨包正则改写，而正则**不区分代码与注释**——
`internal/layering/layering.go` 里那句解释「形状门禁为什么按声明列表而不是按名字后缀」的注释，
被同一个改写从 `database.RBACListAccess` 改成了 `store.Access`，读起来变成"数据层声明了一个叫
store.Access 的接口"。是编译和门禁全绿之后我重读注释才发现的（注释不在任何门禁的射程里）。
现在那两句改写成不点名具体类型的说法，并把这条规矩落到流程上：**跨包正则改写之后必须重读被改到的注释与文档句**，
不能只看 build/vet/test 的颜色。

第三块铺垫（`appendVulnerabilityAccessFilter` 与 `store/access.go` 那条子句的差分归一）
仍未做（当夜第十九刀做的正是这块），所以当时漏洞域整片还没开工——原因清单与后续进度在
§12.3「漏洞域：三件铺垫已拆完，行结构已经搬过去」。

### P6 第十九刀 —— 同一张表的可见性规则的两份拷贝合成一条，顺带把"无身份 = 全见"这个默认翻过来

`internal/database/vulnerability.go` 里的 `appendVulnerabilityAccessFilter` 与
`internal/store/vulnerability.go` 的 `ConstrainFinding`（通知摘要在用）是**同一条规则的两份拷贝**：
六条可达路径（本人 owner / 被分到该漏洞 / 拥有其项目 / 被分到其项目 / 拥有其会话 / 被分到其会话）
**逐字相同**，只有一处不同——数据层那份在 `userID == ""` 时**什么都不加**。
6 个调用点（漏洞 5 条查询 + 资产看板的风险趋势 1 条）全部改指 store 那一条，重复的那份删除。

**差分测试**（`internal/database/access_clause_parity_test.go`）：种 7 条漏洞，一条一条对着可达路径
（外加一条谁都不属于的孤儿），7 个身份 × `own`/`assigned` 两种 scope 共 **14 次比对**，
每次同时跑「钉在测试里的旧拼写」与新的 store 子句，行集合必须一模一样；
`scope=all` 单独一条（两边都不加条件）；**唯一允许的差别**单独断言：
无身份时旧拼写返回全库 7 条、新子句返回 0 条。旧拼写以原文常量形式钉进测试，
不是拿新实现跟自己比。

**这次合并真正翻出来的东西**：`ListVulnerabilities` / `CountVulnerabilities` 这两个"不做过滤"的读，
过去是**靠传一个空 access** 来表达"不加限制"的——它们依赖的正是那个 fail-open 默认。
现在显式写 `Access{Scope: store.ScopeAll}`，意图落在参数上而不是落在缺省行为里。
顺带记下两处**策略问题**（不是今晚该改的行为）：
`handler/openapi.go:89`（按会话导出漏洞的文档接口）与 `internal/app/vulnerability_tools.go:391/399`
（MCP 的漏洞列表工具）调的都是这个"不加限制"的读——也就是说，任何一个通过鉴权的调用者、
任何一次带上下文的 agent 工具调用，看到的都是**全库**漏洞。要不要按调用者收窄是产品/安全决策，
列为 §10 决策项 10。

还有一处只能靠**回归测试**才看得见：合并之后 `internal/database/asset_test.go` 里
`TestAssetScanLinkReturnsTimeAndRelatedVulnerabilities` 当场红了（它走的就是那条无身份读）。
这条红不是测试坏了，是**默认值变了**——处理方式就是上面那个显式 `ScopeAll`，
改完原测试原样通过，没有为了让它绿而放宽任何断言。

**探针（注入即红 / 撤销即绿）**：① 把 store 子句的空身份分支改回"什么都不加" →
`a caller with no identity read 7 findings, want none`；
② 把"被分到其项目"那条 EXISTS 掐掉 → `project-assigned/own: the store clause answered [], want [f-project-assigned]`。
两条都是差分测试自己红的，不是编译红。

至此 §12.3 列的三块铺垫全部拆完（复数改名、访问类型合一、可见性子句归一），
漏洞域整片（11 + 7 个方法、两张告警表、`robot_user_bindings` 的归属认领）已经没有前置阻塞。

### P6 第二十刀 —— 漏洞的行结构与它的列表过滤器进 store，把"还剩多少"变成可比较的数字

这一刀不搬任何查询，只搬**类型**：`Vulnerability`（22 字段的行结构，JSON tag 就是控制台、
导出文档、告警文本三方共同读的线上形状）与 `VulnerabilityListFilter`（列表/统计/导出共用的
过滤器）**连同它的 SQL 构造器**一起从 `internal/database/vulnerability.go` 搬进
`internal/store/vulnerability.go`。构造器从非导出的 `appendWhere` 改名为导出的
`ConstrainWhere`，转义器 `escapeVulnerabilityLikePattern` 跟着改名为 `escapeLikePattern`——
第十九刀记录的就是"构造器与类型不可分"，搬法的区别只在于这次是**带着 4 个调用点一起改**，
而不是留着它们隔着包引用一个不可见的方法。

全仓 20 个文件的类型引用跟着改：`handler/vulnerability.go`、`handler/openapi.go`、
`app/vulnerability_tools.go`、`app/vulnerability_alert_route.go`、`audit/resource_availability.go`、
`database/stores.go` 的 3 个窄接口（`ResourceExistence` / `OpenAPIStore` / `VulnerabilityStore`，
另有 `RobotStore` 的告警收件人签名）与 6 处测试夹具。字段与 tag 一个没动，编译器逐项验收。

**搬完才第一次量得出剩余**：`internal/database/vulnerability.go` **532 → 396** 行、
方法 **11** 个；`vulnerability_alert.go` **206** 行、**7** 个方法。`*database.DB` 的方法总数
这一刀不变（仍是 **306**），因为搬的是类型不是方法——这正是这一刀的全部目的：
让下一刀（18 个方法连三张表的 DDL）变成纯机械搬迁，不再夹带跨包类型决策。

**三处自伤，都就地修好**：① 脚本按行区间切块时把紧跟在过滤器下面的 `CreateVulnerability`
一起拖进了 store（搬回老家）；② 删除老家旧定义的那条正则**先于**在新家写好执行，把过滤器的
SQL 构造器删掉了——从 `git show HEAD:internal/database/vulnerability.go` 逐字节取回，
再改名导出。**规矩**：跨包搬类型时「先在新家写好，再删老家」，且删的那条正则必须限定在老家文件。
③ 同一批跨包正则又一次改了注释（`// VulnerabilityListFilter 列表/统计/导出共用的筛选条件`
被改成 `// store.VulnerabilityListFilter …` 并留在老家，成了没人认领的孤儿注释）；
按第十九刀定下的流程重读了全部被改到的注释行，孤儿注释与 `store` 包里那两句指向已改标题的
文档引用一并删改。**注释不在任何门禁射程内这件事，是这两刀连续撞上的同一个坑。**

### P6 第二十一刀（前半）—— 搬查询之前先修好它：升级库里的漏洞行从列表里消失

第二十刀把 11 个方法标成"下一刀纯机械搬迁"，真搬之前先照 `RecentFindings` 那条规矩查了一遍
**同一类缺陷是否也在记录读里**，结论：**在，而且比摘要那回更严重**。

`vulnerabilities` 表里有 **7** 列可为 NULL，却在两条 SELECT 里**没有 COALESCE**、直接扫进
`string`：`description`、`conversation_tag`、`task_tag`、`vulnerability_type`、`target`、
`impact`、`recommendation`。它们为 NULL 有两条来路，都不是假设：
① `conversation_tag`/`task_tag`/`project_id`/`preconditions`/`reproduction_steps`/`evidence`/
`retest_notes` 是 `ALTER TABLE ... ADD COLUMN` 后加的（`database.go:1455-1461`），
**升级过的库里旧行这些列就是 NULL**；
② 本仓自己的表重建迁移（`database.go:1376-1390`）把旧表逐列搬进新表时，**只给其中 4 列写了
`COALESCE(x,'')`**，`description`、`conversation_tag`、`task_tag`、`vulnerability_type`、`target`、
`impact`、`recommendation` 原样搬运——这段迁移本身就是"NULL 是真实存在的状态"的最硬证据。
新建库走 `CreateVulnerability` 永远绑值，所以从零装机看不出问题。

- 列表读对 scan error 的处理是 `db.logger.Warn` + `continue`——这些行**从列表、
  从批量删除的候选、从导出里消失**，只留一行 warning；
  而 `GetVulnerabilityStatsForAccess` 的 `total` 是 SQL 数出来的，**统计说有几条、列表一条不显示**，
  这就是它一直没人看见的原因。
- 单条读更响：一个确实存在的 id 回答 `获取漏洞失败`。

**修复**：两条 SELECT 的这 7 列全部 COALESCE 成 `''`；列表循环里 scan error 改为
`return err` 并补 `rows.Err()`（COALESCE 之后这条分支只在"表和查询对不上"时才可能进，
继续 skip 等于把结构故障变成少几条结果）。`zap` 在该文件不再有引用，import 一并删除。

**回归测试**（`internal/database/vulnerability_legacy_row_test.go`，真库、零 mock）：
用生产 schema（`database.NewDB`）插一条"升级形状"的行——只给 NOT NULL 列赋值，
并在断言前先**验证那五列真的是 NULL**（否则夹具就不再复现升级形状了，这条自检是防夹具比浏览器聪明
那一类的）；断言列表返回它、详情读得到它、`total` 与列表**一致地**都是 1；
另设一条"有值的行必须把值读回来"的反向用例，防止 COALESCE 把真 tag 抹成空串。

**先红后绿**：修复前该测试**如实红在**`list returned 0 rows [], want the legacy finding`。
两个方向的探针（注入即红 / 撤销即绿）：① 只把**详情**那条 SELECT 的两列 COALESCE 摘掉 →
`detail read of an existing finding failed: ... converting NULL to string is unsupported`；
② 只把**列表**那条摘掉 → `list: 扫描漏洞记录失败: ... column index 7, name "conversation_tag"`。
两条红话不同，说明测试分别盯住了两次读；恢复后绿。
**探针也否证了一件事**：把 `return err` 换回 `continue` 之后测试仍然绿——因为 COALESCE 已经让
这一分支不可达。所以本刀的测试证明的是**查询**，不是那句错误处理；文档不许把它写成"测试覆盖了错误分支"。

**同一类还留在原地的两处**（本片没动，如实记下）：`GetVulnerabilityStatsForAccess` 的两个
GROUP BY 循环、`GetVulnerabilityFilterOptionsForAccess` 的 `collect` 仍是
`scan err → continue` 且不读 `rows.Err()`。它们扫的列 NOT NULL 或已被
`IS NOT NULL` 过滤，**升级形状进不去**，所以不是同一个缺陷；要改的动机是"少一个吞错的循环"，
属于第二十一刀后半搬迁时顺手做，不能算作已修。

### 明确还没做（不假装完成）

- P6 剩余：数据层按域切 Store（已落地 HITL/会话(含 messages 内容写回)/通知已读/漏洞最近条目/执行失败条目
  共 5 个面 + 共享可见性子句，handler 裸 SQL **已归零**；**这一项已完成**：`internal/handler` 里
  **没有任何结构体再持有 `*database.DB` 或 `*sql.DB`**（19 → 0；18 个域换成各自的窄接口字段，
  另 1 个是没人读的死字段，直接删。见 §11「P6 数据层第五片」与 §12.1 表）；
  `internal/database` 那 361 个方法本身也按域继续切（**已交回 11 个面：skill_stats 是第六片，
  `knowledge_base_items` + `knowledge_embeddings` 是第十一（合并算一片），`model_token_usage` 是第十二，
  现测 308 个方法（第十六刀之后），由 `TestDatabaseSurfaceOnlyShrinks` 钉成只降水位**；**分层裸 SQL 已归零**——`internal/handler`
  之后第二个持有 SQL 的 `internal/knowledge` 也交完了，见 §11「P6 数据层第十一片」）、
  `AgentHandler` 分解（**水位实测 + 门禁 + 六刀已落**：起点 130 方法/23 文件，
  现已搬到 **88 方法/21 文件**——中断队列读面 9 个方法进 `HITLQueue`、收尾链路 10 个方法进
  `runFinalizer`、11 个审批配置端点 + 它们读的配置状态进 `HitlPolicy`、挂起审批的应答面 3 个端点
  回 `HITLQueue`，见 §11「分解第 3–5 刀」；
  边界已定：剩下的 **13 个方法/6 个文件**（`hitl.go` 4、`hitl_context.go` 3、`hitl_audit_agent.go` 3、
  `hitl_execution.go`/`hitl_config_savers.go`/`batch_hitl.go` 各 1）是"工具调用里等一个决定"那一族，
  与 task/会话/SSE 同呼吸，**按边界就是留**；再搬要为搬运造两层接口；
  `internal/handler` 整包 64 个 `Set*` 未增；
  另落地 1 处内聚塌陷 + 1 道审计注入完整性门禁，见 §11「P6 `AgentHandler` 分解」；
  报告原记的"19 个文件/26 处 SetXxx"是低估）、Eino 收口至 ≤1 包（**已进门禁并在收**：
  引入包 11→6、适配边界之外债面 8 包/100 文件 → 3 包/96 文件，见 §11「P6 Eino 收敛」）、session 事件溯源。
  （provider catalog 代码生成**已完成**，见 §11「P6 Provider 方言目录代码生成」。）
- P3 剩余（SSE 线格式已归一，手拼帧 0；三套事件名契约——流式 / 持久化 / C2——均已双侧比对，
  生成枚举已由 `index.html` 加载并在分发入口调用；`_t` 一族 7 份 / 3 种实现已收口成两个具名行为）：
  逐文件 ES 模块改造（去重 2,800–3,200 行）；调用点旁仍手写的 `typeof window.t === 'function'`
  判断还剩 **23 个文件**（`i18n-tag.test.cjs` 里是只准降的 ratchet）。
- P4 剩余：netns/seccomp 级硬出网边界（现在只有代理白名单 + cgroup/rlimit；**包携带的插件二进制
  走同一套 `StrictEgress` + 代理，并且开关必须重新核对能力清单**，见 §11「能力包携带可执行代码」）、
  内嵌 CPython 发行、包内二进制的**制品签名链**（撤销联动**已做**：能力带 `Publisher` +
  二进制自身的 `ArtifactDigest`，执行路径每次调用查；单元的指纹也盖住二进制，换文件即报漂移）。
- P5 剩余：registry 服务端（签名发布、灰度、release-age 冷却）、气隙离线包导出/导入、沙箱引爆自动化。
- §6.1 待决策：角色/skill/markdown-agent 文本是否也按运行期不可信处理（当前视为"已安装的运维者配置"）。
- **决策项 10（第十九刀翻出来的）**：会话漏洞导出接口与 MCP 漏洞列表工具目前读的是**不加限制**的那条路
  （任何通过鉴权的调用者 / 任何一次带 principal 的工具调用都能看到全库漏洞）。
  要不要按调用者可见性收窄是产品决定；本刀只把"不加限制"从隐式默认改成显式参数，没有改变任何可达行为。
- `docs/zh-CN/agent-finalization-best-practices.md` 缺 en-US 且引用旧域名 `docs.anthropic.com/en/docs/claude-code/*`，本报告已更正但未代改。

## 十二、交接口径（验收清单 · 剩余工作 · 未提交状态）

### 12.1 逐阶段验收：每条都能用一条命令复验

| 阶段 | 状态 | 复验证据 |
|---|---|---|
| P0 护栏 + S1–S6 | **已落地** | `make ci`（fmt 硬零 + vet + `test-race` + lint + arch-lint + layering + wiring）；`internal/settings`（S4）、`internal/assets`+`web/embed.go`（S5）、`internal/capability`（S1/S2/S3）、`internal/provider`（S6）。**本机注意**：`golangci-lint` 与 `go-arch-lint` 二进制未安装，这两个 target 显式报错并给出安装命令（不是静默跳过），故本机验收用 `make fmt-check vet test-race layering-check wiring-check`，CI 里五条加上 lint/arch-lint 全跑 |
| P1 授权/审批 | **已落地** | `go test -count=1 -run 'Approval|Capability|Declared' ./internal/app/ ./internal/capability/`；`grep -cE '^\s+case ' internal/app/mcp_authorization.go` = 6 |
| P2 能力身份 + 一份清单出多份产物 | **已落地** | `make generate` 后 regenerate-and-diff 不报差异；`docs/zh-CN/capability-catalog.md` 149 条（150 行含表头） |
| P3 契约与前端 | **部分：三套事件名契约已完成并双侧比对**；逐文件 ES 模块未做 | `go test -count=1 -run 'TestSSEPage|TestPersistedDetail|TestGeneratedSSEEnum|TestPageLoads' ./internal/handler/`；手拼帧基线 0 |
| P4 进程外插件宿主 | **部分：进程外 ABI + 软出网已落**；netns/seccomp 硬边界与内嵌 CPython **未做** | `ls internal/pluginhost`；`grep -rl 'seccomp\|CLONE_NEWNET' internal/` → **无匹配**（这就是"未做"的证据） |
| P5 审核流水线/商店 | **部分：客户端强制 + 制品签名/撤销已落**；registry 服务端、气隙离线包、沙箱引爆自动化 **未做** | `ls internal/artifact`；`ls internal/registry` → **不存在** |
| P6 常规解耦 | **部分**：`setupRoutes` 分域、Provider 方言 + 目录代码生成、**数据层按域切出 11 个面 / `internal/store` 18 个生产文件**（`*database.DB` 361 → **306**，只降门禁）、**分层裸 SQL 归零**（两个自有层之外 0 条）、DATETIME 读法 24 处 → 1 处、应用回调不再挂在连接包装上、**handler 层不持有任何数据库句柄**、Eino 6 包（适配外 3 包）、`AgentHandler` 六刀至 **88 方法 / 20 文件**、审计注入门禁、手写 OpenAPI 文档按域拆成 5 个分组文件 + golden（157 操作逐字节等值） | `make layering-check` + `make wiring-check`；`go test -count=1 -v -run TestHandlerLayerHoldsNoGodObject ./internal/layering/` 报 `transport layer: 0 structs hold *database.DB, 18 fields hold a narrowed database interface, 13 hold their own table store, 1143 struct fields scanned (started 19/0)`；`go test -count=1 -v -run TestRawSQLIsOnlyWrittenByTheLayersThatOwnIt ./internal/layering/` 报 `0 statements in 0 files, over 508 production files scanned`；`go test -count=1 -run 'TestOpenAPI' ./internal/handler/` |
| §6.1 社区知识控制 | **代码层已落**（围栏 + 入库拒绝 + 装配点守卫）；是否按运行期不可信处理仍待裁决（决策项 4） | `go test -count=1 ./internal/contentpolicy/` |

**没有做成的事**（不假装完成）：`AgentHandler` 分解本体（88 方法 / 20 文件，仍是全仓最大的类型；
六刀搬出中断队列读面、收尾链路、审批配置面与其状态、挂起审批应答面四族，
剩下的 13 个方法按已定边界属于运行链路、就该留在这里，见 §11「分解第 3–5 刀」）、
~~剩余 3 个域的窄接口~~（**已在第五片做完**：阻塞点是 `h.db` 逃逸进别包签名，解法是给那些函数
声明消费者接口——`project.Store`/`agentfinalizer.Store`/`attackchain.Store`/`workflow.Store`，
19 → 0）、Eino 收到 ≤1 包、
session 事件溯源、逐文件 ES 模块、`internal/database` 剩下的 **306** 个方法继续按域切
（下一刀把漏洞域的 **11 + 7** 个方法连三张表的 DDL 一起搬；前置的三件铺垫与本片的行结构搬迁
都已落地，细节见本节末「漏洞域：三件铺垫已拆完，行结构已经搬过去」）、
P4 硬网络边界、P4 内嵌 CPython、P5 registry / 气隙包 / 引爆自动化、
以及 **§10 的 9 个决策项一个都没有被裁决**（其中 1、4、5、6 直接决定 P4/P5 的形态）。
因此**目标未达成**，本表就是"还差什么"的清单。

**漏洞域：三件铺垫已拆完，行结构已经搬过去**
本晚试过把 `database.Vulnerability`（行结构）与 `VulnerabilityListFilter` 先搬进 store、
方法随后再搬。第一次做到第三步编译就停住了，原因不是工作量而是**耦合形状**，
三条阻塞逐一处理如下：
1. ~~`VulnerabilityListFilter.appendWhere`（约 55 行）是**类型的成员**，被
   `ListVulnerabilities*` / `Count*` / `GetVulnerabilityStats*` / 导出这 **4 条查询**调用；
   类型搬走后它只能变成 store 的非导出方法，留在数据层的那 4 条查询立刻 `cannot refer to
   unexported method`。也就是说**过滤条件与它的 SQL 构造器不可分**，要么连着 4 条查询一起搬。~~
   **已解**：构造器跟着类型进 store 并导出为 `VulnerabilityListFilter.ConstrainWhere`，
   转义器同步导出边界内的 `escapeLikePattern`。数据层的 4 条查询改为调用它，编译通过。
2. ~~同一个文件里还定义着 `RBACListAccess`。~~ **已解**（第十七、十八片）：它并入 `store.Access`，
   授权词汇只剩一份。
3. ~~`appendVulnerabilityAccessFilter` 与 `internal/store/access.go` 已有的那份是**同一条规则的
   第二种拼写**。~~ **已解**（第十九片）：先写差分测试 `access_clause_parity_test.go`
   （7 条种子漏洞 × 7 个身份 × `own`/`assigned` 两种范围 = 14 次逐一比对旧拼写的原文常量），
   把差异限定成"无身份时失败闭合"这一条并单独断言（旧写法在此返回全部 7 条），再合成
   `store.ConstrainFinding` 一条。差分测试同时抓出了我在合并过程中写出的自比 bug
   （`ConstrainConversation(..., "c.id", ...)` 退化成 `WHERE c.id = c.id`，等价于不设限），
   探针重放确认门禁真会红。

于是本片的实际内容：**行结构与过滤器进 `internal/store/vulnerability.go`**，全仓 19 个文件的
类型引用跟着改（`handler/vulnerability.go`、`app/vulnerability_tools.go`、`handler/openapi.go`、
`app/vulnerability_alert_route.go`、`audit/resource_availability.go`、`stores.go` 的 3 个窄接口
与测试夹具）。搬完后剩余工作量第一次变得可比：`internal/database/vulnerability.go` 532 → **396**
行 / **11** 个方法，`vulnerability_alert.go` **206** 行 / **7** 个方法。
下一刀把这 **18 个方法**连 `vulnerabilities`、`vulnerability_alert_subscriptions`、
`vulnerability_alert_deliveries` 三张表的 DDL 一起搬进 `store.Vulnerabilities` 与告警 store，
搬完才能把 `robot_user_bindings` 补进归属清单（现在为那条 JOIN 故意没认领）。

**搬动过程中自伤两次，都已就地修好并记为规矩**：脚本切块时把 `CreateVulnerability` 一并拖进
了 store（搬回），删旧定义的正则先于新增把过滤器的 SQL 构造器**删了**（从
`git show HEAD:internal/database/vulnerability.go` 逐字节取回再改名导出）。
教训：跨包搬类型时**先在新家写好，再删老家**，删的那条正则必须限定在老家文件。

### 12.2 交付方式：二开分叉，任务结束推自己的 fork，不提 PR

**曾经的风险**（这一版报告写完时的状态）：`git log -1` 还指向重构前的 `470eb5e`，
全部改动（约 260 个路径）只存在于工作区——一次 `git checkout .` / `git clean -fd` 就不可恢复，
而我当时没有提交权限。

**现已按用户的明确授权解决**：这是从 `AIPentest/CyberStrikeAI` 拉下来做二开的分叉，
每轮任务结束上传到用户自己的 GitHub，**不向任何仓库提 PR**（除非用户明确说要）。落地口径：

```
mine    https://github.com/Sycun/CyberStrikeAI.git   # 我的 fork，交付推这里
origin  https://github.com/AIPentest/CyberStrikeAI.git  # 上游父仓库，只读，永不推
```

- 提交 `14fde40`（319 路径，+45283/-3235）→ `git push mine main` 是 **fast-forward**
  （推之前 `git ls-remote` 确认过 `mine/main` 恰等于本地 HEAD `470eb5e`），没有 `--force`、没有改写历史。
- 复验：`mine/main` = `14fde40`；`origin/main` 仍是 `470eb5e`（未被触碰）；未创建任何 PR。
- 仓库级 `user.name` / `user.email` 设为 GitHub noreply 身份（`165354365+Sycun@users.noreply.github.com`），
  只写进 `.git/config`，不动全局配置。
- 提交前的两道人工检查：`git status --short` 看清包含什么，以及对暂存清单按
  `config.yaml|.env|*.db|secret|token|credential|*.backup` 过滤（`.gitignore` 已挡运行期数据与本地配置）。
- 往后的每一轮交付都按这个口径落远端快照；若 `mine/main` 已领先本地，先问用户而不是 force 或 rebase。

### 12.3 下一轮的第一件事（已排好，直接接着做）

1. **窄接口的下一层（原任务 #18）已完成**：handler 层持有裸句柄的结构体 **3 → 0**，
   判据也从"只许降"翻成硬零 `TestHandlerLayerHoldsNoGodObject` + 形状门禁
   `TestNarrowedFieldsAreOnlyAssignedThroughNarrow`。**`AgentHandler` 分解六刀已落**：
   中断队列读面 9 个方法进 `HITLQueue`、收尾链路 10 个方法进 `runFinalizer`、
   11 个审批配置端点与其配置状态进 `HitlPolicy`、挂起审批的应答面 3 个端点回 `HITLQueue`，
   水位 **130 → 88 方法、23 → 21 文件**，上限逐刀收紧并探针验红（见 §11「分解第 3–5 刀」）。
   **HITL 这一族的边界已经定完并落地**：规则*是什么*归 `HitlPolicy`，谁在等人归 `HITLQueue`，
   "工具调用里等一个决定"那 **13 个方法/6 个文件**留在 run loop —— 它们与 task/会话/SSE 同呼吸，
   再搬就是为搬运造两层接口（第二刀的 `tryAutoContinueAfterFinalization` 已示范过一次"该留就留"）。
   三条只降门禁（方法数 88、文件数 21、整包 setter 64）会把它锁住：搬走得让数字下降，塞回来会红；
   另加两条防回潮：`TestPendingInterruptMethodsAreNotOnAgentHandler`（应答面长回 agent 即红）与
   `TestHITLManagerPrivateStateStaysPrivate`（manager 私有状态被第二个主人经字段触碰即红）。
2. **下一处真正的大头是前端，不是 handler**：`web/static/js/chat.js` 11,232 行、
   `monitor.js` 9,712 行（`wc -l web/static/js/chat.js web/static/js/monitor.js`），
   逐文件 ES 模块改造仍在报告 P3 的"去重 2,800–3,200 行"上；
   后端 API 错误串未 i18n——口径与数字都要可复验：
   `grep -rhoE '"(error|message)": "[^"]*"' internal/handler/*.go | grep -c '[一-龥]'` = **391 条中文**
   （同一条命令去掉 `grep -c` 换 `-vc` = 132 条 ASCII）。属契约变更，要连同前端字典一起动。
   再往后才是 `internal/database` 那 320 个方法（**分层裸 SQL 已归零**：`internal/knowledge` 是
   `internal/handler` 之后最后一个在两个自有层之外写 SQL 的包，见 §11「P6 数据层第十一片」；
   剩下的 SQL 全在主人手里——`internal/database` 429 处、`internal/store` 87 处，
   下一刀从这个连接包装自己按域拆开开始）：按 `*DB` 接收者当场数的大水面是
   **`conversation.go 48 / c2.go 47 / rbac.go 40 / monitor.go 22 / batch_task.go 22 / project.go 18 / asset.go 18`**，
   复现命令：`for f in internal/database/*.go; do case "$f" in *_test.go) continue;; esac; n=$(grep -cE '^func \([a-zA-Z_]+ \*DB\)' "$f"); [ "$n" -gt 0 ] && printf "%4d %s\n" "$n" "$f"; done | sort -rn`，
   **合计 308**。第十六刀之后另发现一条**排序约束**，值得排在下一轮第一位：
   `vulnerability_alert_subscriptions` + `vulnerability_alert_deliveries` 这两张表（8 个方法）**不能**
   先于 `vulnerabilities`（11 个方法）单独搬——`ListDueVulnerabilityAlertDeliveries` 的返回体里带着
   整条 `Vulnerability` 行结构（12 个字段、handler 与 MCP 两侧都消费它），先把提醒表搬走就会让 store
   反向依赖 `internal/database` 的行类型，层次直接违反 `make arch-lint`。
   所以漏洞域必须**一次搬两个文件**（19 个方法、`store.Vulnerability` 已在位、只差行结构体与 3 个消费者），
   搬完才能把 `robot_user_bindings` 补进归属清单（现在为这条 JOIN 故意没认领，原因写在
   `internal/store/ownership_test.go` 上方）。
   其余小文件里 `c2_payload.go`(2) / `tool_execution_args_lookup.go`(1) / `tool_guard_migration.go`(1)
   是最后几个"单域小文件"，各 1-2 个方法即可整文件删掉；
   `storage_activity.go` / `plantask.go` / `project_dashboard.go` 读的是**别人家**的表
   （conversations / projects / process_details），按"以表定主人"应当随会话与项目那两片一起走，
   先搬只会造出第二个写者。
   Eino 收到 ≤1 包、session 事件溯源。
3. 需要你插队的只有一件：**§10 决策项 1**（`agent:local-execute` 是否作为阻断项立即处理），
   它决定社区制品的攻击面；其余决策项可以在 P4/P5 动工前再定。

## 附录 A：如何复现本报告的关键数字

```bash
# 内部包依赖图与 SCC（0 循环）
go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./internal/... ./cmd/... > /tmp/deps.txt

# DB 方法数 / 裸 SQL / 越层 import
grep -rhE '^func \((db|d) \*DB\)' internal/database/*.go | grep -v _test | wc -l   # 361 起测，现 320（已交回 11 个域，由 internal/layering 的只降门禁钉住）
# 分层裸 SQL（判据与接收者名字无关：字符串字面量以 SQL 开头就算）——两个自有层之外为 0
grep -rhnE '["`][[:space:]]*(SELECT|INSERT INTO|UPDATE|DELETE FROM)\b' $(find internal cmd -name '*.go' ! -name '*_test.go' ! -path 'internal/database/*' ! -path 'internal/store/*') | wc -l   # 0
# 同一条判据在两个自有层里：internal/database 429 处、internal/store 87 处（它们是 SQL 的主人，不是泄漏）
grep -rnE 'h\.db\.(Exec|Query|QueryRow|Begin)' internal/handler/*.go | grep -v _test | wc -l  # 32（原报告口径）
# 49 = 重构前的 HTTP 层全部接收者；当前树为 0（见 §11「当前水位」）
grep -rhoE '\b[a-z]+\.db\.(Exec|Query|QueryRow|Begin|Prepare)\(' $(ls internal/handler/*.go | grep -v _test) | wc -l
grep -rln '"database/sql"' internal/ cmd/ --include='*.go' | grep -v _test | xargs -n1 dirname | sort -u

# 路由与参数
awk 'NR>=918 && NR<=1480' internal/app/app.go | grep -cE '\.(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS|Any)\('  # 278

# 常量表 / 授权 switch / RBAC 资源枚举
grep -cE '^\s+[A-Z][A-Za-z0-9]* += +"' internal/mcp/builtin/constants.go   # 51
grep -cE '^\s+case ' internal/app/mcp_authorization.go                      # 37
grep -cE 'case "' internal/database/rbac.go                                 # 46

# 前端
grep -roE "typeof [A-Za-z_]+ === ['\"]function['\"]" web/static/js/*.js | wc -l  # 446
grep -rc "window.t *=" web/static/js/*.js | awk -F: '{s+=$2} END{print s}'       # 486
grep -rn "go:embed" --include="*.go" . | wc -l                                     # 0

# 基线
go build ./... && go test -count=1 ./...    # 32 包全绿 / 0 失败
```

## 附录 B：核实标注

**已逐行核实**：所有 §1、§2 的行号与计数；`mcp_authorization.go:188-195` 默认分支；`hitl.go:401-402` 豁免语义与 `:393-395` 会话级 opt-in；`server.go:535-541 / :944-950` nil 分支；`executor.go:1381-1396` 未实现；`angr.yaml:33` `exec()`；`run.sh:178/:487`；`goccy/go-python` README 与其 **issue #6**（无 pip、无 C 扩展、缺 `ssl`/`ctypes`/`sqlite3`）；`hashicorp/go-plugin`/`Extism`/`wazero`/`starlark-go` 的活跃度元数据；`kluctl/go-embed-python` 与 `tamnd/goempy` 的模式描述；Nuclei `CONTRIBUTING.md` 与 `TEMPLATE-REVIEW-GUIDE.md` 引文；XSOAR contribution checklist（playbook 不要求测试）；Elastic 包签名与 `ignoreUnverified`；VS Code Extension Host；Neovim `remote_plugin.txt`；Claude Code `allowed-tools`/hooks/permissions 文档；gh CLI 2.101.0 自带 help 文本。

**未核实 / 需谨慎引用**：Nuclei 的两个 2026 advisories（CVE-2026-76819、CVE-2026-41282）；yakit 是否存在面向作者的"插件源码加密/授权"功能（官方文档检索无权威出处，只有打包完整性加密）；wazero 对 CPython 的支持现状是否已改变；"Burp 扩展进程内 JVM"为推断；`processguard` 内部硬编码路径细节来自子代理报告（其扇出 0 与全局 `configured` 加锁结构我已核实）；`goccy/go-python` 的基准数字为作者自报。

## 附录 C：外部来源

Claude Code：[how it works](https://code.claude.com/docs/en/how-claude-code-works)、[hooks](https://code.claude.com/docs/en/hooks)、[skills](https://code.claude.com/docs/en/skills)、[plugins](https://code.claude.com/docs/en/plugins)、[permissions](https://code.claude.com/docs/en/permissions)、[permission modes](https://code.claude.com/docs/en/permission-modes)、[MCP](https://code.claude.com/docs/en/mcp)、[tool search](https://code.claude.com/docs/en/agent-sdk/tool-search)、[context window](https://code.claude.com/docs/en/context-window)、[Agent Skills 工程文](https://www.anthropic.com/engineering/equipping-agents-for-the-real-world-with-agent-skills)、[Advanced tool use](https://www.anthropic.com/engineering/advanced-tool-use)、[GTG-1002](https://www.anthropic.com/news/disrupting-AI-espionage)

pi：[pi.dev](https://pi.dev/)、[earendil-works/pi](https://github.com/earendil-works/pi)、[pi-ai README](https://github.com/earendil-works/pi/blob/main/packages/ai/README.md)、[pi-agent-core README](https://github.com/earendil-works/pi/blob/main/packages/agent/README.md)、[extensions.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md)、[security.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/security.md)、[compaction.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/compaction.md)、[skills.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/skills.md)、[MCP issue #563](https://github.com/earendil-works/pi/issues/563)、[pi-mcp-adapter](https://pi.dev/packages/pi-mcp-adapter)、[作者长文](https://mariozechner.at/posts/2025-11-30-pi-coding-agent/)

yakit：[yaklang/yaklang](https://github.com/yaklang/yaklang)、[yaklang/yakit](https://github.com/yaklang/yakit)、[架构 2.1](https://yaklang.com/products/chapter-2/2-1/)、[插件生态 5.2](https://yaklang.com/products/chapter-5/5-2/)、[编写指南 5.3](https://yaklang.com/products/chapter-5/5-3/)、[插件商店指南](https://yaklang.com/blog/yakit-plugin-store-guide/)、[AI Agent 能力编排](https://yaklang.com/blog/ai-agent-orchestration-in-yakit-plugin-store/)、[db API](https://yaklang.com/docs/api/db/)、[#3673](https://github.com/yaklang/yakit/issues/3673)、[#3798](https://github.com/yaklang/yakit/issues/3798)、[#2909](https://github.com/yaklang/yakit/issues/2909)、[#2507](https://github.com/yaklang/yakit/issues/2507)、[团队页](https://yaklang.com/team/)

市场与供应链：[VS Code extension host](https://code.visualstudio.com/api/advanced-topics/extension-host)、[Workspace Trust](https://code.visualstudio.com/api/extension-guides/workspace-trust)、[Chrome 权限声明](https://developer.chrome.com/docs/extensions/develop/concepts/declare-permissions)与[审核流程](https://developer.chrome.com/docs/webstore/review-process)、[Actions 加固指南](https://docs.github.com/en/actions/security-guides/security-hardening-for-github-actions)、[fork 审批](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/approve-runs-from-forks)、[npm provenance](https://docs.npmjs.com/generating-provenance-statements/)与[trusted publishing](https://docs.npmjs.com/trusted-publishers/)、[SLSA 等级](https://slsa.dev/spec/v1.0/levels)、[PyPI 隔离区](https://blog.pypi.org/posts/2024-12-30-quarantine/)与[2025 年度回顾](https://blog.pypi.org/posts/2025-12-31-pypi-2025-in-review/)、[Shai-Hulud（Wiz）](https://www.wiz.io/blog/shai-hulud-npm-supply-chain-attack)、[AppExchange 安全评审](https://developer.salesforce.com/docs/platform/isvforce/guide/security-review-required-materials.html)、[Homebrew 安全](https://docs.brew.sh/Homebrew-Security-and-Supply-Chain)、[palant 对 CWS 的批评](https://palant.info/2025-01-13/chrome-web-store-is-a-mess/)

安全内容社区：[Nuclei CONTRIBUTING](https://github.com/projectdiscovery/nuclei-templates/blob/main/CONTRIBUTING.md)、[TEMPLATE-REVIEW-GUIDE](https://github.com/projectdiscovery/nuclei-templates/blob/main/TEMPLATE-REVIEW-GUIDE.md)、[workflows](https://docs.projectdiscovery.io/templates/workflows/overview)、[interactsh](https://projectdiscovery.io/blog/nuclei-interactsh-integration)、[CVE-2024-43405](https://nvd.nist.gov/vuln/detail/cve-2024-43405)与[Wiz writeup](https://www.wiz.io/blog/nuclei-signature-verification-bypass)、[Metasploit msftidy](https://rapid7.github.io/metasploit-framework/docs/development/quality/msftidy.html)与[私有模块](https://docs.metasploit.com/docs/using-metasploit/intermediate/running-private-modules.html)、[Sigma 规范](https://github.com/SigmaHQ/sigma-specification/blob/main/specification/sigma-rules-specification.md)、[Livehunt YARA 限制](https://gtidocs.virustotal.com/docs/writing-yara-rules-for-livehunt)、[Burp BApp 验收标准](https://portswigger.net/burp/documentation/desktop/extend-burp/extensions/creating/bapp-store-acceptance-criteria)、[XSOAR contribution checklist](https://xsoar.pan.dev/docs/contributing/checklist)、[Splunk SOAR 审批](https://lantern.splunk.com/Security_Use_Cases/Automation_and_Orchestration/Building_a_SOAR_playbook_for_user-initiated_approval_of_automation)、[Elastic 包签名](https://www.elastic.co/docs/reference/fleet/package-signatures)

注入与 RAG：[PoisonedRAG（USENIX Sec'25）](https://www.usenix.org/system/files/usenixsecurity25-zou-poisonedrag.pdf)、[arXiv:2505.11548](https://arxiv.org/html/2505.11548v4)、[arXiv:2607.04379](https://arxiv.org/abs/2607.04379)、[Instruction Hierarchy arXiv:2404.13208](https://arxiv.org/abs/2404.13208)、[CaMeL arXiv:2503.18813](https://arxiv.org/abs/2503.18813)、[skill 生态规模研究 arXiv:2602.06547](https://arxiv.org/html/2602.06547v1)、[Snyk ToxicSkills](https://snyk.io/blog/toxicskills-malicious-ai-agent-skills-clawhub/)、[Datadog 恶意 skill 动态上下文](https://securitylabs.datadoghq.com/articles/malicious-skills-supply-chain-risks-in-coding-agents-with-dynamic-context/)、[GPT Store 研究 arXiv:2311.11538](https://arxiv.org/html/2311.11538v2)、[Invariant Labs GitHub MCP](https://invariantlabs.ai/blog/mcp-github-vulnerability)、[VS Code agent 安全](https://code.visualstudio.com/docs/agents/run/security)

运行时与嵌入：[Datadog: Cgo and Python](https://www.datadoghq.com/blog/engineering/cgo-and-python/)、[goccy/go-python](https://github.com/goccy/go-python)与[issue #6](https://github.com/goccy/go-python/issues/6)、[kluctl/go-embed-python](https://github.com/kluctl/go-embed-python)、[tamnd/goempy](https://github.com/tamnd/goempy)、[python-build-standalone](https://github.com/astral-sh/python-build-standalone)、[wazero](https://github.com/wazero/wazero)与[#769](https://github.com/wazero/wazero/issues/769)、[Extism](https://github.com/extism/extism)、[starlark-go](https://github.com/google/starlark-go)、[gpython](https://github.com/go-python/gpython)、[hashicorp/go-plugin](https://github.com/hashicorp/go-plugin)、[criyle/go-sandbox](https://github.com/criyle/go-sandbox)、[Envoy Wasm](https://www.envoyproxy.io/docs/envoy/latest/configuration/other_features/wasm)、[Pyodide](https://pyodide.org/en/stable/usage/index.html)、[Go plugin 包](https://pkg.go.dev/plugin)

架构方法论：[Google Go 风格指南](https://google.github.io/styleguide/go/decisions)、[Google Small CLs](https://google.github.io/eng-practices/review/developer/small-cls.html)、[Feathers 关键要点](https://understandlegacycode.com/blog/key-points-of-working-effectively-with-legacy-code/)、[Fowler Strangler Fig](https://martinfowler.com/bliki/StranglerFigApplication.html)/[Monolith First](https://martinfowler.com/bliki/MonolithFirst.html)/[Parallel Change](https://martinfowler.com/bliki/ParallelChange.html)、[Simon Brown 模块化单体](https://simonbrown.je/modular-monolith/)、[Grzybek 边界强制](https://www.kamilgrzybek.com/blog/posts/modular-monolith-architecture-enforcement)、[go-arch-lint](https://github.com/fe3dback/go-arch-lint)、[depguard](https://github.com/OpenPeeDeeP/depguard)、[golangci-lint v2 配置](https://golangci-lint.run/docs/configuration/file/)、[Ben Johnson](https://medium.com/@benbjohnson/structuring-applications-in-go-3b04be4ff091)、[函数式选项](https://dave.cheney.net/2013/10/13/functional-options-for-friendly-apis)、[原子快照热重载](https://dethlex.com/garden/go-atomic-swap-reload/)、[goose](https://github.com/pressly/goose)、[SQLite ALTER TABLE](https://www.sqlite.org/lang_altertable.html)、[repository pattern 无 ORM](https://baodev.studio/blog/repository-pattern-go/)
