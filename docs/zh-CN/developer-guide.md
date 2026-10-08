# 开发者指南

本文面向二次开发者，说明项目结构、启动方式、主要扩展点和开发习惯。

## 项目结构

```text
cmd/server/              Web 服务入口
internal/app/            应用组装、路由注册、MCP 工具注册、能力策略装配（唯一装配点）
internal/capability/     能力身份/清单/授权/审批下限（叶子包，不依赖任何业务包）
internal/handler/        HTTP Handler
internal/database/       SQLite 数据访问（存量；新域见 internal/store）
internal/store/          按域拆分的持久层：一个域一个文件，只吃 *sql.DB，能独立用真库测试
internal/security/       认证、限流、Shell 执行
internal/mcp/            MCP Server、外部 MCP 管理
internal/multiagent/     Eino 单代理、多代理、中间件
internal/workflow/       工作流运行时
internal/knowledge/      知识库索引与检索
internal/c2/             内置 C2
internal/project/        项目事实黑板
web/static/              前端 JS/CSS/资源
web/templates/           HTML 模板
tools/                   命令工具 YAML
roles/                   角色 YAML
agents/                  多代理 Markdown 定义
skills/                  Agent Skills
docs/                    项目文档
```

## 启动开发环境

```bash
go run ./cmd/server --config config.yaml
```

前端是静态页面，模板在 `web/templates/`，JS/CSS 在 `web/static/`。修改后刷新浏览器即可验证，多数场景不需要单独前端构建。

## 开发树与测试树

重构现场（开发树）只放源码。**编译、跑门禁、起服务做真机点验都在测试树**（默认
`~/csai-测试版`，它是开发树的一个 clone）——这样 build 出来的二进制、跑起来写的
`config.yaml` / `data/` / `log/` 永远不会落在开发树里，也就不会被 `git add` 顺走。

两件现场已经遇到的事：

- **偶发的清理竞争不是回归**。`make test-race` 在 macOS 上偶尔以
  `TempDir RemoveAll cleanup: unlinkat …/TestXxx/001: directory not empty` 失败——那是临时目录被
  清理时仍有后台写入者，不是被测逻辑变了。实测（2026-10-06）：同一提交上整包 `-race` 连跑三次全绿，
  唯一一次红就是这条。**碰到就重跑**，别把它记成失败。
- **要点验就用测试树里的第二实例**，别在 `/tmp` 建沙箱（那样每换一次验证就攒一份库和一次性口令）：
  在测试树里 `mkdir .livecheck`，把 `roles agents skills tools bundles` **软链**进去（配置里的相对路径
  按 config 文件所在目录解析），复制一份 `config.yaml` 改端口与 `sqlite` 路径，用它启动
  `../cyberstrike-ai -config ./config.yaml`，从 stdout 抓 `Password` 那行的一次性管理员口令换 token。
  用完 `rm -rf .livecheck` 并把改过的配置项恢复原样。今晚的插件链就是这么验出来的：
  冷启动只声明不启动、启用后 6 条能力挂上工具面、停用后 `pluginHost` 归零。

一次性建法：

```bash
git clone --single-branch --branch main <开发树路径> ~/csai-测试版
cp ~/csai-测试版/config.example.yaml ~/csai-测试版/config.yaml  # 端口改成自己的，别与生产实例抢
```

日常四条（`scripts/testtree.sh` 是同一件事的底层命令）：

| 命令 | 做什么 |
|---|---|
| `make test-sync` | 开发树源码灌进测试树；开发树已不存在的**代码文件**在测试树同步删除 |
| `make test-verify` | 逐文件摘要比对两树，不一致就非零退出并列出文件 |
| `make test-gates` | sync + verify，然后在测试树里跑 `fmt-check vet layering-check wiring-check js-check test-race` 并 build 两个二进制 |
| `make test-run` | sync + verify，然后在测试树里 build 并起服务（它自己的库与端口） |

判据是**内容**而不是 git HEAD：调试期间的改动常常还没提交，而规矩是"两边一起改"。
所以在测试树里单独改一个代码文件，`make test-verify` 会直接红并指名那个文件；
正确做法是回开发树改完再 `make test-sync`。测试树的 `config.yaml` / `data/` / `log/`
是被忽略的运行时文件，既不参与比对也不会被删；手工放进测试树 `bundles/` 里待验的能力包
同样只在 verify 里提示、不删（那是点验的输入，不是源码）。
测试树位置可用 `CSAI_TESTTREE` 或 `make test-gates TESTTREE=...` 覆盖。

## 一键更新：让安装目录更新它自己的源码

部署现场的日常是"我的 fork 又前进了几十个提交 → 把这套安装带上去 → 重新编译 → 换二进制 → 重启"。
这件事以前只有 `upgrade.sh`，而它写死了别人的仓库：对跑 fork 的人来说"升级"等于"用别人的代码覆盖自己"。
现在这个能力做进了平台自己（`internal/update`），页面、REST 与 CLI 三条入口共用同一份实现，
所以它们给出的拒绝理由不可能各说各话。

### 三种入口

| 入口 | 怎么用 |
|---|---|
| 控制台 | 「系统设置 → 一键更新」（`#system-update` 老深链仍可用）。打开这一区只读本机状态、不联网；点「检查更新」才去 fetch；「一键更新」发起任务并轮询进度；"更新完成后退出进程"在检测到守护进程（launchd/systemd 的启动标记）时默认勾选，重启期间页面守着重连、新进程一应答就自动刷新（会话在内存里，刷新后需重新登录）；另有回滚按钮 |
| REST | `GET /api/system/update`（磁盘现状，不联网）、`POST /api/system/update/check`（fetch 后报告差集）、`POST /api/system/update/apply`（`202` 返回 `job_id`，用 `GET /api/system/update/job` 轮询）、`POST /api/system/update/rollback` |
| CLI | `./cyberstrike-ai -check-update`、`./cyberstrike-ai -update`、`./cyberstrike-ai -update-rollback`。安装目录 = `--config` 所在目录，未给 `--config` 时是当前目录 |

权限是 `update:read`（GET）与 `update:apply`（四个写接口）。`update:apply` 还要求会话是 global（`all` scope）：
一台机器只有一份源码，`assigned`/`own` scope 的账号不该能移动别人正在跑的代码。

### 它从哪里取代码

从**这个目录自己的远端**，没有任何写死的第三方仓库地址。远端按 `mine → origin → upstream` 的次序在这个目录已有的
remote 里取（这是本项目实际的克隆形态：自己的 fork 叫 `mine` 并跟踪自己的工作，上游只留作参考），
分支默认取当前分支 `@{upstream}` 指向的名字（没有 upstream 时用当前分支名）。
`upgrade.sh` 现在只是这条实现的薄壳：本目录是 git 工作树时它直接调 `./cyberstrike-ai -update`。

### 选择更新源（config.yaml 的 update 段）

不配置时按上面的规则跟随本树；配置后显式来源优先：

```yaml
update:
  remote: origin        # 已有远端名（mine/origin/upstream/任意名），与 remote_url 二选一
  # remote_url: https://github.com/AIPentest/CyberStrikeAI.git
  branch: main          # 可选；留空按 upstream/当前分支推断
```

控制台的「一键更新」页可以直接编辑并保存这三项。服务端保存时校验：远端名与分支名走与更新同一套白名单，
地址只允许 https/http/ssh/git/file:// 与本机绝对路径——git 的 `ext::` 传输会执行命令，一律拒绝。
想让这套安装跟随官方仓库、自己的二开或别人的二开，改的都是这一处。

### 接入非 git 安装（解压/打包装的那类）

一开始用 Release 包解压安装、目录里没有 `.git` 的，配好上面的地址后可在同一页执行「预览并接入」：
预览在临时仓库里 fetch，先列出**会被目标版本替换的本机文件**与**会保留的运维者内容**；确认后目录接入
成为 git 工作树（`git init` + 添加 origin + 落地目标分支），被替换的文件全部留底在
`.update-backup/<时间戳>/overwritten/`，运维者内容照旧先暂存再放回，随后重编译二进制。
接入后它就是正常安装，一键更新与回滚都可用了；注意接入前没有 git 历史，因此「接入」本身没有可回滚的
上一提交，第一个回滚点由接入后的第一次更新写下。

### 四种拒绝场景

| 场景 | reason | 含义与处理 |
|---|---|---|
| 本地改过**产品源码**（不在 protected 清单里的路径） | `local_source_edits` | 一键更新是"下载"，不是"替你决定冲突"。点名文件，先提交或还原再重试 |
| 分支与远端**分叉**（本地既领先又落后） | `diverged` | 那是合并决策，给出 ahead/behind 数字；手工处理后再点 |
| 本机没有 Go 工具链 | `no_toolchain` | **源码照样更新**，只有二进制没换；装好 `go` 再点一次即可补上 |
| 这个目录不是 git 工作树 | `not_a_repo`（状态里 `installed: false`） | tarball 安装的目录没有可快进的远端，按发布包方式更新 |

`apply` 是异步的，所以它总是先回 `202`，上表的拒绝出现在轮询到的 `job.failure`（`reason`/`message`/`items`）里；
`rollback` 是同步的，拒绝直接落到状态码（`local_source_edits`、`diverged`、`no_state`、`moved_since_update`、
`no_binary` → 409，其余 → 400）。其他失败（`merge_failed`、`build_failed`、`swap_failed`）带着 git/go 的原始输出；
编译失败时二进制保持原版本，而源码已移动、状态文件已写好，因此仍然可以回滚。

### 运维者的内容如何被保留

`roles/ skills/ tools/ agents/ bundles/ knowledge_base/ data/ log/ venv/ config.yaml .env` 是**运维者的内容**，
判定在 `update.Protected`。合并前，只有本次更新**确实会写到**的那些路径被复制到
`.update-backup/<时间戳>/` 并从工作树移开（你多出来的、上游没动的目录连碰都不碰），合并后再原样放回，
于是"更新代码、留下我的工作"成立。结果里的 `keptContent` 逐个点名被保留的文件——绝不静默丢弃：
当上游与你在同一个文件上都有改动，落地的是你的版本，而这件事写在结果里。
包（bundle）声明的 MCP 与凭据跟这条路径无关，那由能力表与插件流程负责（见 [capability-platform.md](capability-platform.md)）。

### 回滚

`./cyberstrike-ai -update-rollback` 或页面按钮：`git reset --hard` 回到那次更新前的提交，
并把换二进制时留下的 `cyberstrike-ai.prev` 放回原位，随后删除 `.update-state.json`。
它会拒绝的情形：没有更新记录（`no_state`）、没有留下旧二进制（`no_binary`）、记录里的提交在本仓库不存在
（`bad_state`）、**HEAD 自那次更新之后又动过**（`moved_since_update`：回滚只该撤到更新前，不该顺手抹掉之后的工作）、
以及存在本地源码改动（`local_source_edits`）。

### 重启的两种情形

只有请求显式带 `restart: true` **并且**本次启动装配了重启钩子时，进程才会在优雅 `Shutdown` 之后以 0 退出；
是否真的"再起来"取决于外部守护（systemd、`run.sh`），页面文案也照这个说，不许诺一次可能不会发生的重启。
`restart: true` 而本次启动没有钩子时直接 400，而不是把服务停掉然后声称重启过了。

**待生效状态是持久的、可补重启。** 状态接口对比"启动时记下的二进制身份（大小 + 纳秒 mtime）"与磁盘
现值：更新、回滚或 CLI 换过二进制而没重启时，`needsRestart` 为真、`binaryBuiltAt` 给出构建时间，
控制台顶部出现常驻横幅与「立即重启服务」（`POST /api/system/update/restart`；没有待生效版本时
`409 nothing_pending`、有任务在跑 `409`、没钩子 `400`）。`supervised` 字段来自环境标记（launchd 的
`XPC_SERVICE_NAME`、systemd 的 `INVOCATION_ID`/`JOURNAL_STREAM`），只用来决定勾选框的默认值。

**重启后页面自己回来。** 重启期间控制台换成自恢复视图，每 2 秒探一次状态接口：旧进程还在应答（200）
就继续等；连不上说明正在退出；新进程接客但对旧会话只回 401——这就是"重启已完成"的判据，页面随即
`location.replace` 回 `#system-update` 整页刷新。离开控制台则静默停表，不把已经走开的用户拽回来。

## 路由

路由由 `internal/app` 的**分域注册器**装配：`setupRoutes(routeDeps)` 只负责建组、挂中间件并逐个调用
`routes_<domain>.go` 里的 `register<Domain>Routes`。装配点仍然唯一（`internal/app`），但"所有路由集中在
`app.go` 一个函数里"这条旧约定已被解掉——那个函数曾经 558 行、30 个位置参数，加一个接口要在大函数里找位置。
路由表由 `internal/app/testdata/routes.golden.txt`（292 条）与 `TestRouteTableMatchesGolden` 守住，
所以移动注册不会悄悄改变服务端实际暴露的路径。新增业务接口通常需要：

1. 在 `internal/handler/` 增加 Handler。
2. 增加必要的数据访问：新域放 `internal/store/`（一张表一个主人，handler 侧只依赖消费方自定义接口），
   存量域的扩展才放 `internal/database/`。**HTTP 层不许写裸 SQL**——`TestHandlerRawSQLRatchet` 按
   `h.db`/`m.db` 两个接收者分别设基线，只许降不许升。
3. 在对应域的 `internal/app/routes_<domain>.go` 里注册路由（没有对应文件时才新建一个），
   并让 `go test ./internal/routes -run TestWriteGolden`（需 `CSAI_WRITE_ROUTE_GOLDEN=1`）更新路由基线。
4. 如需对外文档，把 path 加进 `internal/handler/openapi_paths_<分组>.go` 的对应分组。
5. 如需前端调用，更新 `web/static/js/`。

## 数据库

默认 SQLite。新增表或字段时：

- 新域优先落在 `internal/store/`：构造时只要 `*sql.DB`，因此可以脱离 HTTP 层用真 SQLite 测试；
  它不 import `internal/database`/`internal/config`/`internal/handler`，这条边界由 `.go-arch-lint.yml`
  与 `TestStorePackageHoldsNoHTTPConcerns` 双向钉住。
- 将迁移逻辑放到数据库初始化或对应模块迁移函数。
- 保持向后兼容，避免破坏已有 `data/conversations.db`。
- 添加针对迁移和核心查询的单测。

## 新增工具

命令工具优先通过 `tools/*.yaml` 增加，不必改 Go 代码。需要 Go 内置工具时：

- 在合适模块注册 MCP Tool，并在 `internal/capability` 登记策略：内置工具加一行 `policy_builtin.go`，配方工具在 `tools/*.yaml` 写 `capability:` 段（新增配方不需要改 Go 代码）。运行 `make generate` 更新生成目录。未登记策略的工具会被拒绝执行。
- 定义清晰 `InputSchema`。
- 处理超时、错误、审计和 HITL 上下文。
- 避免把高风险操作默认免审批。

工具 YAML 规则见 `tools/README.md`。

## 新增角色

角色通过 `roles/*.yaml` 管理。常见字段包括名称、描述、系统提示词和工具列表。角色应遵循最小工具集原则，不要把所有工具默认交给专用角色。

## 新增子代理

多代理子 Agent 放在 `agents/*.md`。Front matter 示例：

```yaml
---
name: Vulnerability Triage
id: vulnerability-triage
description: 对漏洞线索进行验证、定级和修复建议整理
tools:
  - nmap
  - nuclei
bind_role: 综合漏洞扫描
max_iterations: 200
---
```

正文是系统提示词。主代理可使用固定文件名或 `kind: orchestrator`。

## 新增 Skill

Skill 放在 `skills/<name>/SKILL.md`。用于提供专题能力、流程说明或附属资料。详见 [Skills 指南](skills-guide.md)。

## 前端开发

前端代码按功能拆分在 `web/static/js/`。新增页面或模块时：

- 复用现有 `apiFetch`、modal、通知、i18n 工具。
- 同步更新 `web/static/i18n/zh-CN.json` 和 `en-US.json`。
- 避免把敏感 Key 放到前端。
- 高风险按钮要有确认和清晰状态反馈。

i18n 规范见 [前端国际化方案](frontend-i18n.md)。

## OpenAPI

`internal/handler/openapi.go` 组装内置 OpenAPI 输出；路径数据按域分在
`openapi_paths_chat.go`、`openapi_paths_knowledge.go`、`openapi_paths_capabilities.go`、
`openapi_paths_mcp.go`、`openapi_paths_ops.go`，公共 schema 在 `openapi_components.go`。
分组之间不许出现同名 path（合并器会 panic），整张表由
`TestOpenAPIOperationsGolden` 对着 golden 钉住。新增公开接口后建议同步补：

- path
- method
- summary/description
- requestBody
- responses
- security

这样 `/api-docs` 才能反映最新接口。

## 开发习惯

- 优先保持现有模块边界。
- 大模型、外部 API、文件系统、Shell 相关改动必须考虑超时和错误路径。
- 高风险能力要接入 HITL 或至少有清晰审计。
- 代码变更后运行相关包单测。

## 新增业务模块的完整配方

不要只加一个 Handler。完整模块通常要考虑：

1. 数据模型：是否需要 SQLite 表和迁移。
2. Handler：HTTP 参数、错误码、分页、过滤。
3. Audit：管理动作是否要审计。
4. Monitor：如果会执行长任务，是否要记录执行状态。
5. MCP：是否要暴露给 Agent。
6. HITL：MCP 工具是否有审批边界。
7. OpenAPI：是否更新 `/api/openapi/spec`。
8. Frontend：是否需要 i18n、状态、空态、错误提示。
9. Tests：数据库、handler、边界条件。
10. Docs：配置、使用、排错和安全影响。

少做其中一项，后面通常会以“用户看不懂”“Agent 调错”“接口没人会用”的形式返工。

## Handler 错误设计

建议错误响应保持：

```json
{
  "error": "machine_readable_code",
  "message": "给用户看的说明"
}
```

不要只返回 Go error 字符串。前端需要稳定字段，用户需要可操作建议，日志需要详细错误。

## 长任务设计

扫描、索引、批量任务、C2 等都可能长时间运行。设计时要回答：

- 是否能取消？
- 是否能查询进度？
- 失败后能否重试？
- 结果写在哪里？
- 页面刷新后状态是否还在？
- 是否会阻塞 HTTP 请求？

如果答案是否定的，应考虑接入任务表、事件流或监控模块。

## 测试优先级

最值得补测试的地方：

- 配置热应用。
- HITL 审批分支。
- Shell 超时和无输出。
- 外部 MCP 失败恢复。
- 知识库索引和检索后处理。
- WebShell 编码和系统识别。
- SQLite 迁移兼容。

这些地方比普通 getter/setter 更容易出现真实用户故障。
