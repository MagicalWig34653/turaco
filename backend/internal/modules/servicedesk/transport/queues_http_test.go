package transport_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func letters(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	out := make([]byte, n)
	for i := range b {
		out[i] = 'A' + b[i]%26
	}
	return string(out)
}

type queueBody struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Prefix    string `json:"prefix"`
	Version   int    `json:"version"`
	CanCreate bool   `json:"canCreate"`
	Level     string `json:"level"`
}

func errCode(body string) string {
	var e struct {
		Error struct{ Code string }
	}
	_ = json.Unmarshal([]byte(body), &e)
	return e.Error.Code
}

func createQueue(t *testing.T, admin http.Handler, visibility string) queueBody {
	t.Helper()
	pfx := "H" + letters(5)
	rec := do(admin, "POST", "/api/v1/service-desk/queues", `{"key":"`+strings.ToLower(pfx)+`","prefix":"`+pfx+`","name":"Desk `+letters(4)+`","visibility":"`+visibility+`","routingMode":"both"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create queue = %d %s", rec.Code, rec.Body)
	}
	var q queueBody
	_ = json.Unmarshal(rec.Body.Bytes(), &q)
	pool := dbtest.Pool(t)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE queue_id = $1::uuid OR id IN (SELECT ticket_id FROM servicedesk.reference_registry WHERE queue_id = $1::uuid)`, q.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_id = $1`, q.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.queues WHERE id = $1::uuid`, q.ID)
	})
	return q
}

func TestQueuesOverHTTP(t *testing.T) {
	admin := serve(t, as(agent, "servicedesk.queues.manage", "tickets.manage", "tickets.view"))
	staff := serve(t, as(agent, "tickets.manage", "tickets.view"))
	employee := serve(t, as(alice))
	stranger := serve(t, as(bob))

	pub := createQueue(t, admin, "public")
	hidden := createQueue(t, admin, "internal")

	// Administration needs servicedesk.queues.manage; ticket managers are not enough.
	if rec := do(staff, "POST", "/api/v1/service-desk/queues", `{"key":"nope","prefix":"NOPE","name":"x"}`); rec.Code != http.StatusForbidden {
		t.Errorf("create queue by ticket manager = %d", rec.Code)
	}
	if rec := do(employee, "PATCH", "/api/v1/service-desk/queues/"+pub.ID, `{"expectedVersion":1,"name":"x"}`); rec.Code != http.StatusForbidden {
		t.Errorf("update by employee = %d", rec.Code)
	}
	if rec := do(admin, "PATCH", "/api/v1/service-desk/queues/"+pub.ID, `{"name":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("update without version = %d %s", rec.Code, rec.Body)
	}
	if rec := do(admin, "PATCH", "/api/v1/service-desk/queues/"+pub.ID, `{"expectedVersion":9,"name":"x"}`); rec.Code != http.StatusConflict {
		t.Errorf("stale update = %d", rec.Code)
	}
	rec := do(admin, "POST", "/api/v1/service-desk/queues", `{"key":"dup-`+strings.ToLower(letters(3))+`","prefix":"`+pub.Prefix+`","name":"Duplicate"}`)
	if rec.Code != http.StatusConflict || errCode(rec.Body.String()) != "servicedesk.queue_prefix_taken" {
		t.Errorf("duplicate prefix = %d %s", rec.Code, rec.Body)
	}
	// The counter never leaves the service.
	if rec := do(admin, "GET", "/api/v1/service-desk/queues/"+pub.ID, ""); rec.Code != http.StatusOK || strings.Contains(strings.ToLower(rec.Body.String()), "next") || strings.Contains(rec.Body.String(), "number") {
		t.Errorf("queue detail = %d %s", rec.Code, rec.Body)
	}

	// Visibility of the Queues themselves: an internal Queue is the same 404 as one that does not exist.
	recHidden := do(employee, "GET", "/api/v1/service-desk/queues/"+hidden.ID, "")
	recUnknown := do(employee, "GET", "/api/v1/service-desk/queues/00000000-0000-7000-8000-000000000000", "")
	if recHidden.Code != http.StatusNotFound || recUnknown.Code != http.StatusNotFound || errCode(recHidden.Body.String()) != errCode(recUnknown.Body.String()) {
		t.Errorf("queue oracle: %d %s / %d %s", recHidden.Code, recHidden.Body, recUnknown.Code, recUnknown.Body)
	}
	list := do(employee, "GET", "/api/v1/service-desk/queues?for=create", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), pub.ID) || strings.Contains(list.Body.String(), hidden.ID) || strings.Contains(list.Body.String(), `"routingMode"`) {
		t.Errorf("employee intake list = %d %s", list.Code, list.Body)
	}
	if rec := do(employee, "GET", "/api/v1/service-desk/queues?for=weird", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad for = %d", rec.Code)
	}

	// Raising a ticket: the chosen Queue needs the create grant; an unknown and a forbidden Queue answer alike.
	rec = do(employee, "POST", "/api/v1/tickets", `{"title":"Mouse broken","queueId":"`+pub.ID+`"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"prefix":"`+pub.Prefix+`"`) || !strings.Contains(rec.Body.String(), `"reference":"`+pub.Prefix+`-0001"`) {
		t.Fatalf("create in public queue = %d %s", rec.Code, rec.Body)
	}
	var tk struct {
		ID      string
		Version int
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tk)
	forbidden := do(employee, "POST", "/api/v1/tickets", `{"title":"x","queueId":"`+hidden.ID+`"}`)
	unknown := do(employee, "POST", "/api/v1/tickets", `{"title":"x","queueId":"00000000-0000-7000-8000-000000000000"}`)
	if forbidden.Code != http.StatusForbidden || unknown.Code != http.StatusForbidden || errCode(unknown.Body.String()) != errCode(forbidden.Body.String()) || errCode(forbidden.Body.String()) != "servicedesk.queue_not_permitted" {
		t.Errorf("create oracle: %d %s / %d %s", forbidden.Code, forbidden.Body, unknown.Code, unknown.Body)
	}
	if rec := do(employee, "POST", "/api/v1/tickets", `{"title":"x","queueKey":"`+hidden.Key+`"}`); rec.Code != http.StatusForbidden {
		t.Errorf("create by key into a hidden queue = %d", rec.Code)
	}

	// Move: version required, staff only; the requester then sees the neutral label, not the internal desk.
	body := `{"expectedVersion":` + itoa(tk.Version) + `,"targetQueueId":"` + hidden.ID + `","reasonCode":"misrouted"}`
	if rec := do(employee, "POST", "/api/v1/tickets/"+tk.ID+"/move-queue", body); rec.Code != http.StatusForbidden {
		t.Errorf("move by the requester = %d %s", rec.Code, rec.Body)
	}
	if rec := do(stranger, "POST", "/api/v1/tickets/"+tk.ID+"/move-queue", body); rec.Code != http.StatusNotFound {
		t.Errorf("move by a stranger = %d", rec.Code)
	}
	if rec := do(staff, "POST", "/api/v1/tickets/"+tk.ID+"/move-queue", `{"targetQueueId":"`+hidden.ID+`","reasonCode":"misrouted"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("move without version = %d", rec.Code)
	}
	rec = do(staff, "POST", "/api/v1/tickets/"+tk.ID+"/move-queue", body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reference":"`+hidden.Prefix+`-0001"`) {
		t.Fatalf("move = %d %s", rec.Code, rec.Body)
	}
	detail := do(employee, "GET", "/api/v1/tickets/"+tk.ID, "")
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), hidden.Prefix) || strings.Contains(detail.Body.String(), hidden.ID) || !strings.Contains(detail.Body.String(), `"queueLabel"`) ||
		!strings.Contains(detail.Body.String(), `"reference":"`+pub.Prefix+`-0001"`) {
		t.Errorf("requester detail = %d %s", detail.Code, detail.Body)
	}
	// Lookup by an earlier number: staff resolve it, the requester resolves their own, everybody else gets one 404.
	if rec := do(staff, "GET", "/api/v1/tickets/by-reference?reference="+strings.ToLower(pub.Prefix)+"-0001", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"alias":true`) || !strings.Contains(rec.Body.String(), hidden.Prefix+"-0001") {
		t.Errorf("staff alias lookup = %d %s", rec.Code, rec.Body)
	}
	if rec := do(employee, "GET", "/api/v1/tickets/by-reference?reference="+pub.Prefix+"-0001", ""); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), hidden.Prefix) {
		t.Errorf("requester alias lookup = %d %s", rec.Code, rec.Body)
	}
	a := do(stranger, "GET", "/api/v1/tickets/by-reference?reference="+pub.Prefix+"-0001", "")
	b := do(stranger, "GET", "/api/v1/tickets/by-reference?reference=ZZZZ-4242", "")
	c := do(stranger, "GET", "/api/v1/tickets/by-reference?reference=%27%20OR%201%3D1", "")
	if a.Code != http.StatusNotFound || b.Code != http.StatusNotFound || c.Code != http.StatusNotFound || errCode(a.Body.String()) != errCode(b.Body.String()) || errCode(b.Body.String()) != errCode(c.Body.String()) {
		t.Errorf("reference oracle: %d %s / %d %s / %d %s", a.Code, a.Body, b.Code, b.Body, c.Code, c.Body)
	}
	if rec := do(serve(t, fakeAuth{}), "GET", "/api/v1/tickets/by-reference?reference=X", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous lookup = %d", rec.Code)
	}

	// Archive is refused while an open ticket lives in the Queue, and then allowed.
	if rec := do(admin, "POST", "/api/v1/service-desk/queues/"+hidden.ID+"/archive", `{"expectedVersion":`+itoa(hidden.Version)+`}`); rec.Code != http.StatusConflict ||
		errCode(rec.Body.String()) != "servicedesk.queue_has_open_tickets" {
		t.Errorf("archive with open ticket = %d %s", rec.Code, rec.Body)
	}
	var moved struct{ Version int }
	_ = json.Unmarshal(rec.Body.Bytes(), &moved)
	back := do(staff, "POST", "/api/v1/tickets/"+tk.ID+"/move-queue", `{"expectedVersion":`+itoa(movedVersion(t, staff, tk.ID))+`,"targetQueueId":"`+pub.ID+`","reasonCode":"other"}`)
	if back.Code != http.StatusOK {
		t.Fatalf("move back = %d %s", back.Code, back.Body)
	}
	if rec := do(admin, "POST", "/api/v1/service-desk/queues/"+hidden.ID+"/archive", `{"expectedVersion":`+itoa(hidden.Version)+`}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"archived"`) {
		t.Errorf("archive = %d %s", rec.Code, rec.Body)
	}
	// Grants over HTTP: full replace with a version.
	if rec := do(admin, "PUT", "/api/v1/service-desk/queues/"+pub.ID+"/grants", `{"expectedVersion":`+itoa(pub.Version)+`,"grants":[{"subjectType":"user","subjectId":"`+bob+`","level":"view"}]}`); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), bob) {
		t.Errorf("grants = %d %s", rec.Code, rec.Body)
	}
	if rec := do(stranger, "GET", "/api/v1/tickets/"+tk.ID, ""); rec.Code != http.StatusOK {
		t.Errorf("a view grant must open the queue's tickets = %d", rec.Code)
	}
	if rec := do(admin, "PUT", "/api/v1/service-desk/queues/"+pub.ID+"/grants", `{"expectedVersion":`+itoa(pub.Version)+`,"grants":[]}`); rec.Code != http.StatusConflict {
		t.Errorf("stale grants = %d", rec.Code)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func movedVersion(t *testing.T, h http.Handler, id string) int {
	t.Helper()
	rec := do(h, "GET", "/api/v1/tickets/"+id, "")
	var d struct{ Version int }
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d.Version
}
