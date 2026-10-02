// Package public is the Products module's contract for other modules.
package public

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/products/application"
)

// Product is the minimal view other modules get.
type Product struct {
	ID     string
	Name   string
	Active bool
}

// Reader loads products by id (implemented by the repository).
type Reader interface {
	GetProduct(ctx context.Context, id string) (application.Product, error)
	ListProducts(ctx context.Context, f application.ProductFilter) (application.Result[application.Product], error)
}

// Directory answers product questions for catalog and request code without
// exposing Products storage.
type Directory struct{ r Reader }

func NewDirectory(r Reader) *Directory { return &Directory{r: r} }

// Products returns id -> Product for existing products; unknown or malformed
// ids are absent.
func (d *Directory) Products(ctx context.Context, ids []string) (map[string]Product, error) {
	out := map[string]Product{}
	for _, id := range ids {
		if _, seen := out[id]; seen {
			continue
		}
		p, err := d.r.GetProduct(ctx, id)
		if errors.Is(err, application.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = Product{ID: p.ID, Name: p.Name, Active: p.Active}
	}
	return out, nil
}
