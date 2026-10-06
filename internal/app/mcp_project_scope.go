package app

import (
	"context"
	"strings"

	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/store"
)

func mcpEffectiveProjectFilter(ctx context.Context, db *store.Conversations) string {
	if projectID := strings.TrimSpace(mcp.MCPProjectIDFromContext(ctx)); projectID != "" {
		return projectID
	}
	if conversationID := mcpAuthorizationConversationID(ctx); conversationID != "" {
		if db != nil {
			if projectID, err := db.GetConversationProjectID(conversationID); err == nil {
				if projectID = strings.TrimSpace(projectID); projectID != "" {
					return projectID
				}
			}
		}
		return store.ProjectUnbound
	}
	return ""
}
