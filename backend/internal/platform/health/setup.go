package health

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Item states. "done" is derived from real data and always wins over a stored skip.
const (
	ItemDone      = "done"
	ItemTodo      = "todo"
	ItemSkipped   = "skipped"
	ItemConfirmed = "confirmed"
)

var (
	// ErrForbidden means the caller may not change setup items (platform.admin).
	ErrForbidden = errors.New("health: not permitted")
	// ErrUnknownItem marks a key that is not a setup item.
	ErrUnknownItem = errors.New("health: unknown setup item")
	// ErrInvalid marks a malformed request (state, reason).
	ErrInvalid = errors.New("health: invalid request")
	// ErrConflict is a version mismatch.
	ErrConflict = errors.New("health: version conflict")
	// ErrNotConfirmable marks a confirm of an item that is derived from data.
	ErrNotConfirmable = errors.New("health: item cannot be confirmed")
)

// ItemDef describes one setup checklist item. Derive reports whether the item is done from stored facts.
type ItemDef struct {
	Key   string
	Order int
	// Route is the screen where the item is resolved.
	Route string
	// Module removes the item while that module is switched off ("" keeps it always).
	Module string
	// Confirmable items have no derivable "done": an administrator confirms them once (for example the module defaults).
	Confirmable bool
	Derive      func(ctx context.Context) (done bool, attention bool, err error)
}

// ItemView is one item as shown.
type ItemView struct {
	Key       string     `json:"key"`
	Order     int        `json:"order"`
	Route     string     `json:"route"`
	State     string     `json:"state"`
	Attention bool       `json:"attention,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	Version   int        `json:"version,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// Checklist is the whole checklist with its progress.
type Checklist struct {
	Items []ItemView `json:"items"`
	// Open is the number of items that are todo; Total counts all shown items.
	Open  int `json:"open"`
	Total int `json:"total"`
}

// Setup derives and stores the setup checklist.
type Setup struct {
	pool          *pgxpool.Pool
	items         []ItemDef
	moduleEnabled func(ctx context.Context, key string) bool
	now           func() time.Time
}

// NewSetup returns the checklist. moduleEnabled may be nil (every module counts as on).
func NewSetup(pool *pgxpool.Pool, items []ItemDef, moduleEnabled func(context.Context, string) bool) *Setup {
	cp := append([]ItemDef(nil), items...)
	sort.SliceStable(cp, func(i, j int) bool { return cp[i].Order < cp[j].Order })
	return &Setup{pool: pool, items: cp, moduleEnabled: moduleEnabled, now: time.Now}
}

type stored struct {
	state     string
	reason    string
	version   int
	updatedAt time.Time
}

func (s *Setup) storedItems(ctx context.Context) (map[string]stored, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, state, reason, version, updated_at FROM platform.setup_items`)
	if err != nil {
		return nil, fmt.Errorf("read setup items: %w", err)
	}
	defer rows.Close()
	out := map[string]stored{}
	for rows.Next() {
		var k string
		var st stored
		if err := rows.Scan(&k, &st.state, &st.reason, &st.version, &st.updatedAt); err != nil {
			return nil, fmt.Errorf("read setup items: %w", err)
		}
		out[k] = st
	}
	return out, rows.Err()
}

// List derives every item and merges the stored skips and confirmations. A failing derivation leaves the item
// "todo" with attention set rather than failing the page.
func (s *Setup) List(ctx context.Context) (Checklist, error) {
	st, err := s.storedItems(ctx)
	if err != nil {
		return Checklist{}, err
	}
	out := Checklist{Items: []ItemView{}}
	for _, d := range s.items {
		if d.Module != "" && s.moduleEnabled != nil && !s.moduleEnabled(ctx, d.Module) {
			continue
		}
		v := ItemView{Key: d.Key, Order: d.Order, Route: d.Route, State: ItemTodo}
		done := false
		if d.Derive != nil {
			var attention bool
			var derr error
			done, attention, derr = d.Derive(ctx)
			if derr != nil {
				v.Attention = true
			} else {
				v.Attention = attention
			}
		}
		rec, has := st[d.Key]
		switch {
		case done:
			v.State = ItemDone
		case has:
			v.State, v.Reason, v.Version = rec.state, rec.reason, rec.version
			t := rec.updatedAt
			v.UpdatedAt = &t
		}
		if has {
			v.Version = rec.version
		}
		out.Items = append(out.Items, v)
		out.Total++
		if v.State == ItemTodo {
			out.Open++
		}
	}
	return out, nil
}

// Put sets an item to skipped or confirmed, or clears it. It needs platform.admin (canAdmin), is audited and uses
// optimistic versioning for existing rows. Items derived from data cannot be confirmed.
func (s *Setup) Put(ctx context.Context, userID string, canAdmin bool, key, state, reason string, expectedVersion *int, correlationID string) (ItemView, error) {
	if userID == "" || !canAdmin {
		return ItemView{}, ErrForbidden
	}
	var def *ItemDef
	for i := range s.items {
		if s.items[i].Key == key {
			def = &s.items[i]
		}
	}
	if def == nil {
		return ItemView{}, ErrUnknownItem
	}
	switch state {
	case ItemSkipped, ItemConfirmed, "cleared":
	default:
		return ItemView{}, ErrInvalid
	}
	if utf8.RuneCountInString(reason) > 500 {
		return ItemView{}, ErrInvalid
	}
	if state == ItemSkipped && reason == "" {
		return ItemView{}, ErrInvalid
	}
	if state == ItemConfirmed && !def.Confirmable {
		return ItemView{}, ErrNotConfirmable
	}
	if correlationID == "" {
		correlationID = "setup-" + key
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur int
		var prev string
		err := tx.QueryRow(ctx, `SELECT version, state FROM platform.setup_items WHERE key = $1 FOR UPDATE`, key).Scan(&cur, &prev)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if exists && (expectedVersion == nil || *expectedVersion != cur) {
			return ErrConflict
		}
		if !exists && expectedVersion != nil && *expectedVersion != 0 {
			return ErrConflict
		}
		action := "platform.setup.item_" + state
		switch state {
		case "cleared":
			if !exists {
				return nil
			}
			if _, err := tx.Exec(ctx, `DELETE FROM platform.setup_items WHERE key = $1`, key); err != nil {
				return err
			}
		case ItemSkipped, ItemConfirmed:
			if exists {
				if _, err := tx.Exec(ctx, `UPDATE platform.setup_items SET state = $2, reason = $3, updated_by = $4::uuid, updated_at = now(), version = version + 1 WHERE key = $1`,
					key, state, reason, userID); err != nil {
					return err
				}
			} else if _, err := tx.Exec(ctx, `INSERT INTO platform.setup_items (key, state, reason, updated_by) VALUES ($1, $2, $3, $4::uuid)`, key, state, reason, userID); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "setup_item", TargetID: key, Actor: audit.UserActor(userID), CorrelationID: correlationID,
			Before: map[string]any{"state": prev}, After: map[string]any{"state": state}})
	})
	if err != nil {
		return ItemView{}, err
	}
	list, err := s.List(ctx)
	if err != nil {
		return ItemView{}, err
	}
	for _, v := range list.Items {
		if v.Key == key {
			return v, nil
		}
	}
	return ItemView{Key: key, State: ItemTodo}, nil
}
