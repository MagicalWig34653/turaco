package repository

import (
	"context"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

type queryDirectory struct{}

func (queryDirectory) ActiveUsers(context.Context, []string) (map[string]bool, error) {
	return nil, nil
}
func (queryDirectory) UserNames(context.Context, []string) (map[string]string, error) {
	return nil, nil
}
func (queryDirectory) ActiveTeams(context.Context, []string) (map[string]bool, error) {
	return nil, nil
}
func (queryDirectory) TeamNames(context.Context, []string) (map[string]string, error) {
	return nil, nil
}
func (queryDirectory) CurrentTeamIDs(context.Context, string) ([]string, error)   { return nil, nil }
func (queryDirectory) CurrentMemberIDs(context.Context, string) ([]string, error) { return nil, nil }

func TestTaskQueryPreservesAssignmentScope(t *testing.T) {
	f := newFixture(t)
	mine := f.insert("my query task", application.PriorityNormal, nil)
	other := f.insert("another query task", application.PriorityHigh, nil)
	const callerID = "11111111-1111-4111-8111-111111111111"
	if _, err := f.pool.Exec(context.Background(), `UPDATE platform.tasks SET assigned_user_id = $1::uuid WHERE id = $2::uuid`, callerID, mine.ID); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(f.repo, queryDirectory{}, nil)
	filter := &query.Filter{V: 1, Root: &query.Node{Type: "condition", Field: "title", Op: string(query.OpContains), Value: []byte(`"query task"`)}}
	p := application.Principal{UserID: callerID, Work: true}
	page, err := svc.Query(context.Background(), p, query.Request{Filter: filter, Count: true}, false, nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != mine.ID || page.Count == nil || *page.Count != 1 {
		t.Fatalf("work scope widened to task %s: %+v, %v", other.ID, page, err)
	}
	filter.Root.Value = []byte(`"another"`)
	page, err = svc.Query(context.Background(), p, query.Request{Filter: filter}, false, nil)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("invisible task matched: %+v, %v", page, err)
	}
	if _, err := svc.QueryFields(context.Background(), application.Principal{UserID: callerID}); err != application.ErrForbidden {
		t.Fatalf("catalog available without task access: %v", err)
	}
}
