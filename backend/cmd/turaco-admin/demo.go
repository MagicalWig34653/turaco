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
//
// "demo seed-hospital" additionally creates the hospital IT simulation (docs/development/simulation-hospital.md).
func runDemo(ctx context.Context, e env, command string, args []string) error {
	if (command != "seed" && command != "seed-hospital") || len(args) != 0 {
		return errUsage
	}
	if e.cfg.Environment != "development" {
		return fmt.Errorf("demo %s runs only with APP_ENV=development (APP_ENV is %q)", command, e.cfg.Environment)
	}
	if command == "seed-hospital" {
		return seedHospital(ctx, e)
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

// newDemoSeeder wires the services of the seeders; correlationID tags every audit entry of the run.
func newDemoSeeder(e env, correlationID string) *demoSeeder {
	productsRepo := productsrepository.New(e.pool)
	return &demoSeeder{
		e:        e,
		products: productsapp.NewService(productsRepo),
		catalog: catalogapp.NewService(catalogrepository.New(e.pool), orgpublic.NewWorkDirectory(orgrepo.New(e.pool)),
			catalogpublic.NewProducts(productspublic.NewDirectory(productsRepo))),
		pc: productsapp.Caller{Actor: e.auditActor(), CorrelationID: correlationID},
		cc: catalogapp.Caller{Actor: e.auditActor(), CorrelationID: correlationID},
	}
}

func seedDemo(ctx context.Context, e env) error {
	d := newDemoSeeder(e, "demo-seed")
	laptops, err := d.category(ctx, "Notebooks", "")
	if err != nil {
		return err
	}
	monitors, err := d.category(ctx, "Monitore", "Monitors")
	if err != nil {
		return err
	}
	peripherals, err := d.category(ctx, "Peripherie", "Peripherals")
	if err != nil {
		return err
	}
	vendor, err := d.manufacturer(ctx, "Example Hardware Inc.")
	if err != nil {
		return err
	}
	ids := map[string]string{}
	// The product names are German; legacy is the English name an earlier seed run used (renamed in place).
	for _, p := range []struct {
		name, legacy, mpn, ipn, category string
		serialized, asset                bool
	}{
		{"Business-Notebook 14\"", "Business notebook 14\"", "EH-NB-14", "NB-14", laptops, true, true},
		{"Business-Notebook 16\"", "Business notebook 16\"", "EH-NB-16", "NB-16", laptops, true, true},
		{"Monitor 27\"", "", "EH-MON-27", "MON-27", monitors, true, true},
		{"Dockingstation", "Docking station", "EH-DOCK", "DOCK-1", peripherals, true, true},
		{"Kabellose Maus", "Wireless mouse", "EH-MOUSE", "MOUSE-1", peripherals, false, false},
	} {
		id, err := d.product(ctx, vendor, p.category, p.name, p.legacy, p.mpn, p.ipn, p.serialized, p.asset)
		if err != nil {
			return err
		}
		ids[p.ipn] = id
	}
	if err := d.logistics(ctx, ids); err != nil {
		return err
	}
	if err := d.servicedesk(ctx); err != nil {
		return err
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

// category returns the category of that name. legacy is the English name an earlier seed run used: that category is
// renamed in place instead of creating a second one.
func (d *demoSeeder) category(ctx context.Context, name, legacy string) (string, error) {
	if legacy != "" {
		list, err := d.products.ListCategories(ctx, d.manage(), legacy, productsapp.Page{Limit: 50})
		if err != nil {
			return "", err
		}
		for _, x := range list.Items {
			if x.Name == legacy && x.ParentID == nil {
				if _, err := d.products.RenameCategory(ctx, d.pc, d.manage(), x.ID, x.Version, name); err != nil && !errors.Is(err, productsapp.ErrConflict) {
					return "", err
				}
				return x.ID, nil
			}
		}
	}
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

// product creates a product or returns the existing one of that name.
func (d *demoSeeder) product(ctx context.Context, manufacturerID, categoryID, name, legacy, mpn, ipn string, serialized, asset bool) (string, error) {
	if legacy != "" {
		list, err := d.products.ListProducts(ctx, d.manage(), productsapp.ProductFilter{TitlePrefix: legacy, Page: productsapp.Page{Limit: 50}})
		if err != nil {
			return "", err
		}
		for _, x := range list.Items {
			if x.Name == legacy {
				if _, err := d.products.UpdateProduct(ctx, d.pc, d.manage(), x.ID, x.Version, productsapp.UpdateProductInput{Name: &name}); err != nil {
					return "", err
				}
				return x.ID, nil
			}
		}
	}
	stock := !serialized
	p, err := d.products.CreateProduct(ctx, d.pc, d.manage(), productsapp.ProductInput{
		Name: name, ManufacturerID: &manufacturerID, CategoryID: &categoryID,
		ManufacturerPartNumber: mpn, InternalPartNumber: ipn, Serialized: serialized, StockManaged: &stock, AssetManaged: asset,
	})
	if errors.Is(err, productsapp.ErrConflict) {
		list, lerr := d.products.ListProducts(ctx, d.manage(), productsapp.ProductFilter{TitlePrefix: name, Page: productsapp.Page{Limit: 50}})
		if lerr != nil {
			return "", lerr
		}
		for _, x := range list.Items {
			if x.Name == name {
				return x.ID, nil
			}
		}
	}
	return p.ID, err
}

// item creates a catalog item unless its key exists; it reports whether it was created.
func (d *demoSeeder) item(ctx context.Context, it demoItem) (bool, error) {
	raw, err := json.Marshal(it.Definition)
	if err != nil {
		return false, err
	}
	p := catalogapp.Principal{Manage: true}
	_, err = d.catalog.Create(ctx, d.cc, p, catalogapp.CreateInput{
		Key: it.Key, Title: it.Title, Description: it.Description, Definition: raw,
	})
	if errors.Is(err, catalogapp.ErrConflict) {
		// The item exists from an earlier run: bring its texts (and form) up to date, e.g. the German translation.
		return false, d.refreshItem(ctx, p, it, raw)
	}
	return err == nil, err
}

// itemByKey finds a catalog item by its key (all statuses).
func (d *demoSeeder) itemByKey(ctx context.Context, p catalogapp.Principal, key string) (catalogapp.Item, bool, error) {
	page := catalogapp.Page{Limit: 100}
	for {
		res, err := d.catalog.List(ctx, p, "", page)
		if err != nil {
			return catalogapp.Item{}, false, err
		}
		for _, x := range res.Items {
			if x.Key == key {
				return x, true, nil
			}
		}
		if res.NextCursor == "" {
			return catalogapp.Item{}, false, nil
		}
		page.Cursor = res.NextCursor
	}
}

// refreshItem updates an existing item when its title, description or definition differs from the seed's.
func (d *demoSeeder) refreshItem(ctx context.Context, p catalogapp.Principal, it demoItem, raw []byte) error {
	cur, ok, err := d.itemByKey(ctx, p, it.Key)
	if err != nil || !ok {
		return err
	}
	want, err := catalogapp.ParseDefinition(raw)
	if err != nil {
		return err
	}
	// Keep a fallback Team set by another seed step: it is not part of the base definition.
	for i := range want.Approvals {
		if i < len(cur.Definition.Approvals) && want.Approvals[i].FallbackTeamID == nil {
			want.Approvals[i].FallbackTeamID = cur.Definition.Approvals[i].FallbackTeamID
		}
	}
	wantRaw, err := want.Marshal()
	if err != nil {
		return err
	}
	curRaw, err := cur.Definition.Marshal()
	if err != nil {
		return err
	}
	if cur.Title == it.Title && cur.Description == it.Description && string(curRaw) == string(wantRaw) {
		return nil
	}
	_, err = d.catalog.Update(ctx, d.cc, p, cur.ID, cur.Version, catalogapp.UpdateInput{Title: &it.Title, Description: &it.Description, Definition: wantRaw})
	return err
}

// setManagerFallback lets the manager approval steps of the items fall back to a Team when the requested-for person has
// no manager (or the manager may not decide), so a request never dead-ends.
func (d *demoSeeder) setManagerFallback(ctx context.Context, keys []string, teamID string) error {
	p := catalogapp.Principal{Manage: true}
	for _, key := range keys {
		cur, ok, err := d.itemByKey(ctx, p, key)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		def := cur.Definition
		changed := false
		for i := range def.Approvals {
			if def.Approvals[i].Approver == "manager" && (def.Approvals[i].FallbackTeamID == nil || *def.Approvals[i].FallbackTeamID != teamID) {
				def.Approvals[i].FallbackTeamID, changed = &teamID, true
			}
		}
		if !changed {
			continue
		}
		raw, err := def.Marshal()
		if err != nil {
			return err
		}
		if _, err := d.catalog.Update(ctx, d.cc, p, cur.ID, cur.Version, catalogapp.UpdateInput{Definition: raw}); err != nil {
			return fmt.Errorf("fallback of %s: %w", key, err)
		}
	}
	return nil
}

type demoItem struct {
	Key, Title, Description string
	Definition              map[string]any
}

// demoItems are the four example catalog items of docs/product/f3-requests-design.md. Approval is by
// the requested-for User's manager; fulfillment tasks are unassigned so any task manager can pick
// them up. Teams and approvers are environment specific, so a real catalog sets them explicitly.
func demoItems(laptops, monitors, peripherals string) []demoItem {
	reason := map[string]any{"key": "reason", "type": "longtext", "label": "Begründung", "required": true, "maxLength": 1000}
	manager := []map[string]any{{"approver": "manager"}}
	return []demoItem{
		{
			Key: "hardware-notebook", Title: "Notebook", Description: "Ein Notebook für sich selbst oder für eine Kollegin bzw. einen Kollegen beantragen.",
			Definition: map[string]any{
				"allowRequestedFor": true,
				"fields": []map[string]any{
					{"key": "model", "type": "product", "label": "Modell", "required": true, "categoryId": laptops},
					reason,
				},
				"approvals": manager,
				"fulfillment": []map[string]any{
					{"title": "Notebook vorbereiten und installieren", "priority": "normal", "dueAfterHours": 72},
					{"title": "Notebook übergeben", "priority": "normal", "dueAfterHours": 120},
				},
			},
		},
		{
			Key: "software-request", Title: "Software", Description: "Eine Anwendung beantragen, die nicht zur Standardausstattung gehört.",
			Definition: map[string]any{
				"fields": []map[string]any{
					{"key": "software", "type": "text", "label": "Name der Software", "required": true, "maxLength": 200},
					{"key": "licence", "type": "select", "label": "Lizenz", "required": true, "options": []map[string]any{
						{"value": "free", "label": "Kostenlos oder Open Source"},
						{"value": "paid", "label": "Kostenpflichtige Lizenz"},
					}},
					reason,
				},
				"approvals": manager,
				"fulfillment": []map[string]any{
					{"title": "Lizenz und Sicherheit der Software prüfen", "priority": "normal"},
					{"title": "Software installieren", "priority": "normal"},
				},
			},
		},
		{
			Key: "access-request", Title: "Systemzugang", Description: "Zugriff auf eine Anwendung oder ein Laufwerk beantragen.",
			Definition: map[string]any{
				"allowRequestedFor": true,
				"fields": []map[string]any{
					{"key": "system", "type": "text", "label": "System oder Laufwerk", "required": true, "maxLength": 200},
					{"key": "level", "type": "select", "label": "Zugriffsstufe", "required": true, "options": []map[string]any{
						{"value": "read", "label": "Lesen"},
						{"value": "write", "label": "Schreiben"},
					}},
					reason,
				},
				"approvals":   manager,
				"fulfillment": []map[string]any{{"title": "Zugriff einrichten", "priority": "high", "dueAfterHours": 24}},
			},
		},
		{
			Key: "new-workplace", Title: "Neuer Arbeitsplatz", Description: "Alles, was eine neue Kollegin oder ein neuer Kollege am ersten Tag braucht.",
			Definition: map[string]any{
				"allowRequestedFor": true,
				"fields": []map[string]any{
					{"key": "startDate", "type": "date", "label": "Erster Arbeitstag", "required": true},
					{"key": "notebook", "type": "product", "label": "Notebook", "required": true, "categoryId": laptops},
					{"key": "monitor", "type": "product", "label": "Monitor", "required": false, "categoryId": monitors},
					{"key": "peripheral", "type": "product", "label": "Maus oder Dockingstation", "required": false, "categoryId": peripherals},
					{"key": "remarks", "type": "longtext", "label": "Anmerkungen", "required": false, "maxLength": 1000},
				},
				"approvals": manager,
				"fulfillment": []map[string]any{
					{"title": "Notebook vorbereiten", "priority": "normal", "dueAfterHours": 72},
					{"title": "Monitor und Peripherie einrichten", "priority": "normal", "mandatory": false},
					{"title": "Konten und Postfach anlegen", "priority": "high", "dueAfterHours": 48},
					{"title": "Neue Kollegin bzw. neuen Kollegen begrüßen und Ausstattung übergeben", "priority": "normal", "dueAfterHours": 120},
				},
			},
		},
	}
}
