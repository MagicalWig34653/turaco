package wiring

import (
	"encoding/json"
	"testing"

	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query/querytest"
)

// The SLA due times are sortable across Queues only because indexes serve the keyset order (F17 S-E).
func TestSLADueSortsAreServedByIndexes(t *testing.T) {
	pool := dbtest.Pool(t)
	engine := QueryEngine(pool)
	staff := query.Subject{UserID: "00000000-0000-7000-8000-0000000009a2", Permissions: map[string]bool{
		"tickets.view": true, "servicedesk.sla": true}}
	for _, field := range []string{"sla_response_due_at", "sla_resolution_due_at"} {
		for _, dir := range []string{"asc", "desc"} {
			req := query.Request{Limit: 50, Filter: &query.Filter{V: 1, Sort: []query.SortSpec{{Field: field, Dir: dir}}}}
			plan, err := engine.Prepare(servicedeskapp.TicketCatalog(), staff, req, "all", query.Options{})
			if err != nil {
				t.Fatalf("%s %s: %v", field, dir, err)
			}
			sql, args := plan.Statement(query.Select{Columns: "t.id"})
			querytest.ExplainUsesIndex(t, pool, sql, args)
		}
	}
}

// The derived slaState exists for staff only while the switch is on, and never as a sortable column.
func TestSLAStateFieldIsGatedAndNotSortable(t *testing.T) {
	cat := servicedeskapp.TicketCatalog()
	has := func(s query.Subject, key string) (found, sortable bool) {
		for _, f := range cat.Describe(s).Fields {
			if f.Key == key {
				return true, f.Sortable
			}
		}
		return false, false
	}
	on := query.Subject{UserID: "u", Permissions: map[string]bool{"tickets.view": true, "servicedesk.sla": true}}
	off := query.Subject{UserID: "u", Permissions: map[string]bool{"tickets.view": true}}
	employee := query.Subject{UserID: "u", Permissions: map[string]bool{"servicedesk.sla": true}}
	if found, sortable := has(on, "sla_state"); !found || sortable {
		t.Errorf("staff with the switch on: found=%v sortable=%v", found, sortable)
	}
	for name, s := range map[string]query.Subject{"switch off": off, "employee": employee} {
		for _, key := range []string{"sla_state", "sla_breached", "sla_response_due_at", "sla_resolution_due_at", "sla_response_outcome", "sla_resolution_outcome"} {
			if found, _ := has(s, key); found {
				t.Errorf("%s: field %s is visible", name, key)
			}
		}
	}
	// A request that uses a hidden field is refused instead of ignored.
	req := query.Request{Filter: &query.Filter{V: 1, Root: &query.Node{Type: "condition", Field: "sla_state", Op: string(query.OpEquals), Value: json.RawMessage(`"at_risk"`)}}}
	if _, err := query.NewEphemeralEngine().Prepare(cat, off, req, "all", query.Options{}); err == nil {
		t.Error("a filter on sla_state was accepted while the switch is off")
	}
}
