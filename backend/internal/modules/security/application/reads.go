package application

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Reads need security.view, security.manage or security.accept_risk; anyone else gets ErrForbidden.
// Findings show their Device by id and name only to callers holding endpoints.view|manage; for anyone
// else the device id is replaced by a placeholder (hidden-N) and DeviceHidden is set.

// AdvisoryDetail is an Advisory with its criteria and the names of their Software Products.
type AdvisoryDetail struct {
	Advisory
	Criteria     []Criterion
	ProductNames map[string]SoftwareProduct
}

// FindingView is a Finding as the caller may see it.
type FindingView struct {
	Finding
	// DeviceHidden reports that DeviceID is a placeholder (the caller lacks endpoints.view).
	DeviceHidden bool
	// DeviceName is set when the caller may see it and the Device is known.
	DeviceName *string
	// ProductName is the Software Product's name when known.
	ProductName *string
	// AdvisoryReference and AdvisoryTitle name the Advisory.
	AdvisoryReference string
	AdvisoryTitle     string
}

// ListAdvisories lists Advisories, newest first.
func (s *Service) ListAdvisories(ctx context.Context, p Principal, f AdvisoryFilter) (Result[Advisory], error) {
	if !p.reads() {
		return Result[Advisory]{}, ErrForbidden
	}
	if f.Status != "" && !slices.Contains(AdvisoryStatuses, f.Status) {
		return Result[Advisory]{}, invalid("status must be one of %s", strings.Join(AdvisoryStatuses, ", "))
	}
	if f.Severity != "" && !slices.Contains(Severities, f.Severity) {
		return Result[Advisory]{}, invalid("severity must be one of %s", strings.Join(Severities, ", "))
	}
	if q, err := cleanLine("q", f.Query, maxQuery, false); err != nil {
		return Result[Advisory]{}, err
	} else if q != nil {
		f.Query = *q
	}
	f.Page = f.Page.Normalize()
	return s.store.ListAdvisories(ctx, f)
}

// GetAdvisory returns an Advisory with its criteria.
func (s *Service) GetAdvisory(ctx context.Context, p Principal, id string) (AdvisoryDetail, error) {
	if !p.reads() {
		return AdvisoryDetail{}, ErrForbidden
	}
	if !uuidPattern.MatchString(id) {
		return AdvisoryDetail{}, ErrNotFound
	}
	a, err := s.store.GetAdvisory(ctx, strings.ToLower(id))
	if err != nil {
		return AdvisoryDetail{}, err
	}
	crit, err := s.store.Criteria(ctx, a.ID)
	if err != nil {
		return AdvisoryDetail{}, err
	}
	names, err := s.inventory.SoftwareProducts(ctx, productIDs(crit))
	if err != nil {
		return AdvisoryDetail{}, fmt.Errorf("load software products: %w", err)
	}
	return AdvisoryDetail{Advisory: a, Criteria: crit, ProductNames: names}, nil
}

// AdvisoryTransitions pages the state history of an Advisory, oldest first.
func (s *Service) AdvisoryTransitions(ctx context.Context, p Principal, id string, page Page) (Result[Transition], error) {
	if !p.reads() {
		return Result[Transition]{}, ErrForbidden
	}
	if !uuidPattern.MatchString(id) {
		return Result[Transition]{}, ErrNotFound
	}
	a, err := s.store.GetAdvisory(ctx, strings.ToLower(id))
	if err != nil {
		return Result[Transition]{}, err
	}
	return s.store.AdvisoryTransitions(ctx, a.ID, page.Normalize())
}

// Summary counts the findings of an Advisory.
func (s *Service) Summary(ctx context.Context, p Principal, id string) (Summary, error) {
	if !p.reads() {
		return Summary{}, ErrForbidden
	}
	if !uuidPattern.MatchString(id) {
		return Summary{}, ErrNotFound
	}
	a, err := s.store.GetAdvisory(ctx, strings.ToLower(id))
	if err != nil {
		return Summary{}, err
	}
	return s.store.Summary(ctx, a.ID)
}

// ListFindings lists findings, newest first; with an AdvisoryID only that Advisory's (ErrNotFound for an
// unknown Advisory).
func (s *Service) ListFindings(ctx context.Context, p Principal, f FindingFilter) (Result[FindingView], error) {
	if !p.reads() {
		return Result[FindingView]{}, ErrForbidden
	}
	if f.Status != "" && !slices.Contains(FindingStatuses, f.Status) {
		return Result[FindingView]{}, invalid("status must be one of %s", strings.Join(FindingStatuses, ", "))
	}
	if f.Confidence != "" && !slices.Contains(Confidences, f.Confidence) {
		return Result[FindingView]{}, invalid("confidence must be one of %s", strings.Join(Confidences, ", "))
	}
	if f.AdvisoryID != "" {
		if !uuidPattern.MatchString(f.AdvisoryID) {
			return Result[FindingView]{}, ErrNotFound
		}
		a, err := s.store.GetAdvisory(ctx, strings.ToLower(f.AdvisoryID))
		if err != nil {
			return Result[FindingView]{}, err
		}
		f.AdvisoryID = a.ID
	}
	f.Page = f.Page.Normalize()
	res, err := s.store.ListFindings(ctx, f)
	if err != nil {
		return Result[FindingView]{}, err
	}
	views, err := s.views(ctx, p, res.Items)
	if err != nil {
		return Result[FindingView]{}, err
	}
	return Result[FindingView]{Items: views, NextCursor: res.NextCursor}, nil
}

// GetFinding returns one finding.
func (s *Service) GetFinding(ctx context.Context, p Principal, id string) (FindingView, error) {
	if !p.reads() {
		return FindingView{}, ErrForbidden
	}
	if !uuidPattern.MatchString(id) {
		return FindingView{}, ErrNotFound
	}
	f, err := s.store.GetFinding(ctx, strings.ToLower(id))
	if err != nil {
		return FindingView{}, err
	}
	views, err := s.views(ctx, p, []Finding{f})
	if err != nil {
		return FindingView{}, err
	}
	return views[0], nil
}

// FindingTransitions pages the state history of a finding, oldest first.
func (s *Service) FindingTransitions(ctx context.Context, p Principal, id string, page Page) (Result[Transition], error) {
	if !p.reads() {
		return Result[Transition]{}, ErrForbidden
	}
	if !uuidPattern.MatchString(id) {
		return Result[Transition]{}, ErrNotFound
	}
	f, err := s.store.GetFinding(ctx, strings.ToLower(id))
	if err != nil {
		return Result[Transition]{}, err
	}
	return s.store.FindingTransitions(ctx, f.ID, page.Normalize())
}

// views adds names and applies the device redaction.
func (s *Service) views(ctx context.Context, p Principal, in []Finding) ([]FindingView, error) {
	var devices, products, advisoryIDs []string
	for _, f := range in {
		devices = append(devices, f.DeviceID)
		products = append(products, f.SoftwareProductID)
		if !slices.Contains(advisoryIDs, f.AdvisoryID) {
			advisoryIDs = append(advisoryIDs, f.AdvisoryID)
		}
	}
	names := map[string]string{}
	if p.EndpointsView && len(devices) > 0 {
		var err error
		if names, err = s.inventory.DeviceNames(ctx, devices); err != nil {
			return nil, fmt.Errorf("load device names: %w", err)
		}
	}
	prods, err := s.inventory.SoftwareProducts(ctx, products)
	if err != nil {
		return nil, fmt.Errorf("load software products: %w", err)
	}
	advs := map[string]Advisory{}
	for _, id := range advisoryIDs {
		a, err := s.store.GetAdvisory(ctx, id)
		if err != nil {
			return nil, err
		}
		advs[id] = a
	}
	out := make([]FindingView, 0, len(in))
	for i, f := range in {
		v := FindingView{Finding: f, AdvisoryReference: advs[f.AdvisoryID].Reference, AdvisoryTitle: advs[f.AdvisoryID].Title}
		if pr, ok := prods[f.SoftwareProductID]; ok {
			n := pr.Name
			v.ProductName = &n
		}
		if p.EndpointsView {
			if n, ok := names[f.DeviceID]; ok {
				v.DeviceName = &n
			}
		} else {
			v.DeviceID, v.DeviceHidden = fmt.Sprintf("hidden-%d", i+1), true
		}
		out = append(out, v)
	}
	return out, nil
}
