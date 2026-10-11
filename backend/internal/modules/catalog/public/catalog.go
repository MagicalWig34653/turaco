// Package public is the Catalog module's contract for other modules (the
// requests module): load an item for submission and validate answers against
// its definition, without touching Catalog storage.
package public

import (
	"context"
	"encoding/json"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
)

type Locations struct{ dir *orgpublic.WorkDirectory }

func NewLocations(dir *orgpublic.WorkDirectory) *Locations { return &Locations{dir: dir} }

func (l *Locations) SearchActiveLocations(ctx context.Context, query string, limit int) ([]application.LocationChoice, error) {
	got, err := l.dir.SearchActiveLocations(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]application.LocationChoice, 0, len(got))
	for _, item := range got {
		out = append(out, application.LocationChoice{ID: item.ID, Name: item.Name})
	}
	return out, nil
}

// Definition and its parts are the schema types; requests keep a JSON
// snapshot of a definition and decode it again with ParseSnapshot.
type (
	Definition     = application.Definition
	Field          = application.Field
	ApprovalStep   = application.ApprovalStep
	TaskTemplate   = application.TaskTemplate
	Reference      = application.Reference
	FieldErrors    = application.FieldErrors
	InvalidInput   = application.InvalidInputError
	UserLookup     = application.UserLookup
	ProductLookup  = application.ProductLookup
	LocationLookup = application.LocationLookup
	AssetLookup    = application.AssetLookup
	AssetChoice    = application.AssetChoice
)

// ErrNotFound is returned for an unknown catalog item.
var ErrNotFound = application.ErrNotFound

// Submission is what the requests module needs to submit a request.
type Submission struct {
	ID         string
	Key        string
	Title      string
	Active     bool
	Version    int
	Definition Definition
	// Snapshot is the canonical JSON of the definition, stored with the request.
	Snapshot []byte
}

// Reader loads items (implemented by the repository).
type Reader interface {
	Get(ctx context.Context, id string) (application.Item, error)
}

// Catalog is the module's public service.
type Catalog struct{ r Reader }

func New(r Reader) *Catalog { return &Catalog{r: r} }

// ForSubmission loads an item. The caller must refuse inactive items.
func (c *Catalog) ForSubmission(ctx context.Context, id string) (Submission, error) {
	it, err := c.r.Get(ctx, id)
	if err != nil {
		return Submission{}, err
	}
	snap, err := it.Definition.Marshal()
	if err != nil {
		return Submission{}, err
	}
	return Submission{ID: it.ID, Key: it.Key, Title: it.Title, Active: it.Active, Version: it.Version, Definition: it.Definition, Snapshot: snap}, nil
}

// ParseSnapshot decodes a stored definition snapshot.
func ParseSnapshot(raw []byte) (Definition, error) {
	var d Definition
	if err := json.Unmarshal(raw, &d); err != nil {
		return Definition{}, err
	}
	return d, nil
}

// ValidateAnswers checks answers against a definition (see
// application.ValidateAnswers) and returns the normalized answers and the
// typed references.
func ValidateAnswers(ctx context.Context, d Definition, answers map[string]any, users UserLookup, products ProductLookup) (map[string]any, []Reference, error) {
	return application.ValidateAnswers(ctx, d, answers, users, products)
}

func ValidateRequestAnswers(ctx context.Context, d Definition, answers map[string]any, users UserLookup, products ProductLookup, locations LocationLookup, assets AssetLookup, requesterID string, viewAssets bool) (map[string]any, []Reference, error) {
	return application.ValidateRequestAnswers(ctx, d, answers, users, products, locations, assets, requesterID, viewAssets)
}
