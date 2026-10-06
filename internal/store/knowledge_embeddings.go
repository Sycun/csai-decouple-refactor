package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// KnowledgeEmbeddings owns knowledge_embeddings: the vector rows a knowledge item is indexed into.
//
// Until now the table's shape had two independent definitions - one in the knowledge database's
// start-up sweep, one as a column migration - and the *same* column backfill existed twice:
// database.(*DB).migrateKnowledgeEmbeddingsColumns and knowledge.EnsureKnowledgeEmbeddingsSchema,
// with the same three ALTER statements side by side. Which one ran first decided nothing, because
// both are guarded by pragma_table_info, but two copies of a schema rule is how one of them drifts.
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
