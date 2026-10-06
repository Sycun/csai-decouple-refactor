package database

import "cyberstrike-ai/internal/store"

// The assets SQL lives in store.Assets. What is left here is one line of delegation per method
// the consumers still call through database.AssetStore: no default, no logging, no retry.
// Removing them is the next cut, once each handler holds the store itself.

// NewAssets is nil-safe: a handler built without a database gets a connectionless store that
// refuses every call with an error rather than panicking on db.DB.
func NewAssets(db *DB) *store.Assets {
	if db == nil {
		return store.NewAssets(nil)
	}
	return store.NewAssets(db.DB)
}

func (db *DB) UpsertAssets(assets []*store.Asset, ownerUserID string, allowGlobal ...bool) (store.AssetImportResult, error) {
	return NewAssets(db).UpsertAssets(assets, ownerUserID, allowGlobal...)
}

func (db *DB) MarkAssetScanned(id, conversationID, queueID, taskID string, access store.Access) error {
	return NewAssets(db).MarkAssetScanned(id, conversationID, queueID, taskID, access)
}

func (db *DB) CompleteAssetScan(id, conversationID string, access store.Access) error {
	return NewAssets(db).CompleteAssetScan(id, conversationID, access)
}

func (db *DB) BatchTaskBelongsToQueue(taskID, queueID string) bool {
	return NewAssets(db).BatchTaskBelongsToQueue(taskID, queueID)
}

func (db *DB) RefreshAssetRiskCache(assetID string) error {
	return NewAssets(db).RefreshAssetRiskCache(assetID)
}

func (db *DB) AssetIDsForVulnerabilityConversations(conversationIDs []string) ([]string, error) {
	return NewAssets(db).AssetIDsForVulnerabilityConversations(conversationIDs)
}

func (db *DB) RefreshAssetRiskCacheForConversations(conversationIDs ...string) error {
	return NewAssets(db).RefreshAssetRiskCacheForConversations(conversationIDs...)
}

func (db *DB) ListAssets(limit, offset int, filter store.AssetListFilter, access store.Access) ([]*store.Asset, int, error) {
	return NewAssets(db).ListAssets(limit, offset, filter, access)
}

func (db *DB) ListAssetsForOperation(limit int, filter store.AssetListFilter, access store.Access) ([]*store.Asset, int, error) {
	return NewAssets(db).ListAssetsForOperation(limit, filter, access)
}

func (db *DB) GetAsset(id string, access store.Access) (*store.Asset, error) {
	return NewAssets(db).GetAsset(id, access)
}

func (db *DB) UpdateAsset(id string, a *store.Asset, access store.Access) error {
	return NewAssets(db).UpdateAsset(id, a, access)
}

func (db *DB) UpdateAssetsBulk(ids []string, patch store.AssetBulkPatch, access store.Access) (int, error) {
	return NewAssets(db).UpdateAssetsBulk(ids, patch, access)
}

func (db *DB) DeleteAssets(ids []string, access store.Access) (int, error) {
	return NewAssets(db).DeleteAssets(ids, access)
}

func (db *DB) MergeAssets(primary *store.Asset, duplicateIDs []string, writeAccess, deleteAccess store.Access) (int, error) {
	return NewAssets(db).MergeAssets(primary, duplicateIDs, writeAccess, deleteAccess)
}

func (db *DB) UpdateAssetsProject(ids []string, projectID string, access store.Access) (int, error) {
	return NewAssets(db).UpdateAssetsProject(ids, projectID, access)
}

func (db *DB) DeleteAsset(id string, access store.Access) error {
	return NewAssets(db).DeleteAsset(id, access)
}

func (db *DB) GetAssetStats(access store.Access, requestedDays ...int) (map[string]interface{}, error) {
	return NewAssets(db).GetAssetStats(access, requestedDays...)
}
