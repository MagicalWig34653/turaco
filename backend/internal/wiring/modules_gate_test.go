package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	briefingapp "github.com/MagicalWig34653/turaco/backend/internal/modules/briefing/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
)

type (
	securityStub  struct{ briefingapp.SecurityFeed }
	planningStub  struct{ briefingapp.PlanningFeed }
	deskStub      struct{ briefingapp.DeskFeed }
	endpointStub  struct{ briefingapp.EndpointFeed }
	deployStub    struct{ briefingapp.DeploymentFeed }
	directoryStub struct{ briefingapp.DirectoryFeed }
)

func TestDisabledModulesAreRemovedFromBriefingAndAITools(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM platform.module_switches`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM platform.module_switches`) })
	mods := modules.NewService(pool, modules.Options{CacheTTL: time.Millisecond})
	admin := modules.Caller{UserID: "9b0a8c1e-0000-4000-8000-000000000001", CorrelationID: "r", CanManage: true}
	zero := 0
	// planning must go before changes is touched; security before endpoints; only the leaves are needed here.
	for _, key := range []string{"planning", "security", "remoteaccess", "endpoints", "knowledge", "servicedesk"} {
		if _, err := mods.Disable(ctx, admin, key, "not_needed", &zero); err != nil {
			t.Fatalf("disable %s: %v", key, err)
		}
	}

	all := briefingapp.FeedSources{Security: securityStub{}, Planning: planningStub{}, Desk: deskStub{}, Endpoints: endpointStub{},
		Deployments: deployStub{}, Directory: directoryStub{}}
	got := briefingSourceFilter(mods)(ctx, all)
	if got.Security != nil || got.Planning != nil || got.Desk != nil || got.Endpoints != nil || got.Deployments != nil {
		t.Errorf("sources of disabled modules must be removed: %+v", got)
	}
	if got.Directory == nil {
		t.Error("the directory source belongs to a core module and must stay")
	}

	called := 0
	tool := ai.Tool{Name: "t", Handler: func(context.Context, ai.Caller, json.RawMessage) (any, error) { called++; return "ok", nil }}
	gated := gateTools(mods, "knowledge", []ai.Tool{tool})
	if _, err := gated[0].Handler(ctx, ai.Caller{}, nil); !errors.Is(err, ai.ErrToolNotFound) || called != 0 {
		t.Errorf("tool of a disabled module: err=%v called=%d; want not found and no call", err, called)
	}
	if _, err := gateTools(mods, "assets", []ai.Tool{tool})[0].Handler(ctx, ai.Caller{}, nil); err != nil || called != 1 {
		t.Errorf("tool of an enabled module: err=%v called=%d", err, called)
	}
	if _, err := gateTools(nil, "knowledge", []ai.Tool{tool})[0].Handler(ctx, ai.Caller{}, nil); err != nil || called != 2 {
		t.Errorf("without a registry tools are untouched: err=%v called=%d", err, called)
	}
}
