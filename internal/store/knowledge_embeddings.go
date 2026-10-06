package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// KnowledgeEmbeddings owns knowledge_embeddings: the vector rows a knowledge item is indexed into,
// from creating the table to writing a batch and reading the candidates retrieval scores.
//
// The table arrived with two independent definitions of its shape - a start-up sweep in the knowledge
// database and a column migration - and the same three-column backfill existed twice, in both places,
// each guarded by pragma_table_info so neither was wrong and either could drift. EnsureSchema is now
// the one spelling: it creates what a new base needs and backfills what an old one is missing.
type KnowledgeEmbeddings struct {
	db *sql.DB
}

// NewKnowledgeEmbeddings binds the store to a connection.
func NewKnowledgeEmbeddings(db *sql.DB) *KnowledgeEmbeddings {
	return &KnowledgeEmbeddings{db: db}
}

func (k *KnowledgeEmbeddings) requireDB() error {
	if k == nil || k.db == nil {
		return errors.New("store: knowledge embeddings requires a database")
	}
	return nil
}

// The statement is the one the knowledge database used, foreign key included: deleting a knowledge
// item is what removes its vectors.
const knowledgeEmbeddingsSchema = `
	CREATE TABLE IF NOT EXISTS knowledge_embeddings (
		id TEXT PRIMARY KEY,
		item_id TEXT NOT NULL,
		chunk_index INTEGER NOT NULL,
		chunk_text TEXT NOT NULL,
		embedding TEXT NOT NULL,
		sub_indexes TEXT NOT NULL DEFAULT '',
		embedding_model TEXT NOT NULL DEFAULT '',
		embedding_dim INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (item_id) REFERENCES knowledge_base_items(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_knowledge_embeddings_item_id ON knowledge_embeddings(item_id);
`

// embeddingColumnMigrations backfills the three columns that arrived after the first releases of this
// table. A table created by knowledgeEmbeddingsSchema already has them, so each ALTER is guarded.
var embeddingColumnMigrations = []struct {
	column string
	stmt   string
}{
	{"sub_indexes", `ALTER TABLE knowledge_embeddings ADD COLUMN sub_indexes TEXT NOT NULL DEFAULT ''`},
	{"embedding_model", `ALTER TABLE knowledge_embeddings ADD COLUMN embedding_model TEXT NOT NULL DEFAULT ''`},
	{"embedding_dim", `ALTER TABLE knowledge_embeddings ADD COLUMN embedding_dim INTEGER NOT NULL DEFAULT 0`},
}

// EnsureSchema creates the table and its index, then backfills the columns - the whole shape of the
// table in one call.
func (k *KnowledgeEmbeddings) EnsureSchema() error {
	if err := k.requireDB(); err != nil {
		return err
	}
	if _, err := k.db.Exec(knowledgeEmbeddingsSchema); err != nil {
		return fmt.Errorf("创建knowledge_embeddings表失败: %w", err)
	}
	return k.EnsureColumns()
}

// EnsureColumns backfills sub_indexes / embedding_model / embedding_dim on a table that already
// exists. A missing table is not an error here: that is the indexer's construction path running
// before the knowledge database has created anything.
func (k *KnowledgeEmbeddings) EnsureColumns() error {
	if err := k.requireDB(); err != nil {
		return err
	}
	var n int
	if err := k.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='knowledge_embeddings'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	for _, m := range embeddingColumnMigrations {
		var colCount int
		if err := k.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('knowledge_embeddings') WHERE name = ?`, m.column).Scan(&colCount); err != nil {
			return err
		}
		if colCount > 0 {
			continue
		}
		if _, err := k.db.Exec(m.stmt); err != nil {
			return err
		}
	}
	return nil
}

// VectorChunk is one row of the table with the two item columns retrieval reads off the join. The
// embedding stays the stored JSON text: decoding it, checking its length against the row's declared
// dimension and scoring it are the retriever's decisions, not the row's.
type VectorChunk struct {
	ID         string
	ItemID     string
	ChunkIndex int
	ChunkText  string
	Embedding  string
	Model      string
	Dim        int
	Category   string
	Title      string
}

// Searchable returns every vector row retrieval may score, narrowed by category and by the
// sub-index tag. It is a full scan by design - the similarity is computed in Go, so there is no
// index to steer this at - which is why it takes a context and gives up on the same cadence the
// caller used to check it on.
//
// A row whose item has no category or title is dropped by the inner join, exactly as it was before.
func (k *KnowledgeEmbeddings) Searchable(ctx context.Context, riskType, subIndexFilter string) ([]VectorChunk, error) {
	if err := k.requireDB(); err != nil {
		return nil, err
	}
	query := `SELECT e.id, e.item_id, e.chunk_index, e.chunk_text, e.embedding, e.embedding_model, e.embedding_dim, i.category, i.title
FROM knowledge_embeddings e
JOIN knowledge_base_items i ON e.item_id = i.id
WHERE 1=1`
	var args []interface{}
	if strings.TrimSpace(riskType) != "" {
		query += ` AND TRIM(i.category) = TRIM(?) COLLATE NOCASE`
		args = append(args, riskType)
	}
	if tag := strings.TrimSpace(subIndexFilter); tag != "" {
		tag = strings.ToLower(strings.ReplaceAll(tag, " ", ""))
		query += ` AND (TRIM(COALESCE(e.sub_indexes,'')) = '' OR INSTR(',' || LOWER(REPLACE(e.sub_indexes,' ','')) || ',', ',' || ? || ',') > 0)`
		args = append(args, tag)
	}
	rows, err := k.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]VectorChunk, 0)
	for rowNum := 1; rows.Next(); rowNum++ {
		if rowNum%48 == 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
		}
		var c VectorChunk
		if err := rows.Scan(&c.ID, &c.ItemID, &c.ChunkIndex, &c.ChunkText, &c.Embedding, &c.Model, &c.Dim, &c.Category, &c.Title); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NewChunk is a row the indexer is writing. Every field is final: the id, the encoded vector and the
// dimension are the caller's, and only created_at is generated by the statement.
type NewChunk struct {
	ID         string
	ItemID     string
	ChunkIndex int
	ChunkText  string
	Embedding  string
	SubIndexes string
	Model      string
	Dim        int
}

// InsertChunks writes a batch as one transaction: a document rejected half-way through leaves no
// vectors for the item, which is what the indexer's rollback meant.
func (k *KnowledgeEmbeddings) InsertChunks(ctx context.Context, chunks []NewChunk) error {
	if err := k.requireDB(); err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	tx, err := k.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, c := range chunks {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO knowledge_embeddings (id, item_id, chunk_index, chunk_text, embedding, sub_indexes, embedding_model, embedding_dim, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
			c.ID, c.ItemID, c.ChunkIndex, c.ChunkText, c.Embedding, c.SubIndexes, c.Model, c.Dim,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteItem drops an item's vectors. Zero rows is not an error: an item that was never indexed
// still deletes cleanly.
func (k *KnowledgeEmbeddings) DeleteItem(itemID string) error {
	if err := k.requireDB(); err != nil {
		return err
	}
	_, err := k.db.Exec(`DELETE FROM knowledge_embeddings WHERE item_id = ?`, itemID)
	return err
}

// CountRows is the size of the table, the number the index-status page reports beside the item count.
func (k *KnowledgeEmbeddings) CountRows() (int, error) {
	if err := k.requireDB(); err != nil {
		return 0, err
	}
	var count int
	err := k.db.QueryRow(`SELECT COUNT(*) FROM knowledge_embeddings`).Scan(&count)
	return count, err
}

// IndexedItems counts the items that have at least one vector row - how far a rebuild has got.
func (k *KnowledgeEmbeddings) IndexedItems() (int, error) {
	if err := k.requireDB(); err != nil {
		return 0, err
	}
	var count int
	err := k.db.QueryRow(`SELECT COUNT(DISTINCT item_id) FROM knowledge_embeddings`).Scan(&count)
	return count, err
}
