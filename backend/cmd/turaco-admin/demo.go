package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	catalogapp "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/application"
	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	catalogrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepo "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	productsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/products/application"
	productspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/products/public"
	productsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/products/repository"
)

// runDemo implements the "demo" command group. "demo seed" creates sample
// master data and example catalog items through the same audited application
// operations as the API. It is for local test instances only and refuses to
// run unless APP_ENV=development. It is idempotent.
func runDemo(ctx context.Context, e env, command string, args []string) error {
	if command != "seed" || len(args) != 0 {
		return errUsage
	}
	if e.cfg.Environment != "development" {
		return fmt.Errorf("demo seed runs only with APP_ENV=development (APP_ENV is %q)", e.cfg.Environment)
	}
	return seedDemo(ctx, e)
}

type demoSeeder struct {
	e        env
	products *productsapp.Service
	catalog  *catalogapp.Service
	pc       productsapp.Caller
	cc       catalogapp.Caller
}

func seedDemo(ctx context.Context, e env) error {
	productsRepo := productsrepository.New(e.pool)
	d := &demoSeeder{
		e:        e,
		products: productsapp.NewService(productsRepo),
		catalog: catalogapp.NewService(catalogrepository.New(e.pool), orgpublic.NewWorkDirectory(orgrepo.New(e.pool)),
			catalogpublic.NewProducts(productspublic.NewDirectory(productsRepo))),
		pc: productsapp.Caller{Actor: e.auditActor(), CorrelationID: "demo-seed"},
		cc: catalogapp.Caller{Actor: e.auditActor(), CorrelationID: "demo-seed"},
	}
	laptops, err := d.category(ctx, "Notebooks")
	if err != nil {
		return err
	}
	monitors, err := d.category(ctx, "Monitors")
	if err != nil {
		return err
	}
	peripherals, err := d.category(ctx, "Peripherals")
	if err != nil {
		return err
	}
	vendor, err := d.manufacturer(ctx, "Example Hardware Inc.")
	if err != nil {
		return err
	}
	for _, p := range []struct {
		name, mpn, ipn, category string
		serialized, asset        bool
	}{
		{"Business notebook 14\"", "EH-NB-14", "NB-14", laptops, true, true},
		{"Business notebook 16\"", "EH-NB-16", "NB-16", laptops, true, true},
		{"Monitor 27\"", "EH-MON-27", "MON-27", monitors, true, true},
		{"Docking station", "EH-DOCK", "DOCK-1", peripherals, true, true},
		{"Wireless mouse", "EH-MOUSE", "MOUSE-1", peripherals, false, false},
	} {
		if err := d.product(ctx, vendor, p.category, p.name, p.mpn, p.ipn, p.serialized, p.asset); err != nil {
			return err
		}
	}
	for _, item := range demoItems(laptops, monitors, peripherals) {
		created, err := d.item(ctx, item)
		if err != nil {
			return fmt.Errorf("catalog item %s: %w", item.Key, err)
		}
		if created {
			fmt.Fprintf(e.stdout, "created catalog item %s\n", item.Key)
		}
	}
	fmt.Fprintln(e.stdout, "demo data is ready")
	return nil
}

func (d *demoSeeder) manage() productsapp.Principal { return productsapp.Principal{Manage: true} }

func (d *demoSeeder) manufacturer(ctx context.Context, name string) (string, error) {
	m, err := d.products.CreateManufacturer(ctx, d.pc, d.manage(), name)
	if errors.Is(err, productsapp.ErrConflict) {
		list, lerr := d.products.ListManufacturers(ctx, d.manage(), name, productsapp.Page{Limit: 50})
		if lerr != nil {
			return "", lerr
		}
		for _, x := range list.Items {
			if x.Name == name {
				return x.ID, nil
			}
		}
	}
	return m.ID, err
}

func (d *demoSeeder) category(ctx context.Context, name string) (string, error) {
	c, err := d.products.CreateCategory(ctx, d.pc, d.manage(), name, nil)
	if errors.Is(err, productsapp.ErrConflict) {
		list, lerr := d.products.ListCategories(ctx, d.manage(), name, productsapp.Page{Limit: 50})
		if lerr != nil {
			return "", lerr
		}
		for _, x := range list.Items {
			if x.Name == name && x.ParentID == nil {
				return x.ID, nil
			}
		}
	}
	return c.ID, err
}

func (d *demoSeeder) product(ctx context.Context, manufacturerID, categoryID, name, mpn, ipn string, serialized, asset bool) error {
	stock := !serialized
	_, err := d.products.CreateProduct(ctx, d.pc, d.manage(), productsapp.ProductInput{
		Name: name, ManufacturerID: &manufacturerID, CategoryID: &categoryID,
		ManufacturerPartNumber: mpn, InternalPartNumber: ipn, Serialized: serialized, StockManaged: &stock, AssetManaged: asset,
	})
	if errors.Is(err, productsapp.ErrConflict) {
		return nil
	}
	return err
}

// item creates a catalog item unless its key exists; it reports whether it was created.
func (d *demoSeeder) item(ctx context.Context, it demoItem) (bool, error) {
	raw, err := json.Marshal(it.Definition)
	if err != nil {
		return false, err
	}
	_, err = d.catalog.Create(ctx, d.cc, catalogapp.Principal{Manage: true}, catalogapp.CreateInput{
		Key: it.Key, Title: it.Title, Description: it.Description, Definition: raw,
	})
	if errors.Is(err, catalogapp.ErrConflict) {
		return false, nil
	}
	return err == nil, err
}

type demoItem struct {
	Key, Title, Description string
	Definition              map[string]any
}

// demoItems are the four example catalog items of docs/product/f3-requests-design.md. Approval is by
// the requested-for User's manager; fulfillment tasks are unassigned so any task manager can pick
// them up. Teams and approvers are environment specific, so a real catalog sets them explicitly.
func demoItems(laptops, monitors, peripherals string) []demoItem {
	reason := map[string]any{"key": "reason", "type": "longtext", "label": "Business reason", "required": true, "maxLength": 1000}
	manager := []map[string]any{{"approver": "manager"}}
	return []demoItem{
		{
			Key: "hardware-notebook", Title: "Notebook", Description: "Request a notebook for yourself or a colleague.",
			Definition: map[string]any{
				"allowRequestedFor": true,
				"fields": []map[string]any{
					{"key": "model", "type": "product", "label": "Model", "required": true, "categoryId": laptops},
					reason,
				},
				"approvals": manager,
				"fulfillment": []map[string]any{
					{"title": "Prepare and image the notebook", "priority": "normal", "dueAfterHours": 72},
					{"title": "Hand over the notebook", "priority": "normal", "dueAfterHours": 120},
				},
			},
		},
		{
			Key: "software-request", Title: "Software", Description: "Request an application that is not part of the standard setup.",
			Definition: map[string]any{
				"fields": []map[string]any{
					{"key": "software", "type": "text", "label": "Software name", "required": true, "maxLength": 200},
					{"key": "licence", "type": "select", "label": "Licence", "required": true, "options": []map[string]any{
						{"value": "free", "label": "Free or open source"},
						{"value": "paid", "label": "Paid licence"},
					}},
					reason,
				},
				"approvals": manager,
				"fulfillment": []map[string]any{
					{"title": "Check licence and security of the software", "priority": "normal"},
					{"title": "Install the software", "priority": "normal"},
				},
			},
		},
		{
			Key: "access-request", Title: "System access", Description: "Request access to an application or a share.",
			Definition: map[string]any{
				"allowRequestedFor": true,
				"fields": []map[string]any{
					{"key": "system", "type": "text", "label": "System or share", "required": true, "maxLength": 200},
					{"key": "level", "type": "select", "label": "Access level", "required": true, "options": []map[string]any{
						{"value": "read", "label": "Read"},
						{"value": "write", "label": "Write"},
					}},
					reason,
				},
				"approvals":   manager,
				"fulfillment": []map[string]any{{"title": "Grant the access", "priority": "high", "dueAfterHours": 24}},
			},
		},
		{
			Key: "new-workplace", Title: "New workplace", Description: "Everything a new colleague needs on the first day.",
			Definition: map[string]any{
				"allowRequestedFor": true,
				"fields": []map[string]any{
					{"key": "startDate", "type": "date", "label": "First day", "required": true},
					{"key": "notebook", "type": "product", "label": "Notebook", "required": true, "categoryId": laptops},
					{"key": "monitor", "type": "product", "label": "Monitor", "required": false, "categoryId": monitors},
					{"key": "peripheral", "type": "product", "label": "Mouse or dock", "required": false, "categoryId": peripherals},
					{"key": "remarks", "type": "longtext", "label": "Remarks", "required": false, "maxLength": 1000},
				},
				"approvals": manager,
				"fulfillment": []map[string]any{
					{"title": "Prepare the notebook", "priority": "normal", "dueAfterHours": 72},
					{"title": "Set up the monitor and peripherals", "priority": "normal", "mandatory": false},
					{"title": "Create accounts and mailbox", "priority": "high", "dueAfterHours": 48},
					{"title": "Welcome the colleague and hand over the equipment", "priority": "normal", "dueAfterHours": 120},
				},
			},
		},
	}
}
