package wiring

import (
	"context"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/providerstatus"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/health"
)

type fixedReporter providerstatus.Snapshot

func (f fixedReporter) Status() providerstatus.Snapshot { return providerstatus.Snapshot(f) }

func TestProviderCheckNeverClaimsMoreThanObserved(t *testing.T) {
	ok, bad := time.Now().Add(-time.Hour), time.Now()
	run := func(syncOn bool, p ProviderHealth) health.Result {
		return providerCheck("intune", syncOn, "INTUNE_SYNC", "docs/integrations/intune.md", p, nil).Run(context.Background())
	}
	if r := run(false, ProviderHealth{Built: true}); r.Status != health.StatusDisabled {
		t.Errorf("disabled: %+v", r)
	}
	if r := run(true, ProviderHealth{}); r.Status != health.StatusNotConfigured || r.ErrorCode != "client_not_built" {
		t.Errorf("not built: %+v", r)
	}
	if r := run(true, ProviderHealth{Built: true, MissingKeys: []string{"MICROSOFT_GRAPH_TENANT_ID"}}); r.Status != health.StatusNotConfigured || r.ErrorCode != "client_not_configured" || len(r.NextStep.ConfigKeys) != 1 {
		t.Errorf("not configured: %+v", r)
	}
	if r := run(true, ProviderHealth{Built: true, Configured: true, Reporter: fixedReporter{State: providerstatus.Unverified}}); r.Status != health.StatusUnknown || r.Mode != health.ModeReal || r.ErrorCode != "unverified" {
		t.Errorf("unverified: %+v", r)
	}
	if r := run(true, ProviderHealth{Built: true, Configured: true, Reporter: fixedReporter{State: providerstatus.Verified, LastSuccessAt: &ok}}); r.Status != health.StatusOK || r.LastSuccessAt == nil {
		t.Errorf("verified: %+v", r)
	}
	if r := run(true, ProviderHealth{Built: true, Configured: true, Reporter: fixedReporter{State: providerstatus.Failing, LastSuccessAt: &ok, LastFailureAt: &bad, LastErrorCode: "http_403"}}); r.Status != health.StatusFailing || r.ErrorCode != "http_403" {
		t.Errorf("failing: %+v", r)
	}
	expired := time.Now().Add(-time.Hour)
	if r := run(true, ProviderHealth{Built: true, Configured: true, CredentialExpires: &expired, Reporter: fixedReporter{State: providerstatus.Verified, LastSuccessAt: &ok}}); r.Status != health.StatusFailing || r.ErrorCode != "credential_expired" {
		t.Errorf("expired: %+v", r)
	}
	// A success stored by another process (the worker's Autotask pushes) counts as observed.
	stored := func(context.Context) (*time.Time, *time.Time, error) { return &ok, nil, nil }
	r := providerCheck("autotask", true, "AUTOTASK_SYNC", "d", ProviderHealth{Built: true, Configured: true, Reporter: fixedReporter{State: providerstatus.Unverified}}, stored).Run(context.Background())
	if r.Status != health.StatusOK {
		t.Errorf("stored success: %+v", r)
	}
}
