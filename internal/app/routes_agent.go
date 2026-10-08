package app

import (
	"github.com/gin-gonic/gin"
)

// registerAgentRoutes registers the agent endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerAgentRoutes(protected *gin.RouterGroup) {
	agentHandler := deps.agentHandler
	markdownAgentsHandler := deps.markdownAgentsHandler

	// Eino ADK 单代理（ChatModelAgent + Runner；不依赖 multi_agent.enabled）
	protected.POST("/eino-agent", agentHandler.EinoSingleAgentLoop)
	protected.POST("/eino-agent/stream", agentHandler.EinoSingleAgentLoopStream)
	// 对话模式目录：前端选择器、WebShell 助手与机器人「模式」命令的数据源
	// （内置单代理恒在；多代理模式随「多代理编排包」的安装出现）
	protected.GET("/agent-modes", deps.agentModeHandler.GetAgentModes)
	// Agent Loop 取消与任务列表
	protected.POST("/agent-loop/cancel", agentHandler.CancelAgentLoop)
	protected.GET("/agent-loop/tasks", agentHandler.ListAgentTasks)
	protected.GET("/agent-loop/task-events", agentHandler.SubscribeAgentTaskEvents)
	protected.GET("/agent-loop/tasks/completed", agentHandler.ListCompletedTasks)

	// Eino DeepAgent 多代理（与单 Agent 并存，需 config.multi_agent.enabled）
	// 多代理路由常注册；是否可用由运行时 h.config.MultiAgent.Enabled 决定（应用配置后无需重启）
	protected.POST("/multi-agent", agentHandler.MultiAgentLoop)
	protected.POST("/multi-agent/stream", agentHandler.MultiAgentLoopStream)
	protected.GET("/multi-agent/markdown-agents", markdownAgentsHandler.ListMarkdownAgents)
	protected.GET("/multi-agent/markdown-agents/:filename", markdownAgentsHandler.GetMarkdownAgent)
	protected.POST("/multi-agent/markdown-agents", markdownAgentsHandler.CreateMarkdownAgent)
	protected.PUT("/multi-agent/markdown-agents/:filename", markdownAgentsHandler.UpdateMarkdownAgent)
	protected.DELETE("/multi-agent/markdown-agents/:filename", markdownAgentsHandler.DeleteMarkdownAgent)
}
