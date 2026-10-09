package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

var _ application.PeopleLookupStore = (*Repository)(nil)

// LookupPeople implements application.PeopleLookupStore: active internal employees whose name or e-mail contains
// the text, only when the caller is an active employee themselves (an external account gets nothing). The text is
// a bind parameter with LIKE wildcards escaped; the trigram indexes serve the ILIKE for 3 or more characters. The
// use is audited in the same transaction with the text length and the result count only.
func (r *Repository) LookupPeople(ctx context.Context, actor audit.Actor, correlationID, text string, limit int) ([]application.PersonRef, error) {
	if actor.UserID == "" {
		return []application.PersonRef{}, nil
	}
	out := []application.PersonRef{}
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT u.id::text, u.display_name, d.name
			FROM organization.users u
			LEFT JOIN organization.departments d ON d.id = u.department_id
			WHERE u.status = 'active' AND u.account_kind = 'employee' AND u.origin <> 'emergency'
			  AND (u.display_name ILIKE '%' || $2 || '%' OR u.primary_email ILIKE '%' || $2 || '%')
			  AND EXISTS (SELECT 1 FROM organization.users c WHERE c.id = $1::uuid AND c.status = 'active' AND c.account_kind = 'employee')
			ORDER BY u.display_name, u.id LIMIT $3`, actor.UserID, prefixPattern(text), limit)
		if err != nil {
			return fmt.Errorf("lookup people: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var p application.PersonRef
			var dept *string
			if err := rows.Scan(&p.ID, &p.DisplayName, &dept); err != nil {
				return fmt.Errorf("lookup people: scan: %w", err)
			}
			if dept != nil {
				p.Department = *dept
			}
			out = append(out, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		return audit.Record(ctx, tx, audit.Change{Action: "organization.people.lookup", TargetType: "user", TargetID: actor.UserID, Actor: actor,
			CorrelationID: correlationID, Metadata: map[string]any{"queryLength": len([]rune(text)), "resultCount": len(out)}})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
