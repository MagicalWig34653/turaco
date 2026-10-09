package repository_test

import (
	"context"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

func TestDeviceQueryCatalogAndScope(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("query-a", "Query Alpha", "q-a"), dev("query-b", "Query Beta", "q-b"))
	info, err := e.svc.QueryFields(e.view)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range info.Fields {
		if f.Key == "management_state" {
			t.Fatal("management state disclosed in the regular device catalog")
		}
	}
	filter := &query.Filter{V: 1, Root: &query.Node{Type: "condition", Field: "name", Op: string(query.OpStartsWith), Value: []byte(`"Query A"`)}}
	page, err := e.svc.QueryDevices(context.Background(), e.view, query.Request{Filter: filter, Count: true}, application.DeviceFilter{})
	if err != nil || len(page.Items) != 1 || page.Count == nil || *page.Count != 1 {
		t.Fatalf("filtered devices: %+v, %v", page, err)
	}
	filter.Root.Field = "management_state"
	filter.Root.Op = string(query.OpHasAny)
	filter.Root.Value = []byte(`["failed"]`)
	_, err = e.svc.QueryDevices(context.Background(), e.view, query.Request{Filter: filter}, application.DeviceFilter{})
	if qe, ok := query.AsError(err); !ok || qe.Code != query.CodeInvalidFilter {
		t.Fatalf("management probe: %v", err)
	}
	_, err = e.svc.QueryDevices(context.Background(), application.Principal{UserID: e.user}, query.Request{}, application.DeviceFilter{})
	if err != application.ErrForbidden {
		t.Fatalf("unprivileged query: %v", err)
	}
}
