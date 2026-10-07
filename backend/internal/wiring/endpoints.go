package wiring

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
	approvalsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	approvalsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	changespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/public"
	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	endpointsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	securitypublic "github.com/MagicalWig34653/turaco/backend/internal/modules/security/public"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// assetLookup adapts the Assets public contract to the questions Endpoints ask.
type assetLookup struct{ a *assetspublic.Assets }

func (l assetLookup) FindBySerial(ctx context.Context, serial string) (endpointsapp.AssetInfo, error) {
	a, err := l.a.FindBySerial(ctx, serial)
	switch {
	case errors.Is(err, assetspublic.ErrNotFound):
		return endpointsapp.AssetInfo{}, endpointsapp.ErrAssetNotFound
	case errors.Is(err, assetspublic.ErrConflict):
		return endpointsapp.AssetInfo{}, endpointsapp.ErrAssetAmbiguous
	case err != nil:
		return endpointsapp.AssetInfo{}, err
	}
	return endpointsapp.AssetInfo{ID: a.ID, SerialNumber: a.SerialNumber, Status: a.Status}, nil
}

func (l assetLookup) ByID(ctx context.Context, assetID string) (endpointsapp.AssetInfo, bool, error) {
	found, err := l.a.Assets(ctx, []string{assetID})
	if err != nil {
		return endpointsapp.AssetInfo{}, false, err
	}
	a, ok := found[assetID]
	if !ok {
		return endpointsapp.AssetInfo{}, false, nil
	}
	return endpointsapp.AssetInfo{ID: a.ID, SerialNumber: a.SerialNumber, Status: a.Status}, true, nil
}

// directoryLookup adapts the Organization directory graph and work directory contracts to the questions the
// management views ask. All lookups are bounded by the Organization contract and report truncation.
type directoryLookup struct {
	graph *orgpublic.DirectoryGraph
	names *orgpublic.WorkDirectory
}

func (d directoryLookup) groups(in []orgpublic.DirectoryGroupRef) []endpointsapp.DirectoryGroup {
	out := make([]endpointsapp.DirectoryGroup, 0, len(in))
	for _, g := range in {
		out = append(out, endpointsapp.DirectoryGroup{ID: g.ID, ExternalID: g.ExternalID, Name: g.DisplayName, ObservedAt: g.LastObservedAt})
	}
	return out
}

func (d directoryLookup) GroupsByExternalIDs(ctx context.Context, providerKey string, ids []string) ([]endpointsapp.DirectoryGroup, bool, error) {
	g, cut, err := d.graph.GroupsByExternalIDs(ctx, providerKey, ids, orgpublic.MaxGraphRows)
	return d.groups(g), cut, err
}

func (d directoryLookup) GroupsByIDs(ctx context.Context, providerKey string, ids []string) ([]endpointsapp.DirectoryGroup, bool, error) {
	g, cut, err := d.graph.GroupsByIDs(ctx, providerKey, ids, orgpublic.MaxGraphRows)
	return d.groups(g), cut, err
}

func edges(in []orgpublic.GroupNestingEdge) []endpointsapp.NestingEdge {
	out := make([]endpointsapp.NestingEdge, 0, len(in))
	for _, e := range in {
		out = append(out, endpointsapp.NestingEdge{ChildID: e.ChildID, ParentID: e.ParentID})
	}
	return out
}

func (d directoryLookup) NestingUp(ctx context.Context, providerKey string, ids []string) ([]endpointsapp.NestingEdge, bool, error) {
	e, cut, err := d.graph.NestingUp(ctx, providerKey, ids, orgpublic.MaxGraphRows)
	return edges(e), cut, err
}

func (d directoryLookup) NestingDown(ctx context.Context, providerKey string, ids []string) ([]endpointsapp.NestingEdge, bool, error) {
	e, cut, err := d.graph.NestingDown(ctx, providerKey, ids, orgpublic.MaxGraphRows)
	return edges(e), cut, err
}

func memberships(in []orgpublic.GroupUserMembership) []endpointsapp.UserMembership {
	out := make([]endpointsapp.UserMembership, 0, len(in))
	for _, m := range in {
		out = append(out, endpointsapp.UserMembership{UserID: m.UserID, GroupID: m.GroupID, ObservedAt: m.LastObservedAt})
	}
	return out
}

func (d directoryLookup) UserMemberships(ctx context.Context, providerKey string, ids []string) ([]endpointsapp.UserMembership, bool, error) {
	m, cut, err := d.graph.UserMemberships(ctx, providerKey, ids, orgpublic.MaxGraphRows)
	return memberships(m), cut, err
}

func (d directoryLookup) GroupMembers(ctx context.Context, providerKey string, ids []string, limit int) ([]endpointsapp.UserMembership, bool, error) {
	m, cut, err := d.graph.GroupMembers(ctx, providerKey, ids, limit)
	return memberships(m), cut, err
}

func (d directoryLookup) UsersWithIdentity(ctx context.Context, providerKey string, ids []string) (map[string]bool, error) {
	return d.graph.UsersWithIdentity(ctx, providerKey, ids)
}

func (d directoryLookup) UserNames(ctx context.Context, ids []string) (map[string]string, error) {
	return d.names.UserNames(ctx, ids)
}

// Endpoints builds the Endpoints service over the Assets public contract, the Organization directory graph,
// the endpoint provider and the Software Management Provider. syncEnabled switches POST /api/v1/endpoint-sync
// on; softwareSync switches the Software Package synchronization on.
func Endpoints(pool *pgxpool.Pool, provider intune.Provider, syncEnabled bool, software softwaremgmt.Provider, softwareSync bool) *endpointsapp.Service {
	org := orgrepository.New(pool)
	assets := assetspublic.New(Assets(pool))
	dir := directoryLookup{graph: orgpublic.NewDirectoryGraph(org), names: orgpublic.NewWorkDirectory(org)}
	approvals := deploymentApprovals{a: approvalspublic.New(approvalsapp.NewService(approvalsrepository.New(pool), orgpublic.NewWorkDirectory(org), nil))}
	approvers := deploymentApprovers{perms: roles.NewEvaluator(pool, orgpublic.NewAuthorizationSubjects(org)), teams: orgpublic.NewWorkDirectory(org)}
	return endpointsapp.NewService(endpointsrepository.New(pool), assetLookup{assets}, provider, syncEnabled, nil).
		WithViews(dir, assets).WithSoftware(software, softwareSync).
		WithDeployments(approvals, changeWindows{c: changespublic.NewChanges(Changes(pool))}, assets).
		WithDeploymentApprovers(approvers).
		WithSecurity(deploymentSecurity{a: securitypublic.NewAdvisories(Security(pool))})
}

// DeploymentCorrelation is the Endpoints service of the correlation job: the engine service plus the follow-up Tasks
// (Tasks contract, context type deployment) and notifications.
func DeploymentCorrelation(pool *pgxpool.Pool, notifier *notifications.Service) *endpointsapp.Service {
	dir := orgpublic.NewWorkDirectory(orgrepository.New(pool))
	return Endpoints(pool, intune.NotConfigured{}, false, softwaremgmt.NotConfigured{}, false).
		WithFollowUps(deploymentTasks{c: taskspublicCreator(pool, dir, endpointsapp.FollowUpTaskContext)}, notifier)
}

// deploymentTasks adapts the Tasks contract to the follow-up Tasks of a Deployment. A Task whose assignee is no longer
// active is created unassigned rather than not at all.
type deploymentTasks struct{ c *taskspublic.Creator }

func (t deploymentTasks) CreateInTx(ctx context.Context, tx pgx.Tx, in endpointsapp.FollowUpTask) (string, bool, error) {
	due := in.DueAt
	create := taskspublic.CreateInput{Title: in.Title, DueAt: &due, ContextType: endpointsapp.FollowUpTaskContext, ContextID: in.DeploymentID}
	if in.AssignedUserID != "" {
		u := in.AssignedUserID
		create.AssignedUserID = &u
	}
	caller := taskspublic.Caller{Actor: in.Actor, CorrelationID: in.CorrelationID}
	id, err := t.c.CreateInTx(ctx, tx, caller, create)
	if errors.Is(err, taskspublic.ErrAssigneeInvalid) {
		// An inactive owner: the Task is created unassigned and reported as such.
		create.AssignedUserID = nil
		id, err = t.c.CreateInTx(ctx, tx, caller, create)
		return id, false, err
	}
	return id, create.AssignedUserID != nil, err
}

// deploymentSecurity adapts the Security public contract to the security context of a Deployment.
type deploymentSecurity struct{ a *securitypublic.Advisories }

func (d deploymentSecurity) DeploymentContext(ctx context.Context, productID string, deviceIDs []string, details bool) (endpointsapp.SecurityContext, error) {
	res, err := d.a.DeploymentContext(ctx, productID, deviceIDs, details)
	if err != nil {
		return endpointsapp.SecurityContext{}, err
	}
	out := endpointsapp.SecurityContext{AdvisoryCount: res.AdvisoryCount, OpenFindings: res.OpenFindings, Truncated: res.Truncated}
	for _, a := range res.Advisories {
		out.Advisories = append(out.Advisories, endpointsapp.SecurityContextAdvisory{ID: a.ID, Reference: a.Reference, Title: a.Title, Severity: a.Severity,
			Status: a.Status, KnownExploited: a.KnownExploited, OpenFindings: a.OpenFindings, AffectedDevices: a.AffectedDevices})
	}
	return out, nil
}

// deploymentApprovers answers who may approve plans: effective permissions (platform/authorization/roles) and
// current Team memberships (Organization work directory).
type deploymentApprovers struct {
	perms *roles.Evaluator
	teams *orgpublic.WorkDirectory
}

func (x deploymentApprovers) Permissions(ctx context.Context, userID string) (map[string]struct{}, error) {
	return x.perms.Permissions(ctx, userID)
}

func (x deploymentApprovers) TeamMemberIDs(ctx context.Context, teamID string) ([]string, error) {
	return x.teams.CurrentMemberIDs(ctx, teamID)
}

func (x deploymentApprovers) TeamIDsOfUser(ctx context.Context, userID string) ([]string, error) {
	return x.teams.CurrentTeamIDs(ctx, userID)
}

// deploymentApprovals adapts the Approvals contract to Deployment planning (subject deployment).
type deploymentApprovals struct{ a *approvalspublic.Approvals }

func (x deploymentApprovals) RequestInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID, label string, approver endpointsapp.Approver, excluded []string) (string, error) {
	return x.request(ctx, tx, endpointsapp.DeploymentApprovalSubject, actor, correlationID, subjectID, label, approver, excluded)
}

func (x deploymentApprovals) RequestRingInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID, label string, approver endpointsapp.Approver, excluded []string) (string, error) {
	return x.request(ctx, tx, endpointsapp.RingApprovalSubject, actor, correlationID, subjectID, label, approver, excluded)
}

func (x deploymentApprovals) request(ctx context.Context, tx pgx.Tx, subjectType string, actor audit.Actor, correlationID, subjectID, label string, approver endpointsapp.Approver, excluded []string) (string, error) {
	var by *string
	if actor.UserID != "" {
		u := actor.UserID
		by = &u
	}
	id, err := x.a.RequestInTx(ctx, tx, approvalspublic.Caller{Actor: actor, CorrelationID: correlationID}, approvalspublic.Request{
		SubjectType: subjectType, SubjectID: subjectID, SubjectLabel: label, StepIndex: 0,
		ApproverUserID: approver.UserID, ApproverTeamID: approver.TeamID, ExcludedUserIDs: excluded, RequestedBy: by,
	})
	var inv *approvalspublic.InvalidInputError
	if errors.Is(err, approvalspublic.ErrApproverInvalid) || errors.As(err, &inv) {
		return "", endpointsapp.ErrNoEligibleApprover
	}
	return id, err
}

func (x deploymentApprovals) CancelBySubjectInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID string) error {
	_, err := x.a.CancelBySubjectInTx(ctx, tx, approvalspublic.Caller{Actor: actor, CorrelationID: correlationID}, endpointsapp.DeploymentApprovalSubject, subjectID)
	return err
}

func (x deploymentApprovals) CancelRingBySubjectInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID string) error {
	_, err := x.a.CancelBySubjectInTx(ctx, tx, approvalspublic.Caller{Actor: actor, CorrelationID: correlationID}, endpointsapp.RingApprovalSubject, subjectID)
	return err
}

func (x deploymentApprovals) ForSubject(ctx context.Context, subjectID string) ([]endpointsapp.DeploymentApprovalInfo, error) {
	return x.forSubject(ctx, endpointsapp.DeploymentApprovalSubject, subjectID)
}

func (x deploymentApprovals) RingForSubject(ctx context.Context, subjectID string) ([]endpointsapp.DeploymentApprovalInfo, error) {
	return x.forSubject(ctx, endpointsapp.RingApprovalSubject, subjectID)
}

func (x deploymentApprovals) forSubject(ctx context.Context, subjectType, subjectID string) ([]endpointsapp.DeploymentApprovalInfo, error) {
	list, err := x.a.ForSubject(ctx, subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	out := make([]endpointsapp.DeploymentApprovalInfo, 0, len(list))
	for _, a := range list {
		out = append(out, endpointsapp.DeploymentApprovalInfo{ID: a.ID, Status: a.Status, ApproverUserID: a.ApproverUserID,
			ApproverTeamID: a.ApproverTeamID, DecidedByUserID: a.DecidedByUserID, DecidedAt: a.DecidedAt})
	}
	return out, nil
}

// changeWindows reads Changes with details so Deployment planning can apply the Changes read rule (requester,
// owner) to the caller; it passes on reference, status, window, requester and owner only (never the title).
type changeWindows struct{ c *changespublic.Changes }

func (x changeWindows) Lookup(ctx context.Context, ids []string) (map[string]endpointsapp.ChangeWindow, error) {
	found, err := x.c.Lookup(ctx, ids, changespublic.ReadScope{IncludeDetails: true})
	if err != nil {
		return nil, err
	}
	out := make(map[string]endpointsapp.ChangeWindow, len(found))
	for id, c := range found {
		out[id] = endpointsapp.ChangeWindow{ID: c.ID, Reference: c.Reference, Status: c.Status, RequesterID: c.RequesterID, OwnerID: c.OwnerID,
			WindowStart: c.WindowStart, WindowEnd: c.WindowEnd}
	}
	return out, nil
}

// SoftwareNotifications builds the consumer that tells software.approve holders about approval requests.
func SoftwareNotifications(pool *pgxpool.Pool, notifier *notifications.Service) *endpointsapp.SoftwareNotifications {
	org := orgrepository.New(pool)
	return endpointsapp.NewSoftwareNotifications(endpointsrepository.New(pool), orgpublic.NewWorkDirectory(org),
		orgpublic.NewNotificationRecipients(org), roles.NewEvaluator(pool, orgpublic.NewAuthorizationSubjects(org)), notifier)
}
