package database

import (
	"cyberstrike-ai/internal/store"
	"database/sql"

	"go.uber.org/zap"
)

// NewFindings wires the findings store to this connection.
//
// The two effects a record write owes the rest of the application are answered here rather than
// inside the store: the project a new finding inherits comes from the conversations table (project
// domain) and the asset risk cache belongs to assets. Neither is the findings store's to reach into,
// and this is also where the logger for the best-effort cache refresh lives - the same logger that
// did it when the SQL sat in this package.
func NewFindings(db *DB) *store.Vulnerabilities {
	return store.NewVulnerabilities(db.DB, findingEffects{db: db})
}

type findingEffects struct{ db *DB }

func (e findingEffects) ConversationProjectID(conversationID string) (string, error) {
	return e.db.GetConversationProjectID(conversationID)
}

// UnlinkFactReferences hands the project domain's own write to it. The findings store cannot do this
// itself without becoming a second owner of project_facts.
func (e findingEffects) UnlinkFactReferences(tx *sql.Tx, findingIDs []string) error {
	return NewFacts(e.db).UnlinkFindingReferences(tx, findingIDs)
}

func (e findingEffects) RefreshAssetRiskCache(conversationIDs ...string) {
	if err := NewAssets(e.db).RefreshAssetRiskCacheForConversations(conversationIDs...); err != nil && e.db.logger != nil {
		e.db.logger.Warn("刷新资产风险缓存失败", zap.Error(err))
	}
}
