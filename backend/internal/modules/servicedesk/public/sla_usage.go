package public

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// CalendarUsage exposes Service Desk's active-policy check to the composition root.
type CalendarUsage struct{}

func (CalendarUsage) ActivePolicyUsesCalendar(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	return ActivePolicyUsesCalendar(ctx, tx, id)
}
