package repository_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Tests for the F6 slice 4 history views.

func (e *env) artifactHistory(p application.Principal, ext string, page application.Page) application.ArtifactHistory {
	e.t.Helper()
	h, err := e.svc.ArtifactHistory(context.Background(), p, e.artifactID(ext), page)
	if err != nil {
		e.t.Fatalf("artifact history: %v", err)
	}
	return h
}

func kinds(items []application.HistoryEntry) []string {
	var out []string
	for _, i := range items {
		out = append(out, i.Kind)
	}
	return out
}

func TestArtifactHistoryHasMeaningfulChangesOnly(t *testing.T) {
	v := newViewEnv(t)
	snap := func(as ...intune.AssignmentRecord) intune.ManagementSnapshot {
		return intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy", as...)}}
	}
	v.mingest(true, snap(grp("x1", "g1", "required")))
	// Identical syncs are freshness only: no history.
	for i := 0; i < 3; i++ {
		v.mingest(true, snap(grp("x1", "g1", "required")))
	}
	if got := kinds(v.artifactHistory(v.full, "a1", application.Page{}).Items); !slices.Equal(got, []string{application.HistoryAssignmentAdded}) {
		t.Fatalf("after repeated syncs = %v", got)
	}
	v.mingest(true, snap(grp("x1", "g1", "available")))
	v.mingest(true, snap(grp("x1", "g2", "available")))
	v.mingest(true, snap(grp("x1", "g2", "available"), grp("x2", "g3", "required")))
	v.mingest(true, snap(grp("x1", "g2", "available")))
	v.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("other", "Other")}}) // a1 tombstoned (1 of 2 live: guard is off below 10)

	h := v.artifactHistory(v.full, "a1", application.Page{})
	got := kinds(h.Items)
	want := []string{application.HistoryArtifactRemoved, application.HistoryAssignmentRemoved, application.HistoryAssignmentRemoved, application.HistoryAssignmentAdded,
		application.HistoryAssignmentChanged, application.HistoryAssignmentChanged, application.HistoryAssignmentAdded}
	if !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := 1; i < len(h.Items); i++ {
		if h.Items[i].OccurredAt.After(h.Items[i-1].OccurredAt) {
			t.Errorf("not newest first at %d: %v", i, got)
		}
	}
	var changed []application.HistoryEntry
	for _, it := range h.Items {
		if it.Kind == application.HistoryAssignmentChanged {
			changed = append(changed, it)
		}
		if it.Source != application.SourceSync || it.ObservedAt.IsZero() || it.OccurredAt.IsZero() || it.Artifact == nil || it.Artifact.ID != v.artifactID("a1") {
			t.Errorf("entry lacks provenance: %+v", it)
		}
	}
	if len(changed) != 2 {
		t.Fatalf("changed = %v", got)
	}
	// Newest change first: g1 -> g2 (target), then required -> available (intent).
	if !slices.Equal(changed[0].Assignment.Changes, []string{application.ChangeTarget}) || !slices.Equal(changed[1].Assignment.Changes, []string{application.ChangeIntent}) ||
		changed[1].Assignment.Previous == nil || changed[1].Assignment.Previous.Intent != "required" || changed[1].Assignment.Current.Intent != "available" {
		t.Errorf("changes = %+v / %+v", changed[0].Assignment, changed[1].Assignment)
	}
	if g := changed[0].Assignment.Current.Group; g == nil || g.Redacted || g.ExternalID == nil || *g.ExternalID != "g2" {
		t.Errorf("group = %+v", g)
	}
	if h.Items[0].Kind != application.HistoryArtifactRemoved {
		t.Errorf("the tombstone is the newest entry: %v", got)
	}

	// Keyset: pages of two cover the list exactly once.
	var paged []string
	cursor := ""
	for i := 0; i < 20; i++ {
		pg := v.artifactHistory(v.full, "a1", application.Page{Limit: 2, Cursor: cursor})
		for _, it := range pg.Items {
			paged = append(paged, it.Kind+"@"+it.OccurredAt.String())
		}
		if pg.NextCursor == "" {
			break
		}
		cursor = pg.NextCursor
	}
	var full []string
	for _, it := range h.Items {
		full = append(full, it.Kind+"@"+it.OccurredAt.String())
	}
	if !slices.Equal(paged, full) {
		t.Errorf("paged %v != full %v", paged, full)
	}

	// Redaction: without organization.directory.view no group id or name.
	noDir := v.full
	noDir.DirectoryView = false
	for _, it := range v.artifactHistory(noDir, "a1", application.Page{}).Items {
		if it.Assignment != nil && (it.Assignment.Current.Group == nil || !it.Assignment.Current.Group.Redacted || it.Assignment.Current.Group.ExternalID != nil || it.Assignment.Current.Group.Name != nil) {
			t.Errorf("group not redacted: %+v", it.Assignment.Current.Group)
		}
	}
}

func TestHistoryPermissionsAndInvalidInput(t *testing.T) {
	v := newViewEnv(t)
	v.ingest(dev("d1", "PC-1", "SN1"))
	v.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "P", allDevices("x1"))}})
	ctx := context.Background()
	a, d := v.artifactID("a1"), v.device("d1").ID
	viewOnly := application.Principal{UserID: v.user, View: true}
	mgmtOnly := application.Principal{UserID: v.user, ManagementView: true}
	if _, err := v.svc.ArtifactHistory(ctx, viewOnly, a, application.Page{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view-only artifact history = %v", err)
	}
	if _, err := v.svc.ArtifactHistory(ctx, mgmtOnly, a, application.Page{}); err != nil {
		t.Errorf("management view should read artifact history: %v", err)
	}
	if _, err := v.svc.DeviceHistory(ctx, mgmtOnly, d, application.Page{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("device history needs device access: %v", err)
	}
	if _, err := v.svc.DeviceHistory(ctx, viewOnly, d, application.Page{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("device history needs management access: %v", err)
	}
	if _, err := v.svc.DeviceHistory(ctx, v.full, v.newID(), application.Page{}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown device = %v", err)
	}
	if _, err := v.svc.ArtifactHistory(ctx, v.full, v.newID(), application.Page{}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown artifact = %v", err)
	}
	if _, err := v.svc.ArtifactHistory(ctx, v.full, "not-a-uuid", application.Page{}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed artifact id = %v", err)
	}
	for _, c := range []string{"zz", "AAAA", "YWJjfHg"} {
		if _, err := v.svc.ArtifactHistory(ctx, v.full, a, application.Page{Cursor: c}); !errors.Is(err, application.ErrInvalidCursor) {
			t.Errorf("cursor %q = %v", c, err)
		}
		if _, err := v.svc.DeviceHistory(ctx, v.full, d, application.Page{Cursor: c}); !errors.Is(err, application.ErrInvalidCursor) {
			t.Errorf("device cursor %q = %v", c, err)
		}
	}
}

func TestDeviceHistoryCombinesObservationMembershipAndAssignmentChanges(t *testing.T) {
	v := newViewEnv(t)
	v.ingest(dev("d1", "PC-1", "SN1"))
	d := v.device("d1").ID
	snap := func(state, member string, a1 intune.AssignmentRecord) intune.ManagementSnapshot {
		s := intune.ManagementSnapshot{
			Artifacts: []intune.ArtifactRecord{
				art("a1", "Reaching", a1),
				art("a2", "Everyone", allDevices("y1")),
				art("a3", "Elsewhere", grp("z1", "g9", "required")), // never addresses the device
			},
			Observations: []intune.ObservationRecord{obs("d1", "a1", state, "raw "+state)},
		}
		s.Memberships = []intune.DeviceGroupMembershipRecord{mship("d1", member)}
		return s
	}
	v.mingest(true, snap("pending", "g1", grp("x1", "g1", "required")))
	count := func() int { return len(mustDeviceHistory(t, v, v.full, d, application.Page{Limit: 200}).Items) }
	base := count()
	// Repeated identical syncs add nothing (no per-sync noise).
	for i := 0; i < 3; i++ {
		v.mingest(true, snap("pending", "g1", grp("x1", "g1", "required")))
	}
	if got := count(); got != base {
		t.Fatalf("identical syncs added entries: %d -> %d", base, got)
	}
	v.mingest(true, snap("applied", "g1", grp("x1", "g1", "available")))
	v.mingest(true, snap("applied", "g2", grp("x1", "g1", "available")))

	h := mustDeviceHistory(t, v, v.full, d, application.Page{Limit: 200})
	if h.AssignmentScope != application.AssignmentScopeDevice {
		t.Errorf("scope = %q", h.AssignmentScope)
	}
	counts := map[string]int{}
	for i, it := range h.Items {
		counts[it.Kind]++
		if it.Source == "" || it.ObservedAt.IsZero() {
			t.Errorf("entry lacks provenance: %+v", it)
		}
		if i > 0 && it.OccurredAt.After(h.Items[i-1].OccurredAt) {
			t.Errorf("not newest first at %d", i)
		}
		if it.Artifact != nil && it.Artifact.Name == "Elsewhere" {
			t.Errorf("an assignment to a group the device never belonged to leaked: %+v", it)
		}
	}
	// pending (first seen) then applied (changed); joined and left g1; a1 added, a2 added (all_devices), a1 changed.
	want := map[string]int{application.HistoryObservationFirstSeen: 1, application.HistoryObservationChanged: 1, application.HistoryGroupJoined: 2, application.HistoryGroupLeft: 1,
		application.HistoryAssignmentAdded: 2, application.HistoryAssignmentChanged: 1}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("%s = %d, want %d (all: %v)", k, counts[k], n, counts)
		}
	}
	for _, it := range h.Items {
		if it.Kind == application.HistoryObservationChanged && (it.Observation.PreviousState != "pending" || it.Observation.State != "applied") {
			t.Errorf("observation change = %+v", it.Observation)
		}
		if (it.Kind == application.HistoryGroupJoined || it.Kind == application.HistoryGroupLeft) && (it.Group == nil || it.Group.ExternalID == nil || (*it.Group.ExternalID != "g1" && *it.Group.ExternalID != "g2")) {
			t.Errorf("membership group = %+v", it.Group)
		}
	}

	// Paging with a small limit yields the same list.
	var paged []string
	cursor := ""
	for i := 0; i < 50; i++ {
		pg := mustDeviceHistory(t, v, v.full, d, application.Page{Limit: 3, Cursor: cursor})
		for _, it := range pg.Items {
			paged = append(paged, it.Kind)
		}
		if pg.NextCursor == "" {
			break
		}
		cursor = pg.NextCursor
	}
	if !slices.Equal(paged, kinds(h.Items)) {
		t.Errorf("paged %v != full %v", paged, kinds(h.Items))
	}

	// Redaction.
	noDir := v.full
	noDir.DirectoryView = false
	for _, it := range mustDeviceHistory(t, v, noDir, d, application.Page{Limit: 200}).Items {
		if it.Group != nil && (!it.Group.Redacted || it.Group.ExternalID != nil) {
			t.Errorf("membership group not redacted: %+v", it.Group)
		}
		if it.Assignment != nil && it.Assignment.Current.Group != nil && (!it.Assignment.Current.Group.Redacted || it.Assignment.Current.Group.ExternalID != nil) {
			t.Errorf("assignment group not redacted: %+v", it.Assignment.Current.Group)
		}
	}
}

func mustDeviceHistory(t *testing.T, v *viewEnv, p application.Principal, device string, page application.Page) application.DeviceHistory {
	t.Helper()
	h, err := v.svc.DeviceHistory(context.Background(), p, device, page)
	if err != nil {
		t.Fatalf("device history: %v", err)
	}
	return h
}

func TestHistoryCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 123456000, time.UTC)
	c := application.HistoryCursor{At: at, Key: "a:0199-abc:o"}
	got, err := application.DecodeHistoryCursor(application.EncodeHistoryCursor(c))
	if err != nil || got == nil || !got.At.Equal(at) || got.Key != c.Key {
		t.Errorf("round trip = %+v %v", got, err)
	}
	if none, err := application.DecodeHistoryCursor(""); none != nil || err != nil {
		t.Errorf("empty = %v %v", none, err)
	}
}
