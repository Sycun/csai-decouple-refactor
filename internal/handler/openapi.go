package handler

import (
	"net/http"

	"cyberstrike-ai/internal/database"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// OpenAPIHandler OpenAPI处理器
type OpenAPIHandler struct {
	db               database.OpenAPIStore
	logger           *zap.Logger
	conversationHdlr *ConversationHandler
	agentHdlr        *AgentHandler
}

// NewOpenAPIHandler 创建新的OpenAPI处理器
func NewOpenAPIHandler(db *database.DB, logger *zap.Logger, conversationHdlr *ConversationHandler, agentHdlr *AgentHandler) *OpenAPIHandler {
	return &OpenAPIHandler{
		db:               database.Narrow[database.OpenAPIStore](db),
		logger:           logger,
		conversationHdlr: conversationHdlr,
		agentHdlr:        agentHdlr,
	}
}

// GetOpenAPISpec 获取OpenAPI规范
func (h *OpenAPIHandler) GetOpenAPISpec(c *gin.Context) {
	host := c.Request.Host
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}

	spec := map[string]interface{}{
		"openapi": "3.0.0",
		"info": map[string]interface{}{
			"title":       "CyberStrikeAI API",
			"description": "AI驱动的自动化安全测试平台API文档",
			"version":     "1.0.0",
			"contact": map[string]interface{}{
				"name": "CyberStrikeAI",
			},
		},
		"servers": []map[string]interface{}{
			{
				"url":         scheme + "://" + host,
				"description": "当前服务器",
			},
		},
		"components": openAPIComponents(),
		"paths":      openAPIPaths(),
		"security": []map[string]interface{}{
			{
				"bearerAuth": []string{},
			},
		},
	}

	// 整份文档每次请求重新构造：enrichSpecWithI18nKeys 会就地往每个 operation 写 x-i18n-tags。
	// 把这些定义提成包级共享变量，就是两个并发的 GET /api/openapi/spec 同时写同一个 map。
	enrichSpecWithI18nKeys(spec)
	c.JSON(http.StatusOK, spec)
}

func (h *OpenAPIHandler) GetConversationResults(c *gin.Context) {
	conversationID := c.Param("id")

	// 验证对话是否存在
	conv, err := h.db.GetConversation(conversationID)
	if err != nil {
		h.logger.Error("获取对话失败", zap.Error(err))
		c.JSON(http.StatusNotFound, gin.H{"error": "对话不存在"})
		return
	}

	// 获取消息列表
	messages, err := h.db.GetMessages(conversationID)
	if err != nil {
		h.logger.Error("获取消息失败", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 获取漏洞列表
	vulnList, err := h.db.ListVulnerabilities(1000, 0, database.VulnerabilityListFilter{ConversationID: conversationID})
	if err != nil {
		h.logger.Warn("获取漏洞列表失败", zap.Error(err))
		vulnList = []*database.Vulnerability{}
	}
	vulnerabilities := make([]database.Vulnerability, len(vulnList))
	for i, v := range vulnList {
		vulnerabilities[i] = *v
	}

	// 获取执行结果（历史大结果由 Eino reduction 落盘，此处不再聚合文件存储）
	executionResults := []map[string]interface{}{}

	response := map[string]interface{}{
		"conversationId":   conv.ID,
		"messages":         messages,
		"vulnerabilities":  vulnerabilities,
		"executionResults": executionResults,
	}

	c.JSON(http.StatusOK, response)
}
