package wiring

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
	approvalsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	approvalsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	endpointspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/public"
	endpointsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	raapp "github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
	rarepository "github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/repository"
	servicedeskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
)

// RemoteAccess builds the Remote Access service over the other modules' public contracts. providers decides which
// Remote Access Providers are enabled (an empty registry switches the feature off); approvalOwnership are the
// Device ownerships whose sessions need a second approver.
func RemoteAccess(pool *pgxpool.Pool, providers *remoteaccess.Registry, approvalOwnership []string) *raapp.Service {
	org := orgrepository.New(pool)
	work := orgpublic.NewWorkDirectory(org)
	approvals := remoteApprovals{a: approvalspublic.New(approvalsapp.NewService(approvalsrepository.New(pool), work, nil))}
	approvers := remoteApprovers{perms: roles.NewEvaluator(pool, orgpublic.NewAuthorizationSubjects(org)), teams: work}
	return raapp.NewService(rarepository.New(pool), remoteDevices{endpointspublic.NewDevices(endpointsrepository.New(pool))},
		remoteTickets{servicedeskpublic.NewTickets(pool)}, assetspublic.New(Assets(pool)), approvals, approvers, providers).
		WithApprovalOwnership(approvalOwnership)
}

// RemoteAccessNotifications builds the consumer that tells a Device's holder that a session was started.
func RemoteAccessNotifications(pool *pgxpool.Pool, notifier raapp.Notifier) *raapp.Notifications {
	return raapp.NewNotifications(rarepository.New(pool), remoteDevices{endpointspublic.NewDevices(endpointsrepository.New(pool))},
		assetspublic.New(Assets(pool)), orgpublic.NewWorkDirectory(orgrepository.New(pool)), notifier)
}

type remoteDevices struct{ d *endpointspublic.Devices }

func (x remoteDevices) Device(ctx context.Context, id string) (raapp.DeviceInfo, bool, error) {
	d, ok, err := x.d.Device(ctx, id)
	if err != nil || !ok {
		return raapp.DeviceInfo{}, false, err
	}
	return raapp.DeviceInfo{ID: d.ID, Name: d.Name, AssetID: d.AssetID, Ownership: d.Ownership, ObservedAt: d.ObservedAt, RetiredAt: d.RetiredAt}, true, nil
}

type remoteTickets struct{ t *servicedeskpublic.Tickets }

func (x remoteTickets) Ticket(ctx context.Context, id string) (raapp.TicketInfo, bool, error) {
	t, ok, err := x.t.Ticket(ctx, id)
	if err != nil || !ok {
		return raapp.TicketInfo{}, false, err
	}
	return raapp.TicketInfo{ID: t.ID, Reference: t.Reference, Open: servicedeskpublic.IsOpen(t.Status),
		AffectedUserID: t.AffectedUserID, ReporterUserID: t.ReporterUserID}, true, nil
}

type remoteApprovers struct {
	perms *roles.Evaluator
	teams *orgpublic.WorkDirectory
}

func (x remoteApprovers) Permissions(ctx context.Context, userID string) (map[string]struct{}, error) {
	return x.perms.Permissions(ctx, userID)
}

func (x remoteApprovers) TeamMemberIDs(ctx context.Context, teamID string) ([]string, error) {
	return x.teams.CurrentMemberIDs(ctx, teamID)
}

func (x remoteApprovers) TeamIDsOfUser(ctx context.Context, userID string) ([]string, error) {
	return x.teams.CurrentTeamIDs(ctx, userID)
}

// remoteApprovals adapts the Approvals contract to Remote Access (subject remote_access_session).
type remoteApprovals struct{ a *approvalspublic.Approvals }

func (x remoteApprovals) RequestInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID, label string, approver raapp.Approver, excluded []string) (string, error) {
	var by *string
	if actor.UserID != "" {
		u := actor.UserID
		by = &u
	}
	id, err := x.a.RequestInTx(ctx, tx, approvalspublic.Caller{Actor: actor, CorrelationID: correlationID}, approvalspublic.Request{
		SubjectType: raapp.SubjectType, SubjectID: subjectID, SubjectLabel: label, StepIndex: 0,
		ApproverUserID: approver.UserID, ApproverTeamID: approver.TeamID, ExcludedUserIDs: excluded, RequestedBy: by,
	})
	var inv *approvalspublic.InvalidInputError
	if errors.Is(err, approvalspublic.ErrApproverInvalid) || errors.As(err, &inv) {
		return "", raapp.ErrNoEligibleApprover
	}
	return id, err
}

func (x remoteApprovals) CancelBySubjectInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID string) error {
	_, err := x.a.CancelBySubjectInTx(ctx, tx, approvalspublic.Caller{Actor: actor, CorrelationID: correlationID}, raapp.SubjectType, subjectID)
	return err
}

func (x remoteApprovals) ForSubject(ctx context.Context, subjectID string) ([]raapp.ApprovalInfo, error) {
	list, err := x.a.ForSubject(ctx, raapp.SubjectType, subjectID)
	if err != nil {
		return nil, err
	}
	out := make([]raapp.ApprovalInfo, 0, len(list))
	for _, a := range list {
		out = append(out, raapp.ApprovalInfo{ID: a.ID, Status: a.Status, ApproverUserID: a.ApproverUserID, ApproverTeamID: a.ApproverTeamID, DecidedByUserID: a.DecidedByUserID})
	}
	return out, nil
}
