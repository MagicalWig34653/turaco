package repository_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Tests for the F6 slice 4 diff views.

func diffByArtifact(items []application.DeviceDiffItem) map[string]application.DeviceDiffItem {
	out := map[string]application.DeviceDiffItem{}
	for _, it := range items {
		out[it.Artifact.ExternalID] = it
	}
	return out
}

func TestDeviceDiffClassifiesAndKeepsUnknownsUnknown(t *testing.T) {
	v := newViewEnv(t)
	v.orgGroup("g1")
	v.orgGroup("g2")
	user1, user2 := v.newUser("Alice"), v.newUser("Bob")
	v.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"), dev("d3", "PC-3", "SN3"))
	d1, d2 := v.holderOf("d1", user1), v.holderOf("d2", user2)
	d3 := v.device("d3").ID // no User: unknown
	v.mingest(true, intune.ManagementSnapshot{
		Memberships: []intune.DeviceGroupMembershipRecord{mship("d1", "g1"), mship("d2", "g2"), mship("d3", "g2")},
		Artifacts: []intune.ArtifactRecord{
			art("only-d1", "OnlyD1", grp("x1", "g1", "required")),
			art("both", "Both", allDevices("x2")),
			art("only-d2", "OnlyD2", grp("x3", "g2", "required")),
			art("excl", "Excl", allDevices("x4"), excl("x5", "g2")),
		},
		Observations: []intune.ObservationRecord{
			obs("d1", "both", "applied", ""), obs("d2", "both", "failed", "err"),
			obs("d1", "only-d1", "applied", ""),
		},
	})
	ctx := context.Background()
	res, err := v.svc.DeviceDiff(ctx, v.full, d1, d2, application.DiffFilter{})
	if err != nil {
		t.Fatal(err)
	}
	by := diffByArtifact(res.Items)
	if got := by["only-d1"]; got.Class != application.DiffOnlyLeft || got.Left.Observed == nil || got.Right.Expected.Result != application.ExpectedNotApplicable {
		t.Errorf("only-d1 = %+v", got)
	}
	if got := by["only-d2"]; got.Class != application.DiffOnlyRight {
		t.Errorf("only-d2 = %+v", got)
	}
	if got := by["both"]; got.Class != application.DiffDifferent || got.Dimensions.Observed != application.DimDifferent || got.Dimensions.Expected != application.DimSame ||
		got.Left.Observed.State != "applied" || got.Right.Observed.State != "failed" {
		t.Errorf("both = %+v", got)
	}
	if got := by["excl"]; got.Class != application.DiffDifferent || got.Left.Expected.Result != application.ExpectedApplicable || got.Right.Expected.Result != application.ExpectedExcluded {
		t.Errorf("excl = %+v", got)
	}

	// Differences filter drops the identical ones (there is none here, so all remain); against d3 whose User is
	// unknown the group-targeted artifacts stay unknown: uncertain, never equal.
	res, err = v.svc.DeviceDiff(ctx, v.full, d1, d3, application.DiffFilter{OnlyDifferences: true})
	if err != nil {
		t.Fatal(err)
	}
	by = diffByArtifact(res.Items)
	if got := by["only-d1"]; !got.Uncertain || got.Class == application.DiffOnlyLeft || got.Right.Expected.Result != application.ExpectedUnknown || !got.Right.AssignedUnknown {
		t.Errorf("only-d1 vs unknown-user device = %+v", got)
	}

	// Same two devices diffed with themselves is refused; unknown ids are 404.
	if _, err := v.svc.DeviceDiff(ctx, v.full, d1, d1, application.DiffFilter{}); err == nil {
		t.Error("diff with itself accepted")
	}
	if _, err := v.svc.DeviceDiff(ctx, v.full, d1, v.newID(), application.DiffFilter{}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown other device = %v", err)
	}
	if _, err := v.svc.DeviceDiff(ctx, v.full, d1, "zz", application.DiffFilter{}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed other device = %v", err)
	}
	if _, err := v.svc.DeviceDiff(ctx, v.full, d1, d2, application.DiffFilter{Kind: "x"}); err == nil {
		t.Error("invalid kind accepted")
	}
	if _, err := v.svc.DeviceDiff(ctx, v.full, d1, d2, application.DiffFilter{Page: application.Page{Cursor: "zz"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("cursor = %v", err)
	}

	// Permissions: both management and device access; redaction without directory.view; no User without assets.view.
	mgmtOnly := application.Principal{UserID: v.user, ManagementView: true}
	viewOnly := application.Principal{UserID: v.user, View: true}
	for _, p := range []application.Principal{mgmtOnly, viewOnly} {
		if _, err := v.svc.DeviceDiff(ctx, p, d1, d2, application.DiffFilter{}); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%+v = %v", p, err)
		}
	}
	noDir := v.full
	noDir.DirectoryView = false
	res, err = v.svc.DeviceDiff(ctx, noDir, d1, d2, application.DiffFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res.Items {
		for _, as := range append(it.Left.Assignments, it.Right.Assignments...) {
			if as.Group != nil && (!as.Group.Redacted || as.Group.ExternalID != nil || as.Group.Name != nil) {
				t.Errorf("group not redacted: %+v", as.Group)
			}
		}
	}
	noAssets := v.full
	noAssets.AssetsView = false
	res, err = v.svc.DeviceDiff(ctx, noAssets, d1, d2, application.DiffFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res.Items {
		for _, as := range append(it.Left.Assignments, it.Right.Assignments...) {
			for _, o := range as.Origins {
				if o == "user" {
					t.Errorf("User origin without assets.view: %+v", as)
				}
			}
		}
	}
}

func TestDeviceDiffKeysetAndTruncation(t *testing.T) {
	v := newViewEnv(t)
	v.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	d1, d2 := v.device("d1").ID, v.device("d2").ID
	var arts []intune.ArtifactRecord
	for i := 0; i < 7; i++ {
		arts = append(arts, art(fmt.Sprintf("a%d", i), fmt.Sprintf("A%d", i), allDevices(fmt.Sprintf("x%d", i))))
	}
	v.mingest(true, intune.ManagementSnapshot{Artifacts: arts})
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 10; i++ {
		res, err := v.svc.DeviceDiff(context.Background(), v.full, d1, d2, application.DiffFilter{Page: application.Page{Limit: 3, Cursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range res.Items {
			if seen[it.Artifact.ID] {
				t.Fatalf("duplicate %s", it.Artifact.ID)
			}
			seen[it.Artifact.ID] = true
			if it.Class != application.DiffSame {
				t.Errorf("identical devices: %+v", it.Class)
			}
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if len(seen) != 7 {
		t.Errorf("seen = %d", len(seen))
	}
	// Identical devices have no differences.
	res, err := v.svc.DeviceDiff(context.Background(), v.full, d1, d2, application.DiffFilter{OnlyDifferences: true})
	if err != nil || len(res.Items) != 0 {
		t.Errorf("differences = %d %v", len(res.Items), err)
	}
}

func TestGroupDiffComparesAssignmentProperties(t *testing.T) {
	v := newViewEnv(t)
	g1, g2 := v.orgGroup("g1"), v.orgGroup("g2")
	parent := v.orgGroup("parent")
	v.dir.parents[g2] = []string{parent}
	v.mingest(true, intune.ManagementSnapshot{
		Filters: []intune.FilterRecord{{ExternalID: "f1", Name: "F1", Platform: "windows", Rule: `(device.model -eq "X")`}},
		Artifacts: []intune.ArtifactRecord{
			art("left", "Left", grp("x1", "g1", "required")),
			art("right", "Right", grp("x2", "g2", "required")),
			art("same", "Same", grp("x3", "g1", "required"), grp("x4", "g2", "required")),
			art("intent", "Intent", grp("x5", "g1", "required"), grp("x6", "g2", "available")),
			art("filter", "Filter", grp("x7", "g1", "required"), intune.AssignmentRecord{ProviderAssignmentID: "x8", TargetKind: "group", TargetGroupExternalID: "g2", Mode: "include", Intent: "required", FilterExternalID: "f1", FilterMode: "include"}),
			art("nested", "Nested", grp("x9", "g1", "required"), grp("x10", "parent", "required")), // reaches g2 through its parent
			art("everyone", "Everyone", allDevices("x11")),                                         // not a group target
		},
	})
	ctx := context.Background()
	res, err := v.svc.GroupDiff(ctx, v.full, g1, g2, application.DiffFilter{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]application.GroupDiffItem{}
	for _, it := range res.Items {
		by[it.Artifact.ExternalID] = it
	}
	want := map[string]string{"left": application.DiffOnlyLeft, "right": application.DiffOnlyRight, "same": application.DiffSame, "intent": application.DiffDifferent,
		"filter": application.DiffDifferent, "nested": application.DiffSame}
	if len(by) != len(want) {
		t.Fatalf("items = %d, want %d", len(by), len(want))
	}
	for k, c := range want {
		if by[k].Class != c {
			t.Errorf("%s = %s, want %s", k, by[k].Class, c)
		}
	}
	if d := by["intent"].Differences; len(d) != 1 || d[0] != "intent" {
		t.Errorf("intent differences = %v", d)
	}
	if d := by["filter"].Differences; len(d) != 1 || d[0] != "filter" {
		t.Errorf("filter differences = %v", d)
	}
	if n := by["nested"].Right.Assignments; len(n) != 1 || !n[0].Nested {
		t.Errorf("nested = %+v", n)
	}
	// Differences only.
	res, err = v.svc.GroupDiff(ctx, v.full, g1, g2, application.DiffFilter{OnlyDifferences: true})
	if err != nil || len(res.Items) != 4 {
		t.Errorf("differences only = %d %v", len(res.Items), err)
	}
	// Kind filter and keyset.
	res, err = v.svc.GroupDiff(ctx, v.full, g1, g2, application.DiffFilter{Kind: "script"})
	if err != nil || len(res.Items) != 0 {
		t.Errorf("kind = %d %v", len(res.Items), err)
	}
	seen := 0
	cursor := ""
	for i := 0; i < 10; i++ {
		pg, err := v.svc.GroupDiff(ctx, v.full, g1, g2, application.DiffFilter{Page: application.Page{Limit: 2, Cursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		seen += len(pg.Items)
		if pg.NextCursor == "" {
			break
		}
		cursor = pg.NextCursor
	}
	if seen != 6 {
		t.Errorf("paged = %d", seen)
	}
	// Permissions, ids.
	noDir := v.full
	noDir.DirectoryView = false
	mgmtOnly := application.Principal{UserID: v.user, ManagementView: true, DirectoryView: true}
	if _, err := v.svc.GroupDiff(ctx, noDir, g1, g2, application.DiffFilter{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("no directory.view = %v", err)
	}
	if _, err := v.svc.GroupDiff(ctx, application.Principal{UserID: v.user, View: true, DirectoryView: true}, g1, g2, application.DiffFilter{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("no management = %v", err)
	}
	if _, err := v.svc.GroupDiff(ctx, mgmtOnly, g1, g2, application.DiffFilter{}); err != nil {
		t.Errorf("management+directory should work without device access: %v", err)
	}
	if _, err := v.svc.GroupDiff(ctx, v.full, g1, v.newID(), application.DiffFilter{}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown group = %v", err)
	}
	if _, err := v.svc.GroupDiff(ctx, v.full, g1, g1, application.DiffFilter{}); err == nil {
		t.Error("diff with itself accepted")
	}
}

func TestDeviceDiffScanIsBoundedAndReportsTruncation(t *testing.T) {
	v := newViewEnv(t)
	v.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	d1, d2 := v.device("d1").ID, v.device("d2").ID
	arts := make([]intune.ArtifactRecord, 0, application.MaxDiffScan+100)
	for i := 0; i < application.MaxDiffScan+100; i++ {
		arts = append(arts, art(fmt.Sprintf("a%05d", i), fmt.Sprintf("A%d", i), allDevices(fmt.Sprintf("x%d", i))))
	}
	v.mingest(true, intune.ManagementSnapshot{Artifacts: arts})
	// Every artifact is the same on both devices: with the differences filter nothing matches, the scan stops at
	// the limit, says so and hands out a cursor that continues it.
	res, err := v.svc.DeviceDiff(context.Background(), v.full, d1, d2, application.DiffFilter{OnlyDifferences: true})
	if err != nil || len(res.Items) != 0 || !res.Truncated || res.NextCursor == "" {
		t.Fatalf("truncated diff = items %d truncated %v cursor %q err %v", len(res.Items), res.Truncated, res.NextCursor, err)
	}
	next, err := v.svc.DeviceDiff(context.Background(), v.full, d1, d2, application.DiffFilter{OnlyDifferences: true, Page: application.Page{Cursor: res.NextCursor}})
	if err != nil || next.Truncated {
		t.Errorf("continuation = truncated %v err %v", next.Truncated, err)
	}
}
