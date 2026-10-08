# 能力市场优化研究：分发、发现与运营面

调研日期：2026-10-07 ｜ 基线提交：`b9fb44eb` ｜ 分支：`feat/capability-market-optimization` ｜ 方式：全量只读实测 + 外部一手资料核查
本文只含研究，不含代码改动。前置：[capability-platform-decoupling-research.md](capability-platform-decoupling-research.md)（2026-09-30，P0–P2 已落地）；
能力平台契约：[capability-platform.md](capability-platform.md)；包装格式与规则：[../../bundles/README.md](../../bundles/README.md)。

## 摘要

一句话结论：**能力平台的"内核"已经是可比产品里安全结构最完整的一个；缺的不是能力机制，而是市场层——把"能力"变成"可分发、可发现、可升级、可运营"的东西。**

四条优先线（详细方案见第四节）：

| 线 | 内容 | 是否依赖服务端 |
|---|---|---|
| **P0 客户端闭环** | 安装预览、升级可视化、provenance/撤销可见、包元数据与发现 | 否，本仓自足 |
| **P1 分发层** | 签名索引 + 发布 CLI、下载→验签→增量门→安装链路、撤销刷新、气隙包 | 索引可先做成静态文件，服务端后置 |
| **P2 治理面** | 私有来源策略（Qoder 式三档的本地等价物）、场景化推荐（Goby 式）、OS 级硬隔离 | 部分 |
| **观察** | 对话式检索/安装（Qoder 式），等 P0/P1 落地后评估 | — |

一个贯穿全文的取舍：本仓库是**自托管安全工具**，不是 SaaS。市场的"服务端"应当先降级为
**一组可签名的静态文件**（索引 + 制品归档），发布方可以是任何文件托管——包括内网目录、
git 仓库、对象存储。账号体系、在线付费、遥测回传不在本路线内（见第五节）。

## 一、现状基线（实测）

### 1.1 平台已具备的（对标中领先的部分）

| 维度 | 现状 | 证据 |
|---|---|---|
| 身份与分级 | `publisher.capability.name`；readonly/mutating/destructive 三档 + 审批下限 | `capability-platform.md` 第二、三节 |
| 执行决策 | 唯一入口 `authorizeWithCapability`，未登记即拒绝；审批账本单次 60 秒 | `internal/app/capability_policy.go:260` |
| 运行时隔离 | 一信任域一子进程、无凭据继承、出网走主机侧代理（`grants` ∩ 批准元组） | `internal/pluginhost`，`capability-platform.md` 第八节 |
| 人审双向核对 | 声明清单 ↔ `capabilities/list` 双向比对，不一致整单元拒绝并回滚 | `bundles/README.md`「包声明的插件」 |
| 签名库 | Ed25519 签名覆盖全部安全字段；无 `ignoreUnverified`；密钥/制品/发布者三档撤销 | `internal/artifact/artifact.go`、`revocation.go` |
| 增量门 | 新增 grants/级别上调/权限变更 → 双人复核、作者不得自审 | `internal/artifact/review.go:184` `Decide` |
| 撤销强制执行 | 启动装载 + **每次调用前**复查 provenance | `internal/app/app.go:223`、`capability_policy.go:227-238` |
| 七类 kind 热插拔 | role/agent/skill/tool/mcp/mode/plugin 同表同生命周期，装/摘/启停即时生效 | `internal/plugin`，`internal/app/capability_scan.go` |
| 控制台 | 「平台管理 → 能力包」页：装/摘/启停 + `served`/`drift`/插件运行时行 | `web/static/js/plugins.js`、`web/templates/index.html:430` |
| 随仓样例包 | 4 个按角色打包的示例，装即生效，重启重新装入 | `bundles/`、`internal/app/boot_bundles_test.go` |

### 1.2 市场层的缺口（「声明了但没有调用方」口径）

以下是本报告的关键证据：**这四类东西全部存在，但没有任何运行路径消费它们**（复现命令见附录 B）：

1. **验签与审核管线零消费**。`artifact.Verify` / `Sign` / `NewTrustStore` / `Decide` / `Quarantine`
   的非测试调用方为 **0**：整个商店的客户端强制部分（验签、信任库、增量门、隔离区）是一套
   只被测试驱动过的库。唯一被运行路径消费的是 `Lint` / `Blocks` / `SanitizeForIndex`——
   走的是知识入库（`internal/contentpolicy/contentpolicy.go:72`），与包安装无关。
2. **`ArtifactConfig` 五个字段只有一个有消费者**。`RevocationsPath` 在启动时装载
   （`app.go:223`）；`TrustStorePath` / `InstallDir` / `QuarantineDir` / `RefreshMinutes`
   零消费者（`internal/config/config.go:1648-1654`）。
3. **撤销列表没有刷新循环**。`MergeRevocations`（`capability_policy.go:421`）只有定义，
   运行路径与测试都没有调用方——「能合并」但「没有人去取」。
4. **市场一层的 Go 代码命中为 0**（`grep -rin "market" --include="*.go"` → 0）。
   可安装清单只读 `<configDir>/bundles` 单一本地目录（`app.go:649` → `handler/plugin.go:691`
   `ListAvailable`），没有第二个来源的概念。
5. 附带发现两处死字段：`IsolationMode`（`config.go:1678` 与 `pluginhost/host.go:47` 各声明一次，
   无消费者）；`ArtifactConfig` 的四个字段同上。它们不是 bug，是"协议先于实现"留下的锚点，
   本报告把它们收进路线图，要么接线、要么删除。

### 1.3 与 `capability-platform.md` §14「尚未实现」的关系

§14 已列出的 registry 服务端（签名发布、灰度、release-age 冷却、自助刷新）、气隙离线包、
沙箱引爆自动化、netns/seccomp 硬强制，本报告不重复立项，但把它们**组织成有顺序的路线**：
签名发布与自助刷新进 P1，气隙包进 P1，硬强制进 P2，沙箱引爆保持现状（人工记录）到有真实提交量再评估。

## 二、参考市场核查（抓取于 2026-10-07）

### 2.1 DSH（DeepSeek Harness）——"Everything is a Plugin"

- **产品形态**：官方开源 agent harness（`deepseek-ai/deepseek-harness`，MIT，24.4 万星，
  2026-10-07 抓取），基于 Cordis 的插件架构；官方 README 明确 developer preview 与
  `SAFETY.md` 安全须知，兼容性会有破坏性变更。
- **分发**：插件即 npm 包，`dsh plugin add <包名>` 经包管理器拉取注册；官方以 GitHub topic
  `dsh-plugin` 做可发现性——**发现层外包给 GitHub，自己不建索引**。
- **市场（社区聚合站）**：按 11 个分类组织；每项带源码直链、star/fork 计数、兼容引擎版本代号、
  变更记录；治理标签为 `verified`（人工复核）/ `unconfirmed`（未定级）两档（社区站点口径）。
- **信任模型**：无密码学签名。机制是**人工审核 + 版本锁定**：要求安装时 pin commit
  （`github:owner/repo#sha`）防止上游静默替换；安装期**无沙箱**，pnpm 的 `allowBuilds`
  白名单等于"授权该包在你的机器上于安装时真实执行"（其安全教程原话承认这一点）。
- **生态信号**：官方只给"topic + 包管理器"，信任层由第三方补位（已出现第三方
  safe-plugin-manager）——平台没给的信任层，社区会用别的方式补上，质量参差。

**抄**：分类/复核标签/来源可溯/兼容版本徽章这套展示词汇；**不抄**：无签名、安装期脚本执行、
把发现权完全外包（我们已有更强的身份与签名体系，展示层向它对齐即可）。

### 2.2 Goby

- **分发**：官方扩展商店（gobysec.net/extensions）；商店页为 JS 渲染，字段细节未能静态核实（标注）。
- **上架流程**（来源：官方博客，2024-07）：开发者在客户端内"压缩上传"进入审核队列 →
  官方人工审核、择优上架 → 上架后"向匹配场景的用户定向推送"。
- **分发时机是场景化的**：来自扫描结果——资产探测命中漏洞时，界面动态给出"是否下载安装对应
  Exp 插件"的入口，而不是让人去商店里搜。Exp 插件可调用外部 Python 脚本实现一键利用。
- **激励**：上架送授权时长 + 周边；该文未披露公开评分/下载榜等社区度量。

**抄**：提交即审核的上架流程形态；**场景化分发**（"你面前这件事，缺哪个能力"比"目录里有什么"
有效得多）；**不抄**：不可读封装、无签名分发（与既有原则冲突，见第五节）。

### 2.3 Qoder

- **形态**：Skill = 文件夹 + `SKILL.md`（`~/.qoderwork/skills/<name>/`）；Skills（编排层）/
  Plugins（组件层）/ MCP（接入协议层）三层职责。
- **安装渠道有五种**：对话式检索安装（自然语言 → AI 检索并一键装）、技能广场（内置市场分类浏览）、
  GitHub 开源社区、手动上传、直接管目录。分享用 24 小时有效的一键安装链接。
- **企业治理**：私有市场（Private Marketplace）+ Collection（**单集合单类型**，插件在客户端
  统一展示为"专家套件"）+ 可见范围；管理员下发三档策略——「客户端可配置 / 强制开启 / 静默启用」。
  文档明确警告：**下发范围 > 可见范围** 时，成员会静默收到但搜不到——可见性与下发是两个正交维度。
- **运行时**：技能依赖修复与安全工作环境自成一套（VM/沙箱）；细粒度权限文档未在抓取页展开（标注）。

**抄**：私有来源 + 可见范围/下发分离的策略模型；**安装前把"来源与即将发生的动作"摆出来**
（DSH 安全教程同样描述了这个确认弹窗）；**不抄**：把"静默启用"下沉到携带可执行代码的第三方单元——
与本仓"装 != 开、开不落库、核对不过整包回滚"的同意边界直接冲突（见第五节）。

### 2.4 交叉对照（含本仓）

| 维度 | DSH | Goby | Qoder | yakit（既有研究） | 本仓现状 |
|---|---|---|---|---|---|
| 打包形态 | npm 包 | 压缩归档（Exp 等） | 文件夹 + SKILL.md | 头部声明 `##type:poc` | `bundles/<id>/bundle.yaml`，七类单元 |
| 分发渠道 | npm + GitHub topic | 官方商店 | 广场 / 对话 / GitHub / 上传 | 官方商店 | **本地目录（唯一来源）** |
| 发现 | 社区聚合站分类 + 复核标签 | 商店分类 + 场景化推送 | 广场分类 + 对话检索 | 商店 | 无（列表 = 目录内容） |
| 信任 | 人工审核 + pin commit，无签名 | 官方人工审核 | 企业内托管；平台审核细节未公开 | 加密插件（不可审） | Ed25519 签名 + 双向核对 + 增量门（**库在，链路未接**） |
| 安装同意 | 确认弹窗（来源 + 命令） | 扫描命中时提示下载 | 一键 + 分享链接 | 一键 | 按钮即装（**无预览**） |
| 升级/回滚 | 无（dev preview 破坏性变更） | 未见披露 | 集合版本管理 | 商店更新 | 同 id 再装 = 升级（语义在，**无 UX**） |
| 撤销 | 无 | 未见披露 | 企业可不下发 | 无 | 执行期复查已强（**无刷新、无 UI**） |
| 离线/气隙 | 无 | 未披露 | 企业私有市场近似 | 无 | 无 |

### 2.5 抄 / 不抄清单

| 条目 | 判定 | 理由 |
|---|---|---|
| 分类 + 复核标签 + 来源直链 + 兼容版本徽章 | 抄（展示层） | 数据大多已有（publisher/digest/版本），缺的是渲染 |
| 安装前确认（来源、将改变什么） | 抄 | 与本仓 `served`/`drift` 同一哲学：不让用户误解"点了会发生什么" |
| 场景化分发（扫到什么→推荐装什么） | 抄（P2） | Goby 最独特的一条；本仓有漏洞/资产域，天然有锚点 |
| 私有来源 + 可见范围/下发策略 | 抄（P2，改造后） | 映射为"包的来源与启用策略"，且必须服从同意边界 |
| 对话式检索安装 | 观察 | 依赖成熟的索引（P1）与只读工具面，先不立项 |
| 无签名 + pin commit | 不抄 | 本仓已有更强体系，回去不可接受 |
| 安装期脚本执行（allowBuilds） | 不抄 | 直接违反"声明 != 启动" |
| 加密插件 | 不抄 | 既有原则：碰运维者凭据的代码必须可读可审 |
| 在线评分/下载榜、遥测回传 | 不抄 | 自托管定位 + 离线优先；热度信号如有需要，取有来源者（如仓库 star） |

## 三、差距清单（按市场八要素定位）

| # | 要素 | 状态 | 缺口 |
|---|---|---|---|
| 1 | 发现 | ✗ | 无分类、无搜索、无详情页；"可安装"= 目录列表 |
| 2 | 信任 | ◐ | 签名/信任库/增量门全在库里，安装路径不消费（实测零调用方） |
| 3 | 安装 | ◐ | 单步按钮；无能力预览（这个包会请求什么 class/grants） |
| 4 | 版本 | ◐ | 升级语义有、不可见；无版本对比、无回滚 UX；包元数据只有 id/name/version/description |
| 5 | 撤销 | ◐ | 执行期复查强；无刷新循环（`RefreshMinutes` 死字段）、控制台无撤销/隔离区视图 |
| 6 | 离线 | ✗ | 无导出/导入 |
| 7 | 治理 | ✗ | 无来源策略、无可见范围概念 |
| 8 | 运行时 | ◐ | 进程外宿主 + 代理出网已强；OS 级硬隔离缺（`IsolationMode` 死字段），边界已在文档写明 |

## 四、优化方案（分期）

每项格式：**做什么 → 为什么 → 改动面 → 验收**。所有新增端点按
`csai-new-protected-console-endpoint` 的接线顺序落地（路由 golden、权限目录、
`isProcessGlobalMutationPath`、OpenAPI、审计注入），所有新增 UI 走双侧 i18n 与 js 契约测试。

### P0 — 客户端闭环（不依赖服务端，本仓即可验收）

**P0-1 安装预览与能力摘要**
- 做什么：`GET /api/plugins/available` 的每个包在安装按钮旁给出"安装会发生什么"的摘要：
  单元清单（已有一半）、每单元的 class 徽章（tool 的 `capability.class`、plugin 声明的
  `class/permission/grants` 汇总）、无声明能力的 fail-closed 标注；前端安装前弹确认。
- 为什么：Qoder/DSH 的安装确认弹窗是共识；本仓数据都在 `bundle.yaml` 与声明文件里，
  读一下就能给——"装好了"与"会发生什么"必须同时可见（`served`/`drift` 同一哲学）。
- 改动面：`handler/plugin.go` 的 `availableBundle` 扩展（只读、失败软化为标注）；
  `plugins.js` 卡片 + 确认弹窗；i18n 双侧；契约测试。
- 验收：契约测试断言"声明了 destructive 的包在预览里带该徽章、无 `capability:` 的配方标注
  fail-closed"；夹具包 `reporting-pack` 扩一个 plugin 单元；真机点验一次安装确认。

**P0-2 升级与回滚可视化**
- 做什么：已装包显示"目录里的版本 vs 表里的版本"；目录版本更高时给出"升级"动作，
  升级前展示单元级差异（新增/移除单元、digest 变化的单元）；带代码的 kind 升级后
  开关回停用（既有规则）在 UI 里说明。回滚 = 目录里放回旧版本再装（保留上一版目录的
  约定写入 README），或先做"升级前整包快照到 `.previous/`"。
- 为什么：`bundles/README.md` 说"没有版本就无法升级或回滚"，但升级今天是不可见的；
  增量门（`artifact.Diff`）已有判定形状，缺的是升级路径上的展示。
- 改动面：`plugin.Table` 增加"已装 bundle 的版本视图"（Bundle 结构已带 Version）；
  handler 比较目录/表；**每次安装成功后**把包目录按版本快照进
  `bundles/.previous/<id>/<version>/`（口径修正见「实施状态」：升级动作前旧文件已被目录覆盖，
  "升级前快照"不可实现）；回滚复用 install 端点的 `from_version`（快照核对 → 覆盖回目录 →
  标准安装；安装记录随回滚更新）；前端卡片状态机（已装/可升级/回滚目标）。
- 验收：升级夹具包（v1→v2 改版本与单元摘要）走 `ListAvailable` → preview → install 全程契约测试；
  探针：坏版本名与遍历版本回滚必须拒绝且不改变表状态；快照失败只进响应、不失败安装。

**P0-3 provenance / 撤销状态可见**
- 做什么：已装 unit 行增加 `publisher` 与 `artifact digest` 展示（数据已在
  `StampProvenance`/`ProvenanceOf`）；撤销列表里的 digest/publisher 在控制台有只读视图
  （含从哪个文件装载、条目数）；漂移（`Drifted()`）与撤销并列显示。
- 为什么：撤销机制已经"咬得住"，但运维者看不见自己装了谁的东西——GFW 之外，这是
  事后复盘的全部线索。`CheckProvenance` 已在每次调用执行，展示只是把已有事实说出来。
- 改动面：`GetState` 响应带 provenance；`plugins.js` 单元行；新的只读端点或复用
  `GET /api/plugins`（推荐复用，避免新面）；i18n。
- 验收：契约测试断言"撤销列表装载 N 条后控制台可读出 N 条"；真机点验一条撤销
  （用测试树的 revocations 文件）后单元行标记变化。

**P0-4 包元数据扩展与发现**
- 做什么：`bundle.yaml` 可选字段 `categories` / `author` / `homepage` / `license` /
  `compatibility`（对引擎版本的约束）/ `changelog`；控制台按 categories 过滤 + 关键字搜索
  （纯前端过滤起步）；详情展开（描述、单元表、能力摘要）。
- 为什么：DSH/Qoder 的发现层词汇均可映射到这些字段；向后兼容（全部可选，缺省不渲染）。
- 改动面：`plugin.Manifest` + `Bundle`（填默认值，丢失即空）；`ListAvailable` 透传；
  `plugins.js` 过滤条与详情；README 表格更新；i18n。
- 验收：契约测试断言"旧清单（四样例包）解析结果逐字段不变" + "新字段往返一致"；
  js 契约测试过滤行为。

### P1 — 分发层（"服务端"先做成静态文件）

**P1-1 签名索引 + 发布 CLI**
- 做什么：`registry/` 形态定为一个**静态目录**：`index.json`（包列表：id/版本/分类/摘要/
  制品 URL/签名/审核状态/兼容性）+ 每个包的制品归档（`<id>-<version>.tar.gz` + 分离签名）。
  新增子命令（照 `cmd/server/update_cli.go` 模式）：`pack sign`（发布方本地签名）、
  `pack verify`（索引与制品全量校验）、`pack index`（从目录生成索引）。
  复用 `internal/artifact`：`Sign`/`Verify`/`TrustStore`/`Revocations`/`Lint`。
- 为什么：服务端降级为静态文件后，"registry 服务端"与"内网共享目录"是同一个形态；
  这是自托管定位下唯一不引入账号体系的分发方式。
- 改动面：新包 `internal/market`（索引读写 + 校验 + 版本比较）；`cmd/server` CLI 分支；
  文档新章。
- 验收：`pack sign → pack index → pack verify` 往返测试（含坏签名、坏摘要、坏 URL 三种拒绝）；
  生成物 `index.json` 的 golden + CI regenerate-and-diff。

**P1-2 下载 → 验签 → 增量门 → 安装 链路**
- 做什么：`GET /api/market/index`（可配置来源，默认空 = 功能不可见）、
  `POST /api/market/install {id, version}`：下载制品 → `Verify`（信任库，未签名/未知发布者拒绝）
  → `Revocations.Check` → 与已装版本比对 `Delta`（新增 grants/级别上调 → 明确提示，需确认参数
  `accept_capability_increase:true` 且写审计）→ 解包到 `bundles/<id>/`（路径收敛用
  `skillpackage.SafeRelPath`）→ 走既有 `InstallBundle`。断网/坏包/版本回退全部具名拒绝。
- 为什么：这是"市场"真正落地的一刀；`artifact` 库的所有判据在这一刻全部上线，
  且拒绝语义与既有安装端点一致（409/400 指名）。
- 改动面：`internal/market` 下载器 + `internal/handler/market.go` + `routes_market.go`；
  权限 `market:read`/`market:install`（或复用 `plugins:install`，落地时定）；前端市场页签。
- 验收：httptest + 本地静态服务器：正常装、坏签名拒、撤销命中拒、增量需确认、
  越界路径拒；探针各一（注入即红）。真机：装一个新样例包并回读 `GET /api/plugins`。

**P1-3 撤销刷新与信任管理页**
- 做什么：`RefreshMinutes` 接线（启动时 + 周期拉取 revocations 文件/URL，`Merge` 只增不减）；
  控制台"信任与撤销"页：发布者公钥导入/列出/吊销、撤销条目视图、隔离区（`QuarantineDir`）列表。
- 为什么：三处死字段（`RefreshMinutes`/`TrustStorePath`/`QuarantineDir`）在这里一次接线或删除；
  撤销"咬得住"的前提是它真的会被更新。
- 改动面：`internal/app` 启动装配 + 一个定时器（用既有 scheduler 或 ticker）；
  handler + 页面；config.example.yaml 注释。
- 验收：`TestRevocationsRefreshMergesAndNeverUnrevokes`（合并后删除条目→合并结果仍在）；
  真机：改本机 revocations 文件，等一个周期，调用行为变化。

**P1-4 气隙导出 / 导入**
- 做什么：`pack export <id>@<version> -o file.csai-pack`（目录 + 索引条目 + 签名打包）；
  `pack import file.csai-pack`（校验链同 P1-2）→ 装入。为"物理隔离环境"提供与在线市场
  同一条验证路径。
- 改动面：`internal/market` + CLI；文档。
- 验收：export→(模拟无网)→import 往返；坏签名/被撤销/信任库为空三种拒绝。

### P2 — 治理与体验（先讨论再立项）

- **P2-1 来源策略**：把 Qoder 的"可见范围 vs 下发范围"映射为：每个来源（本地目录/静态索引/
  气隙包）可标 `visible` / `installable`；"强制开启/静默启用"**不适用于携带代码的单元**
  （与同意边界冲突），只允许作用于纯内容单元（role/skill/agent），且实现时必须回答：
  强制开启与"开不落库"如何共存。预期结论：先只做"来源白名单 + 可见性"，下发放置。
- **P2-2 场景化推荐（Goby 式）**：漏洞/资产类型 → 关联包与能力的只读提示
  （映射表本地维护，不做遥测）。锚点：漏洞管理页、C2 页的"缺什么能力"空态。
- **P2-3 OS 级硬隔离**：netns/seccomp；`IsolationMode` 死字段在此接线或删除。
  与 `capability-platform.md` §8 的边界说明衔接（代理不约束裸 socket）。

## 五、边界与不做清单

1. **不做安装期脚本执行**：任何"装的时候跑一段作者代码"（DSH 的 pnpm `prepare` 形态）都不接受；
   本仓安装只做"读声明 + 摘要 + 进表"。
2. **不加密插件**：碰运维者凭据的代码必须可读可审——与 yakit 划清界限（既有原则，重申）。
3. **没有 `ignoreUnverified`**：未签名、未知发布者、密钥与发布者不符一律拒绝，不设逃生门。
4. **不做服务端遥测/评分/下载榜**：自托管 + 离线优先；若要热度信号，采用有来源者
   （如仓库 star），且不得回传本机数据。
5. **registry 不做账号/付费/在线发布**：发布 = 本地签名 + 把静态文件放到某个托管点；
   审核状态是索引里的一个字段，不是一套审核后台（自动化审核用例全部复用 `artifact.Decide`）。

## 六、推荐落地顺序与验收总表

| 顺位 | 交付物 | 验收（除 `make ci` 全绿外） | 回滚 |
|---|---|---|---|
| 1 | P0-1 安装预览 | 契约测试 + `reporting-pack` 扩展 + 真机确认弹窗 | 纯展示，回退无副作用 |
| 2 | P0-3 provenance/撤销可见 | 契约测试 + 真机读撤销条数 | 同上 |
| 3 | P0-4 元数据扩展 | 旧样例包逐字段不变 + 新字段往返 | 字段可选，向后兼容 |
| 4 | P0-2 升级可视化 | 升级夹具 v1→v2 全链路 + 快照坏损探针 | 快照目录即回滚靶心 |
| 5 | P1-1 索引/签名 CLI | `sign→index→verify` 往返 + golden | 静态文件，无状态 |
| 6 | P1-2 下载安装链路 | 五种拒绝用例 + 双向探针 + 真机安装 | 走既有 Uninstall（只摘表不删文件） |
| 7 | P1-3 撤销刷新/信任页 | 合并语义测试 + 真机周期验证 | 关开关即回旧行为 |
| 8 | P1-4 气隙包 | 往返 + 三种拒绝 | CLI 独立，不影响运行面 |

每条落地时必须回填本文件与 `capability-platform.md` §14 对应行；新增文档章节遵守
zh-CN / en-US 双侧约定（本文档为研究稿，保持 zh-CN 单侧，与解耦研究同例）。

## 实施状态

**前置件已落地（2026-10-07，非 P0–P2 项）**：出厂极简 + 安装记录（`installed_bundles`）——
出厂只留默认角色、5 个技能与 90 个配方，16 个域包躺在 `bundles/` 货架上等待安装，
"不点不存在、点了跨重启保留"（实施记录见解耦研究报告 §11「出厂极简与安装记录」）。
这是 P0「安装预览 / 发现」与 P1「下载 → 验签 → 安装」的前置：市场要有东西可发现、
可安装，前提是出厂不再全量自带。**P0 四项已落地（2026-10-07，与前置件同一次合流交付）**；P1/P2 未开始。

**P0-1 安装预览与能力摘要（已落地）**：`GET /api/plugins/available` 每项带 `preview`
（`internal/handler/plugin_preview.go`：按单元读配方 `capability:` 与插件声明，聚合成
`classes` / `liveCodeUnits` / `undeclaredUnits` / `problemUnits`；读不动的单元给单行 `error`，
不让一份坏声明打掉整个列表）。前端卡片渲染徽章，安装前弹确认（文案由预览拼装；取消不发请求）。
测试：`TestPluginAvailableShowsWhatInstallingWouldRegister` + js「install asks first」一节。

**P0-2 升级与回滚（已落地；口径修正一处）**：原计划的「升级前整包快照」不可实现——升级动作发生前，
旧文件已被目录覆盖。落地为**每次安装成功后**按版本快照到 `bundles/.previous/<id>/<version>/`
（`internal/plugin/snapshot.go`：保真复制含可执行位与软链、同版本替换、拒绝自我快照、坏/遍历版本名
拒绝）。升级 = 目录版本 ≠ 已装版本时卡片给出「已装 vX → 目录 vY」与单元级摘要差异；回滚 =
`POST /api/plugins/install {"from_version": ...}`（快照核对 → 覆盖回目录 → 标准安装），安装记录
随回滚更新，下次启动重放的是回滚后的版本。快照失败不阻断安装、进 `snapshot_error`。
测试：`internal/plugin/snapshot_test.go`、`TestPluginInstallSnapshotsUpgradesAndRollsBack`、
`TestPluginInstallReportsASnapshotFailure`。

**P0-3 provenance 与撤销可见（已落地）**：`GET /api/plugins` 增加 `revocations`（来源、条目计数、
是否装载；`app.ConsoleTrustProvider` 适配 `internal/artifact.Revocations`，启动装载处记下来源路径）
与每包 `rollbacks`；已装单元行显示安装时摘要、plugin 单元的发布者与 `已撤销` 标记
（`revokedMatch` 与执行路径 `CheckProvenance` 同一判据：publisher 或二进制 artifact digest 命中）。
装配漏传信任提供者会被 `settings_wiring_test.go` 的 AST 断言抓住。测试：
`TestPluginConsoleShowsRevocationsAndProvenance` + js「rows carry digest, publisher and revoked state」一节。

**P0-4 包元数据与发现（已落地）**：`bundle.yaml` 可选 `categories` / `author` / `homepage` /
`license` / `compatibility` / `changelog`（`plugin.Manifest`/`Bundle`，全部可选、向后兼容、
不参与任何安装规则）；卡片渲染 + 关键词/分类前端筛选（`applyPluginFilter` 只重渲染两个列表节，
保住输入焦点）。测试：`TestCatalogueMetadataIsOptionalAndNormalized` +
`TestPluginAvailableShowsWhatInstallingWouldRegister` 的元数据断言 + js
「catalogue metadata renders and the filter narrows the lists」一节。

**顺带修复（同类扫全量）**：`plugins.*` 命名空间里两处单花括号占位符（`confirmUnplug` 的 `{name}`、
`switchCapabilities` 的 `{count}`）在本仓 i18next（默认 `{{...}}` 语法）下**从不插值**，浏览器里
显示字面量；改为 head/tail 拼接，js 测试直接断言渲染文本。

## 附录 A：外部资料（抓取日期均为 2026-10-07）

| 来源 | 链接 | 可信度 |
|---|---|---|
| DSH 官方仓库（README：npm 分发、MIT、Cordis、topic 可发现性） | <https://github.com/deepseek-ai/deepseek-harness> | 官方一手 |
| DSH 插件安装安全教程（pin commit、allowBuilds、无沙箱） | <https://dsh-plugin.org/zh/tutorials/install-plugin-safety> | 社区站点，机制方向与 npm 生态一致 |
| DSH 插件市场（11 分类、verified/unconfirmed、star/fork、引擎兼容） | <https://dsh-plugin.org/zh> | 社区聚合站 |
| Goby 插件市场上架与激励（官方博客） | <https://rivers.chaitin.cn/blog/cq94ffp0lnechd243800> | 厂商一手（2024-07） |
| Goby 扩展商店（分类与字段为 JS 渲染，未能静态核实） | <https://gobysec.net/extensions> | 官方，字段标注为未核实 |
| Qoder 技能（目录结构、五种安装渠道、24h 分享） | <https://docs.qoder.com/zh/qoderwork/skills> | 官方一手 |
| Qoder 企业市场（私有市场、Collection、可见范围、三档下发） | <https://docs.qoder.com/zh/account/enterprise/marketplace> | 官方一手 |

## 附录 B：实测命令与输出摘要（复现用）

```bash
# 1) 验签/信任库/审核管线：非测试调用方 = 0（无输出，grep 退出码 1）
grep -rn "artifact\.Verify\|artifact\.Sign\|artifact\.NewTrustStore\|artifact\.Decide\|artifact\.Quarantine" \
  --include="*.go" . | grep -v _test.go

# 2) ArtifactConfig 四字段无消费者（只命中声明处）
grep -rn "TrustStorePath\|InstallDir\|QuarantineDir\|RefreshMinutes" --include="*.go" . | grep -v _test.go

# 3) 撤销合并无运行调用方（只命中定义）
grep -rn "MergeRevocations" --include="*.go" . | grep -v _test.go

# 4) Go 代码无市场概念
grep -rin "marketplace\|market" --include="*.go" . | wc -l   # → 0

# 5) IsolationMode 无消费者（只命中两处声明）
grep -rn "IsolationMode" --include="*.go" . | grep -v _test.go

# 6) 可安装清单的来源 = <configDir>/bundles
grep -n "NewPluginHandler(" internal/app/*.go   # app.go:649 传 filepath.Join(configDir, "bundles")
```

输出与断言见上文 §1.2；本文档所有"有/没有"结论均以这些命令在 `b9fb44eb` 上的实际输出为准。
