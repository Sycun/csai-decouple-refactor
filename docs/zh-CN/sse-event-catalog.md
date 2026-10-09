# SSE 事件目录（生成物）

由 `make generate` 提取：服务端一侧扫 `internal/handler`（帧在这里拼装），
再并上 `internal` 全树的进度回调调用点（事件名多半拼在发射包之外）；
前端一侧扫 `web/static/js`（排除 `generated/`）。
注册表在 `internal/handler/sse_kinds.go`，写入器在 `internal/sse`：
未在此目录登记的事件名无法写到线上。

两条流各自一套契约：`agent` 是 `{type,message,data}`，`terminal` 是短键 `{t,d,c}`。

前端消费列由同一份生成器扫 `web/static/js` 得到：`switch (event.type)` 的 case、以及对帧变量（含 `_et` 这类别名）的比较。历史时间线读的是持久化 `eventType`，那是另一份契约，见文末。

| 事件名 | 流 | 发射点 | 前端消费点 |
|---|---|---|---|
| `cancelled` | agent | `agent_stream.go:97`(forwarded), `task_lifecycle.go:35`(forwarded), `internal/handler/eino_single_agent.go:313`(conduit), `internal/handler/multi_agent.go:327`(conduit), `internal/handler/workflow_integration.go:131`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3644` |
| `conversation` | agent | `agent_stream.go:97`(forwarded), `task_lifecycle.go:35`(forwarded), `internal/handler/eino_single_agent.go:65`(conduit), `internal/handler/multi_agent.go:75`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3553` |
| `done` | agent | `agent_stream.go:97`(forwarded), `task_lifecycle.go:35`(forwarded), `internal/handler/agent.go:1460`(handled), `internal/handler/eino_single_agent.go:34`(conduit), `internal/handler/multi_agent.go:34`(conduit), `internal/handler/task_lifecycle.go:26`(handled), `internal/handler/workflow_integration.go:88`(conduit), `internal/workflow/nodes.go:253`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3651` |
| `eino_agent_reply` | agent | `internal/multiagent/eino_sub_agent_reply_emitter.go:93`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3858` |
| `eino_agent_reply_stream_delta` | agent | `internal/handler/agent.go:1559`(handled), `internal/multiagent/eino_sub_agent_reply_emitter.go:56`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3811` |
| `eino_agent_reply_stream_end` | agent | `internal/handler/agent.go:1387`(handled), `internal/multiagent/eino_sub_agent_reply_emitter.go:72`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3836` |
| `eino_agent_reply_stream_start` | agent | `internal/handler/agent.go:1558`(handled), `internal/multiagent/eino_sub_agent_reply_emitter.go:48`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3794` |
| `eino_context_overflow_retry` | agent | `internal/multiagent/eino_context_overflow_retry.go:83`(conduit) | —（页面未分支） |
| `eino_empty_response_continue` | agent | `internal/handler/eino_empty_response_continue.go:57`(conduit) | `web/static/js/monitor.js:2968` |
| `eino_model_failover` | agent | `internal/multiagent/eino_model_resilience.go:528`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3631` |
| `eino_model_retry` | agent | `internal/multiagent/eino_native_model_retry_progress.go:25`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3625` |
| `eino_pending_orphaned` | agent | `internal/multiagent/eino_run_completion_handler.go:61`(conduit) | `web/static/js/monitor.js:2968` |
| `eino_run_retry` | agent | `internal/multiagent/eino_transient_run_retry_handler.go:136`(conduit) | `web/static/js/monitor.js:2968` |
| `eino_stream_error` | agent | `internal/multiagent/eino_stream_error_handler.go:60`(conduit) | `web/static/js/monitor.js:2968` |
| `eino_trace_end` | agent | `internal/einoobserve/attach.go:258`(conduit), `internal/handler/agent.go:1556`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/monitor.js:3224` |
| `eino_trace_error` | agent | `internal/einoobserve/attach.go:304`(conduit), `internal/handler/agent.go:1557`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/monitor.js:3225` |
| `eino_trace_run` | agent | `internal/einoobserve/attach.go:62`(conduit), `internal/handler/agent.go:1554`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/monitor.js:3222` |
| `eino_trace_start` | agent | `internal/einoobserve/attach.go:208`(conduit), `internal/handler/agent.go:1555`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/monitor.js:3223` |
| `eino_usage_summary` | agent | `internal/multiagent/eino_run_usage_accumulator.go:94`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3637` |
| `err` | terminal | `terminal_stream_windows.go:48`(struct) | —（页面未分支） |
| `error` | agent | `agent_stream.go:97`(forwarded), `task_lifecycle.go:31`(literal), `task_lifecycle.go:35`(forwarded), `internal/handler/eino_single_agent.go:33`(conduit), `internal/handler/multi_agent.go:33`(conduit), `internal/handler/task_lifecycle.go:31`(conduit), `internal/handler/workflow_integration.go:77`(conduit), `internal/multiagent/eino_adk_run_loop.go:211`(conduit), `internal/multiagent/eino_run_cancellation_handler.go:46`(conduit), `internal/multiagent/eino_run_error_handler.go:118`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3639` |
| `exit` | terminal | `terminal_stream_unix.go:21`(struct), `terminal_stream_unix.go:45`(struct), `terminal_stream_windows.go:17`(struct), `terminal_stream_windows.go:22`(struct), `terminal_stream_windows.go:26`(struct), `terminal_stream_windows.go:64`(struct) | —（页面未分支） |
| `finalization_auto_continue` | agent | `internal/handler/finalization_auto_continue.go:57`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3623` |
| `finalization_check` | agent | `agent_stream.go:97`(forwarded), `batch_queue_executor.go:319`(literal), `task_lifecycle.go:35`(forwarded), `internal/handler/batch_queue_executor.go:319`(conduit), `internal/handler/eino_single_agent.go:379`(conduit), `internal/handler/multi_agent.go:393`(conduit), `internal/handler/workflow_integration.go:180`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3620` |
| `finalization_pending_tools_cancelled` | agent | `internal/handler/finalization_auto_continue.go:111`(conduit) | `web/static/js/monitor.js:2968` |
| `heartbeat` | agent | `sse_keepalive.go:73`(literal) | `web/static/js/monitor.js:2968` |
| `hitl_audit_agent` | agent | `hitl.go:607`(literal) | `web/static/js/monitor.js:2968` |
| `hitl_audit_agent_started` | agent | `hitl.go:591`(literal) | `web/static/js/monitor.js:2968` |
| `hitl_interrupt` | agent | `hitl.go:650`(literal) | `web/static/js/monitor.js:2968` |
| `hitl_rejected` | agent | `hitl.go:621`(literal), `hitl.go:692`(literal) | `web/static/js/monitor.js:2968` |
| `hitl_resumed` | agent | `hitl.go:634`(literal), `hitl.go:706`(literal) | `web/static/js/monitor.js:2968` |
| `iteration` | agent | `internal/handler/eino_single_agent.go:170`(handled), `internal/handler/multi_agent.go:183`(handled), `internal/multiagent/eino_run_progress_tracker.go:135`(conduit), `internal/multiagent/runner.go:965`(conduit), `internal/workflow/nodes.go:258`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3663` |
| `iteration_limit_reached` | agent | `internal/multiagent/eino_run_error_handler.go:63`(conduit) | `web/static/js/monitor.js:2968` |
| `message_saved` | agent | `agent_stream.go:97`(forwarded), `task_lifecycle.go:35`(forwarded), `internal/handler/eino_single_agent.go:78`(conduit), `internal/handler/multi_agent.go:88`(conduit) | `web/static/js/monitor.js:2926` |
| `model_output_rejected` | agent | `internal/handler/agent.go:961`(handled) | —（页面未分支） |
| `out` | terminal | `terminal_stream_unix.go:32`(struct), `terminal_stream_windows.go:41`(struct) | —（页面未分支） |
| `progress` | agent | `agent_stream.go:97`(forwarded), `task_lifecycle.go:35`(forwarded), `internal/handler/agent.go:963`(handled), `internal/handler/eino_single_agent.go:103`(conduit), `internal/handler/multi_agent.go:113`(conduit), `internal/multiagent/eino_checkpoint_resume_handler.go:65`(conduit), `internal/multiagent/eino_initial_iterator_start_handler.go:48`(conduit), `internal/multiagent/eino_run_cancellation_handler.go:40`(conduit), `internal/multiagent/eino_run_progress_tracker.go:89`(conduit), `internal/multiagent/eino_turn_loop_event_bridge.go:79`(conduit), `internal/multiagent/eino_turn_loop_iterator_starter.go:149`(conduit), `internal/multiagent/runner.go:113`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3658` |
| `reasoning_chain` | agent | `internal/handler/agent.go:1528`(handled), `internal/multiagent/eino_reasoning_stream_emitter.go:103`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/monitor.js:3268`, `web/static/js/monitor.js:3269`, `web/static/js/monitor.js:3305`, `web/static/js/monitor.js:3327`, `web/static/js/monitor.js:3328`, `web/static/js/webshell.js:3726`, `web/static/js/webshell.js:3731` |
| `reasoning_chain_stream_delta` | agent | `internal/handler/agent.go:1496`(handled), `internal/multiagent/eino_reasoning_stream_emitter.go:70`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3697` |
| `reasoning_chain_stream_end` | agent | `internal/handler/agent.go:1467`(handled), `internal/multiagent/eino_reasoning_stream_emitter.go:84`(conduit) | `web/static/js/webshell.js:3715` |
| `reasoning_chain_stream_start` | agent | `internal/handler/agent.go:1474`(handled), `internal/multiagent/eino_reasoning_stream_emitter.go:62`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/monitor.js:3246`, `web/static/js/webshell.js:3677`, `web/static/js/webshell.js:3678` |
| `response` | agent | `agent_stream.go:97`(forwarded), `batch_queue_executor.go:321`(literal), `task_lifecycle.go:35`(forwarded), `internal/handler/agent.go:1455`(handled), `internal/handler/batch_queue_executor.go:321`(conduit), `internal/handler/eino_single_agent.go:383`(conduit), `internal/handler/multi_agent.go:397`(conduit), `internal/handler/workflow_integration.go:195`(conduit), `internal/workflow/nodes.go:253`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3593` |
| `response_delta` | agent | `internal/handler/agent.go:1423`(handled), `internal/multiagent/eino_main_response_stream_emitter.go:42`(conduit), `internal/workflow/nodes.go:253`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3577` |
| `response_start` | agent | `internal/handler/agent.go:1398`(handled), `internal/multiagent/eino_main_response_stream_emitter.go:62`(conduit), `internal/workflow/nodes.go:253`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3569` |
| `thinking` | agent | `internal/handler/agent.go:1528`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3726` |
| `thinking_stream_delta` | agent | `internal/handler/agent.go:1496`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3697` |
| `thinking_stream_end` | agent | `internal/handler/agent.go:1467`(handled) | `web/static/js/webshell.js:3715` |
| `thinking_stream_start` | agent | `internal/handler/agent.go:1474`(handled) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3677` |
| `tool_call` | agent | `internal/handler/agent.go:1156`(handled), `internal/multiagent/runner.go:1032`(conduit) | `web/static/js/monitor.js:2877`, `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3751` |
| `tool_calls_detected` | agent | `internal/multiagent/runner.go:994`(conduit) | `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3743` |
| `tool_result` | agent | `internal/handler/agent.go:1156`(handled), `internal/multiagent/eino_pending_tool_calls.go:112`(conduit), `internal/multiagent/eino_tool_result_progress_emitter.go:150`(conduit) | `web/static/js/monitor.js:2877`, `web/static/js/monitor.js:2968`, `web/static/js/webshell.js:3765` |
| `tool_result_delta` | agent | `internal/handler/agent.go:1178`(handled) | `web/static/js/monitor.js:2968` |
| `user_interrupt_continue` | agent | `internal/handler/eino_single_agent.go:270`(conduit), `internal/handler/multi_agent.go:284`(conduit), `internal/multiagent/eino_turn_loop_iterator_starter.go:141`(conduit) | `web/static/js/monitor.js:2968` |
| `workflow_agent_output` | agent | `internal/workflow/nodes.go:217`(conduit) | `web/static/js/monitor.js:2968` |
| `workflow_branch_skipped` | agent | `internal/workflow/node_exec.go:141`(assigned) | `web/static/js/monitor.js:2968` |
| `workflow_branch_taken` | agent | `internal/workflow/node_exec.go:141`(assigned) | `web/static/js/monitor.js:2968`, `web/static/js/monitor.js:3136` |
| `workflow_done` | agent | `internal/workflow/eino_compile.go:233`(conduit), `internal/workflow/runner.go:204`(conduit) | `web/static/js/monitor.js:2968` |
| `workflow_hitl_checkpoint` | agent | `internal/workflow/nodes.go:342`(conduit) | `web/static/js/monitor.js:2968` |
| `workflow_hitl_rejected` | agent | `internal/workflow/eino_compile.go:175`(conduit), `internal/workflow/runner.go:156`(conduit) | `web/static/js/monitor.js:2968` |
| `workflow_hitl_resumed` | agent | `internal/workflow/eino_compile.go:189`(conduit), `internal/workflow/runner.go:169`(conduit) | `web/static/js/monitor.js:2968` |
| `workflow_hitl_waiting` | agent | `internal/workflow/eino_compile.go:77`(conduit) | `web/static/js/monitor.js:2968` |
| `workflow_node_result` | agent | `internal/workflow/node_exec.go:106`(conduit) | `web/static/js/monitor.js:2968` |
| `workflow_node_start` | agent | `internal/workflow/node_exec.go:49`(conduit) | `web/static/js/monitor.js:2968` |
| `workflow_paused` | agent | `internal/workflow/runner.go:126`(conduit) | `web/static/js/monitor.js:2968` |
| `workflow_start` | agent | `internal/workflow/runner.go:78`(conduit) | `web/static/js/monitor.js:2968` |
| `workflow_tool_start` | agent | `internal/workflow/nodes.go:72`(conduit) | `web/static/js/monitor.js:2968` |

## 双向差集

- 服务端可发、页面不分支：**2** 个：`eino_context_overflow_retry`, `model_output_rejected`
- 页面分支、服务端从不发：**1** 个：`warning`

这两个数字都是 ratchet，只许降。第一个方向意味着帧到了客户端被丢弃；第二个方向是页面上的死分支——写入器会拒绝未登记的名字，所以那一支永远走不到。

## 持久化契约（`process_details.eventType`）

页面重建历史时间线时读的是库里的行，共 **35** 个名字：cancelled, eino_agent_reply, eino_empty_response_continue, eino_run_retry, eino_usage_summary, error, hitl_audit_agent, hitl_audit_agent_started, hitl_interrupt, hitl_rejected, hitl_resumed, iteration, knowledge_retrieval, model_output_rejected, planning, progress, reasoning_chain, thinking, timeout, tool_call, tool_calls_detected, tool_result, user_interrupt_continue, workflow_agent_output, workflow_agent_start, workflow_branch_skipped, workflow_branch_taken, workflow_done, workflow_hitl_checkpoint, workflow_hitl_waiting, workflow_node_result, workflow_node_start, workflow_paused, workflow_start, workflow_tool_start。
它的生产者不是写入器而是存储层（`AddProcessDetail` 第 3 参、`InterruptedUpdate.EventType`），
因此单列一份服务端真相源并与它做双向比对，见下一节。

## C2 事件流

页面按 `event.category` 分支的名字：session, task。

## 持久化事件类型：服务端可写入的行 vs 页面重建历史时分支的名字

服务端一侧由 `AddProcessDetail`/`AddProcessDetailWithID` 的第 3 参与 `InterruptedUpdate.EventType` 证明。
已知边界：经**进度回调变量**传入的持久化不在这个精确集合里——值图按声明过的函数名记参数位，
而回调是变量。所以第二个方向可能少报，第一个方向用「持久化 ∪ 流式」做上界只会多报、不会误判。

- 服务端可持久化：**8** 个：cancelled, eino_agent_reply, error, finalization_check, knowledge_retrieval, planning, thinking, timeout
- 可持久化而页面历史不分支：**1** 个：`finalization_check`
- 页面历史分支、两个生产者都给不出：**1** 个：`workflow_agent_start`

两个数字都由 `internal/handler/detail_contract_test.go` 钉成只许降的 ratchet。
