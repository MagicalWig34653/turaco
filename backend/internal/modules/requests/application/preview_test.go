package application

import (
	"context"
	"testing"

	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

type previewCatalog struct{ sub catalogpublic.Submission }

func (c previewCatalog) ForSubmission(context.Context, string) (catalogpublic.Submission, error) {
	return c.sub, nil
}

type previewDir struct{ managers map[string]string }

func (previewDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	m := map[string]bool{}
	for _, i := range ids {
		m[i] = true
	}
	return m, nil
}
func (previewDir) ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error) {
	return previewDir{}.ActiveUsers(ctx, ids)
}
func (previewDir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	m := map[string]string{}
	for _, i := range ids {
		m[i] = "User " + i
	}
	return m, nil
}
func (previewDir) TeamNames(_ context.Context, ids []string) (map[string]string, error) {
	m := map[string]string{}
	for _, i := range ids {
		m[i] = "Team " + i
	}
	return m, nil
}
func (d previewDir) ManagerIDs(_ context.Context, ids []string) (map[string]string, error) {
	m := map[string]string{}
	for _, i := range ids {
		if v, ok := d.managers[i]; ok {
			m[i] = v
		}
	}
	return m, nil
}

const (
	pvReq   = "00000000-0000-7000-8000-0000000000a1"
	pvMgr   = "00000000-0000-7000-8000-0000000000a2"
	pvTeam  = "00000000-0000-7000-8000-0000000000e1"
	pvOther = "00000000-0000-7000-8000-0000000000a3"
)

func previewService(def string, managers map[string]string) *Service {
	d, err := catalogpublic.ParseSnapshot([]byte(def))
	if err != nil {
		panic(err)
	}
	return NewService(nil, previewCatalog{catalogpublic.Submission{ID: "i1", Active: true, Definition: d}}, nil, nil, previewDir{managers}, nil, nil, nil)
}

func TestApprovalPreviewManagerFallbackAndSeparationOfDuties(t *testing.T) {
	c := Caller{Actor: audit.UserActor(pvReq), CorrelationID: "t"}
	ctx := context.Background()
	noFallback := `{"fields":[],"approvals":[{"approver":"manager"}],"fulfillment":[]}`
	withFallback := `{"fields":[],"allowRequestedFor":true,"approvals":[{"approver":"manager","fallbackTeamId":"` + pvTeam + `"}],"fulfillment":[]}`

	// No manager and no fallback: the preview says the request cannot be submitted.
	steps, err := previewService(noFallback, nil).ApprovalPreview(ctx, c, "i1", nil)
	if err != nil || len(steps) != 1 || steps[0].Resolved || steps[0].Kind != "manager" {
		t.Fatalf("no manager = %+v %v", steps, err)
	}
	// A configured fallback Team takes over, flagged as fallback.
	steps, err = previewService(withFallback, nil).ApprovalPreview(ctx, c, "i1", nil)
	if err != nil || !steps[0].Resolved || !steps[0].Fallback || steps[0].ApproverName != "Team "+pvTeam {
		t.Fatalf("fallback = %+v %v", steps, err)
	}
	// A manager exists: the manager approves, named for the requester's own request.
	steps, _ = previewService(withFallback, map[string]string{pvReq: pvMgr}).ApprovalPreview(ctx, c, "i1", nil)
	if !steps[0].Resolved || steps[0].Fallback || steps[0].ApproverName != "User "+pvMgr {
		t.Errorf("manager = %+v", steps)
	}
	// The requester is their own manager's manager: nobody approves their own request, the fallback applies.
	steps, _ = previewService(withFallback, map[string]string{pvReq: pvReq}).ApprovalPreview(ctx, c, "i1", nil)
	if !steps[0].Fallback {
		t.Errorf("self-manager must use the fallback: %+v", steps)
	}
	// For another person the manager's name is not disclosed.
	other := pvOther
	steps, _ = previewService(withFallback, map[string]string{pvOther: pvMgr}).ApprovalPreview(ctx, c, "i1", &other)
	if !steps[0].Resolved || steps[0].ApproverName != "" {
		t.Errorf("other person's manager name disclosed: %+v", steps)
	}
}
