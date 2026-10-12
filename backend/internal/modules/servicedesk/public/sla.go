package public

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ActivePolicyUsesCalendar is the Service Desk public contract used while a Business Calendar is locked for archive.
// Policy creation and calendar changes lock that calendar row, so the check cannot race a new active policy.
func ActivePolicyUsesCalendar(ctx context.Context, tx pgx.Tx, calendarID string) (bool, error) {
	var used bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM servicedesk.sla_policies WHERE calendar_id=$1 AND status='active')`, calendarID).Scan(&used)
	return used, err
}
