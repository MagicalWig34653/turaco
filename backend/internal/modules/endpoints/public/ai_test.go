package public_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type noAssets struct{}

func (noAssets) FindBySerial(context.Context, string) (application.AssetInfo, error) {
	return application.AssetInfo{}, application.ErrAssetNotFound
}
func (noAssets) ByID(context.Context, string) (application.AssetInfo, bool, error) {
	return application.AssetInfo{}, false, nil
}

func TestDeviceContextToolCarriesOnlyDeclaredProviderObservedFields(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	suffix := hex.EncodeToString(b)
	provider, corr := "ai"+suffix, "ai-devices-"+suffix
	var user string
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour)
	svc := application.NewService(repository.New(pool), noAssets{}, nil, true, func() time.Time { now = now.Add(time.Second); return now })
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.devices WHERE provider = $1`, provider)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.provider_sync_state WHERE provider = $1`, provider)
	})
	manage := application.Principal{UserID: user, Manage: true, AssetsView: true}
	_, err := svc.Ingest(ctx, application.Caller{Actor: audit.UserActor(user), CorrelationID: corr}, manage, application.Snapshot{Provider: provider, Source: application.SourceSync, Complete: true,
		Devices: []application.SnapshotDevice{{Record: intune.DeviceRecord{ExternalID: "ext-SECRET-77", Name: "LAPTOP-7", SerialNumber: "SN-VERY-SECRET-9", OSPlatform: "windows", OSVersion: "11",
			Manufacturer: "Acme", Model: "X1", Ownership: "corporate", ComplianceState: "compliant"}}}})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM endpoints.devices WHERE provider=$1`, provider).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tools := public.AITools(svc)
	tool := tools[0]
	if err := ai.NewRegistry().RegisterAll(tools...); err != nil || tool.Name != "devices.context_summary" {
		t.Fatalf("self-check: %v", err)
	}
	caller := func(perms ...string) ai.Caller {
		m := map[string]struct{}{}
		for _, p := range perms {
			m[p] = struct{}{}
		}
		return ai.Caller{UserID: user, TenantID: "t", Permissions: m}
	}
	input := json.RawMessage(`{"deviceId":"` + id + `"}`)
	out, err := tool.Handler(ctx, caller("endpoints.view"), input)
	if err != nil {
		t.Fatal(err)
	}
	data, classes, err := ai.CheckOutput(tool, out)
	if err != nil {
		t.Fatalf("DTO violates its allowlist: %v", err)
	}
	got := string(data)
	if len(classes) != 1 || classes[0] != ai.ClassDeviceContext || !strings.Contains(got, "LAPTOP-7") || !strings.Contains(got, `"dataOrigin":"provider_observed"`) || !strings.Contains(got, `"observedAt"`) {
		t.Fatalf("summary: %v %s", classes, got)
	}
	for _, leak := range []string{"SN-VERY-SECRET-9", "ext-SECRET-77", provider, id} {
		if strings.Contains(got, leak) {
			t.Errorf("summary leaks %q: %s", leak, got)
		}
	}
	// The module's own authorization applies as the requesting user: no endpoints permission, no data.
	if _, err := tool.Handler(ctx, caller(), input); !errors.Is(err, ai.ErrToolForbidden) {
		t.Errorf("without endpoints.view: %v", err)
	}
	if _, err := tool.Handler(ctx, caller("endpoints.view"), json.RawMessage(`{"deviceId":"`+user+`"}`)); !errors.Is(err, ai.ErrToolNotFound) {
		t.Errorf("unknown device: %v", err)
	}
}
