# Self-update recovery and MCP edit fixes

## Intake (F0)

PR #346 was merged at 72cd8d62e36e536a6b18e9e10ea73d8d16294044 on explicit user direction to merge first and repair the reviewed defects afterwards. This is corrective work on branch `codex/self-update-recovery-fixes`; the checkout was clean. No release or deployment is requested.

Workflow gate: P10 feedback/iteration, implementation allowed. The frozen review at a63070d and executable PRR-002/PRR-007 counterexamples define the upstream contract; there are no missing product decisions. Acceptance requires preserved MCP stop state and refusal of ambiguous recovery before filesystem mutation.

Maintainability gate: `apply.go` has 1099 lines and combines recovery orchestration and filesystem IO; `external_mcp.go` has 508 lines. Risk is high for recovery. Allowed slice: narrow fix with a separate pure JSON/evidence validation module, small call-site changes, and focused regression tests. Broad refactoring is not required. Existing update/handler tests and reviewer reproductions cover the affected boundary.

## Requirements (F1, confirmed by the user's repair request)

- R1 / PRR-007: editing an existing MCP without an explicit enable/disable field preserves its runtime activation state. Explicit activation fields and Start/Stop remain supported; a new server defaults to enabled. Environment expansion remains isolated from persisted raw values.
- R2 / PRR-002: incomplete, null, duplicate-key, malformed, or contradictory recovery evidence cannot authorize a second swap, wrong rollback binary, or deletion of recovery records. Valid current records and the documented legacy marker retain safe recovery.
- Non-goals: redesigning update UX, changing release policy, or unrelated refactoring. The two nonblocking review observations are outside this corrective slice unless required by the evidence validation boundary.

## Design (F2)

Recovery remains an internal on-disk contract, with no new HTTP fields. Decode exact JSON field names and types, reject duplicate keys at every depth and null scalars, and reject unknown fields rather than interpreting a newer format as current evidence. The one documented plain-text legacy build marker remains supported by its existing conservative path.

For JSON build records require a target commit and exactly one proof of the old binary (SHA256 or explicit absence), validate hash/commit shapes, and reconcile the record with any persisted State for the same update or its immediate predecessor. Before restoring protected content or building, verify the live/previous files match the indicated pre-swap or post-swap phase. A successful State cannot be reinterpreted as an owed build.

Rollback records require an explicit restore_binary decision; reconcile it, the live/previous hashes and the nested State, then compare with the persisted State when it still exists. Missing persisted State is allowed after the final cleanup crash window, but the self-contained record must remain coherent. Invalid evidence returns an existing state_unreadable/bad_state error and preserves recovery files.

MCP request decoding retains field presence separately from bool values. For existing entries, omission preserves the manager's activation state; explicit disabled or external_mcp_enable fields determine it. New entries default to enabled. Keep raw configuration persistence and environment-expanded connection snapshots separate as today.

Compatibility: properly written current journals remain readable, legacy plain-text recovery remains guarded; hand-edited or unsupported JSON requires manual correction instead of guessed mutation. Rollback of this patch restores old behavior and is not recommended while invalid journals remain.

## Implementation plan (F3)

1. Add recovery evidence parser/validators in a separate internal/update module; add narrow calls in build/rollback readers and pre-mutation paths. Keep producer formats unchanged.
2. Preserve explicit field presence in the MCP request without changing its JSON shape; normalize enable state against the existing manager configuration.
3. Promote the two failing review reproductions into portable regression tests; add duplicate/null/type/contradiction matrices and valid retry controls. Assert no build/swap, unchanged binaries and preserved journals for refusals.
4. Run update/handler targeted suites and race checks, full Go tests, build, JS and shell checks; update changelog and verification record. Open a corrective PR and integrate the verified patch as part of the authorized merge-and-fix task, respecting repository checks.

Docs: this phase record plus concise behavior notes in deployment/developer documentation if needed. No screenshots or production external services are required; tests use local Git and harmless command/HTTP markers. No version bump, tag or release publication.

## Implementation (F4)

Implemented strict recovery decoding in recovery_evidence.go, with exact field names, nested duplicate/type/null checks, commit/hash validation and State reconciliation. Build recovery verifies pre-move versus post-move binary evidence before content restore and rechecks files after compilation. Fresh rollback and legacy-marker recovery also read State strictly. Self-contained rollback journals validate absence/hash coherence even after State cleanup, and actual absence is checked before any cleanup.

MCP edits distinguish omitted activation fields from explicit booleans. Omitted fields preserve the manager state; Start/Stop and persisted disabled are synchronized so restart retains the requested state. Existing environment snapshot expansion is preserved.

Tests added for malformed build/rollback journals, all independent review counterexamples, valid partial binary rename and final cleanup retries, and MCP stop/edit/start plus config reload. Targeted reproductions pass; original full Go, build, five-package race, and 277 JS checks passed before final evidence guard refinements. Final exact-patch validation follows in F5. No production external service was contacted.

### F4 follow-up: activation round-trip consistency

Final GET-to-edit inspection found that manager Start/Stop retained a stale disabled flag even after changing ExternalMCPEnable. Synchronize these two existing fields in the manager as well as persistence, and test the actual GET payload after Start followed by the UI-shaped edit. Maintainability assessment: external_manager.go is 1665 lines (high risk); this narrow fix adds only three assignments beside existing activation assignments, without new control flow or broad refactoring. The additional handler/MCP race suites validate this consistency change.

## Verification and user documentation (F5)

- Recovery code at fab7e603: `go test -race ./internal/update ./internal/handler` passed (84.580s / 16.692s). This includes malformed/null/duplicate/contradictory records, legacy and fresh State entry points, actual absence, pre-merge phase rejection and valid crash controls.
- Final activation consistency change at ff50af02: `go test -race ./internal/handler ./internal/mcp -count=1` passed (18.658s / 3.281s), including Stop/edit/reload/Start/GET/edit behavior. Independent targeted handler tests also passed.
- Full Go suite, `go build ./...`, 277 JS tests and shell syntax passed during implementation. A frozen-code full Go/build rerun and exact-head GitHub CI are tracked in F6 before integration.
- Independent fresh-context code review at ff50af02 found no remaining blockers. Six independent recovery counterexamples were promoted to permanent tests and passed. No acceptance relies solely on test status: refusals assert retained journals/binary data, and valid controls prove continued recovery.
- English and Chinese developer guides describe refusal/reconciliation behavior and MCP activation field precedence. No public request shape or release version changed.
- Limits: local validation is macOS arm64; Windows/Linux results come from CI. No production restart/deployment smoke or exhaustive disk fault testing was performed.

## Review and integration record (F6)

Corrective PR: https://github.com/AIPentest/CyberStrikeAI/pull/347 . Code snapshot ff50af028f87465954911533ac07b420c92767b8 passed independent review; subsequent changes are documentation/changelog only. PRR-002 and PRR-007 are closed by the recorded implementation and regression evidence. The Unreleased changelog records both user-visible fixes. Final integration requires exact-head cross-platform CI success and unchanged reviewed production code; no tag or deployment is part of this task.

The earlier nonblocking version-display crash window and recovery-path symlink observation from PR #346 remain follow-up items, outside the two requested blocker fixes. No broader reliability guarantee is inferred from these tests.
