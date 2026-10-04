// Package public holds the Planning module's public read contract for other
// modules and the adapters that connect Planning to the public contracts of
// the modules it depends on, so the application depends only on small
// interfaces of its own.
package public

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	changespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
	procurementpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/public"
	servicespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/services/public"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// ChangesAdapter adapts the Changes contract.
type ChangesAdapter struct{ c *changespublic.Changes }

func NewChangesAdapter(c *changespublic.Changes) *ChangesAdapter { return &ChangesAdapter{c: c} }

func changeInfo(c changespublic.ChangeInfo) application.ChangeInfo {
	return application.ChangeInfo{ID: c.ID, Reference: c.Reference, Title: c.Title, Kind: c.Kind, Risk: c.Risk, Status: c.Status,
		RequesterID: c.RequesterID, OwnerID: c.OwnerID, WindowStart: c.WindowStart, WindowEnd: c.WindowEnd}
}

func (x *ChangesAdapter) Lookup(ctx context.Context, ids []string) (map[string]application.ChangeInfo, error) {
	found, err := x.c.Lookup(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.ChangeInfo, len(found))
	for id, c := range found {
		out[id] = changeInfo(c)
	}
	return out, nil
}

func (x *ChangesAdapter) Calendar(ctx context.Context, from, to time.Time, limit int) ([]application.ChangeCalendarEntry, bool, error) {
	page, err := x.c.Calendar(ctx, from, to, limit)
	if errors.Is(err, changespublic.ErrInvalidRange) {
		return nil, false, application.ErrInvalidRange
	}
	if err != nil {
		return nil, false, err
	}
	out := make([]application.ChangeCalendarEntry, 0, len(page.Entries))
	for _, e := range page.Entries {
		entry := application.ChangeCalendarEntry{Change: changeInfo(e.Change), Affected: make([]application.Node, 0, len(e.Affected))}
		for _, n := range e.Affected {
			entry.Affected = append(entry.Affected, application.Node{Type: n.Type, ID: n.ID})
		}
		out = append(out, entry)
	}
	return out, page.Truncated, nil
}

// Tasks adapts the Tasks contract.
type Tasks struct{ c *taskspublic.Creator }

func NewTasks(c *taskspublic.Creator) *Tasks { return &Tasks{c: c} }

func (x *Tasks) Tasks(ctx context.Context, ids []string) ([]application.TaskInfo, error) {
	ts, err := x.c.Tasks(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]application.TaskInfo, 0, len(ts))
	for _, t := range ts {
		out = append(out, application.TaskInfo{ID: t.ID, Title: t.Title, Status: t.Status, DueAt: t.DueAt})
	}
	return out, nil
}

// Procurement adapts the Procurement Requests contract.
type Procurement struct{ r *procurementpublic.Requests }

func NewProcurement(r *procurementpublic.Requests) *Procurement { return &Procurement{r: r} }

func (x *Procurement) Requests(ctx context.Context, ids []string) (map[string]application.RequestInfo, error) {
	found, err := x.r.Lookup(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.RequestInfo, len(found))
	for id, r := range found {
		out[id] = application.RequestInfo{ID: r.ID, Reference: r.Reference, Quantity: r.Quantity, Status: r.Status}
	}
	return out, nil
}

// Services adapts the Services contract.
type Services struct{ s *servicespublic.Services }

func NewServices(s *servicespublic.Services) *Services { return &Services{s: s} }

func (x *Services) Lookup(ctx context.Context, ids []string) (map[string]application.ServiceInfo, error) {
	found, err := x.s.Lookup(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.ServiceInfo, len(found))
	for id, v := range found {
		out[id] = application.ServiceInfo{ID: v.ID, Reference: v.Reference, Name: v.Name, Status: v.Status, Criticality: v.Criticality}
	}
	return out, nil
}

// Approvals adapts the Approvals contract.
type Approvals struct{ a *approvalspublic.Approvals }

func NewApprovals(a *approvalspublic.Approvals) *Approvals { return &Approvals{a: a} }

func (x *Approvals) RequestInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, r application.ApprovalRequest) (string, error) {
	var by *string
	if actor.UserID != "" {
		u := actor.UserID
		by = &u
	}
	id, err := x.a.RequestInTx(ctx, tx, approvalspublic.Caller{Actor: actor, CorrelationID: correlationID}, approvalspublic.Request{
		SubjectType: application.SubjectType, SubjectID: r.SubjectID, SubjectLabel: r.Label, StepIndex: 0,
		ApproverUserID: r.ApproverUserID, ApproverTeamID: r.ApproverTeamID, ExcludedUserIDs: r.ExcludedUserIDs, RequestedBy: by,
	})
	var inv *approvalspublic.InvalidInputError
	if errors.Is(err, approvalspublic.ErrApproverInvalid) || errors.As(err, &inv) {
		return "", application.ErrNoEligibleApprover
	}
	return id, err
}

func (x *Approvals) CancelBySubjectInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID string) error {
	_, err := x.a.CancelBySubjectInTx(ctx, tx, approvalspublic.Caller{Actor: actor, CorrelationID: correlationID}, application.SubjectType, subjectID)
	return err
}

func (x *Approvals) ForSubject(ctx context.Context, subjectID string) ([]application.ApprovalInfo, error) {
	list, err := x.a.ForSubject(ctx, application.SubjectType, subjectID)
	if err != nil {
		return nil, err
	}
	out := make([]application.ApprovalInfo, 0, len(list))
	for _, a := range list {
		out = append(out, application.ApprovalInfo{ID: a.ID, StepIndex: a.StepIndex, Status: a.Status, ApproverUserID: a.ApproverUserID,
			ApproverTeamID: a.ApproverTeamID, DecidedByUserID: a.DecidedByUserID, DecidedAt: a.DecidedAt})
	}
	return out, nil
}

func (x *Approvals) CanView(ctx context.Context, subjectID, userID string) (bool, error) {
	return x.a.CanView(ctx, application.SubjectType, subjectID, userID)
}
