package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// KnowledgeItems owns knowledge_base_items: one row per knowledge file, holding its text.
//
// Every statement against this table used to be written inline in internal/knowledge, including the
// three page/list/search queries that build their SQL from caller parameters. They live here now, in
// the shape the callers already had: the store returns raw row text, and the knowledge package keeps
// interpreting it (its own time-format fallbacks, its own view types).
type KnowledgeItems struct {
	db *sql.DB
}

// NewKnowledgeItems binds the store to a connection.
func NewKnowledgeItems(db *sql.DB) *KnowledgeItems {
	return &KnowledgeItems{db: db}
}

func (k *KnowledgeItems) requireDB() error {
	if k == nil || k.db == nil {
		return errors.New("store: knowledge items requires a database")
	}
	return nil
}

// Item is one row. CreatedAt and UpdatedAt are the stored text: this table has rows written by
// different builds over the years, and the reader is the place that decides how to interpret them.
type Item struct {
	ID        string
	Category  string
	Title     string
	FilePath  string
	Content   string
	CreatedAt string
	UpdatedAt string
}

// CategoryCount is how many items one category holds.
type CategoryCount struct {
	Category string
	Count    int
}

const knowledgeItemsSchema = `
	CREATE TABLE IF NOT EXISTS knowledge_base_items (
		id TEXT PRIMARY KEY,
		category TEXT NOT NULL,
		title TEXT NOT NULL,
		file_path TEXT NOT NULL,
		content TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_knowledge_items_category ON knowledge_base_items(category);
`

// EnsureSchema creates the table and its category index. Idempotent.
func (k *KnowledgeItems) EnsureSchema() error {
	if err := k.requireDB(); err != nil {
		return err
	}
	if _, err := k.db.Exec(knowledgeItemsSchema); err != nil {
		return fmt.Errorf("创建knowledge_base_items表失败: %w", err)
	}
	return nil
}

const itemColumns = `id, category, title, file_path, content, created_at, updated_at`
const itemColumnsNoContent = `id, category, title, file_path, created_at, updated_at`

func scanItem(row interface{ Scan(...interface{}) error }, withContent bool) (Item, error) {
	var it Item
	if withContent {
		var content sql.NullString
		err := row.Scan(&it.ID, &it.Category, &it.Title, &it.FilePath, &content, &it.CreatedAt, &it.UpdatedAt)
		it.Content = content.String
		return it, err
	}
	err := row.Scan(&it.ID, &it.Category, &it.Title, &it.FilePath, &it.CreatedAt, &it.UpdatedAt)
	return it, err
}

// ExistingAt returns the row stored for a file path, if any. The scan of content and updated_at is
// what lets the directory scan decide between "new", "changed" and "unchanged".
//
// content is read as NULL-able: the column allows it, rows written before the text was stored have
// it, and scanning that into a plain string is an error - one such row failed the whole directory
// scan with 查询知识项失败, and the same row made GetByID report the item as unreadable.
func (k *KnowledgeItems) ExistingAt(filePath string) (id, content string, updatedAt time.Time, found bool, err error) {
	if err := k.requireDB(); err != nil {
		return "", "", time.Time{}, false, err
	}
	var stored sql.NullString
	err = k.db.QueryRow("SELECT id, content, updated_at FROM knowledge_base_items WHERE file_path = ?", filePath).Scan(&id, &stored, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", time.Time{}, false, nil
	}
	if err != nil {
		return "", "", time.Time{}, false, err
	}
	return id, stored.String, updatedAt, true, nil
}

// Insert adds one row with both timestamps set to the same instant.
func (k *KnowledgeItems) Insert(id, category, title, filePath, content string, at time.Time) error {
	if err := k.requireDB(); err != nil {
		return err
	}
	_, err := k.db.Exec(
		"INSERT INTO knowledge_base_items (id, category, title, file_path, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		id, category, title, filePath, content, at, at,
	)
	return err
}

// UpdateContent rewrites the text of a row and moves updated_at; the file path stays, which is what
// the directory scan does when a file's content changed under the same path.
func (k *KnowledgeItems) UpdateContent(id, category, title, content string, at time.Time) error {
	if err := k.requireDB(); err != nil {
		return err
	}
	_, err := k.db.Exec(
		"UPDATE knowledge_base_items SET category = ?, title = ?, content = ?, updated_at = ? WHERE id = ?",
		category, title, content, at, id,
	)
	return err
}

// Update moves the file path as well: a rename through the API changes where the row points.
func (k *KnowledgeItems) Update(id, category, title, filePath, content string, at time.Time) error {
	if err := k.requireDB(); err != nil {
		return err
	}
	_, err := k.db.Exec(
		"UPDATE knowledge_base_items SET category = ?, title = ?, file_path = ?, content = ?, updated_at = ? WHERE id = ?",
		category, title, filePath, content, at, id,
	)
	return err
}

// Categories lists the distinct categories, ordered.
func (k *KnowledgeItems) Categories() ([]string, error) {
	if err := k.requireDB(); err != nil {
		return nil, err
	}
	rows, err := k.db.Query("SELECT DISTINCT category FROM knowledge_base_items ORDER BY category")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var category string
		if err := rows.Scan(&category); err != nil {
			return nil, err
		}
		out = append(out, category)
	}
	return out, rows.Err()
}

// CategoriesWithCounts is the grouped count behind the category page.
func (k *KnowledgeItems) CategoriesWithCounts() ([]CategoryCount, error) {
	if err := k.requireDB(); err != nil {
		return nil, err
	}
	rows, err := k.db.Query(`
		SELECT category, COUNT(*) as item_count 
		FROM knowledge_base_items 
		GROUP BY category 
		ORDER BY category
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategoryCount
	for rows.Next() {
		var c CategoryCount
		if err := rows.Scan(&c.Category, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Count returns the number of rows, or of one category's rows when category is not empty.
func (k *KnowledgeItems) Count(category string) (int, error) {
	if err := k.requireDB(); err != nil {
		return 0, err
	}
	var count int
	var err error
	if category != "" {
		err = k.db.QueryRow("SELECT COUNT(*) FROM knowledge_base_items WHERE category = ?", category).Scan(&count)
	} else {
		err = k.db.QueryRow("SELECT COUNT(*) FROM knowledge_base_items").Scan(&count)
	}
	return count, err
}

// ItemFilter is the caller's page window over the item table. Zero limit means "no limit", which is
// how the callers have always used it, and offset is only applied together with a limit.
type ItemFilter struct {
	Category       string
	Limit          int
	Offset         int
	IncludeContent bool
}

func (f ItemFilter) build(base string) (string, []interface{}) {
	query := base
	var args []interface{}
	if f.Category != "" {
		query += " WHERE category = ?"
		args = append(args, f.Category)
	}
	query += " ORDER BY category, title"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
		if f.Offset > 0 {
			query += " OFFSET ?"
			args = append(args, f.Offset)
		}
	}
	return query, args
}

// List pages through rows, with or without the text.
func (k *KnowledgeItems) List(filter ItemFilter) ([]Item, error) {
	if err := k.requireDB(); err != nil {
		return nil, err
	}
	base := "SELECT " + itemColumns + " FROM knowledge_base_items"
	if !filter.IncludeContent {
		base = "SELECT " + itemColumnsNoContent + " FROM knowledge_base_items"
	}
	return k.listQuery(base, filter)
}

func (k *KnowledgeItems) listQuery(base string, filter ItemFilter) ([]Item, error) {
	query, args := filter.build(base)
	rows, err := k.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		item, scanErr := scanItem(rows, filter.IncludeContent)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Search matches a keyword against title, category, path and content. SQLite's LIKE is already
// case-insensitive for ASCII, and the LOWER() on both sides is what the callers had; a category
// narrows the result without changing the match.
func (k *KnowledgeItems) Search(keyword, category string) ([]Item, error) {
	if err := k.requireDB(); err != nil {
		return nil, err
	}
	if keyword == "" {
		return nil, errors.New("store: knowledge item search needs a keyword")
	}
	pattern := "%" + keyword + "%"
	query := `
		SELECT id, category, title, file_path, created_at, updated_at 
		FROM knowledge_base_items 
		WHERE (LOWER(title) LIKE LOWER(?) OR LOWER(category) LIKE LOWER(?) OR LOWER(file_path) LIKE LOWER(?) OR LOWER(content) LIKE LOWER(?))
	`
	args := []interface{}{pattern, pattern, pattern, pattern}
	if category != "" {
		query += " AND category = ?"
		args = append(args, category)
	}
	query += " ORDER BY category, title"

	rows, err := k.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		item, scanErr := scanItem(rows, false)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetByID returns one row and reports whether it was there.
func (k *KnowledgeItems) GetByID(id string) (Item, bool, error) {
	if err := k.requireDB(); err != nil {
		return Item{}, false, err
	}
	row := k.db.QueryRow("SELECT "+itemColumns+" FROM knowledge_base_items WHERE id = ?", id)
	item, err := scanItem(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	return item, true, nil
}

// FilePath returns where a row's file lives, for the delete path that removes the file too.
func (k *KnowledgeItems) FilePath(id string) (string, bool, error) {
	if err := k.requireDB(); err != nil {
		return "", false, err
	}
	var path string
	err := k.db.QueryRow("SELECT file_path FROM knowledge_base_items WHERE id = ?", id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return path, true, nil
}

// Delete removes one row. The vectors go with it through the foreign key on knowledge_embeddings.
func (k *KnowledgeItems) Delete(id string) error {
	if err := k.requireDB(); err != nil {
		return err
	}
	_, err := k.db.Exec("DELETE FROM knowledge_base_items WHERE id = ?", id)
	return err
}

// AllIDs lists every item id oldest-update first, the order a full rebuild indexes in.
func (k *KnowledgeItems) AllIDs(ctx context.Context) ([]string, error) {
	if err := k.requireDB(); err != nil {
		return nil, err
	}
	rows, err := k.db.QueryContext(ctx, "SELECT id FROM knowledge_base_items ORDER BY updated_at ASC, id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// WithoutVectors lists the items that have no vector rows, oldest update first: the "index what is
// missing" pass. It reads knowledge_embeddings on purpose - the question is about this table's rows,
// and the join is how the answer is expressed.
func (k *KnowledgeItems) WithoutVectors(ctx context.Context) ([]string, error) {
	if err := k.requireDB(); err != nil {
		return nil, err
	}
	rows, err := k.db.QueryContext(ctx, `
		SELECT i.id
		FROM knowledge_base_items i
		LEFT JOIN knowledge_embeddings e ON e.item_id = i.id
		WHERE e.item_id IS NULL
		ORDER BY i.updated_at ASC, i.id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
