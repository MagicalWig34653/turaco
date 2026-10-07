package application

import (
	"context"
	"time"
)

// Security contexts are cached in memory for SecurityContextTTL per Deployment (and detail level), at most
// securityCacheMax entries, to cap the cost of the cross-module read; authorization is checked before the cache.
const (
	SecurityContextTTL = 60 * time.Second
	securityCacheMax   = 200
)

type secCacheEntry struct {
	out     DeploymentSecurityContext
	expires time.Time
}

// Security context of a Deployment (F9 G4 / F8). The Deployment's Software Product and its target Devices go to the
// Security contract, which answers with the advisories that have findings on those Devices for the product and the
// number of open findings. Endpoints does not read Security tables: the port is implemented in wiring over
// security/public.

// SecurityContextAdvisory is an advisory with findings on the target Devices.
type SecurityContextAdvisory struct {
	ID              string
	Reference       string
	Title           string
	Severity        string
	Status          string
	KnownExploited  bool
	OpenFindings    int
	AffectedDevices int
}

// SecurityContext is the answer of the Security contract. Advisories is filled only when details were requested.
type SecurityContext struct {
	Advisories    []SecurityContextAdvisory
	AdvisoryCount int
	OpenFindings  int
	Truncated     bool
}

// DeploymentSecurity is the Security read port. details selects advisory references and titles (the caller holds
// security.view); without it only the counts are returned.
type DeploymentSecurity interface {
	DeploymentContext(ctx context.Context, productID string, deviceIDs []string, details bool) (SecurityContext, error)
}

// DeploymentSecurityContext is what the endpoint returns.
type DeploymentSecurityContext struct {
	Deployment   Deployment
	Context      SecurityContext
	Detailed     bool
	TargetsSeen  int
	TargetsTrunc bool
}

// WithSecurity connects the Security contract (without it the context is empty).
func (s *Service) WithSecurity(sec DeploymentSecurity) *Service {
	if sec != nil {
		s.security = sec
	}
	return s
}

// DeploymentSecurityContext returns the advisories and open findings on the target Devices for the product of the
// Deployment. It needs a deployments read permission; advisory references and titles need security.view as well,
// otherwise only counts are returned.
func (s *Service) DeploymentSecurityContext(ctx context.Context, p Principal, id string) (DeploymentSecurityContext, error) {
	d, err := s.reportDeployment(ctx, p, id)
	if err != nil {
		return DeploymentSecurityContext{}, err
	}
	key := d.ID
	if p.SecurityView {
		key += ":detailed"
	}
	s.secMu.Lock()
	if e, ok := s.secCache[key]; ok && s.now().Before(e.expires) {
		s.secMu.Unlock()
		return e.out, nil
	}
	s.secMu.Unlock()
	out, err := s.buildSecurityContext(ctx, p, d)
	if err != nil {
		return DeploymentSecurityContext{}, err
	}
	s.secMu.Lock()
	if s.secCache == nil {
		s.secCache = map[string]secCacheEntry{}
	}
	if len(s.secCache) >= securityCacheMax {
		now := s.now()
		for k, e := range s.secCache {
			if !now.Before(e.expires) {
				delete(s.secCache, k)
			}
		}
		for k := range s.secCache {
			if len(s.secCache) < securityCacheMax {
				break
			}
			delete(s.secCache, k)
		}
	}
	s.secCache[key] = secCacheEntry{out: out, expires: s.now().Add(SecurityContextTTL)}
	s.secMu.Unlock()
	return out, nil
}

func (s *Service) buildSecurityContext(ctx context.Context, p Principal, d Deployment) (DeploymentSecurityContext, error) {
	devices, truncated, err := s.store.DeploymentDeviceIDs(ctx, d.ID, MaxSecurityContextDevices)
	if err != nil {
		return DeploymentSecurityContext{}, err
	}
	out := DeploymentSecurityContext{Deployment: d, Detailed: p.SecurityView, TargetsSeen: len(devices), TargetsTrunc: truncated}
	if len(devices) == 0 || s.security == nil {
		return out, nil
	}
	out.Context, err = s.security.DeploymentContext(ctx, d.ProductID, devices, p.SecurityView)
	if err != nil {
		return DeploymentSecurityContext{}, err
	}
	if !p.SecurityView {
		// Counts only, whatever the port returned.
		out.Context.Advisories, out.Context.Truncated = nil, false
	}
	if out.Context.Truncated {
		out.TargetsTrunc = true
	}
	return out, nil
}
