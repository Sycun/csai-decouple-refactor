package knowledge

import (
	"cyberstrike-ai/internal/contentpolicy"
	"cyberstrike-ai/internal/sqltime"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cyberstrike-ai/internal/store"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Manager 知识库管理器
type Manager struct {
	items     *store.KnowledgeItems      // knowledge_base_items
	vectors   *store.KnowledgeEmbeddings // knowledge_embeddings
	retrieval *store.KnowledgeRetrieval  // knowledge_retrieval_logs: 这张表只由 store 写
	basePath  string
	logger    *zap.Logger
}

// NewManager 创建新的知识库管理器
func NewManager(db *sql.DB, basePath string, logger *zap.Logger) *Manager {
	return &Manager{
		items:     store.NewKnowledgeItems(db),
		vectors:   store.NewKnowledgeEmbeddings(db),
		retrieval: store.NewKnowledgeRetrieval(db),
		basePath:  basePath,
		logger:    logger,
	}
}

// ScanKnowledgeBase 扫描知识库目录，更新数据库
// 返回需要索引的知识项ID列表（新添加的或更新的）
func (m *Manager) ScanKnowledgeBase() ([]string, error) {
	if m.basePath == "" {
		return nil, fmt.Errorf("知识库路径未配置")
	}

	// 确保目录存在
	if err := os.MkdirAll(m.basePath, 0755); err != nil {
		return nil, fmt.Errorf("创建知识库目录失败: %w", err)
	}

	var itemsToIndex []string

	// 遍历知识库目录
	err := filepath.WalkDir(m.basePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// 跳过目录和非markdown文件
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".md") {
			return nil
		}

		// 计算相对路径和分类
		relPath, err := filepath.Rel(m.basePath, path)
		if err != nil {
			return err
		}

		// 第一个目录名作为分类（风险类型）
		parts := strings.Split(relPath, string(filepath.Separator))
		category := "未分类"
		if len(parts) > 1 {
			category = parts[0]
		}

		// 文件名为标题
		title := strings.TrimSuffix(filepath.Base(path), ".md")

		// 读取文件内容
		content, err := os.ReadFile(path)
		if err != nil {
			m.logger.Warn("读取知识库文件失败", zap.String("path", path), zap.Error(err))
			return nil // 继续处理其他文件
		}

		// 检查是否已存在
		existingID, existingContent, _, found, err := m.items.ExistingAt(path)
		if err != nil {
			return fmt.Errorf("查询知识项失败: %w", err)
		}

		if !found {
			// 创建新项
			id := uuid.New().String()
			now := time.Now()
			err = m.items.Insert(id, category, title, path, string(content), now)
			if err != nil {
				return fmt.Errorf("插入知识项失败: %w", err)
			}
			m.logger.Info("添加知识项", zap.String("id", id), zap.String("title", title), zap.String("category", category))
			// 新添加的项需要索引
			itemsToIndex = append(itemsToIndex, id)
		} else if err == nil {
			// 检查内容是否有变化
			contentChanged := existingContent != string(content)
			if contentChanged {
				// 更新现有项
				err = m.items.UpdateContent(existingID, category, title, string(content), time.Now())
				if err != nil {
					return fmt.Errorf("更新知识项失败: %w", err)
				}
				m.logger.Info("更新知识项", zap.String("id", existingID), zap.String("title", title))
				// 内容已更新的项需要重新索引
				itemsToIndex = append(itemsToIndex, existingID)
			} else {
				m.logger.Debug("知识项未变化，跳过", zap.String("id", existingID), zap.String("title", title))
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return itemsToIndex, nil
}

// GetCategories 获取所有分类（风险类型）
func (m *Manager) GetCategories() ([]string, error) {
	categories, err := m.items.Categories()
	if err != nil {
		return nil, fmt.Errorf("查询分类失败: %w", err)
	}
	return categories, nil
}

// GetStats 获取知识库统计信息
func (m *Manager) GetStats() (int, int, error) {
	// 获取分类总数
	categories, err := m.GetCategories()
	if err != nil {
		return 0, 0, fmt.Errorf("获取分类失败: %w", err)
	}
	totalCategories := len(categories)

	// 获取知识项总数
	totalItems, err := m.items.Count("")
	if err != nil {
		return totalCategories, 0, fmt.Errorf("获取知识项总数失败: %w", err)
	}

	return totalCategories, totalItems, nil
}

// GetCategoriesWithItems 按分类分页获取知识项（每个分类包含其下的所有知识项）
// limit: 每页分类数量（0表示不限制）
// offset: 偏移量（按分类偏移）
func (m *Manager) GetCategoriesWithItems(limit, offset int) ([]*CategoryWithItems, int, error) {
	// 首先获取所有分类（带数量统计）
	// 收集所有分类信息
	type categoryInfo struct {
		name      string
		itemCount int
	}
	var allCategories []categoryInfo
	counts, err := m.items.CategoriesWithCounts()
	if err != nil {
		return nil, 0, fmt.Errorf("查询分类失败: %w", err)
	}
	for _, entry := range counts {
		allCategories = append(allCategories, categoryInfo{name: entry.Category, itemCount: entry.Count})
	}

	totalCategories := len(allCategories)

	// 应用分页（按分类分页）
	var paginatedCategories []categoryInfo
	if limit > 0 {
		start := offset
		end := offset + limit
		if start >= totalCategories {
			paginatedCategories = []categoryInfo{}
		} else {
			if end > totalCategories {
				end = totalCategories
			}
			paginatedCategories = allCategories[start:end]
		}
	} else {
		paginatedCategories = allCategories
	}

	// 为每个分类获取其下的知识项（只返回摘要，不包含完整内容）
	result := make([]*CategoryWithItems, 0, len(paginatedCategories))
	for _, catInfo := range paginatedCategories {
		// 获取该分类下的所有知识项
		items, _, err := m.GetItemsSummary(catInfo.name, 0, 0)
		if err != nil {
			return nil, 0, fmt.Errorf("获取分类 %s 的知识项失败: %w", catInfo.name, err)
		}

		result = append(result, &CategoryWithItems{
			Category:  catInfo.name,
			ItemCount: catInfo.itemCount,
			Items:     items,
		})
	}

	return result, totalCategories, nil
}

// GetItems 获取知识项列表（完整内容，用于向后兼容）
func (m *Manager) GetItems(category string) ([]*KnowledgeItem, error) {
	return m.GetItemsWithOptions(category, 0, 0, true)
}

// GetItemsWithOptions 获取知识项列表（支持分页和可选内容）
// category: 分类筛选（空字符串表示所有分类）
// limit: 每页数量（0表示不限制）
// offset: 偏移量
// includeContent: 是否包含完整内容（false时只返回摘要）
func (m *Manager) GetItemsWithOptions(category string, limit, offset int, includeContent bool) ([]*KnowledgeItem, error) {
	rows, err := m.items.List(store.ItemFilter{Category: category, Limit: limit, Offset: offset, IncludeContent: includeContent})
	if err != nil {
		return nil, fmt.Errorf("查询知识项失败: %w", err)
	}

	items := make([]*KnowledgeItem, 0, len(rows))
	for _, row := range rows {
		created, updated := parseItemTimes(row.CreatedAt, row.UpdatedAt)
		items = append(items, &KnowledgeItem{
			ID: row.ID, Category: row.Category, Title: row.Title, FilePath: row.FilePath,
			Content: row.Content, CreatedAt: created, UpdatedAt: updated,
		})
	}
	return items, nil
}

// GetItemsCount 获取知识项总数
func (m *Manager) GetItemsCount(category string) (int, error) {
	var count int
	var err error

	if category != "" {
		count, err = m.items.Count(category)
	} else {
		count, err = m.items.Count("")
	}

	if err != nil {
		return 0, fmt.Errorf("查询知识项总数失败: %w", err)
	}

	return count, nil
}

// SearchItemsByKeyword 按关键字搜索知识项（在所有数据中搜索，支持标题、分类、路径、内容匹配）
func (m *Manager) SearchItemsByKeyword(keyword string, category string) ([]*KnowledgeItemSummary, error) {
	if keyword == "" {
		return nil, fmt.Errorf("搜索关键字不能为空")
	}
	rows, err := m.items.Search(keyword, category)
	if err != nil {
		return nil, fmt.Errorf("搜索知识项失败: %w", err)
	}

	items := make([]*KnowledgeItemSummary, 0, len(rows))
	for _, row := range rows {
		created, updated := parseItemTimes(row.CreatedAt, row.UpdatedAt)
		items = append(items, &KnowledgeItemSummary{
			ID: row.ID, Category: row.Category, Title: row.Title, FilePath: row.FilePath,
			CreatedAt: created, UpdatedAt: updated,
		})
	}
	return items, nil
}

// GetItemsSummary 获取知识项摘要列表（不包含完整内容，支持分页）
func (m *Manager) GetItemsSummary(category string, limit, offset int) ([]*KnowledgeItemSummary, int, error) {
	total, err := m.GetItemsCount(category)
	if err != nil {
		return nil, 0, err
	}

	rows, err := m.items.List(store.ItemFilter{Category: category, Limit: limit, Offset: offset})
	if err != nil {
		return nil, 0, fmt.Errorf("查询知识项失败: %w", err)
	}

	items := make([]*KnowledgeItemSummary, 0, len(rows))
	for _, row := range rows {
		created, updated := parseItemTimes(row.CreatedAt, row.UpdatedAt)
		items = append(items, &KnowledgeItemSummary{
			ID: row.ID, Category: row.Category, Title: row.Title, FilePath: row.FilePath,
			CreatedAt: created, UpdatedAt: updated,
		})
	}
	return items, total, nil
}

// GetItem 获取单个知识项
func (m *Manager) GetItem(id string) (*KnowledgeItem, error) {
	row, found, err := m.items.GetByID(id)
	if err != nil {
		return nil, fmt.Errorf("查询知识项失败: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("知识项不存在")
	}
	created, updated := parseItemTimes(row.CreatedAt, row.UpdatedAt)
	return &KnowledgeItem{
		ID: row.ID, Category: row.Category, Title: row.Title, FilePath: row.FilePath,
		Content: row.Content, CreatedAt: created, UpdatedAt: updated,
	}, nil
}

// CreateItem 创建知识项
func (m *Manager) CreateItem(category, title, content string) (*KnowledgeItem, error) {
	// Ingest is where a poisoned-retrieval attack is actually stopped: text written
	// as an instruction, or carrying rendering-time execution markup, never becomes
	// an indexable document.
	if err := contentpolicy.RefuseIngest(contentpolicy.SourceKnowledge, title, content); err != nil {
		return nil, err
	}

	id := uuid.New().String()
	now := time.Now()

	// 构建文件路径
	filePath := filepath.Join(m.basePath, category, title+".md")

	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return nil, fmt.Errorf("创建目录失败: %w", err)
	}

	// 写入文件
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("写入文件失败: %w", err)
	}

	// 插入数据库
	err := m.items.Insert(id, category, title, filePath, content, now)
	if err != nil {
		return nil, fmt.Errorf("插入知识项失败: %w", err)
	}

	return &KnowledgeItem{
		ID:        id,
		Category:  category,
		Title:     title,
		FilePath:  filePath,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// UpdateItem 更新知识项
func (m *Manager) UpdateItem(id, category, title, content string) (*KnowledgeItem, error) {
	if err := contentpolicy.RefuseIngest(contentpolicy.SourceKnowledge, title, content); err != nil {
		return nil, err
	}

	// 获取现有项
	item, err := m.GetItem(id)
	if err != nil {
		return nil, err
	}

	// 构建新文件路径
	newFilePath := filepath.Join(m.basePath, category, title+".md")

	// 如果路径改变，需要移动文件
	if item.FilePath != newFilePath {
		// 确保新目录存在
		if err := os.MkdirAll(filepath.Dir(newFilePath), 0755); err != nil {
			return nil, fmt.Errorf("创建目录失败: %w", err)
		}

		// 移动文件
		if err := os.Rename(item.FilePath, newFilePath); err != nil {
			return nil, fmt.Errorf("移动文件失败: %w", err)
		}

		// 删除旧目录（如果为空）
		oldDir := filepath.Dir(item.FilePath)
		if isEmpty, _ := isEmptyDir(oldDir); isEmpty {
			// 只有当目录不是知识库根目录时才删除（避免删除根目录）
			if oldDir != m.basePath {
				if err := os.Remove(oldDir); err != nil {
					m.logger.Warn("删除空目录失败", zap.String("dir", oldDir), zap.Error(err))
				}
			}
		}
	}

	// 写入文件
	if err := os.WriteFile(newFilePath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("写入文件失败: %w", err)
	}

	err = m.items.Update(id, category, title, newFilePath, content, time.Now())
	if err != nil {
		return nil, fmt.Errorf("更新知识项失败: %w", err)
	}

	// 改了正文的条目要重新索引，旧向量先删掉
	if err := m.vectors.DeleteItem(id); err != nil {
		m.logger.Warn("删除旧向量嵌入失败", zap.Error(err))
	}

	return m.GetItem(id)
}

// DeleteItem 删除知识项
func (m *Manager) DeleteItem(id string) error {
	// 获取文件路径
	filePath, found, err := m.items.FilePath(id)
	if err != nil || !found {
		if err == nil {
			err = sql.ErrNoRows
		}
		return fmt.Errorf("查询知识项失败: %w", err)
	}

	// 删除文件
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		m.logger.Warn("删除文件失败", zap.String("path", filePath), zap.Error(err))
	}

	// 删除数据库记录（级联删除向量）
	err = m.items.Delete(id)
	if err != nil {
		return fmt.Errorf("删除知识项失败: %w", err)
	}

	// 删除空目录（如果为空）
	dir := filepath.Dir(filePath)
	if isEmpty, _ := isEmptyDir(dir); isEmpty {
		// 只有当目录不是知识库根目录时才删除（避免删除根目录）
		if dir != m.basePath {
			if err := os.Remove(dir); err != nil {
				m.logger.Warn("删除空目录失败", zap.String("dir", dir), zap.Error(err))
			}
		}
	}

	return nil
}

// isEmptyDir 检查目录是否为空（忽略隐藏文件和 . 开头的文件）
func isEmptyDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		// 忽略隐藏文件（以 . 开头）
		if !strings.HasPrefix(entry.Name(), ".") {
			return false, nil
		}
	}
	return true, nil
}

// LogRetrieval 记录检索日志
func (m *Manager) LogRetrieval(conversationID, messageID, query, riskType string, retrievedItems []string) error {
	id := uuid.New().String()
	itemsJSON, _ := json.Marshal(retrievedItems)

	return m.retrieval.Record(store.RetrievalEntry{
		ID: id, ConversationID: conversationID, MessageID: messageID,
		Query: query, RiskType: riskType, ItemsJSON: string(itemsJSON),
	}, time.Now())
}

// GetIndexStatus 获取索引状态
func (m *Manager) GetIndexStatus() (map[string]interface{}, error) {
	// 获取总知识项数
	totalItems, err := m.items.Count("")
	if err != nil {
		return nil, fmt.Errorf("查询总知识项数失败: %w", err)
	}

	// 获取已索引的知识项数（有向量嵌入的）
	indexedItems, err := m.vectors.IndexedItems()
	if err != nil {
		return nil, fmt.Errorf("查询已索引项数失败: %w", err)
	}

	// 计算进度百分比
	var progressPercent float64
	if totalItems > 0 {
		progressPercent = float64(indexedItems) / float64(totalItems) * 100
	} else {
		progressPercent = 100.0
	}

	// 判断是否完成
	isComplete := indexedItems >= totalItems && totalItems > 0

	return map[string]interface{}{
		"total_items":      totalItems,
		"indexed_items":    indexedItems,
		"progress_percent": progressPercent,
		"is_complete":      isComplete,
	}, nil
}

// GetRetrievalLogs 获取检索日志
func (m *Manager) GetRetrievalLogs(conversationID, messageID string, limit int) ([]*RetrievalLog, error) {
	entries, err := m.retrieval.ListNewest(messageID, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询检索日志失败: %w", err)
	}

	var logs []*RetrievalLog
	for _, entry := range entries {
		log := &RetrievalLog{
			ID: entry.ID, ConversationID: entry.ConversationID, MessageID: entry.MessageID,
			Query: entry.Query, RiskType: entry.RiskType,
		}
		createdAt := entry.CreatedAt
		itemsJSON := sql.NullString{String: entry.ItemsJSON, Valid: entry.ItemsJSON != ""}

		// 解析时间 - 可接受的写法由 internal/sqltime 统一持有
		createdAtValue, parsed := sqltime.ParseOK(createdAt)
		log.CreatedAt = createdAtValue

		// 如果所有格式都失败，记录警告但继续处理
		if !parsed || log.CreatedAt.IsZero() {
			m.logger.Warn("解析检索日志时间失败", zap.String("timeStr", createdAt))
			// 使用当前时间作为fallback
			log.CreatedAt = time.Now()
		}

		// 解析检索项
		if itemsJSON.Valid {
			json.Unmarshal([]byte(itemsJSON.String), &log.RetrievedItems)
		}

		logs = append(logs, log)
	}

	return logs, nil
}

// DeleteRetrievalLog 删除检索日志
func (m *Manager) DeleteRetrievalLog(id string) error {
	found, err := m.retrieval.DeleteByID(id)
	if err != nil {
		return fmt.Errorf("删除检索日志失败: %w", err)
	}
	if !found {
		return fmt.Errorf("检索日志不存在")
	}
	return nil
}

// parseItemTimes reads two stored timestamps, falling back to created_at when updated_at is absent.
// Which text forms count as a stored instant is owned by internal/sqltime.
func parseItemTimes(createdAt, updatedAt string) (time.Time, time.Time) {
	created := sqltime.Parse(createdAt)
	updated := sqltime.Parse(updatedAt)
	if updated.IsZero() && !created.IsZero() {
		updated = created
	}
	return created, updated
}
