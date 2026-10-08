package handler

import (
	"net/http"

	"cyberstrike-ai/internal/agentmode"
	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/plugin"

	"github.com/gin-gonic/gin"
)

// 对话模式目录的读取面：能力表（plugin.Global，装配时装入一次）与活配置快照
// （liveSettings，同 currentRoles 的先例）的组合。做成包级函数而不是每个 handler 的字段，
// 理由与 currentRoles 相同：机器人、聊天、批量、workflow 辅助的多个入口都要问
// 「这个模式此刻可用吗」，给每个构造器加参数的代价换不来说明力。
//
// 未装配能力表（单测里的 bare handler）时目录只剩内核内置的单代理——fail-closed 想要的
// 方向就是它：裸装配的进程不会凭空多出可执行的多代理模式。

func agentModeUnits() []agentmode.Unit {
	table := plugin.Global()
	if table == nil {
		return nil
	}
	units := table.Units(plugin.KindMode)
	out := make([]agentmode.Unit, 0, len(units))
	for _, u := range units {
		out = append(out, agentmode.Unit{Name: u.Name, Path: u.Path, Bundle: u.Bundle, Enabled: u.Enabled})
	}
	return out
}

// agentModeEngineEnabled 回答「多代理引擎此刻是否启用」，活快照优先。
func agentModeEngineEnabled(cfg *config.Config) bool {
	c := currentConfig(cfg)
	return c != nil && c.MultiAgent.Enabled
}

// agentModeEntries 返回此刻的对话模式目录（内置 + 被激活的），是 /api/agent-modes、机器人
// 「模式」命令与前端选择器共同的答案。
func agentModeEntries(cfg *config.Config) []agentmode.Entry {
	return agentmode.Build(agentModeUnits(), agentModeEngineEnabled(cfg))
}

// checkAgentMode 是执行入口的 fail-closed 判据：内置单代理恒过；多代理模式要求单元已被
// 激活（包已安装且未停用）且引擎已启用，否则 error 指名原因。
func checkAgentMode(cfg *config.Config, id string) (agentmode.Entry, error) {
	return agentmode.Check(agentModeUnits(), agentModeEngineEnabled(cfg), id)
}

// AgentModeHandler 是对话模式目录的 HTTP 面。它没有状态（目录从包级能力表与活配置快照读），
// 单独成型是因为体量棘轮（TestHandlerSizesOnlyShrink）的规矩：新能力做协作类型，
// 不往 AgentHandler 这个最大类型上再加方法。
type AgentModeHandler struct {
	config *config.Config
}

func NewAgentModeHandler(cfg *config.Config) *AgentModeHandler {
	return &AgentModeHandler{config: cfg}
}

// GetAgentModes 返回此刻的对话模式目录：内置单代理恒在，多代理模式随「多代理编排包」的
// 安装与引擎开关出现或消失（"不点不存在"）。对话页、WebShell 助手、批量队列与机器人
// 「模式」命令都从这里取数，前端不再各自硬编码模式清单。
func (h *AgentModeHandler) GetAgentModes(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"default": agentmode.DefaultID,
		"modes":   agentModeEntries(h.config),
	})
}
