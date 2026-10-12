package transport_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The report endpoint validates its input before anything else, refuses a per-person grouping, and is closed for
// requesters and while the sla switch is off (the test service has none). The overview then carries no SLA figures.
func TestSLAReportAndOverviewOverHTTPWhileTheSwitchIsOff(t *testing.T) {
	staff := serve(t, as(agent, "tickets.manage", "tickets.view"))
	employee := serve(t, as(alice))
	base := "/api/v1/sla/report?from=2026-01-01&to=2026-01-31"
	for name, c := range map[string]struct {
		h    http.Handler
		path string
		want int
	}{
		"missing dates":        {staff, "/api/v1/sla/report?groupBy=queue", http.StatusBadRequest},
		"bad date":             {staff, "/api/v1/sla/report?from=yesterday&to=2026-01-31&groupBy=queue", http.StatusBadRequest},
		"per assignee":         {staff, base + "&groupBy=assignee", http.StatusBadRequest},
		"per user":             {staff, base + "&groupBy=user", http.StatusBadRequest},
		"more than a year":     {staff, "/api/v1/sla/report?from=2025-01-01&to=2026-01-31&groupBy=queue", http.StatusBadRequest},
		"reversed":             {staff, "/api/v1/sla/report?from=2026-02-01&to=2026-01-31&groupBy=queue", http.StatusBadRequest},
		"switch off":           {staff, base + "&groupBy=queue", http.StatusForbidden},
		"requester":            {employee, base + "&groupBy=queue", http.StatusForbidden},
		"anonymous":            {serve(t, fakeAuth{}), base + "&groupBy=queue", http.StatusUnauthorized},
		"foreign queue filter": {staff, base + "&groupBy=queue&queueId=not-a-uuid", http.StatusBadRequest},
	} {
		if rec := do(c.h, "GET", c.path, ""); rec.Code != c.want {
			t.Errorf("%s = %d %s, want %d", name, rec.Code, rec.Body, c.want)
		}
	}
	rec := do(staff, "GET", "/api/v1/tickets/overview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("overview = %d %s", rec.Code, rec.Body)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var totals map[string]int
	_ = json.Unmarshal(body["totals"], &totals)
	if _, has := totals["atRisk"]; has || string(body["slaEnabled"]) != "false" || body["nextToBreach"] != nil {
		t.Errorf("overview with the switch off carries SLA figures: %s", rec.Body)
	}
}
