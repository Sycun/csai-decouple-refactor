package store

import (
	"database/sql"
	"errors"
	"time"
)

// KnowledgeRetrieval owns knowledge_retrieval_logs: one row per RAG lookup, recording which
// conversation and message asked, what was retrieved, and which risk type was suspected.
//
// Two packages wrote this table - the knowledge manager, and the conversation-deletion path that had
// to clear a conversation's logs before the conversation row went away. Both go through here now, so
// "who deletes the logs of a deleted conversation" has one answer.
//
// What is stored is deliberately uninterpreted: created_at is bound exactly as the caller's
// time.Time, and retrieved_items stays the JSON text the writer produced. The reader parses both,
// including the several historical time formats rows were written in - that is display logic and
// belongs in the knowledge package, not here.
type KnowledgeRetrieval struct {
	db *sql.DB
}

// NewKnowledgeRetrieval binds the store to a connection.
func NewKnowledgeRetrieval(db *sql.DB) *KnowledgeRetrieval {
	return &KnowledgeRetrieval{db: db}
}

func (k *KnowledgeRetrieval) requireDB() error {
	if k == nil || k.db == nil {
		return errors.New("store: knowledge retrieval requires a database")
	}
	return nil
}

// RetrievalEntry is one log row as stored. ItemsJSON and CreatedAtText are passed through untouched;
// CreatedAtText is empty when the caller bound a time.Time, which is what the read path receives
// back as text from SQLite.
type RetrievalEntry struct {
	ID             string
	ConversationID string
	MessageID      string
	Query          string
	RiskType       string
	ItemsJSON      string
	CreatedAt      string
}

// The statement is the one the data layer used, foreign keys included: the rows are cleared by the
// conversation-deletion path *and* set to NULL by the FK, and dropping either would change what a
// deleted conversation leaves behind. EnsureSchema therefore has to run after conversations and
// messages exist.
const knowledgeRetrievalSchema = `
	CREATE TABLE IF NOT EXISTS knowledge_retrieval_logs (
		id TEXT PRIMARY KEY,
		conversation_id TEXT,
		message_id TEXT,
		query TEXT NOT NULL,
		risk_type TEXT,
		retrieved_items TEXT,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE SET NULL,
		FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE SET NULL
	);
	CREATE INDEX IF NOT EXISTS idx_knowledge_retrieval_logs_conversation ON knowledge_retrieval_logs(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_knowledge_retrieval_logs_message ON knowledge_retrieval_logs(message_id);
	CREATE INDEX IF NOT EXISTS idx_knowledge_retrieval_logs_created_at ON knowledge_retrieval_logs(created_at);
`

// EnsureSchema creates the table and its three indexes. Idempotent.
func (k *KnowledgeRetrieval) EnsureSchema() error {
	if err := k.requireDB(); err != nil {
		return err
	}
	_, err := k.db.Exec(knowledgeRetrievalSchema)
	return err
}

// knowledgeRetrievalStandaloneSchema is the same table without the foreign keys, for the separate
// knowledge database: conversations and messages may not exist in that file, so keys referencing them
// cannot be declared there. One table being created by two databases with two statements is a fact
// worth keeping in one place - the difference is intentional and named, not accidental.
const knowledgeRetrievalStandaloneSchema = `
	CREATE TABLE IF NOT EXISTS knowledge_retrieval_logs (
		id TEXT PRIMARY KEY,
		conversation_id TEXT,
		message_id TEXT,
		query TEXT NOT NULL,
		risk_type TEXT,
		retrieved_items TEXT,
		created_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_knowledge_retrieval_logs_conversation ON knowledge_retrieval_logs(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_knowledge_retrieval_logs_message ON knowledge_retrieval_logs(message_id);
	CREATE INDEX IF NOT EXISTS idx_knowledge_retrieval_logs_created_at ON knowledge_retrieval_logs(created_at);
`

// EnsureStandaloneSchema creates the no-foreign-key form, for the knowledge database.
func (k *KnowledgeRetrieval) EnsureStandaloneSchema() error {
	if err := k.requireDB(); err != nil {
		return err
	}
	_, err := k.db.Exec(knowledgeRetrievalStandaloneSchema)
	return err
}

const retrievalEntryColumns = `id, conversation_id, message_id, query, risk_type, retrieved_items, created_at`

// Record inserts one lookup. The created_at value is bound as given so rows keep the exact text form
// the writer used.
func (k *KnowledgeRetrieval) Record(entry RetrievalEntry, createdAt time.Time) error {
	if err := k.requireDB(); err != nil {
		return err
	}
	_, err := k.db.Exec(
		"INSERT INTO knowledge_retrieval_logs (id, conversation_id, message_id, query, risk_type, retrieved_items, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		entry.ID, entry.ConversationID, entry.MessageID, entry.Query, entry.RiskType, entry.ItemsJSON, createdAt,
	)
	return err
}

func scanRetrievalEntry(row *sql.Rows) (RetrievalEntry, error) {
	var e RetrievalEntry
	var conversationID, messageID, riskType, itemsJSON sql.NullString
	err := row.Scan(&e.ID, &conversationID, &messageID, &e.Query, &riskType, &itemsJSON, &e.CreatedAt)
	e.ConversationID, e.MessageID, e.RiskType = conversationID.String, messageID.String, riskType.String
	e.ItemsJSON = itemsJSON.String
	return e, err
}

// ListNewest returns the rows for one message, else one conversation, else everything - the same
// precedence the page has always used, and each branch orders by created_at descending with the
// caller's limit applied.
func (k *KnowledgeRetrieval) ListNewest(messageID, conversationID string, limit int) ([]RetrievalEntry, error) {
	if err := k.requireDB(); err != nil {
		return nil, err
	}
	var (
		rows *sql.Rows
		err  error
	)
	switch {
	case messageID != "":
		rows, err = k.db.Query(
			"SELECT "+retrievalEntryColumns+" FROM knowledge_retrieval_logs WHERE message_id = ? ORDER BY created_at DESC LIMIT ?",
			messageID, limit,
		)
	case conversationID != "":
		rows, err = k.db.Query(
			"SELECT "+retrievalEntryColumns+" FROM knowledge_retrieval_logs WHERE conversation_id = ? ORDER BY created_at DESC LIMIT ?",
			conversationID, limit,
		)
	default:
		rows, err = k.db.Query(
			"SELECT "+retrievalEntryColumns+" FROM knowledge_retrieval_logs ORDER BY created_at DESC LIMIT ?",
			limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RetrievalEntry
	for rows.Next() {
		entry, scanErr := scanRetrievalEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// DeleteByID removes one row and reports whether anything was there.
func (k *KnowledgeRetrieval) DeleteByID(id string) (bool, error) {
	if err := k.requireDB(); err != nil {
		return false, err
	}
	result, err := k.db.Exec("DELETE FROM knowledge_retrieval_logs WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// DeleteForConversation clears a conversation's logs. The conversation-deletion path calls this
// before removing the conversation row, because the rows are this table's, not conversations'.
func (k *KnowledgeRetrieval) DeleteForConversation(conversationID string) error {
	if err := k.requireDB(); err != nil {
		return err
	}
	_, err := k.db.Exec("DELETE FROM knowledge_retrieval_logs WHERE conversation_id = ?", conversationID)
	return err
}
