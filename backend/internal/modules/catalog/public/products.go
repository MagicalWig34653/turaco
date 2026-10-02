package public

import (
	"context"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/application"
	productspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/products/public"
)

// NewProducts adapts the Products public directory to what the catalog needs
// (form product options, reference checks, answer validation).
func NewProducts(dir *productspublic.Directory) *Products { return &Products{dir: dir} }

// Products implements application.Products and application.ProductLookup.
type Products struct{ dir *productspublic.Directory }

var (
	_ application.Products      = (*Products)(nil)
	_ application.ProductLookup = (*Products)(nil)
)

func (p *Products) ActiveProducts(ctx context.Context, ids []string) (map[string]string, error) {
	found, err := p.dir.Products(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for id, pr := range found {
		if pr.Active {
			cat := ""
			if pr.CategoryID != nil {
				cat = *pr.CategoryID
			}
			out[id] = cat
		}
	}
	return out, nil
}

func (p *Products) ProductNames(ctx context.Context, ids []string) (map[string]string, error) {
	found, err := p.dir.Products(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for id, pr := range found {
		out[id] = pr.Name
	}
	return out, nil
}

func (p *Products) CategoriesExist(ctx context.Context, ids []string) (map[string]bool, error) {
	return p.dir.CategoriesExist(ctx, ids)
}

func (p *Products) ActiveInCategory(ctx context.Context, categoryID string, limit int) ([]application.ProductChoice, error) {
	list, err := p.dir.ActiveInCategory(ctx, categoryID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]application.ProductChoice, 0, len(list))
	for _, pr := range list {
		out = append(out, application.ProductChoice{ID: pr.ID, Name: pr.Name})
	}
	return out, nil
}
