package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func TestAuditResolversNameTicketsQueuesAssetsAndRoles(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	one := func(query string, args ...any) string {
		t.Helper()
		var v string
		if err := pool.QueryRow(ctx, query, args...).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	suffix := one(`SELECT substr(uuidv7()::text, 25, 8)`)
	user := one(`SELECT uuidv7()::text`)
	ticket := one(`INSERT INTO servicedesk.tickets(title,reporter_user_id,affected_user_id) VALUES ('Secret ticket title '||$1,$2::uuid,$2::uuid) RETURNING id::text`, suffix, user)
	number := one(`SELECT reference FROM servicedesk.tickets WHERE id=$1::uuid`, ticket)
	queue := one(`INSERT INTO servicedesk.queues(key,prefix,name) VALUES ('aud-'||$1,'Z'||upper(substr($1,1,6)),'Audit queue '||$1) RETURNING id::text`, suffix)
	taggedAsset := one(`INSERT INTO assets.assets(product_id,asset_tag,status) VALUES (uuidv7(),'TAG-'||$1,'available') RETURNING id::text`, suffix)
	plainAsset := one(`INSERT INTO assets.assets(product_id,status) VALUES (uuidv7(),'available') RETURNING id::text`)
	plainRef := one(`SELECT reference FROM assets.assets WHERE id=$1::uuid`, plainAsset)
	role := one(`INSERT INTO platform.roles(key,name) VALUES ('aud-'||$1,'Audit role '||$1) RETURNING id::text`, suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE id=$1::uuid`, ticket)
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.queues WHERE id=$1::uuid`, queue)
		_, _ = pool.Exec(ctx, `DELETE FROM assets.assets WHERE id = ANY($1::uuid[])`, []string{taggedAsset, plainAsset})
		_, _ = pool.Exec(ctx, `DELETE FROM platform.roles WHERE id=$1::uuid`, role)
	})

	missing := one(`SELECT uuidv7()::text`)
	events := []audit.Event{
		{TargetType: "ticket", TargetID: ticket},
		{TargetType: "ticket", TargetID: missing},
		{TargetType: "ticket", TargetID: "not-a-uuid"},
		{TargetType: "ticket_queue", TargetID: queue},
		{TargetType: "asset", TargetID: taggedAsset},
		{TargetType: "asset", TargetID: plainAsset},
		{TargetType: "role", TargetID: role},
	}
	res := AuditResolvers(pool).Resolve(ctx, events)
	if len(res.Unavailable) != 0 {
		t.Fatalf("unavailable = %v", res.Unavailable)
	}
	want := map[string]string{
		audit.TargetKey("ticket", ticket):      number,
		audit.TargetKey("ticket_queue", queue): "Audit queue " + suffix,
		audit.TargetKey("asset", taggedAsset):  "TAG-" + suffix,
		audit.TargetKey("asset", plainAsset):   plainRef,
		audit.TargetKey("role", role):          "Audit role " + suffix,
	}
	for k, v := range want {
		if got := res.Targets[k]; got.Text != v || got.Gone {
			t.Errorf("%s = %+v, want %q", k, got, v)
		}
	}
	if strings.Contains(res.Targets[audit.TargetKey("ticket", ticket)].Text, "Secret") {
		t.Error("ticket label leaks the title")
	}
	for _, id := range []string{missing, "not-a-uuid"} {
		if got := res.Targets[audit.TargetKey("ticket", id)]; !got.Gone || got.Text != "" {
			t.Errorf("ticket %s = %+v, want gone", id, got)
		}
	}
}
