# Capability platform: identity, manifest, policy pipeline

This describes what is **implemented** (research background: `capability-platform-decoupling-research.md`).
The full inventory is generated — `docs/zh-CN/capability-catalog.md`, produced by `make generate`, never edited by hand.

## 1. The invariant

**A capability with no registered manifest cannot execute.**

Previously any non-builtin tool fell through to the coarse `agent:local-execute` permission,
and exactly the recipes that `exec()` a model-supplied string (`exec`, `angr`, `pwntools`)
lived in that fallback — prompt injection was enough to reach host code execution.
There is now one decision entry point on the execution path: the registry and evaluator in
`internal/capability`. A lookup miss is a denial, never a fallback.

## 2. Identity

`publisher.capability.name`, lowercase, dot-separated. `core.*` is reserved for the shipped binary.

The same string is used by permission declarations, approval records, revocation lists, the
generated catalog, and store submissions. `CheckTyposquat` rejects a submission within edit
distance 2 of an existing identity, so `cora.nmap` cannot be published next to `core.nmap`.

Wire-level tool names (`nmap`, `c2_task`) are unchanged; identity is an added layer, not a rename.

## 3. Class and the approval floor

| class | meaning | approval | permission |
|---|---|---|---|
| `readonly` | observes state, changes nothing | exemptible | its `:read` |
| `mutating` | changes local state or probes a target non-destructively | per session HITL | its `:write` / `agent:local-execute` |
| `destructive` | runs model-supplied code, builds payloads, dispatches implant tasks, deletes data | **always requires a human decision; no whitelist can exempt it** | a dedicated permission (`agent:destructive-execute` for recipes) |

A `destructive` capability may not use the `agent:local-execute` fallback. That is a **load-time**
failure, not a runtime convention, so a submitted artifact cannot escalate by reusing a permission
that already exists in the deployment.

The floor is released by `capability.ApprovalLedger`: single-use, 60-second expiry by default,
bound to `(conversation, capability identity)`. One human approval releases exactly one invocation.
The only writer is the HITL layer when an approver accepts
(`handler.TrackApprovedHitlExecution`); request bodies, roles, skills and store artifacts cannot
add to it.

## 4. New semantics for the no-approval whitelist

Intersection, not union. A tool is exempt only when it appears in **both**:

1. `hitl.tool_whitelist` in `config.yaml` (operator-owned; writing it requires `config:write`, i.e. admin), and
2. the tool set submitted for the session.

A session request body can now only *narrow* exemptions. `destructive` capabilities are never
exempt, even when both lists name them.

## 5. Adding a tool: zero Go files for recipes

Add a `capability:` block to `tools/<name>.yaml`:

```yaml
capability:
  id: "core.nmap"                 # or <publisher>.<name>
  version: "1.0.0"
  class: "mutating"               # readonly | mutating | destructive
  permission: "agent:local-execute"
  approval: "inherited"           # never (readonly only) | inherited | always
  runtime: "recipe:exec"
  grants:                         # a ceiling on mediated side effects, not a request
    - "process.exec(nmap)"
    - "net.connect(target)"
  evidence: false
  timeout_seconds: 0
```

Constraints enforced at load time (the tool stays visible but unexecutable when violated):

- `class: destructive` ⇒ `approval` may not be `never`, and `permission` may not be `agent:local-execute`
- `class: readonly` ⇒ may not set `approval: always`
- `runtime: recipe:exec` ⇒ at least one `grants` entry
- `id` must be a valid lowercase dotted identity

A built-in Go tool needs one row in `internal/capability/policy_builtin.go`. Names stay in
`internal/mcp/builtin/constants.go`; `TestDeclaredConstantNamesHavePolicies` parses that file's string
literals and asserts both directions agree, so a fourth hand-maintained list cannot appear.

## 6. One manifest, several downstream artifacts

`make generate` builds from the policy table plus the recipe manifests:

- `web/static/js/generated/capability-catalog.js` — `window.CSAI.capabilities`,
  `capabilityNames`, `capabilityByName` (replaces hand-mirrored frontend tool-name enums)
- `web/static/js/generated/capability-catalog.json` — same data, machine-readable
- `internal/capability/testdata/catalog.golden.json` — drift baseline
- `docs/zh-CN/capability-catalog.md` — human-readable inventory for reviewers

`TestGeneratedCatalogIsUpToDate` plus the CI regenerate-and-diff step fail when a manifest changed
and the artifacts did not.

Argument JSON Schema is generated from the same `parameters:` list (`capability.JSONSchema`) and
drives `capability.ValidateArgs`: a missing required argument now produces an argument error, not a
permission error.

## 7. Assembly point

There is one. `internal/app.InstallCapabilityRegistry` assembles at startup; `cmd/mcp-stdio` must call
`app.InstallStdioPolicy` to reuse the same pipeline, with its identity coming from the `mcp_stdio`
section of `config.yaml`. stdio has no human approval channel, so `destructive` capabilities cannot
execute there; with no `mcp_stdio.permissions` declared the process refuses every tool call.

`mcp.Server.SetRequestContextDecorator` is how an entry point binds identity, so no entry point needs
its own assembly code.

## 8. Plugin runtime

`internal/pluginhost` is where a `runtime: plugin-host:*` capability executes.

- ABI: line-framed JSON-RPC, version constant `csai-plugin/1`. The host calls
  `initialize` / `capabilities/list` / `capabilities/invoke` / `shutdown`; a plugin may
  only call back into `host/grant_check` / `host/log` / `host/progress`, everything else
  is method-not-found. Reference implementation: `internal/pluginhost/testdata/refplugin`.
- Isolation: one child process per trust domain (the publisher namespace), lazy start,
  restart on the next call after a crash, idle reaping, under `processguard` cgroup/rlimit.
- Credentials: the environment is inherited from an allowlist, and any key whose name
  contains `KEY`, `TOKEN`, `SECRET` or `PASSWORD` is refused outright.
- Egress: the child only receives the host-side CONNECT proxy address. What actually gets
  allowed is **manifest `grants` ∩ the operator-approved `(host, ports, method, valid_minutes)`
  tuples**, and an empty approved set denies everything. Neither a store artifact nor the
  plugin can widen that set.
- Unconfigured means refused: with `plugin_host.enabled: false`, a capability declaring a
  plugin runtime fails at execution rather than falling back to running the recipe in-process.

Limitation worth stating: the proxy governs traffic that uses it. Go deliberately never
proxies loopback and raw sockets bypass it, so this is not an OS-level network boundary.
A hard boundary needs a per-instance network namespace, which `processguard` does not
provide. This repository also does not bundle a Python runtime - `plugin-host:python`
expects an interpreter and plugin binary to be supplied.

See the `plugin_host` section of `config.example.yaml`.

## 9. Artifact trust, revocation and the capability-delta gate

`internal/artifact` implements the decision and client-enforcement half of a store; the
registry service itself is not built.

- **The signature covers every security field**: `Manifest.CanonicalBytes()` includes
  id/version/class/permission/approval/runtime/grants/payload digests/publisher, so editing
  a class or adding a grant after approval invalidates the signature.
- **There is no `ignoreUnverified`**: unsigned, unknown-publisher and key-mismatch cases are
  all refused. Executable artifacts must be readable (`File.Text`) - deliberately not copying
  the reference product's plugin encryption, because code that reaches operator credentials has
  to be auditable.
- **Revocation applies at two moments**: loaded at startup, then re-checked by an evaluator
  stage before every call. A hit isolates the capability from the registry so the model cannot
  even see it; publisher-level revocation covers every artifact from that publisher. `Merge`
  never un-revokes, and a malformed list is an error rather than a silently empty one.
- **Provenance is stamped by the installer** after verification - there is no digest field a
  recipe can fill in - and the ledger lives outside the registry, so applying config cannot
  erase the basis for a revocation match.
- **Capability-delta gate**: against the approved baseline, any new grant, class escalation or
  permission change requires an independent second reviewer, and the author may not review
  their own submission. An identical manifest is auto-rescanned; narrowing capabilities does
  not trigger re-review, so human time goes to increases.
- **Static scan** blocks instruction overrides, exfiltration requests, private-key/token
  material, `curl | sh`, `` !`cmd` `` dynamic context, role-tag smuggling and control characters.
- **`SanitizeForIndex`** strips URLs, IPs and CIDRs before embedding.
- **Quarantine** moves content aside and keeps it visible for triage instead of deleting it.

## 10. Content privilege hierarchy (community knowledge, personas, prompts)

`internal/contentpolicy` implements the code-level controls from research section 6. The
reason is direct: retrieved knowledge enters the context of an agent that can operate a real
C2 and webshell, and published results show a small number of poisoned documents is enough.
So this is structure, not a wording request:

- **Tag**: `[[csai:untrusted-advisory]]`, written together with a fixed preamble.
- **One tagged exit**: `RetrievalResult.AdvisoryContent()` is the only way chunk text reaches
  model view; both the MCP tool result and the Eino retriever exit go through it, and a
  source-scan test forbids any unfenced write. Fencing is idempotent.
- **Assembly guard**: `newEinoAgenticChatModelAgent` calls `GuardDecisionPath` on the
  `Instruction`, so tagged content in an operator-controlled channel refuses to build the agent.
- **Rendering never executes**: bang-backtick interpolation, `![x](url)` transclusion, role tags,
  control characters and block terminators are stripped.
- **Poisoning stopped at ingest**: `RefuseIngest` rejects instruction-shaped text before the
  knowledge item is written.
- **Index sanitisation**: `StripForIndex` removes URLs, IPs and CIDRs.

Trade-off stated explicitly: role, skill and markdown-agent text is treated as approved,
installed, operator-consented configuration rather than a runtime-untrusted injection surface;
community knowledge text is the unbounded surface. CaMeL-style dual-model separation is not
implemented.

## 11. Provider dialect layer

`internal/provider` holds the only vendor decision data. **A dialect is not a vendor**:
there are three wire families (`openai-chat`, `openai-responses`, `anthropic-messages`) and
vendors are rows in a table.

- Each `Dialect` carries `API / BaseURL / DefaultBaseURL / ContextWindow / Cost / Capabilities / Retry / OverflowMarkers / AliasesTo`.
- `Resolve(vendor, baseURL)` degrades an unknown name to the chat dialect, so a new gateway
  name in a deployment cannot stop the service from starting.
- Call sites no longer compare provider strings. They ask `AgenticBackendSupported`,
  `IsAnthropicMessagesVendor`, `EffectiveProviderName`, `DefaultBaseURLFor`, `ListsModels`,
  and `ClassifyError(status, body)`.
- **Adding a vendor is a table row** (`Catalog.Register`), not a code change at any call site.
- The consistency suite is this layer's acceptance gate and the only objective basis for
  deciding whether two near-duplicate implementations can be merged: the five scenarios
  (abort, context overflow, tool-call-without-result, unicode surrogates, cross-provider
  handoff) run against **every row** of the catalog.

Rerank provider names are a **separate namespace**; do not fold them into the model dialect
table (noted in `config.go`).

## 12. Capability units and hot-plug

Six kinds of extension - roles, skills, markdown agents, tool recipes, MCP declarations and plugin
binaries - each used to have their own lifecycle, and **none of them could change without a
restart**. They now share
one identity scheme and one live table:

- `internal/plugin`: a `Unit` (identity `<kind>/<name>` plus source path plus install-time digest)
  and a `Bundle` (a set of units installed and removed together). Readers get an **immutable
  snapshot behind an atomic pointer** (lock-free); one mutex serialises writers only.
- Conflicts **refuse and name the owner** (`*ErrConflict`) instead of overwriting: a bundle cannot
  shadow a shipped capability, a directory scan cannot shadow an installed bundle, re-installing the
  same id is an upgrade that reclaims only its own previous units, and unplugging detaches without
  deleting any file.
- `plugin` is the **only kind that ships executable code**: the declaration's `capabilities` list is
  the reviewed set of entry points, and switching the unit on makes the host start the binary, call
  `capabilities/list` and compare **both directions** before anything is registered in
  `capability.LayerPlugin`. An entry point the binary does not provide, or one it provides that
  nobody wrote down, refuses the whole unit, reverts the switch and removes the trust domain it had
  just declared. `class`, `permission` and `grants` come from the declaration file, never from the
  plugin's self-description - a component that could describe itself could describe its way into
  `destructive`. Installing declares nothing and starts nothing, and a switch is never replayed as
  "on" after a restart so that an updated pack cannot run code nobody re-approved. Directory shape
  and the full rule set are in `bundles/README.md`.
- `bundles/<id>/bundle.yaml` is the shape of **packaging by role** (role + sub-agent + skills +
  tools); paths are confined to the bundle directory by `skillpackage.SafeRelPath` and `version` is
  mandatory, because a pack without one cannot be upgraded or rolled back. Format and ownership
  rules: `bundles/README.md`. Four role-shaped packs ship with the repository -
  `mobile-app-security`, `ai-app-redteam`, `source-code-audit` (which carries a semgrep recipe, so the
  tool path has shipped content proving it) and `wireless-hardware`. Every unit of every pack is read
  back through the **existing loader** (role YAML, markdown agent, recipe capability manifest) by
  `TestExampleBundlesInstallAlongsideShippedCapabilities`, rather than compared against the table that
  derived it.
- **Shipped capabilities go through the same table**: `roles/ agents/ skills/ tools/` are scanned
  into units whose identities match the existing loaders entry for entry (measured 142: 13 roles /
  16 agents / 23 skills / 90 tools), pinned by `internal/app/plugin_parity_test.go` - the truth
  source is those loaders, not a hand-written list.
- The safety of hot-swap is **demonstrated, not argued**: replacing the copy-on-write clone with an
  in-place write makes `TestConcurrentReadersNeverTear` report the write-vs-iterate race under
  `-race`. That in-place pattern is precisely what the role API used to do, including allocating
  the map inside a GET.
- Roles are wired through to the run path: a write is "file -> unit -> publish a new snapshot" and
  a read is `currentRoles(h.config)` (`internal/handler/live_config.go`). If assembly forgets to
  install the live store, `make wiring-check` fails - and that omission **compiles cleanly with the
  enabled-path tests green**, which is why it has to be a gate.
- The one-click surface **and its page** exist: `GET /api/plugins` (per-unit `served`, plus `generation` and
  `drift`) and `GET /api/plugins/available` - the catalogue a console needs before "one click" can be a
  button at all - with the console page under Platform management driving install / unplug / enable
  against that same table. Clicking it caught two defects no unit test could see: a toast helper that
  does not exist on that page (so a successful install rendered as "install failed"), and a switch
  button that passed three arguments as one JSON array (the server received
  `/units/tool,semgrep,false/undefined/enabled`), which is why `plugins-ui.test.cjs` now *executes* the
  rendered `onclick` text instead of only parsing it. The endpoints are:
  `drift`), `POST /api/plugins/install`, `DELETE /api/plugins/bundles/{id}`,
  `POST /api/plugins/units/{kind}/{name}/enabled`, `DELETE /api/plugins/units/{kind}/{name}`.
  Installs are confined to `<configDir>/bundles` (`../` and absolute paths are 400), and unit
  identities contain a slash, so the routes split into `:kind/:name` - one escaped segment would
  be unescaped by gin before matching and would never hit. Every mutation republishes the role
  catalog, so the next request already sees the change.
  A pack that was installed is re-installed into the capability table on the next start-up
  (`installBundlesFromDisk`, built-ins scanned first so a pack that shadows a shipped identity is
  still refused by identity), and a pack carrying a recipe rebuilds the tool layer at the end of
  boot - without that, "install" would only mean "until the next restart". Verified by killing the
  process once and re-reading the served counts, not by reasoning.
- For MCP the missing piece was **identity**, not liveness (adding, removing, starting and
  stopping an external server was already hot). `ExternalMCPManager` now reports each server's
  real tool inventory to `internal/app/remote_capabilities.go`, which registers, replaces and
  drops it in `capability.LayerRemote` **one group per server** (identity
  `remote.<server>.<tool>`; `Name` is the wire form the executor authorizes,
  `<server>::<tool>`). The permission and the global-scope floor are inherited from the existing
  namespace policy, so this changes nobody's reach - it makes a single remote tool nameable by a
  rule, an approval prompt and an audit row. Tests pin both the per-tool override (a sibling tool
  of the same server is unaffected) and the fallback (an inventory that has not arrived yet is
  decided by the declared namespace policy, not waved through as unknown).
- **The server declarations themselves moved onto the table** (`mcp/<name>` units, shipped as
  `bundles/<id>/mcp/*.yaml`). Installing a pack writes into the same live manager that
  `/api/external-mcp/*` drives, so there is one server list rather than two. Four rules, each with
  a test: installing and cold-start-up **declare but never start** (the server arrives disabled in
  both the table and the manager, and the unit switch is what spawns the process - `enabled: true`
  in a pack file is the pack author's intent, not the operator's consent to run a command); a pack
  **cannot take over** a server the operator's `config.yaml` already declares (the live manager is
  consulted before anything changes and a collision is a 409 naming the server, and `LoadConfigs` -
  应用配置 - gives the file precedence on a collision, so the pack loses the live slot); the
  reverse is refused too - the four MCP-page endpoints answer 409 with the owning pack for a
  pack-owned name, because they only know a name, and 启动 would read the file's entry that does
  not exist and save the resulting empty value back, turning a working server into a declaration
  that can never connect; and pack declarations get **no `${VAR}` expansion**, because one
  `Authorization: "Bearer ${CSAI_LLM_API_KEY}"` would ship a credential to a server the pack author
  chose. The live map is rebuilt from the file and then overlaid with the pack set, so 应用配置
  cannot erase a pack declaration; unplug removes only servers the pack declared (`PackOwner` that
  points elsewhere is skipped and named in the response).
  **The unit switch is now durable.** A bundle's files cannot be edited, so the console's switch had
  nowhere to live: the table was rebuilt from disk every start-up and every unit a pack owned came
  back enabled, which un-switched whatever the operator had switched off. Saved decisions live in
  `capability_unit_switches` and are re-applied after the packs are, and only in the narrowing
  direction - a saved "on" cannot enable a unit whose file disables it, the same
  `file enabled AND table enabled` rule the tool layer runs. Each row carries the source path it was
  about, so a row for a gone identity or a moved file is pruned instead of applied, and unplug and
  detach name what they forget. MCP is the deliberate exception: start-up re-declares those servers
  disabled regardless of any row, so that one switch answers `switch_persisted:false` and points at
  `config.yaml`, where a server that must survive restarts belongs.
- `served:false` only tells the truth. Every one of the six kinds either reads the table on its run
  path or is deliberately "served by the switch", so
  `servedKinds` is the same size as `plugin.Kinds` and `TestEveryKindReportsItsActualServedState`
  pins both directions (drop a kind and it is red; add a kind to `plugin.Kinds` without wiring it and
  it is red). For MCP there is a second condition the table cannot see: the live manager must
  **still hold** the declaration, so after `config.yaml` claims the same name the console marks the
  unit not-served with the reason instead of going on reporting it served.
  Letting "installed" read as "in effect" is the exact failure this layer exists to prevent - and the
  flag itself rots if nobody watches it: the running server reported `[('role', True), ('agent',
  False), ('skill', True)]` after the agent run path had already moved onto the table, and no test
  caught it because the fixture pack had no agent unit. The pack now ships `agents/report-analyst.md`,
  install asserts `served=true`, and uninstall asserts the unit leaves the table - presence asserted
  before absence, so the second half cannot be passing because the unit was never there.
- Skills are wired through too, and only after **replacing a vendor implementation**: Eino's own
  backend accepts one `BaseDir`, so a skill inside a bundle was structurally unreachable.
  `internal/einoskill` implements that two-method backend over the capability table instead (no
  symlink farm, no copying of somebody else's files), and `internal/multiagent` prefers it whenever
  a table is installed. Swapping a vendor component is guarded by comparing against the vendor as
  the truth source - `TestBackendMatchesEinoBackend` checks front matter, body and base directory
  for all 23 shipped skills - and `TestTabInBodyIsNotStripped` blocks the tempting copy of the
  vendor's `stripLineNumbers`, which exists only because *its* local backend prefixes lines with
  `N\t`; applying it to bytes read straight from disk truncates every real tab. The **admin
  console** moved onto the table in the same step: `GET /api/skills`, the detail view and the file
  read/write paths all resolve directories through it, and writing to a bundle-provided skill is a
  409 naming its owner instead of a same-named shadow copy in the built-in directory. Wiring only
  the run path leaves "a bundled skill is used by agents but invisible in the list" - that state
  was measured on the running server (23 → 24 after install → 23 after unplug), not inferred.
  Markdown agents are wired through as well: `agents.LoadMarkdownAgentPaths` shares one parser with
  the directory scan - parity is pinned by loading all 16 shipped `.md` files both ways and
  requiring byte-identical results - and both the run path and the admin console use it, while
  create/delete sync the table and a bundle-provided definition is readable but not writable
  (409 naming its owner). A side effect of the identity scheme: a bundle cannot smuggle in a
  second `orchestrator.md`, because the built-in scan already owns `agent/orchestrator` and the
  install is refused long before the loader's "at most one orchestrator" rule could turn it into a
  failure for every run. Tool recipes were the last run path that read a directory instead of the
  table: `ToolLayer.Rebuild()` (a collaborator on `ConfigHandler.Tools`) is now the single rebuild entry for the recipe list and
  performs the same three steps `/config/apply` always performed - read paths from the table (fall
  back to the directory scan when the table holds no tool units), rebuild the recipe capability
  layer, then `ClearTools` and re-register the whole tool surface. Install/unplug/switch trigger it
  only when a tool unit is involved, because `ClearTools` wipes every built-in tool too, and the
  sequence is serialised so one rebuild's `ClearTools` cannot land inside another's window. Two
  rules are pinned by tests rather than prose: the live state is `file enabled AND table enabled`
  (a unit's default-true flag treated as an override would silently switch on recipes whose YAML
  says `enabled: false`), and the `PUT /config` loop that persists each tool's `enabled` back into
  its own file must skip table-switched-off tools, or one runtime disable becomes permanent. Parity
  over the 90 shipped recipes compares both sources entry by entry - names, order, enable flags.
  A missed `KindTool` row in the boot scan does not error; it turns "install any pack" into
  "replace the whole built-in recipe list with that pack's one entry", so
  `TestBuiltInCapabilityScanCoversEveryServedKind` also requires every kind in `plugin.Kinds` to be
  scanned or explicitly exempted with a reason. `bundles/README.md` states the remaining gap per
  kind.

## 13. Updating the installation itself

Two surfaces make a machine "different from the release", and they are not the same thing - the
boundary is written into the implementation:

- **Bundles** (`plugins:install`) change the **capability surface**: units land in `bundles/`, are
  written to the capability table, and MCP servers a pack declares are recorded, never started.
- **One-click update** (`update:apply`) changes the **code**: it fast-forwards the remote this
  directory itself tracks, runs `go build` and swaps the binary atomically (keeping the old one as
  `cyberstrike-ai.prev`). The remote is chosen among the ones this tree already has, in the order
  `mine`, `origin`, `upstream` - no third-party repository is hardcoded anywhere.

The two do not cross, and that is a design requirement rather than an accident: the update does not
touch the capability table, and it does not skip protection because a file was "declared by a pack".
It works purely off the protected list (`roles/ skills/ tools/ agents/ bundles/ knowledge_base/ data/
log/ venv/ config.yaml .env`), first copying aside only the paths this update would actually write
into `.update-backup/<timestamp>/`, then putting them back after the merge, naming each one in
`keptContent` instead of dropping it silently. The table lives under `data/`, so installing a pack
survives an update of the code. A bundle in turn never decides where the code comes from.
For the refusal semantics (local source edits, diverged branch, missing Go toolchain, non-git
directory), the three entry points (page / REST / CLI), rollback and the two restart cases, see the
one-click update section of [the developer guide](developer-guide.md).

## 14. Not implemented yet

- P6 remainder: per-domain Store extraction (`internal/store` already owns notification reads, the
  `hitl_interrupts` surface the HTTP layer uses, the shared conversation-visibility clause, and the
  `messages` writes - which were one UPDATE copied to 15 sites across six files plus a twin CASE
  append differing by a single clause; the digest's two remaining cross-domain reads now live in
  `store.Vulnerability` and `store.Execution`, so the transport layer assembles **no SQL at all**
  (it started at 49), and the HITL/session/notification-read tables are pinned to a single writer
  by a repository-wide ownership test; **the narrow interfaces are no longer a paper contract - they
  are this layer's hard invariant** - eighteen domains now hold their own store interface as the field
  type, one dead field was deleted outright (`KnowledgeHandler.db`, never read: the honest fix was to
  drop the field and its constructor parameter rather than invent a store for it), and `internal/handler`
  went from 19 structs holding `*database.DB` to **zero** (zero `*sql.DB` fields as well). The gate
  flipped from a ratchet to `TestHandlerLayerHoldsNoGodObject`, which fails on any single occurrence and
  guards its own emptiness by requiring the walk to have seen ~990 struct fields, plus a shape gate
  (`TestNarrowedFieldsAreOnlyAssignedThroughNarrow`) asserting every assignment to a narrowed field goes
  through `database.Narrow`. Both run in `make layering-check`. Every one of those fields is assigned through `database.Narrow`, which is
  load-bearing rather than cosmetic: `var store AssetStore = (*DB)(nil)` is a *non-nil* interface, so a
  plain assignment would permanently invert all 64 `if h.db == nil` degradation guards in the transport
  layer - and that compiles, with every enabled-path test still green. This was not argued from theory:
  my first substitution matched only the single-spaced `db: db,` and missed five aligned assignments,
  and `TestRobotModeRejectsUnavailableMultiAgent` panicked on `(*DB).GetRobotSessionBinding` with a nil
  receiver. The last three structs fell to declaring the interface at the far end of each chain rather
  than faking one in the handler: `multiagent` turns out to call **no** database method itself and only
  forwards the handle to `internal/project`, so the surface belongs to `project` (13 methods - the
  project row plus the fact and fact-edge ledger); `agentfinalizer` needs two (read/save one tool
  execution); `attackchain` needs the chain rows, the conversation evidence, and the fact ledger its
  promotion path writes; the workflow engine needs its own run/node-run ledger plus project facts.
  Surfaces more than one package needs are declared once in `internal/database/surfaces.go` (every
  consumer imports database, so declaring them consumer-side would create a cycle) and aliased back as
  `project.Store`, `agentfinalizer.Store`, `attackchain.Store` - one method list, one
  `var _ X = (*DB)(nil)` assertion each, instead of copying thirteen signatures. `Close` is
  deliberately absent from all of them: a consumer of the shared handle must not be able to shut it
  down. The shape gate's own delivery story is worth keeping: its first version passed a probe it
  should have failed, because it rebuilt a module-relative path by re-prepending `internal/handler` -
  the file never opened, `continue` swallowed it, and an empty set read as "no violations". Every
  branch of a text-scoped gate now has to prove it saw something
  (`declares a narrowed storage field but no assignment to it was found`), and a second false
  positive - `if h.db == nil` guards parsed as assignments, since RE2 has no negative lookahead -
  had to be excluded explicitly. Two rules earned: **run both probes** (inject a violation, expect
  red; clean tree, expect green) for every new gate, and **never let a text judgement succeed on an
  empty set**. One counter-example argues the process worked: `batch_task_manager.go` has 22 methods on
  `m.db` and zero escapes, so swapping its field type compiled first try - that interface had been
  generated from its real surface all along),
  event-sourced sessions, and `AgentHandler` decomposition -
  which is now measured and gated instead of being a hunch (the provider catalog is generated and
  byte-gated: `internal/provider/publish.go` renders it into `docs/zh-CN/provider-catalog.md` plus
  `internal/provider/testdata/provider-catalog.golden.json`, and CI fails on drift):
  `AgentHandler` started at **130 methods
  across 23 files**; two cuts have since landed - 9 interrupt-queue read methods into `HITLQueue` and
  10 finalization methods into `runFinalizer` - so the ceiling is now **112 methods / 21 files**, and
  `internal/handler` as a whole declares **64 `Set*` injection methods over
  21 receiver types**, 18 of which are byte-identical copies of `SetAudit` (the report's "26 SetXxx
  / 19 files" underestimated both). Three only-down gates cover it (`make layering-check`: per-type
  method ceilings, a file ceiling, and a whole-package setter ceiling), one cohesion collapse has
  landed (the three HITL config savers became one collaborator and one setter), and the real risk in
  those 18 copies is now closed by an **audit-injection completeness gate**: every injection goes
  through `bindAudit`, and `TestEveryAuditableHandlerIsAuditBound` derives the required set by
  parsing the handler package, so a new auditable handler that is constructed but never bound fails
  CI. The consequence of a missed injection is exactly the kind that never shows up as an error -
  the endpoint keeps serving and writes no audit records at all. The 18 `SetAudit` methods were
  deliberately *not* merged into one embedded collaborator: it would flip the meaning of 89
  hand-written `if h.audit != nil` guards, and neither the compiler nor the existing tests report
  that kind of inversion. Catalog
  codegen, and merging the twin function pairs (the dialect consistency suite is now the objective
  judge for that). Converging Eino into one adapter package is now gated rather than merely
  intended: `internal/layering` pins a per-package file count outside the adapter packages (a
  brand-new importer fails outright, growth inside a debt package fails with the file list,
  shrinkage only asks to tighten the baseline), `make layering-check` runs in CI, and the water
  mark is now 6 importing packages down from 11, with the debt surface at 3 packages / 96 files
  from 8 / 100 while the total Eino-importing file count went 104 -> 105. Earlier slices were pure
  moves - code relocated into the boundary packages, nothing deleted, nothing grown - and the +1 is
  the one genuine addition: `internal/einoskill`, which exists because Eino's own skill backend
  accepts a single `BaseDir` and therefore cannot reach a skill that lives inside a bundle (see
  section 12). The debt surface - the side the acceptance criterion actually watches - did not
  regress. The code moved into
  the boundary packages rather than being deleted or grown. Four slices so far: `internal/vision`'s
  model call behind `llm.DescribeImage`, the Claude connection probe behind `llm.PingAgentic`, all
  of `internal/reasoning`'s rules behind its own `ChatModelTarget` interface with the SDK mapping
  in `internal/llm/reasoning_target.go`, and `internal/security`'s streaming shell behind its own
  `ShellEvent`/`ShellSink` with the ADK shim next to the wrapper that already existed. What is left
  (`multiagent`, `knowledge`, `workflow`) hosts the Eino orchestration itself, so shrinking it needs
  interface-ization rather than another move.
- P3 remainder: per-file ES modules (six giant scripts still duplicate their own helper copies,
  2,800-3,200 lines to dedupe); the persisted `process_details.eventType` names the page rebuilds
  its timeline from (35 of them) are now compared against a server-side inventory too; that
  inventory is precise for direct writes and local assignments but not for rows written through a
  progress-callback variable, so the "persisted yet unrendered" direction can under-report until
  the value graph learns that a callback variable holds a function body; and `POST /api/terminal/run/stream` has **no frontend consumer at all**
  (the terminal pane uses the WebSocket) - keeping that endpoint is an open decision.
  Done: the frame-level `type`, the event names carried inside progress callbacks (63 in total,
  49 of which are only provable from the callback's call sites), and the C2 stream's `category` -
  all three wire formats now come out of `internal/sse` with zero hand-assembled frames. The
  generated enum is loaded by `index.html` and checked per frame through `CSAI.isSSEEvent`, and the
  two-sided diff is pinned by only-go-down ratchets (2 events the page does not render, 1 dead
  branch in the page).
- The registry service: keyless signing at publish, staged rollout, release-age cooldowns.
- Air-gapped offline bundle export/import; automated sandbox detonation (gate 2 is recorded and
  required, not yet executed by the pipeline).
- netns/seccomp enforcement of `grants`; distribution of an embedded CPython.
- Deciding whether role, skill and markdown-agent text should also be runtime-untrusted.
- CaMeL-style dual-model control-flow separation (still paper-grade).
