# 能力平台：身份、清单与策略管线

适用版本：能力注册表引入之后。调研背景见 `capability-platform-decoupling-research.md`，
完整能力清单是生成物 `docs/zh-CN/capability-catalog.md`（`make generate` 产出，禁止手工编辑）。

## 一、一条不变式

**没有登记的能力不可执行。**

此前非内置工具会落到 `agent:local-execute` 兜底权限上，而 `exec`/`angr`/`pwntools` 这类
把模型提供的字符串直接 `exec()` 的配方正好都在这个兜底里——提示注入即可拿到宿主机代码执行。
现在执行路径上只有一个决策入口：`internal/capability` 的注册表 + 求值器。查不到清单就是拒绝。

## 二、身份

`publisher.capability.name`，全小写，点分。官方内核保留 `core.*` 命名空间。

同一个字符串用于：权限声明、审批记录、撤销清单、生成目录、商店提交。
注册表提供 `CheckTyposquat`，商店在提交时拒绝与既有身份编辑距离 <2 的命名（`cora.nmap` 会被拦）。

工具在 MCP 线上的名字（`nmap`、`c2_task`）不变，身份是新增的一层，不是替代。

## 三、级别与审批下限

| class | 含义 | 审批 | 权限 |
|---|---|---|---|
| `readonly` | 观测状态，不改变任何东西 | 可豁免 | 各自的 `:read` |
| `mutating` | 改本地状态或对目标做非破坏性探测 | 会话 HITL 决定 | 各自的 `:write` / `agent:local-execute` |
| `destructive` | 执行模型提供的代码、生成 payload、向 implant 下发任务、删数据 | **必须逐次人工授权，白名单无法豁免** | 专用权限（配方类为 `agent:destructive-execute`） |

`destructive` 不允许使用 `agent:local-execute` 兜底权限——这是**加载期**就会失败的检查，
不是运行期约定，因此社区制品无法通过"复用现成权限"来提权。

审批下限由 `capability.ApprovalLedger` 释放：**单次使用、默认 60 秒过期、按 (会话, 能力身份) 绑定**。
人批准一次调用只放行那一次；下一次调用必须重新审批。写这个账本的唯一入口是 HITL 审批通过时
(`handler.TrackApprovedHitlExecution`)，请求体、角色、skill、商店制品都写不进去。

## 四、免审批白名单的新语义

交集，不再是并集：一个工具必须**同时**出现在

1. `config.yaml` 的 `hitl.tool_whitelist`（运维者所有，写它需要 `config:write`，即管理员），且
2. 会话侧提交的工具集

才享受豁免。会话请求体现在只能**缩小**豁免集，不能扩大。
`destructive` 能力不参与豁免判定，两个名单都写了它照样要人工授权。

## 五、给工具配方加清单（新增一个工具：0 个 Go 文件）

`tools/*.yaml` 增加 `capability:` 段：

```yaml
capability:
  id: "core.nmap"                 # 或 <publisher>.<name>
  version: "1.0.0"
  class: "mutating"               # readonly | mutating | destructive
  permission: "agent:local-execute"
  approval: "inherited"           # never(仅 readonly) | inherited | always
  runtime: "recipe:exec"
  grants:                         # 中介能力上限，不是请求权；商店永远不能拓宽它
    - "process.exec(nmap)"
    - "net.connect(target)"
  evidence: false
  timeout_seconds: 0
```

字段约束（不满足就加载期拒绝，工具保持可见但不可执行）：

- `class: destructive` ⇒ `approval` 不能是 `never`，`permission` 不能是 `agent:local-execute`。
- `class: readonly` ⇒ 不能 `approval: always`。
- `runtime: recipe:exec` ⇒ 至少一个 `grants`。
- `id` 必须是合法的小写点分身份。

内置 Go 工具在 `internal/capability/policy_builtin.go` 增加一条表项即可；名字常量仍由
`internal/mcp/builtin/constants.go` 定义，二者一致性由 `TestDeclaredConstantNamesHavePolicies`
（直接解析 constants.go 的字符串字面量）强制，防止再长出手工副本。

## 六、一份清单驱动的下游产物

`make generate` 从策略表 + 配方清单生成：

- `web/static/js/generated/capability-catalog.js` — `window.CSAI.capabilities` /
  `capabilityNames` / `capabilityByName`（取代前端手工镜像的工具名枚举）
- `web/static/js/generated/capability-catalog.json` — 同一份数据的机器可读形式
- `internal/capability/testdata/catalog.golden.json` — 漂移基线
- `docs/zh-CN/capability-catalog.md` — 审核用人可读清单

`TestGeneratedCatalogIsUpToDate` 与 CI 的 regenerate-and-diff 会在清单变了但生成物没变时失败。

参数 JSON Schema 也从同一份 `parameters:` 生成（`capability.JSONSchema`），用于
`capability.ValidateArgs`：缺必填参数得到的是参数错误，不是权限错误。

## 七、装配点

只有一个。`internal/app.InstallCapabilityRegistry` 在服务启动时装配；
`cmd/mcp-stdio` 必须调用 `app.InstallStdioPolicy` 复用同一条管线，身份来自 `config.yaml` 的
`mcp_stdio` 段。stdio 没有人工审批通道，因此 destructive 能力在该进程内不可执行；
未声明 `mcp_stdio.permissions` 时该进程拒绝一切工具调用（fail-closed 默认）。

`mcp.Server.SetRequestContextDecorator` 是入口注入身份的机制，避免为每个入口写一份装配代码。

## 八、插件运行时

`internal/pluginhost` 是能力清单里 `runtime: plugin-host:*` 的执行去处。

- ABI：换行分帧的 JSON-RPC，版本常量 `csai-plugin/1`。主机调用 `initialize` /
  `capabilities/list` / `capabilities/invoke` / `shutdown`；插件只能回调
  `host/grant_check` / `host/log` / `host/progress`，其余一律 method-not-found。
  样例见 `internal/pluginhost/testdata/refplugin`。
- 隔离：一个信任域（=发布者命名空间）一个子进程，懒启动、崩溃后下次调用自动重启、空闲回收；
  子进程接入 `processguard` 的 cgroup/rlimit 约束。
- 凭据：环境按白名单继承，含 `KEY`/`TOKEN`/`SECRET`/`PASSWORD` 的键名直接拒绝转发。
- 出网：子进程只拿到主机侧 CONNECT 代理地址；实际放行集 =
  **清单 `grants` ∩ 运维者批准的 `(host, ports, method, valid_minutes)` 元组**，
  批准集为空时全部拒绝。商店或插件自身无法拓宽这个集合。
- 未配置即拒绝：`plugin_host.enabled: false` 时声明插件运行时的能力在执行处报错，
  **不会退回进程内跑配方命令**。

边界说明（重要）：代理约束的是"走代理的流量"。Go 标准库故意不代理 loopback，裸 socket 也能绕过，
所以这不是 OS 级网络隔离。要做硬边界需要每实例 netns/seccomp，`processguard` 目前不提供。
本仓库也不内置 Python 运行时——`plugin-host:python` 需要外部提供解释器与插件二进制。

配置面见 `config.example.yaml` 的 `plugin_host` 段。

## 九、制品信任、撤销与能力增量门

`internal/artifact` 实现商店的判定与客户端强制部分（registry 服务端本身还没有）：

- **签名覆盖全部安全字段**：`Manifest.CanonicalBytes()` 含 id/version/class/permission/
  approval/runtime/grants/payload 摘要/publisher。审批通过后改级别或加能力，签名即失效。
- **没有 `ignoreUnverified`**：未签名、未知 publisher、密钥与发布者不符一律拒绝。
  可执行制品必须可读（`File.Text`），刻意不抄参考产品的插件加密——碰运维者凭据的代码不可读就没法审。
- **撤销在两个时刻生效**：启动时装载 + 每次调用前求值阶段复查；命中即从注册表隔离（模型连名字都看不见），
  发布者级撤销覆盖该发布者全部制品。`Merge` 永不解除撤销；清单损坏报错而非静默当空。
- **provenance 由安装器落账**，配方 YAML 里没有可填 digest 的字段；台账独立于注册表存储，
  所以 config「应用」重建 recipe 层不会丢掉撤销匹配依据。
- **能力增量门**：与已批准版本比对，`新增 grants / 级别上调 / 权限变更` → 必须第二名独立审核人复核
  且作者不得自审；一致清单的重打包只做自动重扫；能力收窄不触发重审。
- **静态扫描**拦截：指令覆盖、数据外传请求、私钥/token 痕迹、`curl|sh`、
  `` !`cmd` `` 动态上下文、role 标签走私、控制字符。
- **`SanitizeForIndex`** 在入嵌前剥离 URL/IP/CID。
- **隔离区**移走但保留可见（供复盘），不是删除。

## 十、内容特权层级（社区知识 / persona / 提示词）

`internal/contentpolicy` 落实调研 §6 的代码层控制。理由很简单：RAG 结果会进入一个能操作
真实 C2 与 WebShell 的 agent 上下文，而公开结果显示少量投毒文档即可显著提高成功率。
所以这不是 prompt 里的恳求，是结构：

- **标签**：`[[csai:untrusted-advisory]]`，随固定前导声明一起写入围栏。
- **唯一带标签的出口**：`RetrievalResult.AdvisoryContent()` 是 chunk 文本进入模型视野的唯一路径，
  MCP 工具结果与 Eino retriever 两条出口都走它；源码扫描测试禁止任何未围栏写入。围栏幂等。
- **装配点守卫**：`newEinoAgenticChatModelAgent` 对 `Instruction` 调用 `GuardDecisionPath`，
  带标签的内容进入运维者控制通道即**拒绝构建 agent**。
- **渲染期不执行**：`` !`cmd` ``、`![x](url)`、role 标签、控制字符、块终止符一律剥离。
- **投毒挡在入库**：`RefuseIngest` 在知识库写入前拦截指令形态文本。
- **入嵌剥离网络指标**：`StripForIndex` 移除 URL/IP/CID。

边界取舍：角色、skill、markdown agent 文本按**已审核、已安装、运维者知情同意**的配置处理，
不当作运行时不可信注入面；社区知识文本才是无界注入面。双模型（CaMeL）式控制流/数据流分离未实现。

## 十一、Provider 方言层

`internal/provider` 是模型厂商的唯一决策数据。**dialect 不等于 vendor**：线格式只有三种族
（`openai-chat` / `openai-responses` / `anthropic-messages`），厂商是方言表里的行。

- 每个 `Dialect` 带 `API / BaseURL / DefaultBaseURL / ContextWindow / Cost / Capabilities / Retry / OverflowMarkers / AliasesTo`。
- `Resolve(vendor, baseURL)` 对未知名字退化到 chat 方言——部署时写个新 gateway 名不该让服务起不来。
- 调用点不再比较 provider 字符串：`AgenticBackendSupported`、`IsAnthropicMessagesVendor`、
  `EffectiveProviderName`、`DefaultBaseURLFor`、`ListsModels`、`ClassifyError(status, body)`。
- **加一个厂商 = 往表里加一行**（`Catalog.Register`），不动任何调用点。
- 一致性测试套件是这一层的验收判据，也是判断"两对孪生实现能否安全合并"的唯一客观依据：
  abort / context-overflow / tool-call-without-result / unicode 代理对 / 跨厂商交接五个场景
  对方言表里**每一行**都跑一遍。

rerank 的 provider 名字是**另一个命名空间**，不要塞进模型方言表（`config.go` 里已注明）。

## 十二、能力单元表与热插拔

角色 / skill / markdown agent / 工具配方 / MCP 声明 / 插件二进制这六类可扩展的东西，过去各有各的生命周期，
而且**没有一类能在不重启的情况下改变**。现在它们共用一套身份与一张活表：

- `internal/plugin`：`Unit`（身份 `<kind>/<name>` + 源路径 + 安装期摘要）与 `Bundle`
  （一次装、一次摘的一组单元）。读侧是**原子指针换出的不可变快照**（无锁读），写侧一把锁串行化。
- 冲突一律"拒绝并指名道姓"（`*ErrConflict`），不覆盖：包不能顶掉内置能力，目录扫描也不能顶掉
  已安装的包；同 id 再装是升级（只回收自己上一版声明的单元）；卸载只摘表、**不删文件**。
- `bundles/<id>/bundle.yaml` 是**按角色打包**的形状（角色 + 子代理 + 技能 + 工具），路径被
  `skillpackage.SafeRelPath` 关在包目录内，`version` 强制（没有版本就没有升级与回滚）。
  格式与冲突规则见 `bundles/README.md`；随仓库提供四个按角色打包的示例包 ——
  `mobile-app-security`（移动端）、`ai-app-redteam`（AI 应用红队）、
  `source-code-audit`（源码与供应链审计，含一份 semgrep 配方）、`wireless-hardware`（无线与硬件）。
  每个包的每一项交付都由 `TestExampleBundlesInstallAlongsideShippedCapabilities` 拿**既有加载器**验一遍
  （角色 yaml、markdown agent、配方的能力清单），而不是只跟能力表自比。
- `plugin` 是**唯一携带可执行代码**的单元类型：声明文件里的 `capabilities` 是人审过的入口清单，
  启用时宿主启动二进制并调 `capabilities/list` **双向核对**，一致才把能力登记进 `LayerPlugin`；
  `class/permission/grants` 只来自声明文件，绝不来自插件的自我描述。目录形状与全部规则见 `bundles/README.md`。
- 包里插件的能力**会出现在 MCP 工具面上**：它没有配方，所以每次重建由能力表补上，且排在内置注册之后
  （同名保留内置）；停用单元既失去能力身份也失去工具。反过来，启动过程把包里 `plugin` 单元一律置为
  「已声明、未启用」，持久化开关只重放「停用」方向——`internal/app/boot_plugins_test.go` 与
  `make wiring-check` 的 AST 断言钉住这两半。
- **内置能力也走同一张表**：`roles/ agents/ skills/ tools/ 由 ScanDir 扫成单元，身份与既有加载器
  逐项一致（实测 142 个：roles 13 / agents 16 / skills 23 / tools 90），由
  `internal/app/plugin_parity_test.go` 钉住——真相源是既有加载器本身，不是手写清单。
- 热插拔的安全性是**证出来的**：把快照换成原地写之后 `TestConcurrentReadersNeverTear` 在 `-race`
  下当场报出写与迭代的竞争；这正是角色 API 过去做的事（GET 请求里也会原地 `make` 那张 map）。
- 角色一侧已经接通到运行路径：写 = 「写文件 → 进表 → 发布新快照」，读 = `currentRoles(h.config)`
  （`internal/handler/live_config.go`），装配漏装活配置快照会让 `make wiring-check` 变红——
  而这个漏接**编译得过、启用路径测试也全绿**，所以它必须是门禁。
- 一键安装的接口与**页面**都在了：`GET /api/plugins`（含每单元的 `served`、`generation`、`drift`）、
  `GET /api/plugins/available`（可安装清单，前端「一键」要有东西可点），
  前端「平台管理 → 能力包」页把安装/卸载/启停都在同一张表上做完成；
  `POST /api/plugins/install`、`DELETE /api/plugins/bundles/{id}`、
  `POST /api/plugins/units/{kind}/{name}/enabled`、`DELETE /api/plugins/units/{kind}/{name}`。
  安装只允许从 `<configDir>/bundles` 里挑（`../`、绝对路径一律 400）；单元身份含斜杠，
  所以路由拆成 `:kind/:name` 两段（单段会被 gin 在匹配前解掉转义而命中不到）。
  每次变更都会顺带重发角色目录，装完下一个请求就生效。
  装过的包会在**下次启动时重新装入能力表**（`installBundlesFromDisk`，先扫内置再装包，
  所以冒充内置身份的包仍按身份被拒），带配方的包在启动尾段随工具层一起重建；
  否则「安装」就只等于「本次进程内有效」。这条是重启一次实测出来的，不是推演出来的。
- MCP 一侧补的是**身份**而不是热增删（远端服务器的增删启停本来就是热的）：
  `ExternalMCPManager` 把每台服务器的真实工具清单交给 `internal/app/remote_capabilities.go`，
  后者在 `capability.LayerRemote` 里**按服务器成组**登记/替换/摘除
  （身份 `remote.<server>.<tool>`，Name 就是执行器看到的线名 `<server>::<tool>`）。
  权限与 global-scope 下限沿用原来那条命名空间策略，所以这一步不改变"谁能调什么"，
  只是让单个远端工具变成规则、审批与审计**点得名**的对象；测试同时钉住"某工具被单独改权限时
  同服务器兄弟工具不受影响"与"清单没到位时回到命名空间策略（登记在册的策略，不是认不全就放过）"。
- **服务器声明本身也进了表**（`mcp/<name>` 单元，`bundles/<id>/mcp/*.yaml`）：装包写进的就是
  `/api/external-mcp/*` 驱动的那同一个活管理器，所以不存在"两份服务器清单"。四条规则：
  装包与冷启动**只声明不启动**（一律以停用状态进表进管理器，进程由单元开关拉起——包文件里的
  `enabled: true` 是包作者的意图，不是运维者启动进程的同意）；包**不能覆盖** `config.yaml` 里
  已有的同名服务器（装包前查活管理器，撞名 409 点名那台；`LoadConfigs`/应用配置 遇同名**文件优先**，
  包失去这个活动槽位）；反向也不通——MCP 页对包拥有的名字一律 409 并指出包 ID，因为这四个端点
  只认名字，`启动` 会去读文件里根本不存在的那条然后把空值存回去，把一台能用的服务器换成永远连不上
  的空声明；包声明**不做 `${VAR}` 展开**，否则一条 `Authorization: "Bearer ${CSAI_LLM_API_KEY}"`
  就把凭据送到包作者选的服务器上了。`configs` 由文件重建后再叠加包那一份，所以"应用配置"不会清空
  包声明；卸载只摘自己声明过的服务器（`PackOwner` 不指向本包就跳过并在响应里说明是谁的）。
  单元开关会落库（`capability_unit_switches`，只落"停用"这个收窄方向：保存的 on 绝不能打开文件
  已禁用的单元，与工具层 `文件 enabled ∧ 表 enabled` 同一条规则；行里的源路径对不上就当过期清掉，
  卸载与摘除也会指名清理，否则后来同名能力会继承别人关掉的开关），启动时在包重新装入**之后**重新
  套上，并按需要重发角色目录、重建工具层。MCP 这一类**不接受持久化**：每次启动都回到停用，所以那
  一次开关的响应写 `switch_persisted:false` 并指路 `config.yaml`。
- `served:false` 只说真话：六类 kind 的运行路径都要么读表、要么被明确判为「按开关供给」，所以 `servedKinds` 与 `plugin.Kinds`
  同规模，`TestEveryKindReportsItsActualServedState` 双向钉（少一类就红，将来加一类没接线也红）。
  mcp 这一类另有第二个条件：表里声明过还不够，活管理器得**仍然持有**这条声明——`config.yaml`
  同名接管后控制台会把该单元标成未服务并写明"配置文件里有同名服务器"，而不是继续报"已服务"。
  让"装好了"读起来像"能用了"就是这一层存在的理由的反面。
  这个字段本身也会腐烂：agent 的运行路径已经搬到表上之后，真实服务仍报
  `[('role', True), ('agent', False), ('skill', True)]`，而测试抓不到——夹具包里没有一个
  agent 单元。现在 `reporting-pack` 带 `agents/report-analyst.md`，安装后断言 `served=true`，
  卸载后断言该单元离开表；"在"先于"不在"断言，否则后半句可能一直空过。
- skill 一侧同样接通了，并且是**换了实现**才接通的：Eino 自带的 backend 只接受一个 `BaseDir`，
  所以能力包里的 skill 结构上不可能被读到。`internal/einoskill` 改为用能力表实现那个两方法接口
  （不建符号链接、不复制别人的文件），`internal/multiagent` 在有表时优先用它。
  替换厂商实现的风险由 **拿厂商当真相源** 的比对测试挡住：`TestBackendMatchesEinoBackend`
  对内置 23 个 skill 逐个比对 front matter、正文与 base directory；另有一条
  `TestTabInBodyIsNotStripped` 挡住"照抄厂商的 stripLineNumbers"——那是因为它们的
  local backend 会给每行加 `N\t` 前缀，直接读磁盘再照抄会把正文里真实的制表符前截断。
  同一轮把**管理台**也接上了表：`GET /api/skills`、详情与文件读写都按表解析目录，
  写到一个由包提供的 skill 上返回 409 并指名归属，而不是在内置目录里落一份同名副本。
  只改运行路径会留下"包里的 skill 被 Agent 用着、列表看不见"——这条是在真实跑起来的服务上
  实测到的（23 → 装包 24 → 卸载 23），不是推演出来的。
  markdown agent 同样接上了：`agents.LoadMarkdownAgentPaths` 与目录扫描共用同一个解析器
  （对内置 16 个 .md 逐个比对，路径驱动与目录驱动结果**逐字节相同**），运行路径与管理台都走它；
  建/删 agent 会同步进表，包提供的定义可读而不可改（409 指名）。
  一个附带好处：**包不可能塞进第二个 orchestrator.md** —— 内置扫描已占用 `agent/orchestrator`
  这个身份，安装时就被 409 拒掉，而不是等到加载时把"主代理只能有一个"变成每次运行的失败。
  tool 配方是最后一个"读目录而不是读表"的运行路径，现在也接上了：`ToolLayer.Rebuild()` 是配方清单
  唯一的重建入口，做的是 apply 原本做的同一套三步（按表读路径→重建 recipe 能力层→`ClearTools`
  后重注册整个工具面），一键安装/卸载/启停只在**涉及 tool 单元**时触发它。
  两条规则容易说不清就用测试钉住：运行期状态是 `文件 enabled ∧ 表 enabled`（表单元的默认 true
  若当覆盖值，会悄悄打开 `enabled: false` 的内置配方），而 `PUT /config` 那条把 `enabled` 落回
  各配方 yaml 的循环必须跳过"由表停用"的工具，否则一次运行期停用会被固化成永久。
  对内置 90 个配方逐条比对表驱动与目录驱动的结果（名字、顺序、启用位全等）。
  启动扫描漏掉 `KindTool` 的失败模式不是报错而是"装任何一个包就把整批内置配方换成包里那一个"，
  所以 `TestBuiltInCapabilityScanCoversEveryServedKind` 同时要求：`plugin.Kinds` 里每个 kind
  要么被扫描、要么在本测试里写明豁免理由。差距逐行见 `bundles/README.md` 的表。

## 十三、更新这套安装自己

让这台机器"变得不一样"的入口有两个，它们不是一回事，边界写在实现里：

- **能力包**（`plugins:install`）改的是**能力面**：装进 `bundles/` 的单元写进能力表，包声明的 MCP 只登记不启动。
- **一键更新**（`update:apply`）改的是**代码**：把这个目录自己跟踪的远端快进 → `go build` → 原子换二进制
  （旧的留作 `cyberstrike-ai.prev`），远端按 `mine → origin → upstream` 在本地已有的 remote 里取，
  没有任何写死的第三方仓库地址。

两条线不交叉，这一点是设计上要求的：一键更新不做能力表的事，也不会因为"某个文件是包声明过的"就跳过保护——
它只按 protected 清单（`roles/ skills/ tools/ agents/ bundles/ knowledge_base/ data/ log/ venv/
config.yaml .env`）办事，先把本次真会写到的路径暂存进 `.update-backup/<时间戳>/`，合并后原样放回，
`keptContent` 逐个点名，绝不静默丢弃；能力表所在的 `data/` 因此在更新后原样保留，装过的包不会因为更新代码而消失。
反过来，能力包也从不参与"从哪里取代码"。
拒绝语义（本地源码改动、分支分叉、无 Go 工具链、非 git 工作树）与三种入口（页面 / REST / CLI）、
回滚与重启的两种情形，见 [开发者指南](developer-guide.md) 的「一键更新」一节。

## 十四、尚未实现（下一阶段）

- P6 剩余：数据层按域切 Store（`internal/store` 已有 `NotificationReads`、`HITL`、`Session`
  与共享的会话可见性子句；`Session` 现也拥有 `messages` 的内容写回与两条 CASE 追加，
  它们原本是**同一条 UPDATE 抄在 6 个文件的 15 处**加上一对只差一个分支的孪生语句；
  通知摘要里另外两个域（漏洞最近条目、执行失败条目）也已进 `store.Vulnerability` 与 `store.Execution`，
  **handler 裸 SQL 归零**（起点 49），
  且 HITL/会话/通知已读三张表已由全仓归属测试钉住唯一写入者。
  **窄接口已不只是契约，而是这一层的硬不变量**：18 个域的存储字段类型换成了自己的消费者接口
  （`AgentStore`/`AssetStore`/`AttackChainStore`/`AuditStore`/`BatchTaskStore`/`ChatUploadsStore`/
  `ConfigStore`/`ConversationStore`/`MonitorStore`/`NotificationStore`/`OpenAPIStore`/`ProjectStore`/
  `RBACStore`/`RobotStore`/`SkillsStore`/`VulnerabilityStore`/`WebShellStore`/`WorkflowStore`），
  另有 1 个从没被读过的死字段直接删除（`KnowledgeHandler.db` 连构造参数一起删）。
  `internal/handler` 里 `*database.DB` 结构体字段 **19 → 0**，判据已从"只许降"翻成
  硬零门禁 `TestHandlerLayerHoldsNoGodObject`（配"扫到 990 个字段"的反空跑下限）+
  形状门禁 `TestNarrowedFieldsAreOnlyAssignedThroughNarrow`，都在 `make layering-check`。
  构造一律走 `database.Narrow`——它把 nil 指针映射成 nil 接口，
  否则 `var store AssetStore = (*DB)(nil)` 是**非 nil 接口**，传输层 64 处 `if h.db == nil`
  的降级分支会永久走错，而这一切编译通过、启用路径测试全绿；这条陷阱不是论证出来的，
  是我第一版正则漏掉 5 处对齐赋值后由 `TestRobotModeRejectsUnavailableMultiAgent` 直接 panic 抓出来的。
  最后 3 个域是这样打通的：`multiagent` 自己一个 DB 方法都不调、只把句柄转发给 `internal/project`，
  所以接口声明在链条另一端（`database.ProjectFactStore` / `ToolExecutionLedger` / `AttackChainLedger`，
  消费包用类型别名指回去，避免把 13 个签名抄三遍；这些面里刻意不放 `Close`））、
  `AgentHandler` 分解（**水位已实测并进门禁，且已落下两刀**：起点 130 个方法/23 个文件 →
  中断队列读面 9 个方法进 `HITLQueue`、收尾链路 10 个方法进 `runFinalizer`，现 **112 个方法/21 个文件**；
  `internal/handler` 整包 64 个 `Set*` 注入方法分布在 21 个接收者类型上，其中 `SetAudit` 有
  18 份逐字相同的副本——报告原记的"26 处 SetXxx/19 个文件"是低估。已落地：三条只降门禁
  （`make layering-check`）+ 一处内聚塌陷（三个 HITL 配置保存器收成一个构件）+
  一道**审计注入完整性门禁**（19 处注入统一走 `bindAudit`，
  `TestEveryAuditableHandlerIsAuditBound` 用 AST 从 handler 包取真相源，
  新 handler 带 `SetAudit` 而未被绑定会直接红——漏注入的后果是端点照常服务但不写任何审计记录））、
  session 事件溯源
  （provider 方言目录已可生成并逐字节受门禁约束：`internal/provider/publish.go` 渲染，
  `docs/zh-CN/provider-catalog.md` + `internal/provider/testdata/provider-catalog.golden.json` 为产物）、
  Eino 收口至 ≤1 包（**已进门禁并在收**：`internal/layering` 对适配包之外的每个包钉基线、
  新引入包直接硬失败，`make layering-check` 进 CI；总引入包 11→6，
  适配边界之外的债面 8 包/100 文件 → 3 包/96 文件，Eino 生产文件总数 104 → 105——
  前几片是"代码搬进适配层、总数不变"；那 +1 是 `internal/einoskill`（见第十二节），
  它换掉的是 Eino skill backend 只能读一个 BaseDir 这条**能力上限**，不是搬家；债面没有退化——
  `internal/vision`、`internal/handler`、`internal/reasoning`、`internal/security` 等片已搬进适配层，
  剩下的 `multiagent`/`knowledge`/`workflow` 是 Eino 编排的宿主，要把它们抽干净需要接口化而非搬家）、
  20 对孪生函数合并（判据已有=方言一致性套件）。
- P3 剩余：前端逐文件 ES 模块（6 个巨型脚本仍各自复制一份工具函数，去重 2,800–3,200 行）；
  持久化 tier 的精确集合暂不含"经进度回调写入"的行（值图按声明过的函数名记参数位，回调是变量），
  因此"服务端可写而历史不渲染"这一方向可能少报，要收紧需让值图认识回调变量持有函数体；`POST /api/terminal/run/stream` 在前端**没有任何消费者**
  （终端面板走 WebSocket），是否保留这个端点需要决策。
  已完成：帧级 `type`、进度回调内层事件名（63 个，其中 49 个只能从回调调用点证明）、
  C2 的 `category` 三套契约归一到 `internal/sse`（手拼帧 0），生成的枚举已由 `index.html` 加载、
  `monitor.js` 逐帧调用 `CSAI.isSSEEvent`，两侧差集用只许降的 ratchet 钉住（页面漏渲染 2、页面死分支 1）。
- registry 服务端：签名发布、灰度、release-age 冷却、自助刷新。
- 气隙离线包的导出/导入命令；沙箱引爆（第 2 道门）的自动化执行——目前只记录结果并作为放行前置。
- `grants` 的 netns/seccomp 硬强制；内嵌 CPython 分发。
- 角色/skill/markdown agent 文本是否也按运行时不可信处理（当前按已批准配置处理，见第十节）。
- CaMeL 式双模型控制流分离（仍是论文级）。
