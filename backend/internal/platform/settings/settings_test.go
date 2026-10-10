package settings

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func TestNormalize(t *testing.T) {
	dur, _ := lookup(KeyAuthSessionAbsoluteTimeout)
	enum, _ := lookup(KeyAuthEntraSignoutMode)
	b, _ := lookup(KeyTeamsPersonalEnabled)
	cases := []struct {
		d    Definition
		raw  string
		want any
		ok   bool
	}{
		{dur, `28800`, int64(28800), true},
		{dur, `3599`, nil, false},
		{dur, `86401`, nil, false},
		{dur, `"8h"`, nil, false},
		{dur, `1.5`, nil, false},
		{enum, `"always"`, "always", true},
		{enum, `"sometimes"`, nil, false},
		{enum, `true`, nil, false},
		{b, `false`, false, true},
		{b, `"true"`, nil, false},
		{b, `true false`, nil, false},
	}
	for _, c := range cases {
		got, err := normalize(c.d, json.RawMessage(c.raw))
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("%s %s: got %v, %v", c.d.Key, c.raw, got, err)
		}
	}
}

func lookup(key string) (Definition, bool) {
	for _, d := range Definitions() {
		if d.Key == key {
			return d, true
		}
	}
	return Definition{}, false
}

func TestDefinitionsAreConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range Definitions() {
		if seen[d.Key] {
			t.Fatalf("duplicate key %s", d.Key)
		}
		seen[d.Key] = true
		raw, _ := json.Marshal(d.Default)
		if _, err := normalize(d, raw); err != nil {
			t.Errorf("%s: default invalid: %v", d.Key, err)
		}
	}
}

func TestSetAndRead(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var uid string
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.settings`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_type = 'setting' AND actor_id = $1::uuid`, uid)
	}
	clean()
	t.Cleanup(clean)
	svc := New(pool, time.Hour)
	v0, v1, v2 := 0, 1, 2

	if got := svc.Duration(ctx, KeyAuthSessionAbsoluteTimeout); got != 8*time.Hour {
		t.Fatalf("default = %v", got)
	}
	if _, ok := svc.StoredDuration(ctx, KeyAuthSessionAbsoluteTimeout); ok {
		t.Fatal("nothing stored yet")
	}
	raw := json.RawMessage(`7200`)
	if _, err := svc.Set(ctx, uid, false, KeyAuthSessionAbsoluteTimeout, raw, &v0, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin: %v", err)
	}
	if _, err := svc.Set(ctx, uid, true, KeyAuthSessionAbsoluteTimeout, raw, nil, ""); !errors.Is(err, ErrVersionRequired) {
		t.Fatalf("missing version: %v", err)
	}
	if _, err := svc.Set(ctx, uid, true, "nope", raw, &v0, ""); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := svc.Set(ctx, uid, true, KeyAuthSessionAbsoluteTimeout, json.RawMessage(`60`), &v0, ""); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("bounds: %v", err)
	}
	if _, err := svc.Set(ctx, uid, true, KeyAuthSessionAbsoluteTimeout, raw, &v1, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale create: %v", err)
	}
	got, err := svc.Set(ctx, uid, true, KeyAuthSessionAbsoluteTimeout, raw, &v0, "")
	if err != nil || got.Version != 1 || !got.Stored {
		t.Fatalf("create: %+v %v", got, err)
	}
	if d, ok := svc.StoredDuration(ctx, KeyAuthSessionAbsoluteTimeout); !ok || d != 2*time.Hour {
		t.Fatalf("read after set = %v %v", d, ok)
	}
	if _, err := svc.Set(ctx, uid, true, KeyAuthSessionAbsoluteTimeout, json.RawMessage(`3600`), &v0, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	got, err = svc.Set(ctx, uid, true, KeyAuthSessionAbsoluteTimeout, json.RawMessage(`3600`), &v1, "")
	if err != nil || got.Version != 2 {
		t.Fatalf("update: %+v %v", got, err)
	}
	_ = v2
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'platform.settings.changed' AND target_id = $1 AND actor_id = $2::uuid`,
		KeyAuthSessionAbsoluteTimeout, uid).Scan(&n); err != nil || n != 2 {
		t.Fatalf("audit events = %d, %v", n, err)
	}
	list := svc.List(ctx)
	if len(list) != len(Definitions()) || list[0].Version != 2 {
		t.Fatalf("list: %+v", list[0])
	}
}
