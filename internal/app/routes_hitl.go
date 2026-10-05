package app

import (
	"cyberstrike-ai/internal/security"
	"github.com/gin-gonic/gin"
)

// registerHitlRoutes registers the hitl endpoints. The bodies are unchanged from the previous
// single wiring function; only the surrounding structure moved, so the route table
// is verified against testdata/routes.golden.txt.
func (deps routeDeps) registerHitlRoutes(protected *gin.RouterGroup) {
	agentHandler := deps.agentHandler
	// Everything an operator does *to* an interrupt - list what is waiting, read the decided log,
	// answer one, dismiss one - is on HITLQueue now. Paths and methods are unchanged, so
	// testdata/routes.golden.txt still has to match registration-for-registration.
	hitlQueue := agentHandler.HITLQueue()

	protected.GET("/hitl/pending", hitlQueue.ListHITLPending)
	protected.GET("/hitl/logs", hitlQueue.ListHITLLogs)
	protected.DELETE("/hitl/logs", hitlQueue.DeleteHITLLogs)
	protected.GET("/hitl/logs/:id", hitlQueue.GetHITLLog)
	protected.POST("/hitl/decision", hitlQueue.DecideHITLInterrupt)
	protected.POST("/hitl/dismiss", hitlQueue.DismissHITLInterrupt)
	protected.GET("/hitl/config/:conversationId", agentHandler.HitlPolicy().GetConversationConfig)
	protected.PUT("/hitl/config", agentHandler.HitlPolicy().UpsertConversationConfig)
	protected.GET("/hitl/tool-whitelist", agentHandler.HitlPolicy().GetGlobalToolWhitelist)
	// 免审批白名单决定哪些工具跳过人工审批，因此它是运维者所有的配置面：
	// 只有 config:write（管理员）可以改，会话侧栏的请求体不再能拓宽豁免范围。
	protected.PUT("/hitl/tool-whitelist", security.RequirePermission("config:write"), agentHandler.HitlPolicy().SetGlobalToolWhitelist)
	protected.POST("/hitl/tool-whitelist", security.RequirePermission("config:write"), agentHandler.HitlPolicy().MergeGlobalToolWhitelist)
	protected.GET("/hitl/default-config", agentHandler.HitlPolicy().GetDefaultConfig)
	protected.PUT("/hitl/default-config", security.RequirePermission("config:write"), agentHandler.HitlPolicy().UpdateDefaultConfig)
	protected.GET("/hitl/default-reviewer", agentHandler.HitlPolicy().GetDefaultReviewer)
	protected.PUT("/hitl/default-reviewer", security.RequirePermission("config:write"), agentHandler.HitlPolicy().UpdateDefaultReviewer)
	protected.GET("/hitl/audit-strategy", agentHandler.HitlPolicy().GetAuditStrategy)
	protected.PUT("/hitl/audit-strategy", agentHandler.HitlPolicy().UpdateAuditStrategy)
}
