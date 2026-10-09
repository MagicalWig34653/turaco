// Package public holds the adapters that connect the Changes module to the
// public contracts of the modules it depends on, so the application depends
// only on small interfaces of its own.
package public

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	infrapublic "github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/public"
	servicespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/services/public"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

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
		out[id] = application.ServiceInfo{ID: v.ID, Reference: v.Reference, Name: v.Name, Status: v.Status,
			OwnerUserID: v.OwnerUserID, OwnerTeamID: v.OwnerTeamID, SupportTeamID: v.SupportTeamID}
	}
	return out, nil
}

// Impact walks downstream from one affected resource with the caller's
// visibility; services.view is a precondition the application checks.
func (x *Services) Impact(ctx context.Context, p application.Principal, nodeType, id string, depth int) (application.Impact, error) {
	res, err := x.s.Impact(ctx, servicespublic.ImpactCaller{UserID: p.UserID, View: true, InfraView: p.InfraView, AssetsView: p.AssetsView},
		servicespublic.ImpactInput{Type: nodeType, ID: id, Direction: "downstream", Depth: depth})
	if errors.Is(err, servicespublic.ErrNotFound) {
		return application.Impact{}, application.ErrNotFound
	}
	if errors.Is(err, servicespublic.ErrImpactBusy) {
		return application.Impact{}, application.ErrImpactBusy
	}
	if err != nil {
		return application.Impact{}, err
	}
	out := application.Impact{Type: res.Start.Type, ID: res.Start.ID, Name: res.Start.Name, Reference: res.Start.Reference,
		Truncated: res.Truncated, DepthLimited: res.DepthLimited, NodeLimited: res.NodeLimited,
		Nodes: make([]application.ImpactNode, 0, len(res.Nodes))}
	for _, n := range res.Nodes {
		out.Nodes = append(out.Nodes, application.ImpactNode{Type: n.Type, ID: n.ID, Name: n.Name, Reference: n.Reference, Status: n.Status,
			Criticality: n.Criticality, Missing: n.Missing, Hidden: n.Hidden, Depth: n.Depth, Confidence: n.Confidence})
	}
	return out, nil
}

// Infrastructure adapts the Infrastructure contract.
type Infrastructure struct{ i *infrapublic.Infrastructure }

func NewInfrastructure(i *infrapublic.Infrastructure) *Infrastructure { return &Infrastructure{i: i} }

func (x *Infrastructure) VMs(ctx context.Context, ids []string) (map[string]application.VMInfo, error) {
	found, err := x.i.VMs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.VMInfo, len(found))
	for id, v := range found {
		out[id] = application.VMInfo{ID: v.ID, Name: v.Name, State: v.State}
	}
	return out, nil
}

// Assets adapts the Assets contract.
type Assets struct{ a *assetspublic.Assets }

func NewAssets(a *assetspublic.Assets) *Assets { return &Assets{a: a} }

func (x *Assets) Assets(ctx context.Context, ids []string) (map[string]application.AssetInfo, error) {
	found, err := x.a.AssetsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.AssetInfo, len(found))
	for id, a := range found {
		out[id] = application.AssetInfo{ID: a.ID, Reference: a.Reference, Status: a.Status}
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

// Tasks adapts the Tasks contract; the tasks of a Change carry the context type "change".
type Tasks struct{ c *taskspublic.Creator }

func NewTasks(c *taskspublic.Creator) *Tasks { return &Tasks{c: c} }

func (x *Tasks) CreateInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, in application.TaskInput) (string, error) {
	id, err := x.c.CreateInTx(ctx, tx, taskspublic.Caller{Actor: actor, CorrelationID: correlationID}, taskspublic.CreateInput{
		Title: in.Title, Description: in.Description, DueAt: in.DueAt, AssignedUserID: in.AssignedUserID, AssignedTeamID: in.AssignedTeamID,
		ContextType: application.TaskContextType, ContextID: in.ChangeID,
	})
	var inv *taskspublic.InvalidInputError
	switch {
	case errors.Is(err, taskspublic.ErrAssigneeInvalid):
		return "", application.ErrReferenceInvalid
	case errors.As(err, &inv):
		return "", &application.InvalidInputError{Message: inv.Message}
	}
	return id, err
}

func (x *Tasks) CancelByContextInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, changeID, reason string) (int, error) {
	return x.c.CancelByContextInTx(ctx, tx, taskspublic.Caller{Actor: actor, CorrelationID: correlationID}, application.TaskContextType, changeID, reason)
}

func (x *Tasks) StatusesInTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error) {
	return x.c.StatusesInTx(ctx, tx, ids)
}

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

// Search finds active Services for the affected-resource picker.
func (x *Services) Search(ctx context.Context, text string, limit int) ([]application.LookupHit, error) {
	found, err := x.s.Search(ctx, text, limit)
	if err != nil {
		return nil, err
	}
	out := make([]application.LookupHit, 0, len(found))
	for _, v := range found {
		out = append(out, application.LookupHit{Type: application.NodeService, ID: v.ID, Reference: v.Reference, Name: v.Name, Detail: v.Criticality})
	}
	return out, nil
}

// Search finds Virtual Machines that are not decommissioned for the affected-resource picker.
func (x *Infrastructure) Search(ctx context.Context, text string, limit int) ([]application.LookupHit, error) {
	found, err := x.i.SearchVMs(ctx, text, limit)
	if err != nil {
		return nil, err
	}
	out := make([]application.LookupHit, 0, len(found))
	for _, v := range found {
		out = append(out, application.LookupHit{Type: application.NodeVM, ID: v.ID, Name: v.Name, Detail: v.State})
	}
	return out, nil
}

// Search finds usable Assets for the affected-resource picker (by reference, tag, serial number, product name,
// manufacturer or part number).
func (x *Assets) Search(ctx context.Context, text string, limit int) ([]application.LookupHit, error) {
	found, err := x.a.Search(ctx, text, limit)
	if err != nil {
		return nil, err
	}
	out := make([]application.LookupHit, 0, len(found))
	for _, h := range found {
		if !(application.AssetInfo{Status: h.Asset.Status}).Usable() {
			continue
		}
		name := h.ProductName
		if h.Asset.AssetTag != nil {
			name = strings.TrimSpace(name + " " + *h.Asset.AssetTag)
		}
		out = append(out, application.LookupHit{Type: application.NodeAsset, ID: h.Asset.ID, Reference: h.Asset.Reference, Name: name, Detail: h.Asset.Status})
	}
	return out, nil
}
