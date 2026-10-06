# CyberStrikeAI — 开发/CI 入口。
# 此前仓库没有 Makefile、没有 golangci-lint、没有架构约束检查、没有 -race 门禁，
# 解耦工作因此缺少安全网。目标形态的所有阶段都以这里的门禁为验收条件。

GO      ?= go
BIN     := cyberstrike-ai
PKG     := ./...
LDFLAGS ?=

# 固定工具版本：门禁必须可复现，否则"绿"没有意义。
GOLANGCI_VERSION := v2.3.0
ARCHLINT_VERSION := v0.2.35

.PHONY: all
all: build

## ---------------------------------------------------------------------------
## 构建
## ---------------------------------------------------------------------------

.PHONY: build
build:
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/server

.PHONY: build-stdio
build-stdio:
	$(GO) build -o cyberstrike-mcp-stdio ./cmd/mcp-stdio

.PHONY: generate
generate:
	$(GO) generate $(PKG)

## ---------------------------------------------------------------------------
## 测试：-race 是门禁，不是可选项
## ---------------------------------------------------------------------------

.PHONY: test
test:
	$(GO) test -count=1 $(PKG)

.PHONY: test-race
test-race:
	$(GO) test -race -count=1 $(PKG)

## ---------------------------------------------------------------------------
## 静态检查与架构约束
## ---------------------------------------------------------------------------

.PHONY: vet
vet:
	$(GO) vet $(PKG)

.PHONY: fmt
fmt:
	gofmt -w cmd internal

## fmt-check 才是门禁，且已经是**硬零**。重构起点上 `gofmt -l cmd internal` 有 29 个上游遗留文件，
## 一路 ratchet 到 26；这一轮把剩下的债务一次性清成 0，条件是它**只以独立提交出现**：26 个文件逐个
## 用 `diff <(gofmt <(git show HEAD:f)) f` 证过与工作区内容逐字节相同，即纯重排、零语义改动，
## 所以它不混进任何解耦 diff，也不需要一个"存量豁免名单"。
## 硬零之后 `make fmt` 随时可跑、跑完不会带出无关改动——这正是之前 ratchet 阶段不敢这么做的原因。
.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l cmd internal | wc -l | tr -d ' '); \
	if [ "$$unformatted" -ne 0 ]; then \
		echo "gofmt needed on $$unformatted file(s) (the gate is hard zero):"; gofmt -l cmd internal; exit 1; \
	fi; \
	echo "gofmt: clean"

.PHONY: lint
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "golangci-lint missing: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)"; exit 1; }
	golangci-lint run

## 架构约束当前为 warn：解耦期间允许违规存在，但不允许新增后无人知晓。
## go-arch-lint 通过后把 check-only 提为 CI 阻断，是 P6 的验收门之一。
.PHONY: arch-lint
arch-lint:
	@command -v go-arch-lint >/dev/null 2>&1 || { \
	  echo "go-arch-lint missing: go install github.com/fe3dback/go-arch-lint@$(ARCHLINT_VERSION)"; exit 1; }
	go-arch-lint check --arch-file .go-arch-lint.yml --exit-code 0 || \
	  echo "WARN: 架构依赖超出 .go-arch-lint.yml 声明的边界（见上表）"

## ---------------------------------------------------------------------------
## 门禁：CI 与本地提交前检查同一套内容
## ---------------------------------------------------------------------------

.PHONY: precommit
precommit: fmt-check vet test-race lint arch-lint

## ---------------------------------------------------------------------------
## 分层门禁：SDK 只能收在越来越少的包里
## ---------------------------------------------------------------------------

## 分层 ratchet：Eino 逐包基线只许降、新引入的包直接红；handler 体量与 setter 数同样只许降。
## 数字来自 `go test -count=1 -v -run TestEinoImportsOnlyShrink ./internal/layering/` 的日志。
.PHONY: layering-check
layering-check:
	$(GO) test -count=1 -run 'TestEinoImportsOnlyShrink|TestHandler|TestNarrowedFields|TestDatabaseSurfaceOnlyShrinks|TestDatabaseSurfaceHasNoUnreachableMethods|TestConsumerSurfacesDeclareOnlyCalledMethods' ./internal/layering/

.PHONY: wiring-check
wiring-check:
	$(GO) test -count=1 -run 'TestEveryAuditableHandlerIsAuditBound|TestBindAuditReachesTheSetter' ./internal/app/
	$(GO) test -count=1 -run 'TestNarrowedStorage|TestNarrowRejects' ./internal/handler/ ./internal/database/
	## 热插拔接线：装配必须装好活配置快照并发布角色目录；内置能力身份必须与既有加载器一致
	$(GO) test -count=1 -run 'TestAssemblyInstalls|TestShipped|TestEveryShipped|TestExampleBundles' ./internal/app/
	$(GO) test -count=1 -run 'TestBootPublish|TestBundledRole|TestRoleAPI|TestRoleCreateUpdateDelete' ./internal/handler/
	## skill：以厂商 backend 为真相源比对 + 装包后立刻可见 + 空 skills_dir 行为不变
	$(GO) test -count=1 ./internal/einoskill/
	$(GO) test -count=1 -run 'TestPrepareEinoAgenticSkills' ./internal/multiagent/
	## 一键安装接口：装完即生效 / 越界路径 400 / 冲突 409 指名 / 启停不动文件 / 包拥有的单元不可摘
	$(GO) test -count=1 -run 'TestPlugin' ./internal/handler/
	## markdown agent：路径驱动与目录驱动逐字节一致、包内定义只读、建删同步进表
	$(GO) test -count=1 -timeout 200s ./internal/agents/ -run 'TestLoadMarkdownAgentPaths|TestLoadMarkdownAgents|TestEmptyTable|TestDisabledAgent'
	$(GO) test -count=1 -timeout 200s ./internal/handler/ -run 'TestMarkdownAgentList|TestBundledMarkdownAgent|TestCreatedMarkdownAgent'
	## 远端 MCP 工具身份：按服务器成组装卸、按工具判定、清单缺失时回到命名空间策略
	$(GO) test -count=1 ./internal/capability/ -run 'TestRegisterSubset|TestSubsetEntries'
	$(GO) test -count=1 ./internal/app/ -run 'TestRemote|TestEmptyInventory'
	## tool 配方：表驱动与目录加载逐条一致、开关只收窄不改文件、装包立刻成为可执行工具
	$(GO) test -count=1 -timeout 200s ./internal/handler/ -run 'TestToolLayer|TestRebuildToolLayer|TestSaveConfigDoesNot|TestEveryKindReports'
	## MCP 声明：装包/启动都只写声明、进程一律由单元开关拉起；配置文件同名则文件优先且控制台如实标灰；
	## 应用配置不得清空包声明，MCP 页不得改写包声明（否则会把空 servers.<name> 写进 config.yaml）
	$(GO) test -count=1 -run 'TestBootDeclaresPackServers' ./internal/app/
	$(GO) test -count=1 -run 'TestPackDeclaration|TestReloadKeepsPackServers|TestOperatorSideWrites' ./internal/mcp/
	$(GO) test -count=1 -run 'TestPackMCPDeclaration|TestLoadMCPDeclaration|TestExternalMCPPageCannotMutate|TestUninstallDoesNotRemove|TestPluginConsoleReportsAShadowed' ./internal/handler/
	## 单元开关的持久化：只落"停用"、启动后按源路径复核、过期行清理；包声明的 MCP 开关如实说明重启回到停用
	$(GO) test -count=1 ./internal/store/ -run 'TestSwitch|TestForget'
	$(GO) test -count=1 -run 'TestPersistedSwitches|TestPersistedSwitchOn|TestStaleSwitchRows|TestSwitchStoreAccepts|TestAssembly' ./internal/app/
	$(GO) test -count=1 -run 'TestPluginUnitSwitchIsRemembered|TestPluginMCPSwitchStates|TestPluginRemovalForgets|TestPluginSwitchReports' ./internal/handler/
	## 启动扫描必须覆盖每个"运行路径读表"的 kind：漏一行，装任何一个包就会把整批内置配方换掉
	$(GO) test -count=1 -run 'TestBuiltInCapabilityScanCoversEveryServedKind' ./internal/app/
	## 能力包的两种视图必须同源：包自带的单元副本要与表里的状态一致（真机点验抓到的分叉）
	$(GO) test -count=1 -run 'TestBundleViewsFollowTheUnitSwitch' ./internal/plugin/

## 前端契约测试：169 条，全部按源码形状断言。它们此前没有 runner，所以三次改名/搬家把断言
## 锚点挪走之后没人发现（实测：两条在上游提交 470eb5e 上就已经是红的，一条是本轮把裸 SQL
## 搬去 store 层留下的）。现在三条都已对着**新位置**重写并全绿；缺 node 时硬失败，不静默跳过。
.PHONY: js-check
js-check:
	@command -v node >/dev/null 2>&1 || { echo "node missing: the front-end contract tests need Node 18+"; exit 1; }
	@## Parse every console script before running any test. The contract tests each load the
	@## handful of functions they assert on, so a file the browser cannot parse at all - a stray
	@## brace, which is how a hand-edited one-liner replacement once broke 通知 and the whole
	@## 一键更新 page silently, since the container stayed display:none - passes 202 tests and
	@## only shows up when a person opens the page.
	@for f in web/static/js/*.js web/static/js/generated/*.js; do \
	  node --check "$$f" >/dev/null || { echo "js parse failed: $$f"; node --check "$$f"; exit 1; }; \
	done
	node --test web/static/js/*.test.cjs

## ---------------------------------------------------------------------------
## 开发树 / 测试树隔离
##
## 开发树只放源码：编译产物、跑起来的服务、它写的 config.yaml / data/ / log/ 全部留在
## 测试树（默认 ~/csai-测试版，本仓库的一个 clone）。这样"顺手 build"不会在重构现场留下
## 可能被误提交的二进制或临时库。
##
## 一致性按**文件内容**比（tracked + 未被忽略的新文件），不按 git HEAD 比：调试期间的修改
## 常常还没提交，而要求就是"两边一起改"。测试树自己那份 config.yaml/data/ 是被忽略的运行时
## 文件，既不参与比对，也不会被 sync 删除。
## ---------------------------------------------------------------------------

TESTTREE ?= $(HOME)/csai-测试版

.PHONY: test-verify
test-verify: ## 两树源码是否同一份（逐文件摘要比对）
	@CSAI_TESTTREE="$(TESTTREE)" ./scripts/testtree.sh verify

.PHONY: test-sync
test-sync: ## 把开发树源码灌进测试树（并删掉测试树里开发树已没有的源码文件）
	@CSAI_TESTTREE="$(TESTTREE)" ./scripts/testtree.sh sync

.PHONY: test-gates
test-gates: ## 在测试树里 sync + verify + 全套门禁 + build，开发树不产生二进制
	@CSAI_TESTTREE="$(TESTTREE)" ./scripts/testtree.sh gates

.PHONY: test-run
test-run: ## 在测试树里 build 并起服务（它自己的端口与数据库）
	@CSAI_TESTTREE="$(TESTTREE)" ./scripts/testtree.sh run

.PHONY: ci
ci: fmt-check vet test-race js-check lint arch-lint layering-check wiring-check
	$(GO) build $(PKG)

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: clean
clean:
	rm -f $(BIN) cyberstrike-mcp-stdio
