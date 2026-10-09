package application

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Catalog operations. Audit actions: catalog.item.created,
// .updated, .activated, .deactivated; titles and definitions are never copied
// into the audit log (only the version and the names of changed parts).
type Service struct {
	store    Store
	dir      Directory
	products Products
}

func NewService(store Store, dir Directory, products Products) *Service {
	return &Service{store: store, dir: dir, products: products}
}

func cleanTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxTitle || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", invalid("title must be 1-%d characters without control or invisible formatting characters", maxTitle)
	}
	return s, nil
}

func cleanDescription(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxDesc || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, true) {
		return "", invalid("description must be at most %d characters without control or invisible formatting characters", maxDesc)
	}
	return s, nil
}

// checkReferences verifies that everything the definition points at exists
// and is active, so a broken definition cannot be saved.
func (s *Service) checkReferences(ctx context.Context, d Definition) error {
	var users, teams, products, categories []string
	add := func(dst *[]string, v *string) {
		if v != nil {
			*dst = append(*dst, *v)
		}
	}
	for _, a := range d.Approvals {
		add(&users, a.ApproverUserID)
		add(&teams, a.ApproverTeamID)
		add(&teams, a.FallbackTeamID)
	}
	for _, t := range d.Fulfillment {
		add(&users, t.AssignedUserID)
		add(&teams, t.AssignedTeamID)
	}
	for _, f := range d.Fields {
		products = append(products, f.ProductIDs...)
		if f.CategoryID != "" {
			categories = append(categories, f.CategoryID)
		}
	}
	if len(users) > 0 {
		m, err := s.dir.ActiveUsers(ctx, users)
		if err != nil {
			return fmt.Errorf("check users: %w", err)
		}
		for _, id := range users {
			if !m[id] {
				return fmt.Errorf("%w: user %s", ErrReferenceInvalid, id)
			}
		}
	}
	if len(teams) > 0 {
		m, err := s.dir.ActiveTeams(ctx, teams)
		if err != nil {
			return fmt.Errorf("check teams: %w", err)
		}
		for _, id := range teams {
			if !m[id] {
				return fmt.Errorf("%w: team %s", ErrReferenceInvalid, id)
			}
		}
	}
	if len(products) > 0 {
		m, err := s.products.ActiveProducts(ctx, products)
		if err != nil {
			return fmt.Errorf("check products: %w", err)
		}
		for _, id := range products {
			if _, ok := m[id]; !ok {
				return fmt.Errorf("%w: product %s", ErrReferenceInvalid, id)
			}
		}
	}
	if len(categories) > 0 {
		m, err := s.products.CategoriesExist(ctx, categories)
		if err != nil {
			return fmt.Errorf("check categories: %w", err)
		}
		for _, id := range categories {
			if !m[id] {
				return fmt.Errorf("%w: category %s", ErrReferenceInvalid, id)
			}
		}
	}
	return nil
}

func (s *Service) parseAndCheck(ctx context.Context, raw []byte) (Definition, []byte, error) {
	if err := checkSize(raw); err != nil {
		return Definition{}, nil, invalid("the definition is too large")
	}
	d, err := ParseDefinition(raw)
	if err != nil {
		return Definition{}, nil, err
	}
	if err := s.checkReferences(ctx, d); err != nil {
		return Definition{}, nil, err
	}
	canonical, err := d.Marshal()
	if err != nil {
		return Definition{}, nil, err
	}
	return d, canonical, nil
}

// CreateInput is the input of Create.
type CreateInput struct {
	Key         string
	Title       string
	Description string
	// Definition is the definition as JSON.
	Definition []byte
}

// Create creates an active Catalog Item. Requires catalog.manage.
func (s *Service) Create(ctx context.Context, c Caller, p Principal, in CreateInput) (Item, error) {
	if err := c.validate(); err != nil {
		return Item{}, err
	}
	if !p.Manage {
		return Item{}, ErrForbidden
	}
	if !itemKey.MatchString(in.Key) {
		return Item{}, invalid("key must start with a lower-case letter and contain only lower-case letters, digits and hyphens (2-63)")
	}
	title, err := cleanTitle(in.Title)
	if err != nil {
		return Item{}, err
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return Item{}, err
	}
	_, canonical, err := s.parseAndCheck(ctx, in.Definition)
	if err != nil {
		return Item{}, err
	}
	return s.store.Insert(ctx, c, NewItem{Key: in.Key, Title: title, Description: desc, Definition: canonical})
}

// UpdateInput changes an item; nil fields stay unchanged. Changing the
// definition affects only requests submitted afterwards.
type UpdateInput struct {
	Title       *string
	Description *string
	Definition  []byte // nil keeps the definition
}

// Update changes an item. Requires catalog.manage.
func (s *Service) Update(ctx context.Context, c Caller, p Principal, id string, expected int, in UpdateInput) (Item, error) {
	if err := c.validate(); err != nil {
		return Item{}, err
	}
	if !p.Manage {
		return Item{}, ErrForbidden
	}
	if in.Title == nil && in.Description == nil && in.Definition == nil {
		return Item{}, invalid("at least one field to change is required")
	}
	var title, desc *string
	if in.Title != nil {
		t, err := cleanTitle(*in.Title)
		if err != nil {
			return Item{}, err
		}
		title = &t
	}
	if in.Description != nil {
		d, err := cleanDescription(*in.Description)
		if err != nil {
			return Item{}, err
		}
		desc = &d
	}
	var def *Definition
	if in.Definition != nil {
		d, _, err := s.parseAndCheck(ctx, in.Definition)
		if err != nil {
			return Item{}, err
		}
		def = &d
	}
	return s.store.Change(ctx, c, id, func(cur Item) (Change, error) {
		if expected != cur.Version {
			return Change{}, ErrVersionConflict
		}
		next := cur
		var changed []string
		if title != nil && *title != cur.Title {
			next.Title = *title
			changed = append(changed, "title")
		}
		if desc != nil && *desc != cur.Description {
			next.Description = *desc
			changed = append(changed, "description")
		}
		if def != nil {
			a, _ := cur.Definition.Marshal()
			b, _ := def.Marshal()
			if string(a) != string(b) {
				next.Definition = *def
				changed = append(changed, "definition")
			}
		}
		if len(changed) == 0 {
			return Change{NoChange: true, Next: cur}, nil
		}
		return Change{Next: next, Action: "catalog.item.updated", Metadata: map[string]any{"changedFields": changed}}, nil
	})
}

// SetActive activates or deactivates an item. Requires catalog.manage.
func (s *Service) SetActive(ctx context.Context, c Caller, p Principal, id string, expected *int, active bool) (Item, error) {
	if err := c.validate(); err != nil {
		return Item{}, err
	}
	if !p.Manage {
		return Item{}, ErrForbidden
	}
	return s.store.Change(ctx, c, id, func(cur Item) (Change, error) {
		if expected != nil && *expected != cur.Version {
			return Change{}, ErrVersionConflict
		}
		if cur.Active == active {
			return Change{NoChange: true, Next: cur}, nil
		}
		next := cur
		next.Active = active
		action := "catalog.item.deactivated"
		if active {
			action = "catalog.item.activated"
		}
		return Change{Next: next, Action: action}, nil
	})
}

// List returns items; callers without catalog.manage see active items only.
func (s *Service) List(ctx context.Context, p Principal, status string, page Page) (Result, error) {
	if status != "" && status != "active" && status != "inactive" {
		return Result{}, invalid("status must be active or inactive")
	}
	q := ListQuery{Page: page.Normalize()}
	if p.Manage {
		q.Status = status
	} else {
		q.ActiveOnly = true
	}
	return s.store.List(ctx, q)
}

// Get returns an item; inactive items are visible to managers only.
func (s *Service) Get(ctx context.Context, p Principal, id string) (Item, error) {
	it, err := s.store.Get(ctx, id)
	if err != nil {
		return Item{}, err
	}
	if !it.Active && !p.Manage {
		return Item{}, ErrNotFound
	}
	return it, nil
}

// FormField is a form field with its resolved product options.
type FormField struct {
	Field
	ProductOptions []ProductChoice
}

// Form is what an employee needs to fill in the request form; approval steps
// and fulfillment details stay out of it.
type Form struct {
	Item   Item
	Fields []FormField
}

// Form returns the form of an item, resolving product choices (names of the
// listed products, or the active products of the category).
func (s *Service) Form(ctx context.Context, p Principal, id string) (Form, error) {
	it, err := s.Get(ctx, p, id)
	if err != nil {
		return Form{}, err
	}
	f := Form{Item: it, Fields: make([]FormField, 0, len(it.Definition.Fields))}
	for _, fld := range it.Definition.Fields {
		ff := FormField{Field: fld}
		if fld.Type == FieldProduct {
			if fld.CategoryID != "" {
				ff.ProductOptions, err = s.products.ActiveInCategory(ctx, fld.CategoryID, MaxProductChoices)
				if err != nil {
					return Form{}, fmt.Errorf("resolve product options: %w", err)
				}
			} else {
				active, err := s.products.ActiveProducts(ctx, fld.ProductIDs)
				if err != nil {
					return Form{}, fmt.Errorf("resolve product options: %w", err)
				}
				names, err := s.products.ProductNames(ctx, fld.ProductIDs)
				if err != nil {
					return Form{}, fmt.Errorf("resolve product options: %w", err)
				}
				for _, id := range fld.ProductIDs {
					if _, ok := active[id]; ok {
						ff.ProductOptions = append(ff.ProductOptions, ProductChoice{ID: id, Name: names[id]})
					}
				}
			}
		}
		f.Fields = append(f.Fields, ff)
	}
	return f, nil
}
