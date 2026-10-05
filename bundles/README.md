# bundles/ — 按角色打包的能力包

一个 bundle 就是"一个角色所需的一切"：角色定义、它的子代理、它的技能、可选的工具配方与 MCP 声明，
放在同一个目录里，一次安装、一次卸载。实现在 `internal/plugin`。

## 目录形状

```
bundles/<id>/
  bundle.yaml                    # 清单（必需）
  roles/<name>.yaml              # kind: role
  agents/<name>.md               # kind: agent
  skills/<name>/SKILL.md         # kind: skill（目录，必须含 SKILL.md）
  tools/<name>.yaml              # kind: tool
  mcp/<name>.yaml                # kind: mcp
  plugins/<name>.yaml            # kind: plugin（唯一携带可执行代码的一类）
  bin/<name>                     # 插件二进制本体，必须留在包目录内
```

## 清单

```yaml
id: mobile-app-security          # 必需，不含路径分隔符
name: 移动端安全测试角色包
version: 1.0.0                   # 必需：没有版本就无法升级或回滚
description: ...
units:
  - kind: role                   # role | agent | skill | tool | mcp | plugin，仅此六类
    path: roles/移动端安全测试.yaml   # 相对本目录；不允许 `..`，不允许绝对路径
    name: 移动端安全测试            # 可省略：role/tool/agent 取去扩展名的文件名，skill 取目录名
```

清单里没有、也不允许有 `dest` 之类的目标路径：能力落在哪儿由 `kind` 决定，
所以一个包不可能通过写清单去覆盖它管不着的文件。

## 随仓库提供的角色包

四个包对应四个内置角色目录里**没有**的角色。装任何一个都只加东西，不改内置文件：

| 包 | 角色 | 交付的单元 |
|---|---|---|
| `mobile-app-security` | 移动端安全测试 | role + agent `mobile-app-analyst` + skill `mobile-package-triage` |
| `ai-app-redteam` | AI 应用红队测试 | role + agent `llm-tool-surface` + skill `llm-output-boundaries` |
| `source-code-audit` | 源码与供应链审计 | role + agent `sast-finding-triage` + skill `sink-driven-audit` + tool `semgrep` |
| `wireless-hardware` | 无线与硬件安全测试 | role + agent `firmware-triage` + skill `rf-protocol-recon` |

`source-code-audit` 带一份真配方，是为了让"表驱动的工具面"这条路径有**随仓库发布的内容**在跑，
而不只在测试夹具里成立。它的 `capability.id` 用的是 `community.semgrep` 而不是保留的
`core.*` 命名空间 —— 包不能声称自己是内置能力。

每个包里的每一项都会被既有加载器读回来验一遍：角色走 `config.LoadRoleFromFile`、
agent 走 `agents.LoadMarkdownAgentPaths`、配方走 `RecipeSpecs`（缺能力清单就是内容 bug，
不该等到执行时才 fail-closed）。见 `internal/app/bundles_test.go`。

## 身份与冲突

单元身份是 `<kind>/<name>`，全局唯一。安装时的规则是**拒绝并指名道姓**，不是覆盖：

- 包 A 要装 `role/CTF`，而 `roles/CTF.yaml`（内置目录扫出来的）已经在表里 → 拒绝。
  先卸载/停用内置那条，包才能顶上；反方向（目录扫描覆盖已安装的包）同样拒绝。
- 同一个 `id` 再装一次是**升级**：这个包自己上一版声明、这一版没声明的单元会消失，
  别的包的单元一个都不动。
- 卸载只把单元从表里摘掉，**不删任何文件**（源文件本来就在 `bundles/<id>/` 里）。

每条规则都有测试，`internal/plugin/table_test.go`。

## 生效时机

装完即生效，不需要重启：读侧拿到的是一份不可变快照（原子指针切换），
写侧只有一把串行化的锁。并发的读与换不会互相撕开——这条性质不是论证出来的，
是把快照改成原地写之后 `TestConcurrentReadersNeverTear` 在 `-race` 下当场报出来的。

"不需要重启"还得不等于"活不到下次重启"。能力表是在启动时从磁盘重建的，所以
`<configDir>/bundles` 里的包必须在启动过程中被**重新装入**（`installBundlesFromDisk`），
否则一个人装过的东西会在下一次重启后凭空消失——那已经不是"安装"而是"本次会话"。
装入顺序是有条件的：**先把内置能力扫进表，再装包**，这样一个想冒充内置角色的包会被按身份
拒掉（与安装接口同样的拒绝规则），而不是因为它先加载就抢走了内置的身份。
带配方的包还要多一步：所有 registrar 接好之后按表重建一次工具层，
否则包里的配方在表里、却不在 MCP 工具面上，要等谁按一下"应用配置"才出现。

真机验过（两次启动，中间只 kill 进程）：
第一次冷启动 `roles/agents/skills/tools = 17/20/27/139`（四个包都在表里且被服务）→
重启后不做任何安装调用仍是 `17/20/27/139`，`semgrep` 还在工具清单里，
包提供的 agent 依然以 `read_only` 出现在管理台。
门禁：`TestBundlesOnDiskAreReinstalledAtBoot`（含冒充包被拒、损坏包被报出、
运行路径真的看得见）与 `make wiring-check` 里对 `installBundlesFromDisk` 的 AST 断言。

各 kind 离"装完就被服务"还差多远，逐个说清（不写"已全部插件化"这种话）：

| kind | 单元进表 | 运行路径读表 | 一键安装 API |
|---|---|---|---|
| role | ✅ 启动扫描 + 包 | ✅ `currentRoles` → 活配置快照（`internal/handler/live_config.go`） | ✅ |
| skill | ✅ | ✅ `internal/einoskill` 用能力表实现 Eino 的 `skill.Backend` | ✅ |
| agent | ✅ | ✅ 运行路径与管理台都走表（`agents.LoadMarkdownAgents`） | ✅ |
| tool | ✅ | ✅ 配方清单由表驱动重建（`ToolLayer.Rebuild`，与 `POST /config/apply` 同一条序列） | ✅ 装完即重建 |
| mcp | ✅ 包声明的服务器写进**活的** `ExternalMCPManager`（与 `/api/external-mcp/*` 同一个对象）；每个远端工具另有身份（`LayerRemote`，按服务器成组装卸） | ✅ 授权按工具身份判定，判定不到再回到命名空间策略 | ✅ 装完只写声明，**不启动进程** |
| plugin | ✅ 声明 + 二进制进包；开关时把信任域写进**活的** `pluginhost.Service`，能力进 `LayerPlugin`（按单元成组装卸） | ✅ 能力身份 runtime 为 `plugin-host:abi`，执行经 `capabilities/invoke` 走进程外 | ✅ 装完**不声明、不启动**；开关才声明并核对，核对不过就回滚开关 |

## 包声明的插件（可执行代码）

`plugin` 是六类里唯一带二进制的一类，也是唯一"装完还需要核对"的一类。声明文件
`plugins/<name>.yaml` 里的 `capabilities` 是**已经被人审过的入口清单**：

```yaml
pluginId: acme               # 可省略，默认取单元名；三处必须同名：单元名 == 发布者 == 信任域
binary: bin/acme-scan        # 相对包目录；必须存在、可执行、解析软链后仍在包内
args: ["--mcp"]
envAllow: [TZ]               # 子进程只拿到这些环境变量；含 KEY/TOKEN/SECRET/PASSWORD 的键名被拒绝
grants: ["net.connect(10.0.0.0/8)"]   # 中介副作用上限，写在包里而不是由插件自己申报
callTimeoutSeconds: 30
idleTimeoutSeconds: 300
maxRestarts: 3
version: 1.0.0
capabilities:
  - id: acme.scan            # 必须是 <pluginId>.<名字>，否则调用会路由到别的信任域
    title: 端口扫描
    description: ...
    class: mutating          # readonly | mutating | destructive
    permission: agent:acme.scan   # 非只读必须声明：规则要点名它，撤销要点名它
    approval: always         # inherited | always（非只读可强制）| never（仅只读）
    timeoutSeconds: 60
    paramsSchema:            # 直接就是参数的 JSON Schema
      type: object
```

开关拨到"启用"时，宿主启动这个二进制、调 `capabilities/list` 问它到底提供什么，然后**双向核对**：

- 清单里有、插件没报 → 该入口会在调用时失败，整包能力不登记；
- 插件报了、清单里没有 → 这是没人审阅过的入口，它的 class/permission/grants 无从谈起，同样拒绝；
- 两种失败都会**收回刚声明的信任域并把开关退回停用**，回复里指名道姓差在哪。

也就是说：`class`、`permission`、`grants` 只来自这份声明文件（人写的、跟着包一起签的），
**永远不来自插件的自我描述**——能自我描述的组件就能把自己描述成 `destructive`。

**指纹盖住二进制**：`plugin` 单元的 digest 不是声明文件那一份，而是「声明 + 它点名的可执行文件」
合成的一份（`plugin.DigestPaths`，按文件名+字节共同 framing，换文件/换名字都算改动）。
安装期取一次，`Drifted()` 每次按同样算法重算——所以"装完之后有人把包里的可执行文件换了"
会被报成漂移，而不是继续被当成没动过的单元执行下去。其他 kind 一个文件就是全部，走原来的单文件路径。

**能按构建撤销**：登记进能力表的每个插件能力都带 `Publisher`（发布者）与 `ArtifactDigest`
（**只含二进制本身**的摘要——撤销针对的是一次构建，改一行说明文字不该让黑名单条目指向别的东西）。
执行路径上的 `revocation` stage 每次都查这两个字段（`CheckProvenance`），所以
① 按摘要撤销某个构建 → 下一次调用即拒；② 按发布者撤销 → 该包所有能力一起失效；
③  shipped 产品代码不受影响。没有 provenance 的能力等于不可撤销，因此这一项不是可选装饰：
`TestRevokedPackPluginBuildIsNotCallable` 直接断言注册出来的 spec 带摘要，并走真实 authorizer 验拒绝。

其余规则与 MCP 声明同源：安装只进表不启动；`plugin_host.enabled` 为假时装配不装宿主，
启用会被明确拒绝（不会退化成"在本进程里跑一跑看"）；宿主里 config.yaml 已声明的同名信任域
**优先级更高**，包不能覆盖它，卸载包也不能把它删掉；出网一律走 `StrictEgress`，
包没有把这个开关关掉的字段。

## 包声明的 MCP 服务器

一个包可以带 `mcp/<name>.yaml`，字段就是一台外部 MCP 服务器的声明：

```yaml
type: stdio            # stdio | http/sse
command: python3       # stdio 必填；http/sse 用 url
args: ["-c", "pass"]
env: {FOO: bar}        # 只取字面值，见下
url: http://127.0.0.1:8000/sse   # http/sse 必填
headers: {Authorization: "Bearer literal-token"}
description: 实验室 MCP
timeout: 45
enabled: true          # 读得到，但装包时不生效——开关是单元的，不是文件的
```

单元身份是 `mcp/<name>`，`<name>` 就是文件名去掉扩展名，也是管理器里的服务器名。四条规则，
每条都有测试兜着：

1. **装包不等于启动进程。** 装完（以及每次冷启动重新声明时）服务器一律以停用状态进表进管理器，
   控制台与 MCP 页都显示"已声明未启动"。拉起进程是运维者按下单元开关那一下：
   `POST /api/plugins/units/mcp/<name>/enabled {"enabled":true}`。包文件里的 `enabled: true` 是
   包作者的意图，不是运维者的同意。
   单元开关本身会落库（`capability_unit_switches`，只落"停用"这个收窄方向，启动后按源路径复核、
   过期行清理），但 **MCP 这一类不接受持久化**：每次启动都重新按停用声明，所以那一次开关的响应就
   写明 `switch_persisted:false` 并指路——**要让服务器跨重启常驻，请写进 `config.yaml`**
   （那份声明本身就是运维者的同意）。
2. **包不能覆盖运维者在 `config.yaml` 里声明的服务器。** 装包前查活的管理器，撞名直接 409 并点名
   那台服务器；`LoadConfigs`（`应用配置`）遇到同名时**文件优先**，包失去这个活动槽位。
3. **反向也不行：MCP 页不能改写包声明的服务器。** `PUT/DELETE/…/start/…/stop` 对包拥有的名字返回
   409 并指出包 ID。这四个端点只认名字，`启动` 会去读 `config.yaml` 里根本不存在的那一条，
   然后把读到的空值存回文件——一台能用的服务器就被换成一条永远连不上的空声明了。
4. **包声明不做 `${VAR}` 展开。** `config.yaml` 与 MCP 页都会展开环境变量引用，那是运维者自己的
   文件；包是别人写的内容，展开等于让包读本进程的环境，一条
   `Authorization: "Bearer ${CSAI_LLM_API_KEY}"` 就把凭据送去了包作者选的服务器。声明里的字面值
   原样进管理器。

`应用配置` 不会清空包声明：管理器的 `configs` 由文件重建后再叠加包那一份（第 2 条的例外就是这里
的"文件优先"）。卸载只删自己声明过的服务器——`PackOwner` 不指向本包就跳过并在响应里说明是谁的。

这几条在 `make wiring-check` 里都有对应测试：`TestBootDeclaresPackServers`（启动只声明不启动）、
`TestPackDeclaration*` / `TestReloadKeepsPackServers*` / `TestOperatorSideWritesRefuse*`（管理器侧）、
`TestExternalMCPPageCannotMutateAPackDeclaredServer`（HTTP 侧）、
`TestPluginInstallDeclaresMCPServerWithoutStartingIt` 与 `TestPluginConsoleReportsAShadowedMCPServer`
（控制台与表同源）。

随仓库的四个示例包**没有**带 `mcp/` 单元：一个命令不存在、连不上的服务器只会让 MCP 页多一行
错误状态，而装包时它本来也不启动。要示范这一类能力请用真存在的服务器（本仓库的
`cmd/mcp-stdio` 就是一个可选目标，前提是先构建出那个二进制）。契约由测试夹具承担，
`make wiring-check` 里那三条 MCP 测试覆盖的就是这种声明。

## 外部 MCP 工具也有身份

远端服务器本来就支持热增删（`/api/external-mcp/*`），缺的是**身份**：所有远端工具过去一起过
一条 `mcp:external:execute`，规则无法点名某个工具，审批与审计也只能写到"外部 MCP"这一层。

现在 `ExternalMCPManager` 每次拿到某台服务器的真实工具清单，就在
`capability.LayerRemote` 里按服务器成组登记/替换/摘除：

- 身份 `remote.<server>.<tool>`，Name 就是执行器看到的线名 `<server>::<tool>`；
- 权限、runtime 与 **global scope 下限**沿用命名空间那条策略，所以这一步**不改变谁能调用什么**，
  只是让"某个远端工具"变成可被规则、审批与审计点名的对象；
- 一台服务器重连或下线只动它自己那一组，别的服务器与内置策略一律不变；
- 清单还没到位（服务器没连上、刷新在途）时判定回到命名空间策略——那是**一条登记在册的策略**，
  不是"名字不认识就放过"。

装配漏接观察者的话 `make wiring-check` 会红（`SetToolInventoryObserver` 必须恰好调用一次）——
漏接的表现不是报错，而是所有远端工具悄悄退回命名空间判定，所以只能靠门禁。

## 接口

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/api/plugins` | 已装包 + 独立单元 + `generation` + `drift` + 每单元的 `served` |
| GET | `/api/plugins/available` | 能力包目录里**可安装**的包（含每个包声明的单元与是否已装） |
| POST | `/api/plugins/install` | `{"bundle":"<包名>"}` → 装入并立刻生效 |
| DELETE | `/api/plugins/bundles/{id}` | 卸载（只摘表，不删文件） |
| POST | `/api/plugins/units/{kind}/{name}/enabled` | 启停单个单元 |
| DELETE | `/api/plugins/units/{kind}/{name}` | 摘掉一个**扫描得到**的单元（包拥有的会 409） |

`identity` 里带斜杠（`role/CTF`），所以路由拆成 `:kind/:name` 两段 —— 单段会被 gin 在匹配前
就解掉转义而命中不到。

## 前端页面

「平台管理 → 能力包」（`web/static/js/plugins.js`）就是这张表的界面：可安装的包一排「安装」按钮，
已装的包列出每个单元并带 `已生效 / 未生效` 标记（未生效的原因放在悬浮提示里），
启停与卸载都在同一页完成。**这一页是点出来的，不是推出来的**：真机点验抓到过两个单测抓不到的缺陷 ——
调一个页面上并不存在的 toast 助手（安装其实成功了，界面却写「安装失败」），
以及启停按钮把三个参数当成一个 JSON 数组发出去（服务端收到 `/units/tool,semgrep,false/undefined/enabled`）。
所以 `plugins-ui.test.cjs` 现在**执行**渲染出来的 `onclick` 文本，而不是只解析它。

两点不装作已完成：

- 安装**只能**从 `<configDir>/bundles` 里挑，越界路径（`../`、绝对路径）一律 400。
- `served:false` 是**响应里的字段**，不是文档里的脚注。现在还写着 `false` 的只有 mcp 一类：
  远端服务器的活路径是自己的管理器，能力表只登记声明。让"装好了"读起来像"能用了"，
  就是这一层存在的理由的反面。

## tool 配方怎么接上表的

配方清单原本只在两个地方产生：`config.Load` 扫一次 `tools_dir`，`POST /config/apply` 再扫一次。
现在 `ToolLayer.Rebuild()`（`ConfigHandler.Tools` 持有的协作者，见 `internal/handler/tool_table.go`）是唯一的重建入口，它按顺序做三件事，与 apply 原本做的**完全同一套**：

1. 从表里读配方**路径**（表里没有 tool 单元时回到目录扫描——漏跑启动扫描不该把 90 个内置配方清空）；
2. 重建能力注册表的 recipe 层（没有 `capability:` 清单的配方照旧被拒，调用时 fail-closed）；
3. `ClearTools()` 后重新注册全部工具面（配方 + 每个内置 registrar）。

一键安装/卸载/启停只有**涉及 tool 单元**时才触发它——`ClearTools` 会清掉整个工具面，
纯角色包不该付这个代价。整段由 `toolLayerMu` 串行：两个重建重叠时，一方的 `ClearTools`
可能落进另一方"清空后还没重注册"的窗口。

两条不会被说清楚的规则，都用测试钉住了：

- **开关只收窄**：运行期状态是 `文件 enabled ∧ 表 enabled`。表单元的 `Enabled` 默认是 true，
  如果把它当覆盖值，`enabled: false` 的内置配方会被悄悄打开。
- **运行期开关不写回文件**：`PUT /config` 会把每个工具的 `enabled` 落到它自己的 yaml 里
  （既有行为）。若这次落盘的是由表带来的 false，配方就被永久钉死——之后再打开表开关也起不来。
  所以那条写回循环跳过"表说停用"的工具。

漏接的失败模式是**静默**的：装配若不把 `configHandler` 传给 `NewPluginHandler`，包里的配方会被登记、
被列出来、被回答"已安装"，然后永远不能执行。构造函数把它做成必填参数，装配处再传错由
`make wiring-check` 的 AST 断言兜住（第 4 个实参必须是 `configHandler.Tools`，传别的、传 nil 都算漏接）。
启动扫描若漏掉 `KindTool` 这一行，装任何一个包就会把整批内置配方换成包里那一个 ——
这条由 `TestBuiltInCapabilityScanCoversEveryServedKind` 兜住，同时要求新加的 kind 必须被扫描或显式豁免。
"表驱动 vs 目录驱动"对内置 90 个配方逐条比对（名字、顺序、启用位全等），由
`TestToolLayerFromTableMatchesDirectoryLoad` 钉住。

角色这一行是本轮改掉的：`roles/*.yaml` 以前只在 `config.Load` 里解析一次，之后由角色 API
**无锁原地改**那张 map（连 GET 里都会 `h.config.Roles = make(...)`），八个文件在没同步的情况下读它。
现在写的一侧是「写文件 → 进表 → 发布新快照」，读的一侧统一走 `currentRoles(h.config)`；
装配若忘了装活配置快照，`make wiring-check` 会直接红
（`TestAssemblyInstallsTheLiveConfigStoreAndPublishesRoles`，且这种漏接**编译得过**）。

skill 一行的"读表"含两层：运行路径（`internal/einoskill` 换掉了厂商那个只认一个 `BaseDir`
的 backend）**和管理台列表**（`GET /api/skills`、详情、文件读写都按表解析目录）。
两层必须一起改：只改运行路径会出现"包里的 skill 被 Agent 用着、列表里看不见"，
这一条是在真实跑起来的服务上实测到的（23 → 装包 24 → 卸载 23），不是推演出来的。
写路径遇到包拥有的 skill 返回 409 并指名是哪个包，**不会**在内置 skills 目录里悄悄落一份同名副本。

agent 一行的两层同样一起改了：`agents.LoadMarkdownAgentPaths` 与目录扫描共用同一个解析器
（对内置 16 个 `.md` 逐个比对，路径驱动与目录驱动**逐字节相同**），运行路径与管理台都从表里的
路径读；表里一个 agent 单元都没有时**回到目录扫描**，因为漏跑启动扫描是可修的装配问题，
而"每次运行都没有子代理"是不可修的。
这一行是**真实跑起来的服务**指出来的：`GET /api/plugins` 报
`[('role', True), ('agent', False), ('skill', True)]`——运行路径已经搬到表上了，
`served` 字段还留着搬迁时的说明。测试当时抓不到它，因为夹具包里没有 agent 单元；
现在 `reporting-pack` 带一个 `agents/report-analyst.md`，装完断言 `served=true`、
卸载后断言该单元从表里消失（先断"在"再断"不在"，否则后半句可能一直在空过）。

内置的 `roles/`、`agents/`、`skills/`、`tools/` 四个目录同样被扫成单元进表，
所以"内置能力"和"后装能力"走的是同一套身份与同一张表；两侧身份一致性由
`internal/app/plugin_parity_test.go` 钉住（实测 142 个内置单元：roles 13 / agents 16 /
skills 23 / tools 90）。
