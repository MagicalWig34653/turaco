package query_test

import (
	"context"
	"testing"

	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

func TestProductionCatalogsMatchMigratedSchema(t *testing.T) {
	pool := dbtest.Pool(t)
	if err := query.ValidateSchema(context.Background(), pool,
		servicedeskapp.TicketCatalog(), endpointsapp.DeviceCatalog(), tasksapp.TaskCatalog()); err != nil {
		t.Fatal(err)
	}
	bad := query.MustCatalog(query.Resource{Key: "bad", Module: "bad", Schema: "platform", Table: "tasks", Alias: "t", IDColumn: "id",
		DefaultSort: []query.SortSpec{{Field: "missing", Dir: "asc"}}, Fields: []query.Field{{Key: "missing", Type: query.TypeText,
			Column: query.Col("t", "nonexistent_column"), Sortable: true, SortIndexed: true}}})
	if err := query.ValidateSchema(context.Background(), pool, bad); err == nil {
		t.Fatal("a catalog naming a missing column passed startup validation")
	}
}
