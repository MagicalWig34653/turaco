package main

import (
	"context"
	"errors"
	"fmt"

	assetsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	inventoryapp "github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"
	procurementapp "github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// logistics seeds a warehouse with storage locations and opening stock, a few
// notebooks and monitors as assets, a supplier and one open procurement
// request. Everything goes through the audited application operations and is
// skipped when it already exists. ids maps the internal part numbers of the
// demo products to their ids.
func (d *demoSeeder) logistics(ctx context.Context, ids map[string]string) error {
	inv := wiring.Inventory(d.e.pool)
	assets := wiring.Assets(d.e.pool)
	proc := wiring.Procurement(d.e.pool)
	ic := inventoryapp.Caller{Actor: d.e.auditActor(), CorrelationID: "demo-seed"}
	im := inventoryapp.Principal{Manage: true}

	wh, err := inv.CreateWarehouse(ctx, ic, im, "Main warehouse", nil)
	if errors.Is(err, inventoryapp.ErrConflict) {
		list, lerr := inv.ListWarehouses(ctx, im, true, inventoryapp.Page{Limit: 200})
		if lerr != nil {
			return lerr
		}
		for _, w := range list.Items {
			if w.Name == "Main warehouse" {
				wh, err = w, nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("warehouse: %w", err)
	}
	locations := map[string]string{}
	existing, err := inv.ListStorageLocations(ctx, im, wh.ID, true, inventoryapp.Page{Limit: 200})
	if err != nil {
		return err
	}
	for _, l := range existing.Items {
		locations[l.Name] = l.ID
	}
	for _, name := range []string{"Shelf A1", "Shelf A2"} {
		if _, ok := locations[name]; ok {
			continue
		}
		l, err := inv.CreateStorageLocation(ctx, ic, im, wh.ID, name)
		if err != nil {
			return fmt.Errorf("storage location %s: %w", name, err)
		}
		locations[name] = l.ID
	}

	// Opening stock only where there is none yet, so the seed can run again.
	for _, s := range []struct {
		part, shelf string
		quantity    int
	}{{"MOUSE-1", "Shelf A1", 25}} {
		have, err := inv.ListStock(ctx, im, inventoryapp.StockFilter{ProductID: ids[s.part], StorageLocationID: locations[s.shelf]})
		if err != nil {
			return err
		}
		if len(have.Items) > 0 {
			continue
		}
		if _, err := inv.Correct(ctx, ic, im, inventoryapp.Correction{ProductID: ids[s.part], StorageLocationID: locations[s.shelf], Delta: s.quantity, Reason: "demo seed opening stock"}); err != nil {
			return fmt.Errorf("opening stock %s: %w", s.part, err)
		}
	}

	ac := assetsapp.Caller{Actor: d.e.auditActor(), CorrelationID: "demo-seed"}
	am := assetsapp.Principal{Manage: true}
	created := 0
	for _, a := range []struct{ part, serial, tag string }{
		{"NB-14", "DEMO-NB14-0001", "DEMO-0001"}, {"NB-14", "DEMO-NB14-0002", "DEMO-0002"}, {"NB-14", "DEMO-NB14-0003", "DEMO-0003"},
		{"NB-16", "DEMO-NB16-0001", "DEMO-0004"}, {"MON-27", "DEMO-MON27-0001", "DEMO-0005"}, {"DOCK-1", "DEMO-DOCK-0001", "DEMO-0006"},
	} {
		_, err := assets.Create(ctx, ac, am, assetsapp.CreateInput{ProductID: ids[a.part], SerialNumber: a.serial, AssetTag: a.tag})
		if errors.Is(err, assetsapp.ErrConflict) {
			continue
		}
		if err != nil {
			return fmt.Errorf("asset %s: %w", a.serial, err)
		}
		created++
	}
	if created > 0 {
		fmt.Fprintf(d.e.stdout, "created %d demo assets\n", created)
	}

	pc := procurementapp.Caller{Actor: d.e.auditActor(), CorrelationID: "demo-seed"}
	pm := procurementapp.Principal{Manage: true}
	if _, err := proc.CreateSupplier(ctx, pc, pm, "Example Supplies GmbH", "DEMO-1001"); err != nil && !errors.Is(err, procurementapp.ErrConflict) {
		return fmt.Errorf("supplier: %w", err)
	}
	open, err := proc.ListNeeds(ctx, pm, procurementapp.NeedFilter{ProductID: ids["NB-14"], Status: procurementapp.NeedOpen, Page: procurementapp.Page{Limit: 1}})
	if err != nil {
		return err
	}
	if len(open.Items) == 0 {
		if _, err := proc.CreateNeed(ctx, pc, pm, procurementapp.NewNeed{ProductID: ids["NB-14"], Quantity: 5, Notes: "Replenish the notebook pool"}); err != nil {
			return fmt.Errorf("procurement request: %w", err)
		}
	}
	return nil
}
