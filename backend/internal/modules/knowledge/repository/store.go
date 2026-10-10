// Package repository implements the Knowledge store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
)

// Repository stores articles.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const columns = `id::text, reference, title, summary, body, audience, status, author_user_id::text, published_at, version, created_at, updated_at`

func scan(row pgx.Row) (application.Article, error) {
	var a application.Article
	err := row.Scan(&a.ID, &a.Reference, &a.Title, &a.Summary, &a.Body, &a.Audience, &a.Status, &a.AuthorID, &a.PublishedAt, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

func (r *Repository) InsertTx(ctx context.Context, tx pgx.Tx, a application.Article) (application.Article, error) {
	out, err := scan(tx.QueryRow(ctx, `
		INSERT INTO knowledge.articles(title, summary, body, audience, status, author_user_id) VALUES ($1, $2, $3, $4, $5, $6::uuid) RETURNING `+columns,
		a.Title, a.Summary, a.Body, a.Audience, a.Status, a.AuthorID))
	if err != nil {
		return application.Article{}, fmt.Errorf("insert article: %w", err)
	}
	return out, nil
}

func (r *Repository) LockTx(ctx context.Context, tx pgx.Tx, id string) (application.Article, error) {
	if !validUUID(id) {
		return application.Article{}, application.ErrNotFound
	}
	a, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM knowledge.articles WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Article{}, application.ErrNotFound
	}
	if err != nil {
		return application.Article{}, fmt.Errorf("lock article: %w", err)
	}
	return a, nil
}

func (r *Repository) UpdateTx(ctx context.Context, tx pgx.Tx, a application.Article) (application.Article, error) {
	out, err := scan(tx.QueryRow(ctx, `
		UPDATE knowledge.articles SET title = $2, summary = $3, body = $4, audience = $5, status = $6, published_at = $7,
			version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+columns, a.ID, a.Title, a.Summary, a.Body, a.Audience, a.Status, a.PublishedAt))
	if err != nil {
		return application.Article{}, fmt.Errorf("update article: %w", err)
	}
	return out, nil
}

func (r *Repository) Get(ctx context.Context, id string) (application.Article, error) {
	if !validUUID(id) {
		return application.Article{}, application.ErrNotFound
	}
	a, err := scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM knowledge.articles WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Article{}, application.ErrNotFound
	}
	if err != nil {
		return application.Article{}, fmt.Errorf("get article: %w", err)
	}
	return a, nil
}

// List returns articles newest first, or, for a text query, the best matches first
// (a search returns one page without a cursor).
func (r *Repository) List(ctx context.Context, q application.Query) (application.Result, error) {
	page := q.Page.Normalize()
	args := []any{q.Audiences}
	conds := []string{"audience = ANY($1::text[])"}
	if len(q.Statuses) > 0 {
		args = append(args, q.Statuses)
		conds = append(conds, fmt.Sprintf("status = ANY($%d::text[])", len(args)))
	}
	order := "id DESC"
	if q.Text != "" {
		args = append(args, q.Text)
		n := len(args)
		tsq := fmt.Sprintf("websearch_to_tsquery('simple', $%d)", n)
		if q.Any {
			tsq = fmt.Sprintf("replace(websearch_to_tsquery('simple', $%d)::text, '&', '|')::tsquery", n)
		}
		// Partial words (Etikett finds Etikettendrucker): full-text search matches whole words only, so a word of
		// three or more characters also matches as a substring of title, summary, body or reference (trigram
		// indexes, migration 000079).
		conds = append(conds, "(search @@ "+tsq+substringMatch(q.Text, q.Any, &args)+")")
		order = fmt.Sprintf("ts_rank(search, %s) DESC, id DESC", tsq)
	} else if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		conds = append(conds, fmt.Sprintf("id < $%d::uuid", len(args)))
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM knowledge.articles WHERE %s ORDER BY %s LIMIT $%d`, columns, strings.Join(conds, " AND "), order, len(args)), args...)
	if err != nil {
		return application.Result{}, fmt.Errorf("list articles: %w", err)
	}
	defer rows.Close()
	items := make([]application.Article, 0, page.Limit+1)
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return application.Result{}, fmt.Errorf("list articles: scan: %w", err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return application.Result{}, fmt.Errorf("list articles: %w", err)
	}
	res := application.Result{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		if q.Text == "" {
			res.NextCursor = res.Items[page.Limit-1].ID
		}
	}
	return res, nil
}

// substringMatch returns " OR (...)" matching the words of text as substrings, or "" when no word has three
// characters. Every word must match unless any is set. Wildcards of the user's text are escaped.
func substringMatch(text string, any bool, args *[]any) string {
	const maxWords = 8
	var parts []string
	for _, w := range strings.Fields(text) {
		if utf8.RuneCountInString(w) < 3 || len(parts) == maxWords {
			continue
		}
		*args = append(*args, "%"+likeEscape(w)+"%")
		n := len(*args)
		parts = append(parts, fmt.Sprintf(`(title ILIKE $%[1]d ESCAPE '\' OR summary ILIKE $%[1]d ESCAPE '\' OR body ILIKE $%[1]d ESCAPE '\' OR reference ILIKE $%[1]d ESCAPE '\')`, n))
	}
	if len(parts) == 0 {
		return ""
	}
	glue := " AND "
	if any {
		glue = " OR "
	}
	return " OR (" + strings.Join(parts, glue) + ")"
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'):
			return false
		}
	}
	return true
}
