package public

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NameLookup answers read-only display-name questions for other modules (the audit log). It returns identifiers
// and names only: never titles, descriptions or personal data.
type NameLookup struct{ pool *pgxpool.Pool }

// NewNameLookup returns the contract over the database pool.
func NewNameLookup(pool *pgxpool.Pool) *NameLookup { return &NameLookup{pool: pool} }

// TicketNumbers returns ticket id -> issued number (for example INF-000001). Ids must be UUIDs; unknown ids are
// absent. One query per call.
func (n *NameLookup) TicketNumbers(ctx context.Context, ids []string) (map[string]string, error) {
	return n.pairs(ctx, `SELECT id::text, reference FROM servicedesk.tickets WHERE id = ANY($1::uuid[])`, ids)
}

// QueueNames returns Ticket Queue id -> name; unknown ids are absent. One query per call.
func (n *NameLookup) QueueNames(ctx context.Context, ids []string) (map[string]string, error) {
	return n.pairs(ctx, `SELECT id::text, name FROM servicedesk.queues WHERE id = ANY($1::uuid[])`, ids)
}

func (n *NameLookup) pairs(ctx context.Context, query string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := n.pool.Query(ctx, query, ids)
	if err != nil {
		return nil, fmt.Errorf("servicedesk names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan servicedesk name: %w", err)
		}
		out[id] = name
	}
	return out, rows.Err()
}
