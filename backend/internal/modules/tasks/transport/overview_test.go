package transport

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// overviewStore answers the manager overview with fixed data.
type overviewStore struct {
	*stubStore
	asked int
}

func (s *overviewStore) TaskOverview(context.Context, application.OverviewQuery) (application.OverviewData, error) {
	s.asked++
	return application.OverviewData{Totals: application.OverviewTotals{Open: 3, Overdue: 1, Unowned: 2}}, nil
}

func TestOverviewNeedsASeeAllPermission(t *testing.T) {
	cases := []struct {
		name  string
		auth  authorization.Authenticator
		code  int
		reads int
	}{
		{"anonymous", authorization.DenyAll{}, 401, 0},
		{"unrelated permission", with("tickets.view"), 403, 0},
		{"worker sees only own tasks", with("tasks.work"), 403, 0},
		{"viewer", with("tasks.view"), 200, 1},
		{"manager", with("tasks.manage"), 200, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &overviewStore{stubStore: newStore()}
			mux := http.NewServeMux()
			Register(mux, application.NewService(store, stubDir{}, nil), c.auth, slog.New(slog.NewTextHandler(io.Discard, nil)))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/tasks/overview", nil))
			if rec.Code != c.code || store.asked != c.reads {
				t.Fatalf("status = %d (want %d), store reads = %d (want %d): %s", rec.Code, c.code, store.asked, c.reads, rec.Body)
			}
			if c.code == 200 && !strings.Contains(rec.Body.String(), `"unowned":2`) {
				t.Errorf("body = %s", rec.Body)
			}
		})
	}
}
