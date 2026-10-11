# 角色配置文件说明

本目录包含所有角色配置文件，每个角色定义了AI的行为模式与可用工具。

## 创建新角色

创建新角色时，请在 `roles/` 目录下创建 YAML 文件，格式如下：

**方式1：显式指定工具列表（推荐）**
```yaml
name: 角色名称
description: 角色描述
user_prompt: 用户提示词（追加到用户消息前，用于引导AI行为）
icon: "图标（可选）"
# ai_channel: qwen-max   # 可选：该角色专用模型通道（ai.channels 的键）；留空跟随 ai.default_channel
tools:
    # 添加你需要的工具...
    # ⚠️ 重要：建议包含以下核心内置 MCP 工具（漏洞与知识库）
    - record_vulnerability
    - list_knowledge_risk_types
    - search_knowledge_base
enabled: true
```

**方式2：不设置tools字段（使用所有已开启的工具）**
```yaml
name: 角色名称
description: 角色描述
user_prompt: 用户提示词（追加到用户消息前，用于引导AI行为）
icon: "图标（可选）"
# 不设置tools字段，将默认使用所有MCP管理中已开启的工具
enabled: true
```

## ⚠️ 重要提醒：核心内置 MCP 工具

**如果设置了 `tools` 字段，请务必在列表中包含以下工具（至少这三项）：**

1. **`record_vulnerability`** - 漏洞管理工具，用于记录发现的漏洞
2. **`list_knowledge_risk_types`** - 知识库工具，列出可用的风险类型
3. **`search_knowledge_base`** - 知识库工具，搜索知识库内容

按需还可加入 WebShell、批量任务等其它内置或外部工具（以 MCP 管理中已启用的为准）。

**Skills（技能包）**：在 **多代理 / Eino** 会话中由内置 **`skill`** 工具按需加载 `skills_dir` 下的包，与角色 YAML 无绑定关系。

**注意**：如果不设置 `tools` 字段，系统会默认使用所有 MCP 管理中已开启的工具。为明确控制角色可用工具，建议显式设置 `tools` 字段。

## 角色配置字段说明

- **name**: 角色名称（必填）
- **description**: 角色描述（必填）
- **user_prompt**: 用户提示词，会追加到用户消息前，用于引导AI采用特定的测试方法和关注点（可选）
- **icon**: 角色图标，支持Unicode emoji（可选）
- **tools**: 工具列表，指定该角色可用的工具（可选）
  - **如果不设置 `tools` 字段**：默认会选中**全部MCP管理中已开启的工具**
  - **如果设置了 `tools` 字段**：只使用列表中指定的工具（建议至少包含上述核心内置工具）
- **ai_channel**: 角色专用 AI 通道 ID，取值为 `config.yaml` 里 `ai.channels` 的键（可选）——「一个角色一个模型」就是靠它表达的
  - **不设置或留空**：跟随全局默认通道 `ai.default_channel`
  - **设置后**：该角色发起的运行使用这个通道自带的 `provider` / `base_url` / `api_key` / `model` / `reasoning`，因此换一个模型只需要改通道，不必逐个角色改
  - 优先级：**对话页手动选择的通道 > 角色绑定的通道 > `ai.default_channel`**（对话页的通道下拉只有在用户真的选过时才会随请求发出，所以显式选择永远是一次有意的覆盖）
  - 绑定的通道若已被删除或改名，运行时**回落默认通道**并记一条 warning，不会让对话失败；控制台保存角色时会直接拒绝不存在的通道
  - 生效范围：对话页单代理（`/api/eino-agent*`）、多代理（deep / plan_execute / supervisor）以及批量任务队列
- **enabled**: 是否启用该角色（必填，true/false）

## 示例

出厂树里本目录**只有 `默认.yaml`**；`渗透测试`、`Web应用扫描`、`后渗透测试` 等专业角色都在
能力包市场（控制台「平台管理 → 能力包」，如 `web-pentest`、`ad-internal`），
安装后即出现在角色列表里，卸载即回到原状。
