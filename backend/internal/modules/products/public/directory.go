// Package public is the Products module's contract for other modules.
package public

import (
	"context"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/products/application"
)

// Product is the minimal view other modules get.
type Product struct {
	ID         string
	Name       string
	Active     bool
	CategoryID *string
	// Serialized products become one Asset per unit; StockManaged and AssetManaged say how a unit is tracked.
	Serialized   bool
	StockManaged bool
	AssetManaged bool
}

// Reader loads products by id (implemented by the repository).
type Reader interface {
	ProductsByIDs(ctx context.Context, ids []string) ([]application.Product, error)
	ListProducts(ctx context.Context, f application.ProductFilter) (application.Result[application.Product], error)
	CategoriesByIDs(ctx context.Context, ids []string) (map[string]bool, error)
}

// Directory answers product questions for catalog and request code without
// exposing Products storage.
type Directory struct{ r Reader }

func NewDirectory(r Reader) *Directory { return &Directory{r: r} }

// Products returns id -> Product for existing products; unknown or malformed
// ids are absent.
func (d *Directory) Products(ctx context.Context, ids []string) (map[string]Product, error) {
	found, err := d.r.ProductsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Product, len(found))
	for _, p := range found {
		byID[p.ID] = Product{ID: p.ID, Name: p.Name, Active: p.Active, CategoryID: p.CategoryID,
			Serialized: p.Serialized, StockManaged: p.StockManaged, AssetManaged: p.AssetManaged}
	}
	// Callers look products up by the id they passed in, whatever its letter case.
	out := make(map[string]Product, len(ids))
	for _, id := range ids {
		if p, ok := byID[strings.ToLower(id)]; ok {
			out[id] = p
		}
	}
	return out, nil
}

// ActiveInCategory returns up to limit active products of a category (exactly
// that category, not its subcategories), ordered by id.
func (d *Directory) ActiveInCategory(ctx context.Context, categoryID string, limit int) ([]Product, error) {
	active := true
	res, err := d.r.ListProducts(ctx, application.ProductFilter{CategoryID: categoryID, Active: &active, Page: application.Page{Limit: limit}})
	if err != nil {
		return nil, err
	}
	out := make([]Product, 0, len(res.Items))
	for _, p := range res.Items {
		out = append(out, Product{ID: p.ID, Name: p.Name, Active: p.Active, CategoryID: p.CategoryID})
	}
	return out, nil
}

// CategoriesExist returns id -> true for existing product categories among ids.
func (d *Directory) CategoriesExist(ctx context.Context, ids []string) (map[string]bool, error) {
	return d.r.CategoriesByIDs(ctx, ids)
}

// Searcher is the optional Reader capability behind Directory.Search.
type Searcher interface {
	SearchProductIDs(ctx context.Context, text string, limit int) ([]string, error)
}

// Search returns the ids of products whose name, manufacturer or part number matches the text (at most limit).
// Readers without the capability find nothing.
func (d *Directory) Search(ctx context.Context, text string, limit int) ([]string, error) {
	s, ok := d.r.(Searcher)
	if !ok {
		return []string{}, nil
	}
	return s.SearchProductIDs(ctx, text, limit)
}
