package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

type testRemover struct{}

func (testRemover) DeleteLocalCredential(ctx context.Context, tx pgx.Tx, userID string) (int, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM platform.local_credentials WHERE user_id = $1::uuid AND kind = 'local'`, userID)
	return int(tag.RowsAffected()), err
}

// batchCaller is a caller holding exactly perms (platformAdmin: also platform.admin).
func (f *peopleFix) batchCaller(userID string, platformAdmin bool, perms ...string) application.Caller {
	set := map[string]bool{}
	for _, p := range perms {
		set[p] = true
	}
	c := f.caller(userID)
	c.PlatformAdmin = platformAdmin
	c.Has = func(p string) bool { return set[p] }
	f.t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM organization.import_batches WHERE created_by = $1::uuid`, userID)
	})
	return c
}

func (f *peopleFix) department(code string) application.Department {
	f.t.Helper()
	d, err := f.repo.CreateDepartment(context.Background(), f.caller(""), application.NewDepartmentInput{Name: f.pfx + " " + code, Code: &code})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.pool.Exec(ctx, `UPDATE organization.users SET department_id = NULL WHERE department_id = $1`, d.ID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.departments WHERE id = $1`, d.ID)
	})
	return d
}

// cleanupImported removes users created by an import (matched by e-mail prefix).
func (f *peopleFix) cleanupImported() {
	f.t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.pool.Exec(ctx, `UPDATE organization.users SET manager_user_id = NULL WHERE manager_user_id IN (SELECT id FROM organization.users WHERE primary_email LIKE $1 || '%')`, f.pfx)
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.users WHERE primary_email LIKE $1 || '%'`, f.pfx)
	})
}

func (f *peopleFix) previewCSV(c application.Caller, kind, key, mode, csv string) application.BatchPreview {
	f.t.Helper()
	parsed, err := application.ParseImport(kind, key, mode, []byte(csv))
	if err != nil {
		f.t.Fatal(err)
	}
	p, err := f.repo.PreviewImport(context.Background(), c, parsed)
	if err != nil {
		f.t.Fatalf("preview: %v", err)
	}
	return p
}

func rowByNo(rows []application.BatchRow, no int) application.BatchRow {
	for _, r := range rows {
		if r.No == no {
			return r
		}
	}
	return application.BatchRow{}
}

func hasCode(issues []application.RowIssue, code string) bool {
	for _, i := range issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

func (f *peopleFix) userCount(emailPrefix string) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM organization.users WHERE primary_email LIKE $1 || '%'`, emailPrefix).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestImportUsersPreviewAndApply(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	f.cleanupImported()
	admin := f.admin("Importer")
	c := f.batchCaller(admin.ID, true, application.PermUsersManage, application.PermImport)
	dept := f.department(f.pfx + "-IT")
	existing := f.local("Existing")
	dirEmail := f.pfx + "-dir@example.test"
	dir := f.user(f.pfx+" Directory", "active")
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET primary_email = $2 WHERE id = $1`, dir, dirEmail); err != nil {
		t.Fatal(err)
	}
	newEmail := f.pfx + "-new1@example.test"

	csv := "display_name,primary_email,department_code\n" +
		"New One," + newEmail + "," + *dept.Code + "\n" +
		"Renamed Existing," + *existing.PrimaryEmail + "," + *dept.Code + "\n" +
		"Directory Renamed," + dirEmail + ",\n" +
		"Bad Dept," + f.pfx + "-bad@example.test,NOPE\n"
	p := f.previewCSV(c, application.ImportUsers, application.KeyPrimaryEmail, application.ModeUpsert, csv)
	if got := p.Batch.Counts; got["create"] != 1 || got["update"] != 1 || got["reject"] != 2 || got["unchanged"] != 0 {
		t.Fatalf("counts = %v", got)
	}
	if f.userCount(newEmail) != 0 {
		t.Fatal("a preview must not write")
	}
	if r := rowByNo(p.Rows, 3); r.Action != "reject" || !hasCode(r.Errors, "directory_owned") {
		t.Errorf("directory-owned attribute must be a row-level error: %+v", r)
	}
	if r := rowByNo(p.Rows, 4); !hasCode(r.Errors, "reference_not_found") {
		t.Errorf("unknown department code: %+v", r)
	}
	if r := rowByNo(p.Rows, 2); r.Diff["display_name"].To == nil || *r.Diff["display_name"].To != "Renamed Existing" {
		t.Errorf("update diff = %+v", r.Diff)
	}

	// The preview belongs to its creator; permissions are checked again at apply.
	other := f.local("Other", "organization.users.manage")
	if _, err := f.repo.GetBatch(ctx, f.batchCaller(other.ID, false, application.PermUsersManage, application.PermImport), p.Batch.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("foreign batch: %v", err)
	}
	if _, _, err := f.repo.ListBatchRows(ctx, f.batchCaller(other.ID, false, application.PermUsersManage, application.PermImport), p.Batch.ID, application.RowFilter{}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("foreign rows: %v", err)
	}
	if _, err := f.repo.ApplyBatch(ctx, f.batchCaller(other.ID, false, application.PermUsersManage, application.PermImport), p.Batch.ID, p.Batch.PreviewHash, 2); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("foreign apply: %v", err)
	}
	noImport := f.batchCaller(admin.ID, true, application.PermUsersManage)
	if _, err := f.repo.ApplyBatch(ctx, noImport, p.Batch.ID, p.Batch.PreviewHash, 2); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("apply without organization.import: %v", err)
	}
	if _, err := f.repo.PreviewImport(ctx, noImport, application.ParsedImport{Kind: application.ImportUsers}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("preview without organization.import: %v", err)
	}
	if _, err := f.repo.ApplyBatch(ctx, c, p.Batch.ID, strings.Repeat("0", 64), 2); !errors.Is(err, application.ErrImportHashMismatch) {
		t.Errorf("wrong hash: %v", err)
	}
	if _, err := f.repo.ApplyBatch(ctx, c, p.Batch.ID, p.Batch.PreviewHash, 0); !errors.Is(err, application.ErrImportHashMismatch) {
		t.Errorf("wrong expectedRejects: %v", err)
	}
	if f.userCount(newEmail) != 0 {
		t.Fatal("refused applies must not write")
	}

	res, err := f.repo.ApplyBatch(ctx, c, p.Batch.ID, p.Batch.PreviewHash, 2)
	if err != nil || res.Replayed || res.Batch.Status != "applied" || res.Batch.AppliedCounts["create"] != 1 {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	if f.userCount(newEmail) != 1 {
		t.Fatal("the created user is missing")
	}
	if got := f.reload(existing.ID); got.DisplayName != "Renamed Existing" || got.DepartmentID == nil || *got.DepartmentID != dept.ID {
		t.Errorf("updated user = %+v", got)
	}
	var events int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1`, "batch:"+p.Batch.ID).Scan(&events); err != nil || events < 3 {
		t.Errorf("batch audit events = %d (%v): per-row events and the batch event share the correlation id", events, err)
	}
	var kind string
	if err := f.pool.QueryRow(ctx, `SELECT action FROM platform.audit_events WHERE target_id = $1 AND action = 'organization.import.applied'`, p.Batch.ID).Scan(&kind); err != nil {
		t.Errorf("organization.import.applied missing: %v", err)
	}

	// Applying again is idempotent: it reports the stored result and writes nothing.
	again, err := f.repo.ApplyBatch(ctx, c, p.Batch.ID, p.Batch.PreviewHash, 2)
	if err != nil || !again.Replayed || f.userCount(newEmail) != 1 {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	// Importing the same file again changes nothing: idempotent by the external key.
	p2 := f.previewCSV(c, application.ImportUsers, application.KeyPrimaryEmail, application.ModeUpsert, csv)
	if got := p2.Batch.Counts; got["create"] != 0 || got["update"] != 0 || got["unchanged"] != 2 || got["reject"] != 2 {
		t.Errorf("second preview counts = %v", got)
	}
	p3 := f.previewCSV(c, application.ImportUsers, application.KeyPrimaryEmail, application.ModeCreateOnly, csv)
	if r := rowByNo(p3.Rows, 1); !hasCode(r.Errors, "already_exists") {
		t.Errorf("create_only on an existing key: %+v", r)
	}
}

func TestImportApplyIsAllOrNothingAndRejectsStalePreviews(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	f.cleanupImported()
	admin := f.admin("Importer")
	c := f.batchCaller(admin.ID, true, application.PermUsersManage, application.PermImport)
	e1, e2 := f.pfx+"-a1@example.test", f.pfx+"-a2@example.test"
	csv := "display_name,primary_email\nFirst," + e1 + "\nSecond," + e2 + "\n"
	p := f.previewCSV(c, application.ImportUsers, application.KeyPrimaryEmail, application.ModeCreateOnly, csv)
	if p.Batch.Counts["create"] != 2 {
		t.Fatalf("counts = %v", p.Batch.Counts)
	}
	// Someone creates the second address between preview and apply.
	if _, err := f.pool.Exec(ctx, `INSERT INTO organization.users(display_name, primary_email) VALUES ($1, $2)`, f.pfx+" Intruder", e2); err != nil {
		t.Fatal(err)
	}
	_, err := f.repo.ApplyBatch(ctx, c, p.Batch.ID, p.Batch.PreviewHash, 0)
	var rowErr *application.ImportRowError
	if !errors.As(err, &rowErr) || rowErr.Row != 2 {
		t.Fatalf("apply = %v, want a failure at row 2", err)
	}
	if f.userCount(e1) != 0 {
		t.Errorf("row 1 survived a failed apply (count %d)", f.userCount(e1))
	}
	if b, err := f.repo.GetBatch(ctx, c, p.Batch.ID); err != nil || b.Status != "previewed" {
		t.Errorf("a failed apply leaves the preview: %+v %v", b, err)
	}

	// A changed row version makes the preview stale.
	u := f.local("Stale")
	csv2 := "display_name,primary_email\nStale Renamed," + *u.PrimaryEmail + "\n"
	p2 := f.previewCSV(c, application.ImportUsers, application.KeyPrimaryEmail, application.ModeUpdateOnly, csv2)
	if _, err := f.repo.UpdateProfile(ctx, c, u.ID, application.ProfileChange{ExpectedVersion: u.Version, GivenName: application.OptString{Set: true, Value: str("Changed")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.ApplyBatch(ctx, c, p2.Batch.ID, p2.Batch.PreviewHash, 0); !errors.As(err, &rowErr) || !errors.Is(rowErr.Cause, application.ErrVersionConflict) {
		t.Errorf("stale apply = %v", err)
	}

	// Expired previews are gone and purged.
	if _, err := f.pool.Exec(ctx, `UPDATE organization.import_batches SET expires_at = now() - interval '1 minute' WHERE id = $1`, p2.Batch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.GetBatch(ctx, c, p2.Batch.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("expired batch: %v", err)
	}
	if n, err := f.repo.PurgeExpiredBatches(ctx); err != nil || n < 1 {
		t.Errorf("purge = %d, %v", n, err)
	}
}

func TestImportLocationsAndDepartmentsBuildHierarchies(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	admin := f.admin("Importer")
	c := f.batchCaller(admin.ID, true, application.PermLocationsManage, application.PermDepartmentManage, application.PermImport)
	site, area := f.pfx+"-HQ", f.pfx+"-HQ-1"
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.locations WHERE name LIKE $1 || '%'`, f.pfx)
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.departments WHERE name LIKE $1 || '%'`, f.pfx)
	})
	// The child may come before its parent in the file order only if the parent is already known; here the
	// parent precedes it and is created by the same dry run.
	csv := "code,name,parent_code\n" + site + "," + f.pfx + " Headquarters,\n" + area + "," + f.pfx + " Floor 1," + site + "\n"
	p := f.previewCSV(c, application.ImportLocations, "", application.ModeUpsert, csv)
	if p.Batch.Counts["create"] != 2 || p.Batch.Counts["reject"] != 0 {
		t.Fatalf("location counts = %v, rows %+v", p.Batch.Counts, p.Rows)
	}
	if _, err := f.repo.ApplyBatch(ctx, c, p.Batch.ID, p.Batch.PreviewHash, 0); err != nil {
		t.Fatal(err)
	}
	var kind, parent string
	if err := f.pool.QueryRow(ctx, `SELECT kind, coalesce(parent_location_id::text, '') FROM organization.locations WHERE lower(code) = lower($1)`, area).Scan(&kind, &parent); err != nil || kind != "area" || parent == "" {
		t.Errorf("area = %s %s %v", kind, parent, err)
	}
	// Moving is not an import: a different parent is refused per row.
	csv2 := "code,name,parent_code\n" + area + "," + f.pfx + " Floor 1," + area + "\n"
	p2 := f.previewCSV(c, application.ImportLocations, "", application.ModeUpsert, csv2)
	if r := rowByNo(p2.Rows, 1); !hasCode(r.Errors, "parent_change_not_supported") && !hasCode(r.Errors, "invalid_hierarchy") {
		t.Errorf("parent change: %+v", r)
	}
	// Departments: a rename is an update, an unchanged row is reported as such.
	dcode := f.pfx + "-D1"
	pd := f.previewCSV(c, application.ImportDepartments, "", application.ModeCreateOnly, "code,name\n"+dcode+","+f.pfx+" Finance\n")
	if _, err := f.repo.ApplyBatch(ctx, c, pd.Batch.ID, pd.Batch.PreviewHash, 0); err != nil {
		t.Fatal(err)
	}
	pd2 := f.previewCSV(c, application.ImportDepartments, "", application.ModeUpsert, "code,name\n"+dcode+","+f.pfx+" Finance\n")
	if pd2.Batch.Counts["unchanged"] != 1 {
		t.Errorf("department counts = %v", pd2.Batch.Counts)
	}
	// The manage permission of the kind is required in addition to organization.import.
	usersOnly := f.batchCaller(admin.ID, true, application.PermUsersManage, application.PermImport)
	if _, err := f.repo.PreviewImport(ctx, usersOnly, application.ParsedImport{Kind: application.ImportLocations}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("locations import without locations.manage: %v", err)
	}
}

func TestBulkOperationsApplyDominanceAndOwnershipPerRow(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	delegate := f.local("Delegate", "organization.users.manage", "tickets.view")
	plain := f.local("Plain", "tickets.view")
	powerful := f.local("Powerful", "tickets.view", "changes.approve")
	dir := f.user(f.pfx+" Directory", "active")
	c := f.batchCaller(delegate.ID, false, application.PermUsersManage)
	ids := []string{plain.ID, powerful.ID, dir, delegate.ID, "0198f3c2-0000-7000-8000-00000000dead"}

	p, err := f.repo.PreviewBulk(ctx, c, application.BulkInput{Operation: application.BulkDeactivate, UserIDs: ids, Reason: "no_longer_needed"}, ids)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{plain.ID: "update", powerful.ID: "dominance_required", dir: "update", delegate.ID: "self_operation", ids[4]: "not_found"}
	for i, id := range ids {
		r := rowByNo(p.Rows, i+1)
		code := r.Action
		if r.Action == "reject" && len(r.Errors) > 0 {
			code = r.Errors[0].Code
		}
		if code != want[id] {
			t.Errorf("row %d (%s) = %s, want %s", i+1, id, code, want[id])
		}
	}
	if f.reload(plain.ID).Status != "active" {
		t.Fatal("a bulk preview must not write")
	}
	if _, err := f.repo.PreviewBulk(ctx, f.batchCaller(delegate.ID, false), application.BulkInput{Operation: application.BulkDeactivate}, ids); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("bulk preview without organization.users.manage: %v", err)
	}
	res, err := f.repo.ApplyBatch(ctx, c, p.Batch.ID, p.Batch.PreviewHash, p.Batch.Counts["reject"])
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != len(ids) || res.Batch.AppliedCounts["update"] != 2 {
		t.Errorf("apply result = %+v", res)
	}
	if f.reload(plain.ID).Status != "inactive" || f.reload(dir).Status != "inactive" || f.reload(powerful.ID).Status != "active" || f.reload(delegate.ID).Status != "active" {
		t.Error("only the accepted rows are applied")
	}

	// A directory User keeps its manager: the directory owns it, so the row is skipped, not silently changed.
	boss := f.local("Boss")
	other := f.user(f.pfx+" Directory 2", "active")
	pm, err := f.repo.PreviewBulk(ctx, c, application.BulkInput{Operation: application.BulkSetManager, UserIDs: []string{other, powerful.ID}, ManagerUserID: &boss.ID}, []string{other, powerful.ID})
	if err != nil {
		t.Fatal(err)
	}
	if r := rowByNo(pm.Rows, 1); r.Action != "reject" || !hasCode(r.Errors, "directory_owned") {
		t.Errorf("manager of a directory user: %+v", r)
	}
	if r := rowByNo(pm.Rows, 2); r.Action != "update" {
		t.Errorf("manager of a local user: %+v", r)
	}
	// Stale: the target changed after the preview.
	pu := f.reload(powerful.ID)
	if _, err := f.repo.UpdateProfile(ctx, f.caller(""), pu.ID, application.ProfileChange{ExpectedVersion: pu.Version, GivenName: application.OptString{Set: true, Value: str("X")}}); err != nil {
		t.Fatal(err)
	}
	var rowErr *application.ImportRowError
	if _, err := f.repo.ApplyBatch(ctx, c, pm.Batch.ID, pm.Batch.PreviewHash, pm.Batch.Counts["reject"]); !errors.As(err, &rowErr) {
		t.Errorf("stale bulk apply = %v", err)
	}
}

func (f *peopleFix) syncConflict(externalID string) string {
	f.t.Helper()
	provider := f.pfx + "-ldap"
	id := f.insert(`
		INSERT INTO organization.directory_sync_runs(provider_key, trigger, started_at, finished_at, outcome, conflicts, conflict_count)
		VALUES ($1, 'manual', now(), now(), 'succeeded', $2::jsonb, 1) RETURNING id::text`,
		`DELETE FROM organization.directory_sync_runs WHERE id = $1`, provider, fmt.Sprintf(`[{"kind":"email_in_use","externalId":%q,"username":"jdoe"}]`, externalID))
	f.t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM organization.external_identities WHERE provider_key = $1`, provider)
	})
	return id
}

func TestLinkDirectoryIdentityIsAtomicAndAdministratorOnly(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	repo := f.repo.WithCredentialRemover(testRemover{})
	f.repo = repo
	admin := f.admin("Admin")
	adminCaller := f.caller(admin.ID)
	adminCaller.PlatformAdmin = true
	ext := f.pfx + "-ext-1"
	run := f.syncConflict(ext)

	u := f.local("Local")
	f.t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.external_identities WHERE user_id = $1`, u.ID)
	})
	f.mailer.mail = false
	if _, err := repo.IssueCredentialLink(ctx, adminCaller, u.ID, application.CredentialInvitation); err != nil {
		t.Fatal(err)
	}
	var creds, openTokens int
	count := func() {
		_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.local_credentials WHERE user_id = $1`, u.ID).Scan(&creds)
		_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.credential_tokens WHERE user_id = $1 AND used_at IS NULL`, u.ID).Scan(&openTokens)
	}
	count()
	if creds != 1 || openTokens != 1 {
		t.Fatalf("setup: credentials %d, open tokens %d", creds, openTokens)
	}

	// Not an administrator: refused before anything is read.
	notAdmin := f.caller(admin.ID)
	if _, err := repo.LinkDirectoryIdentity(ctx, notAdmin, u.ID, u.Version, run, ext); !errors.Is(err, application.ErrAdminRequired) {
		t.Errorf("non-administrator: %v", err)
	}
	if _, err := repo.LinkDirectoryIdentity(ctx, adminCaller, u.ID, u.Version+5, run, ext); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("version: %v", err)
	}
	if _, err := repo.LinkDirectoryIdentity(ctx, adminCaller, u.ID, u.Version, run, f.pfx+"-unknown"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("an identity that is not in the conflict list: %v", err)
	}
	// Refused while the account holds roles (review rule R5) and nothing changes.
	holder := f.local("Holder", "tickets.view")
	if _, err := repo.LinkDirectoryIdentity(ctx, adminCaller, holder.ID, holder.Version, run, ext); !errors.Is(err, application.ErrDirectoryLinkRoles) {
		t.Errorf("role holder: %v", err)
	}
	if got := f.reload(holder.ID); got.Origin != "local" {
		t.Errorf("a refused link changed the origin: %+v", got)
	}
	count()
	if creds != 1 || openTokens != 1 {
		t.Fatalf("refused links must not touch the credential: %d %d", creds, openTokens)
	}

	linked, err := repo.LinkDirectoryIdentity(ctx, adminCaller, u.ID, u.Version, run, ext)
	if err != nil {
		t.Fatal(err)
	}
	if linked.Origin != "directory" || linked.Version != u.Version+1 {
		t.Errorf("linked = %+v", linked)
	}
	count()
	if creds != 0 || openTokens != 0 {
		t.Errorf("after linking: credentials %d, open tokens %d, want none", creds, openTokens)
	}
	var identities int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM organization.external_identities WHERE user_id = $1`, u.ID).Scan(&identities); err != nil || identities != 1 {
		t.Errorf("identities = %d (%v)", identities, err)
	}
	if acts := f.actions(u.ID); !contains(acts, "organization.user.directory_linked") {
		t.Errorf("audit actions = %v", acts)
	}
	// The identity is taken now, and a directory account cannot be linked again.
	other := f.local("Other")
	if _, err := repo.LinkDirectoryIdentity(ctx, adminCaller, other.ID, other.Version, run, ext); !errors.Is(err, application.ErrDirectoryIdentityInUse) {
		t.Errorf("identity in use: %v", err)
	}
	if _, err := repo.LinkDirectoryIdentity(ctx, adminCaller, u.ID, linked.Version, run, ext); !errors.Is(err, application.ErrDirectoryUser) {
		t.Errorf("already a directory account: %v", err)
	}
	// The database refuses the origin change without the identity or with a remaining credential.
	third := f.local("Third")
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET origin = 'directory' WHERE id = $1`, third.ID); err == nil {
		t.Error("origin must stay immutable without an identity")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestExtendAccessOnExternalAccounts(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	admin := f.admin("Admin")
	c := f.caller(admin.ID)
	c.ExternalPartiesManage = true
	expires := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Microsecond)
	id := f.insert(`
		INSERT INTO organization.users(display_name, primary_email, status, origin, account_kind, access_expires_at)
		VALUES ($1, $2, 'active', 'local', 'external', $3) RETURNING id::text`,
		`DELETE FROM organization.users WHERE id = $1`, f.pfx+" Vendor", f.pfx+"-vendor@example.test", expires)
	u := f.reload(id)

	later := expires.Add(30 * 24 * time.Hour)
	if _, err := f.repo.ExtendAccess(ctx, c, id, u.Version+3, later, "contract_renewed"); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("version: %v", err)
	}
	var inv *application.InvalidInputError
	if _, err := f.repo.ExtendAccess(ctx, c, id, u.Version, expires.Add(-time.Hour), "contract_renewed"); !errors.As(err, &inv) {
		t.Errorf("shortening is not an extension: %v", err)
	}
	got, err := f.repo.ExtendAccess(ctx, c, id, u.Version, later, "contract_renewed")
	if err != nil || got.AccessExpiresAt == nil || !got.AccessExpiresAt.Equal(later) || got.Version != u.Version+1 {
		t.Fatalf("extend = %+v, %v", got, err)
	}
	if acts := f.actions(id); !contains(acts, "organization.user.access_extended") {
		t.Errorf("audit actions = %v", acts)
	}
	// Employees have no access end; departed external accounts are not extended.
	emp := f.local("Employee")
	if _, err := f.repo.ExtendAccess(ctx, c, emp.ID, emp.Version, later, "contract_renewed"); !errors.Is(err, application.ErrWrongState) {
		t.Errorf("employee: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET status = 'departed' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.ExtendAccess(ctx, c, id, got.Version, later.Add(time.Hour), "contract_renewed"); !errors.Is(err, application.ErrWrongState) {
		t.Errorf("departed: %v", err)
	}
}
