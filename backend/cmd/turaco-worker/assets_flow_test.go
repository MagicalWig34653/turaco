package main

import (
	"context"
	"testing"

	assetsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

func TestAssetAssignmentNotifiesTheNewHolderOnly(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	var product string
	if err := w.pool.QueryRow(ctx, `INSERT INTO products.products(name, serialized, stock_managed, asset_managed) VALUES ($1, true, false, true) RETURNING id::text`, w.corr+" notebook").Scan(&product); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.pool.Exec(ctx, `DELETE FROM assets.assets WHERE product_id = $1::uuid`, product)
		_, _ = w.pool.Exec(ctx, `DELETE FROM products.products WHERE id = $1::uuid`, product)
	})
	svc := wiring.Assets(w.pool)
	manager := assetsapp.Principal{UserID: w.creator, Manage: true}
	c := func(user string) assetsapp.Caller {
		return assetsapp.Caller{Actor: audit.UserActor(user), CorrelationID: w.corr}
	}
	a, err := svc.Create(ctx, c(w.creator), manager, assetsapp.CreateInput{ProductID: product, SerialNumber: w.corr + "-sn"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transition(ctx, c(w.creator), manager, a.ID, nil, assetsapp.OpAssign, assetsapp.Params{Assignee: assetsapp.Assignee{Type: "user", ID: w.assignee}}); err != nil {
		t.Fatal(err)
	}
	// Handing equipment to oneself is not announced to oneself.
	b, err := svc.Create(ctx, c(w.creator), manager, assetsapp.CreateInput{ProductID: product, SerialNumber: w.corr + "-sn2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transition(ctx, c(w.creator), manager, b.ID, nil, assetsapp.OpAssign, assetsapp.Params{Assignee: assetsapp.Assignee{Type: "user", ID: w.creator}}); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if w.notified(w.assignee, "asset.assigned") != 1 {
		t.Error("the new holder must be notified once")
	}
	if w.notified(w.creator, "asset.assigned") != 0 {
		t.Error("the actor must not be notified about their own assignment")
	}
	// A stale event (the asset was returned before the consumer ran) notifies nobody.
	c2, err := svc.Create(ctx, c(w.creator), manager, assetsapp.CreateInput{ProductID: product, SerialNumber: w.corr + "-sn3"})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []struct {
		op string
		p  assetsapp.Params
	}{
		{assetsapp.OpAssign, assetsapp.Params{Assignee: assetsapp.Assignee{Type: "user", ID: w.member}}},
		{assetsapp.OpReturn, assetsapp.Params{}},
	} {
		if _, err := svc.Transition(ctx, c(w.creator), manager, c2.ID, nil, op.op, op.p); err != nil {
			t.Fatal(err)
		}
	}
	w.dispatch()
	if w.notified(w.member, "asset.assigned") != 0 {
		t.Error("an assignment that no longer holds must not notify")
	}
}
