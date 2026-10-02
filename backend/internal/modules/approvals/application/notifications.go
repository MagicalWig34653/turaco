package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// NotificationCategories are the notification categories the Approvals module creates.
func NotificationCategories() []notifications.Category {
	return []notifications.Category{{
		Name: "approval.requested", Owner: "approvals", LinkType: "approval", LinkPath: "/approvals/{id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Approval requested: %s", Intro: "Your approval is requested:", Action: "Open approval"},
			"de": {Subject: "Genehmigung angefragt: %s", Intro: "Deine Genehmigung wird angefragt:", Action: "Genehmigung öffnen"},
		},
	}}
}

// Notifier creates notifications inside the consumer's transaction.
type Notifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}

// TxReader reads an approval inside the consumer's transaction.
type TxReader interface {
	GetTx(ctx context.Context, tx pgx.Tx, id string) (Approval, error)
}

// Consumers turn approval events into notifications. They run in the outbox
// dispatcher's claim transaction, write only through tx and are idempotent.
type Consumers struct {
	approvals TxReader
	dir       Directory
	notifier  Notifier
}

func NewConsumers(approvals TxReader, dir Directory, notifier Notifier) *Consumers {
	return &Consumers{approvals: approvals, dir: dir, notifier: notifier}
}

// OnApprovalRequested notifies the approver User, or the current members of the
// approver Team, except Users excluded from deciding. A stale event (the
// approval is no longer pending) notifies nobody. Approvers need no task or
// other permission: being named as an approver is the authority.
func (c *Consumers) OnApprovalRequested(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		ApprovalID string `json:"approvalId"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode ApprovalRequested payload: %w", err))
	}
	a, err := c.approvals.GetTx(ctx, tx, p.ApprovalID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if a.Status != StatusPending {
		return nil
	}
	var candidates []string
	if a.ApproverUserID != nil {
		candidates = append(candidates, *a.ApproverUserID)
	} else if a.ApproverTeamID != nil {
		members, err := c.dir.CurrentMemberIDs(ctx, *a.ApproverTeamID)
		if err != nil {
			return fmt.Errorf("load team members: %w", err)
		}
		candidates = members
	}
	skip := map[string]struct{}{}
	for _, ex := range a.ExcludedUserIDs {
		skip[ex] = struct{}{}
	}
	var ids []string
	for _, id := range candidates {
		if _, no := skip[id]; !no {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	active, err := c.dir.ActiveUsers(ctx, ids)
	if err != nil {
		return fmt.Errorf("check recipients: %w", err)
	}
	for _, id := range ids {
		if !active[id] {
			continue
		}
		if _, err := c.notifier.Create(ctx, tx, notifications.Intent{
			RecipientUserID: id, Category: "approval.requested", Params: map[string]any{"title": a.SubjectLabel},
			LinkType: "approval", LinkID: a.ID, DedupeKey: ev.ID + ":" + id,
		}); err != nil {
			return fmt.Errorf("create notification: %w", err)
		}
	}
	return nil
}
