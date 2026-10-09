package views

import (
	"context"
	"fmt"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Embedding contract for modules that build on a Saved View (Task Boards, F13 slice Q-D). The module keeps its own
// presentation data next to the View; name, definition and sharing stay in the View, so there is one sharing
// mechanism and one access rule. A module never reads views tables: it asks the Service.

// Resolved is a View together with what the caller may do with it.
type Resolved struct {
	View
	// Access is AccessOwner, AccessEdit, AccessUse or AccessAdmin.
	Access    string
	OwnerName string
}

// CanRun reports whether the caller may use the View's data (owner, editor or user; an administrator without a
// share may not).
func (r Resolved) CanRun() bool { return canRun(r.Access) }

// CanEdit reports whether the caller may change the View's definition (owner or editor).
func (r Resolved) CanEdit() bool { return r.Access == AccessOwner || r.Access == AccessEdit }

// Resolve loads a View with the caller's access. Every case the caller may not know about (unknown, private to
// someone else, archived for non-owners, unreadable resource, deactivated owner) is ErrNotFound. The owning module
// must be on (ErrModuleDisabled).
func (s *Service) Resolve(ctx context.Context, c Caller, id string) (Resolved, error) {
	v, acc, _, err := s.loadAccessible(ctx, c, id)
	if err != nil {
		return Resolved{}, err
	}
	res, ok := s.res.canUse(c, v.Resource)
	if !ok {
		return Resolved{}, ErrNotFound
	}
	if err := s.moduleOn(ctx, res); err != nil {
		return Resolved{}, err
	}
	names, err := s.dir.UserNames(ctx, []string{v.OwnerID})
	if err != nil {
		return Resolved{}, fmt.Errorf("resolve owner name: %w", err)
	}
	return Resolved{View: v, Access: acc, OwnerName: names[v.OwnerID]}, nil
}

// ResolveMany returns the active (not archived) Views among ids that the caller may run, keyed by id. Views the
// caller may not know about are simply absent.
func (s *Service) ResolveMany(ctx context.Context, c Caller, ids []string) (map[string]Resolved, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if isUUID(id) {
			valid = append(valid, id)
		}
	}
	vw, err := s.viewerOf(ctx, c)
	if err != nil {
		return nil, err
	}
	batch, err := loadAccessBatch(ctx, s.pool, valid, vw)
	if err != nil {
		return nil, err
	}
	ownerSet := map[string]bool{}
	for _, a := range batch {
		ownerSet[a.view.OwnerID] = true
	}
	owners := make([]string, 0, len(ownerSet))
	for o := range ownerSet {
		owners = append(owners, o)
	}
	active, err := s.dir.ActiveUsers(ctx, owners)
	if err != nil {
		return nil, fmt.Errorf("check owners: %w", err)
	}
	names, err := s.dir.UserNames(ctx, owners)
	if err != nil {
		return nil, fmt.Errorf("resolve owner names: %w", err)
	}
	out := make(map[string]Resolved, len(batch))
	for id, a := range batch {
		if _, ok := s.res.canUse(c, a.view.Resource); !ok {
			continue
		}
		if a.level != AccessOwner && !active[a.view.OwnerID] {
			continue
		}
		out[id] = Resolved{View: a.view, Access: a.level, OwnerName: names[a.view.OwnerID]}
	}
	return out, nil
}

// Degrade rewrites a stored filter for the catalog the viewer may use: a condition the viewer cannot use becomes
// "no rows" and is reported, never dropped, so a lost permission only narrows a result. empty is true when the
// whole filter can match nothing.
func Degrade(f query.Filter, info query.Info) (out query.Filter, warnings []query.Warning, empty bool) {
	return degrade(f, info)
}

// PinAnnotation says what a pinned View stands for beyond a plain list.
type PinAnnotation struct {
	Kind string
	Ref  string
}

// PinAnnotator lets a module mark the pinned Views that are its own objects (a Task Board).
type PinAnnotator interface {
	// AnnotatePins returns the annotations of the given View ids that the module recognizes.
	AnnotatePins(ctx context.Context, viewIDs []string) (map[string]PinAnnotation, error)
}

// WithPinAnnotator sets the annotator of the sidebar and pin entries.
func (s *Service) WithPinAnnotator(a PinAnnotator) *Service {
	s.annotator = a
	return s
}
