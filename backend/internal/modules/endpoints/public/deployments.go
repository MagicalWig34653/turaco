package public

import (
	"context"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Deployment read contract for the IT Briefing (F9 G4). It returns counts and references; names appear only when the
// caller was authorized for deployments.view and sets IncludeNames. No target, device or provider data crosses it.

// DeploymentScope selects the detail of the answer.
type DeploymentScope struct {
	// IncludeNames adds the Deployment and product names (caller holds deployments.view).
	IncludeNames bool
	Limit        int
}

// RolloutAttention kinds.
const (
	RolloutRingHalted         = "ring_halted"
	RolloutPaused             = "paused"
	RolloutAwaitingPromotion  = "awaiting_promotion"
	maxRolloutAttentionLimit  = 50
	defaultRolloutAttentionOf = 20
)

type RolloutItem struct {
	ID        string
	Reference string
	Kind      string
	Since     time.Time
	// Name and ProductName are empty unless IncludeNames.
	Name        string
	ProductName string
}

type RolloutSummary struct {
	Items      []RolloutItem
	More       bool
	InProgress int
}

// EngineStatus is the health of the Deployment execution engine.
type EngineStatus struct {
	LastTickAt     *time.Time
	Active         int
	ClearPending   int
	ResolvingStuck int
	// Stale is true when Deployments need ticks and the latest tick is older than EngineStaleAfter.
	Stale bool
}

// EngineStaleAfter is how old the latest tick may be while Deployments run (the tick runs every minute).
const EngineStaleAfter = 5 * time.Minute

// RolloutSummary returns the rollouts that need attention (halted ring, paused, awaiting promotion) and the number of
// rollouts in progress.
func (h *Health) RolloutSummary(ctx context.Context, scope DeploymentScope) (RolloutSummary, error) {
	limit := scope.Limit
	if limit < 1 || limit > maxRolloutAttentionLimit {
		limit = defaultRolloutAttentionOf
	}
	o, err := h.repo.RolloutOverview(ctx, limit)
	if err != nil {
		return RolloutSummary{}, err
	}
	out := RolloutSummary{More: o.More, InProgress: o.InProgress, Items: make([]RolloutItem, 0, len(o.Attention))}
	for _, a := range o.Attention {
		it := RolloutItem{ID: a.ID, Reference: a.Reference, Kind: a.Kind, Since: a.Since}
		if scope.IncludeNames {
			it.Name, it.ProductName = a.Name, a.ProductName
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

// DeploymentEngineStatus reports the engine health: the age of the last tick and the counts that the progress read also
// shows (clearPending, resolvingStuck).
func (h *Health) DeploymentEngineStatus(ctx context.Context) (EngineStatus, error) {
	e, err := h.repo.EngineHealth(ctx, application.ResolvingStuckAfter)
	if err != nil {
		return EngineStatus{}, err
	}
	st := EngineStatus{LastTickAt: e.LastTickAt, Active: e.Active, ClearPending: e.ClearPending, ResolvingStuck: e.ResolvingStuck}
	st.Stale = e.Active > 0 && (e.LastTickAt == nil || time.Since(*e.LastTickAt) > EngineStaleAfter)
	return st, nil
}
