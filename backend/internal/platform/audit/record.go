package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Actor identifies who performed an audited action. Exactly one of UserID
// (a signed-in User) or System (a non-human actor such as "cli" or
// "directory-sync") is set. OSUser is informational for the CLI only.
//
// Stored form: actor_id = UserID (NULL for system actors) and, for system
// actors, metadata {"actor": System, "osUser": OSUser}.
type Actor struct {
	UserID string
	System string
	OSUser string
}

// UserActor returns the actor for a signed-in User.
func UserActor(userID string) Actor { return Actor{UserID: userID} }

// SystemActor returns a non-human actor, for example "directory-sync".
func SystemActor(system string) Actor { return Actor{System: system} }

// CLIActor returns the actor of a turaco-admin invocation.
func CLIActor(osUser string) Actor { return Actor{System: "cli", OSUser: osUser} }

// Validate reports whether exactly one of UserID or System is set.
func (a Actor) Validate() error {
	if (a.UserID == "") == (a.System == "") {
		return errors.New("audit: actor needs exactly one of user id or system")
	}
	return nil
}

// Change is one audited mutation to record. Before, After and Metadata are
// marshalled to JSON; nil means absent. Metadata must not contain secrets.
type Change struct {
	Action        string
	TargetType    string
	TargetID      string
	Actor         Actor
	CorrelationID string
	Before        any
	After         any
	Metadata      map[string]any
	// OccurredAt defaults to the current time (microsecond precision).
	OccurredAt time.Time
}

// Record writes e inside tx, generating its UUIDv7 id. Every audited
// mutation must call it in the same transaction as the change.
func Record(ctx context.Context, tx pgx.Tx, e Change) error {
	if e.Action == "" || e.TargetType == "" || e.TargetID == "" || e.CorrelationID == "" {
		return errors.New("audit: action, target and correlation id are required")
	}
	if err := e.Actor.Validate(); err != nil {
		return err
	}
	meta := map[string]any{}
	for k, v := range e.Metadata {
		meta[k] = v
	}
	var actorID *string
	if e.Actor.UserID != "" {
		id := e.Actor.UserID
		actorID = &id
	} else {
		meta["actor"] = e.Actor.System
		if e.Actor.OSUser != "" {
			meta["osUser"] = e.Actor.OSUser
		}
	}
	entry := Entry{
		OccurredAt: e.OccurredAt, ActorID: actorID, Action: e.Action,
		TargetType: e.TargetType, TargetID: e.TargetID, CorrelationID: e.CorrelationID,
	}
	if entry.OccurredAt.IsZero() {
		entry.OccurredAt = time.Now()
	}
	entry.OccurredAt = entry.OccurredAt.UTC().Truncate(time.Microsecond)
	var err error
	if entry.Metadata, err = json.Marshal(meta); err != nil {
		return fmt.Errorf("audit: marshal metadata: %w", err)
	}
	if entry.Before, err = marshalOptional(e.Before); err != nil {
		return err
	}
	if entry.After, err = marshalOptional(e.After); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&entry.ID); err != nil {
		return fmt.Errorf("audit: generate id: %w", err)
	}
	return Insert(ctx, tx, entry)
}

func marshalOptional(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal state: %w", err)
	}
	return raw, nil
}
