package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/store"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// Retriever 检索器：SQLite 存向量 + Eino 嵌入，**纯向量检索**（余弦相似度、TopK、阈值），
// 实现语义与 [retriever.Retriever] 适配层 [VectorEinoRetriever] 一致。
type Retriever struct {
	vectors  *store.KnowledgeEmbeddings
	embedder *Embedder
	config   *RetrievalConfig
	logger   *zap.Logger

	rerankMu sync.RWMutex
	reranker DocumentReranker

	pipeline   retriever.Retriever
	wireOpenAI *config.OpenAIConfig
}

// RetrievalConfig 检索配置
type RetrievalConfig struct {
	TopK                int
	SimilarityThreshold float64
	SubIndexFilter      string
	MultiQuery          config.MultiQueryConfig
	Rerank              config.RerankConfig
	PostRetrieve        config.PostRetrieveConfig
}

// NewRetriever 创建新的检索器。db 是知识库连接，只用来绑定向量表的 store。
func NewRetriever(db *sql.DB, embedder *Embedder, config *RetrievalConfig, logger *zap.Logger) *Retriever {
	return &Retriever{
		vectors:  store.NewKnowledgeEmbeddings(db),
		embedder: embedder,
		config:   config,
		logger:   logger,
	}
}

// UpdateConfig 更新检索配置并重建 Eino MultiQuery + 重排流水线。
func (r *Retriever) UpdateConfig(cfg *RetrievalConfig) {
	if cfg != nil {
		r.config = cfg
		if r.logger != nil {
			r.logger.Info("检索器配置已更新",
				zap.Int("top_k", cfg.TopK),
				zap.Float64("similarity_threshold", cfg.SimilarityThreshold),
				zap.String("sub_index_filter", cfg.SubIndexFilter),
				zap.Int("multi_query_max", cfg.MultiQuery.MaxQueriesEffective()),
				zap.Int("post_retrieve_prefetch_top_k", cfg.PostRetrieve.PrefetchTopK),
				zap.Int("post_retrieve_max_context_chars", cfg.PostRetrieve.MaxContextChars),
				zap.Int("post_retrieve_max_context_tokens", cfg.PostRetrieve.MaxContextTokens),
			)
		}
	}
	if r.wireOpenAI != nil {
		if err := WireRetrieverPipeline(context.Background(), r, r.wireOpenAI); err != nil && r.logger != nil {
			r.logger.Warn("检索流水线重建失败", zap.Error(err))
		}
	}
}

// SetDocumentReranker 注入可选重排器（并发安全）；nil 表示禁用。
func (r *Retriever) SetDocumentReranker(rr DocumentReranker) {
	if r == nil {
		return
	}
	r.rerankMu.Lock()
	defer r.rerankMu.Unlock()
	r.reranker = rr
}

func (r *Retriever) documentReranker() DocumentReranker {
	if r == nil {
		return nil
	}
	r.rerankMu.RLock()
	defer r.rerankMu.RUnlock()
	return r.reranker
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0.0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i] * b[i])
		normA += float64(a[i] * a[i])
		normB += float64(b[i] * b[i])
	}

	if normA == 0 || normB == 0 {
		return 0.0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// Search 搜索知识库（Eino MultiQuery → 向量检索 → 重排 → 后处理）。
func (r *Retriever) Search(ctx context.Context, req *SearchRequest) ([]*RetrievalResult, error) {
	if req == nil {
		return nil, fmt.Errorf("请求不能为空")
	}
	q := strings.TrimSpace(req.Query)
	if q == "" {
		return nil, fmt.Errorf("查询不能为空")
	}
	opts := r.einoRetrieverOptions(req)
	docs, err := r.activeEinoRetriever().Retrieve(ctx, q, opts...)
	if err != nil {
		return nil, err
	}
	return documentsToRetrievalResults(docs)
}

func (r *Retriever) einoRetrieverOptions(req *SearchRequest) []retriever.Option {
	var opts []retriever.Option
	if req.TopK > 0 {
		opts = append(opts, retriever.WithTopK(req.TopK))
	}
	dsl := map[string]any{}
	if strings.TrimSpace(req.RiskType) != "" {
		dsl[DSLRiskType] = strings.TrimSpace(req.RiskType)
	}
	if req.Threshold > 0 {
		dsl[DSLSimilarityThreshold] = req.Threshold
	}
	if strings.TrimSpace(req.SubIndexFilter) != "" {
		dsl[DSLSubIndexFilter] = strings.TrimSpace(req.SubIndexFilter)
	}
	if len(dsl) > 0 {
		opts = append(opts, retriever.WithDSLInfo(dsl))
	}
	return opts
}

// EinoRetrieve 直接返回 [schema.Document]，供 Eino Graph / Chain 使用。
func (r *Retriever) EinoRetrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	return r.activeEinoRetriever().Retrieve(ctx, query, opts...)
}

func (r *Retriever) activeEinoRetriever() retriever.Retriever {
	if r != nil && r.pipeline != nil {
		return r.pipeline
	}
	return NewVectorEinoRetriever(r)
}

// AsEinoRetriever 将知识库检索流水线暴露为 Eino [retriever.Retriever]。
func (r *Retriever) AsEinoRetriever() retriever.Retriever {
	return r.activeEinoRetriever()
}

// vectorSearch 纯向量检索：余弦相似度排序，按相似度阈值与 TopK 截断（无 BM25、无混合分、无邻块扩展）。
func (r *Retriever) vectorSearch(ctx context.Context, req *SearchRequest) ([]*RetrievalResult, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("查询不能为空")
	}

	topK := req.TopK
	if topK <= 0 && r.config != nil {
		topK = r.config.TopK
	}
	if topK <= 0 {
		topK = 5
	}

	threshold := req.Threshold
	if threshold <= 0 && r.config != nil {
		threshold = r.config.SimilarityThreshold
	}
	if threshold <= 0 {
		threshold = 0.7
	}

	subIdxFilter := strings.TrimSpace(req.SubIndexFilter)
	if subIdxFilter == "" && r.config != nil {
		subIdxFilter = strings.TrimSpace(r.config.SubIndexFilter)
	}

	queryText := FormatQueryEmbeddingText(req.RiskType, req.Query)
	queryEmbedding, err := r.embedder.EmbedText(ctx, queryText)
	if err != nil {
		return nil, fmt.Errorf("向量化查询失败: %w", err)
	}
	queryDim := len(queryEmbedding)
	expectedModel := ""
	if r.embedder != nil {
		expectedModel = r.embedder.EmbeddingModelName()
	}

	chunks, err := r.vectors.Searchable(ctx, strings.TrimSpace(req.RiskType), subIdxFilter)
	if err != nil {
		return nil, fmt.Errorf("查询向量失败: %w", err)
	}

	type candidate struct {
		chunk      *KnowledgeChunk
		item       *KnowledgeItem
		similarity float64
	}

	candidates := make([]candidate, 0, len(chunks))
	for _, row := range chunks {
		var embedding []float32
		if err := json.Unmarshal([]byte(row.Embedding), &embedding); err != nil {
			r.logger.Warn("解析向量失败", zap.Error(err))
			continue
		}

		if row.Dim > 0 && len(embedding) != row.Dim {
			r.logger.Debug("跳过维度不一致的向量行", zap.String("chunkId", row.ID), zap.Int("rowDim", row.Dim), zap.Int("got", len(embedding)))
			continue
		}
		if queryDim > 0 && len(embedding) != queryDim {
			r.logger.Debug("跳过与查询维度不一致的向量", zap.String("chunkId", row.ID), zap.Int("queryDim", queryDim), zap.Int("got", len(embedding)))
			continue
		}
		if expectedModel != "" && strings.TrimSpace(row.Model) != "" && strings.TrimSpace(row.Model) != expectedModel {
			r.logger.Debug("跳过嵌入模型不一致的行", zap.String("chunkId", row.ID), zap.String("rowModel", row.Model), zap.String("expected", expectedModel))
			continue
		}

		similarity := cosineSimilarity(queryEmbedding, embedding)
		candidates = append(candidates, candidate{
			chunk: &KnowledgeChunk{
				ID:         row.ID,
				ItemID:     row.ItemID,
				ChunkIndex: row.ChunkIndex,
				ChunkText:  row.ChunkText,
				Embedding:  embedding,
			},
			item: &KnowledgeItem{
				ID:       row.ItemID,
				Category: row.Category,
				Title:    row.Title,
			},
			similarity: similarity,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].similarity > candidates[j].similarity
	})

	filtered := make([]candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.similarity >= threshold {
			filtered = append(filtered, c)
		}
	}

	if len(filtered) > topK {
		filtered = filtered[:topK]
	}

	results := make([]*RetrievalResult, len(filtered))
	for i, c := range filtered {
		results[i] = &RetrievalResult{
			Chunk:      c.chunk,
			Item:       c.item,
			Similarity: c.similarity,
			Score:      c.similarity,
		}
	}
	return results, nil
}

// RetrievalConfigFromYAML maps API/YAML retrieval settings into the knowledge package.
func RetrievalConfigFromYAML(r config.RetrievalConfig) *RetrievalConfig {
	return &RetrievalConfig{
		TopK:                r.TopK,
		SimilarityThreshold: r.SimilarityThreshold,
		SubIndexFilter:      r.SubIndexFilter,
		MultiQuery:          r.MultiQuery,
		Rerank:              r.Rerank,
		PostRetrieve:        r.PostRetrieve,
	}
}
