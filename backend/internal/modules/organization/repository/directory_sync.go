package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

var _ public.DirectorySyncStore = (*Repository)(nil)

const (
	maxStoredConflicts = 100
	batchSize          = 500
	safeguardMinimum   = 5 // a run deactivating at most this many identities is never aborted
	emailWithheldMark  = "+email-withheld"
	syncActor          = "directory-sync"
)

// StartRun implements public.DirectorySyncStore.
func (r *Repository) StartRun(ctx context.Context, providerKey string, trigger public.SyncTrigger, startedAt, abandonedBefore time.Time) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("start sync run: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE organization.directory_sync_runs
		SET outcome = 'failed', finished_at = $3, error = 'abandoned'
		WHERE provider_key = $1 AND outcome = 'running' AND started_at < $2`,
		providerKey, abandonedBefore, startedAt); err != nil {
		return "", fmt.Errorf("start sync run: close abandoned runs: %w", err)
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO organization.directory_sync_runs (provider_key, trigger, started_at)
		VALUES ($1, $2, $3) RETURNING id::text`, providerKey, string(trigger), startedAt).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "directory_sync_runs_running_unique" {
		return "", public.ErrSyncAlreadyRunning
	}
	if err != nil {
		return "", fmt.Errorf("start sync run: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("start sync run: commit: %w", err)
	}
	return id, nil
}

// FinishRun implements public.DirectorySyncStore.
func (r *Repository) FinishRun(ctx context.Context, f public.SyncRunFinish) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("finish sync run: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE organization.directory_sync_runs
		SET outcome = $2, observed_at = $3, finished_at = $4, error = $5
		WHERE id = $1 AND outcome = 'running'`,
		f.RunID, f.Outcome, f.ObservedAt, f.FinishedAt, f.Error)
	if err != nil {
		return fmt.Errorf("finish sync run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("finish sync run: run is not running")
	}
	if f.Safeguard != nil {
		meta := syncMetadata(f.ProviderKey, f.RunID)
		meta["activeBefore"] = f.Safeguard.ActiveBefore
		meta["deactivations"] = f.Safeguard.Deactivations
		meta["maxDeactivationPercent"] = f.Safeguard.MaxPercent
		raw, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("finish sync run: marshal audit metadata: %w", err)
		}
		var auditID string
		if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&auditID); err != nil {
			return fmt.Errorf("finish sync run: audit id: %w", err)
		}
		if err := audit.Insert(ctx, tx, audit.Entry{
			ID: auditID, OccurredAt: f.FinishedAt, Action: "organization.directory_sync.aborted",
			TargetType: "directory_sync_run", TargetID: f.RunID, CorrelationID: f.RunID, Metadata: raw,
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("finish sync run: commit: %w", err)
	}
	return nil
}

func syncMetadata(providerKey, runID string) map[string]any {
	return map[string]any{"actor": syncActor, "providerKey": providerKey, "runId": runID}
}

// ApplySnapshot implements public.DirectorySyncStore. Everything happens in one
// transaction; see docs/integrations/ldap-ad-sync-design.md section 5.
func (r *Repository) ApplySnapshot(ctx context.Context, in public.SyncApplyInput) (public.SyncApplyOutput, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return public.SyncApplyOutput{}, fmt.Errorf("apply sync: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a := &syncApply{
		tx: tx, in: in,
		subjectToUser: make(map[string]string, len(in.Users)),
		changes:       map[string]*userChange{},
		counts:        zeroCounts(),
	}
	out, err := a.run(ctx)
	if err != nil {
		return public.SyncApplyOutput{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return public.SyncApplyOutput{}, fmt.Errorf("apply sync: commit: %w", err)
	}
	return out, nil
}

func zeroCounts() map[string]int {
	m := map[string]int{}
	for _, k := range []string{
		"usersObserved", "usersCreated", "usersUpdated", "usersUnchanged", "usersNotObserved",
		"usersActivated", "usersDeactivated", "sessionsRevoked",
		"groupsObserved", "groupsCreated", "groupsUpdated", "groupsNotObserved",
		"membershipsOpened", "membershipsClosed", "nestingOpened", "nestingClosed", "unresolvedMembers",
	} {
		m[k] = 0
	}
	return m
}

type identityRow struct {
	id, userID string
	username   *string
	dn         *string
	enabled    bool
	hash       *string
	deleted    bool
	manager    *string
}

type userRow struct {
	displayName                              string
	givenName, familyName, email, employeeNo *string
}

// userChange accumulates what happened to one User for the outbox event.
type userChange struct {
	created       bool
	fields        map[string]struct{}
	statusChanged bool
	updated       bool // an existing directory-observed user was modified
}

func (c *userChange) field(name string) {
	if c.fields == nil {
		c.fields = map[string]struct{}{}
	}
	c.fields[name] = struct{}{}
}

type syncConflict struct {
	Kind       string `json:"kind"`
	ExternalID string `json:"externalId"`
	Username   string `json:"username"`
}

type syncApply struct {
	tx pgx.Tx
	in public.SyncApplyInput

	identities    map[string]*identityRow // by external subject
	groupIDs      map[string]string       // snapshot group external id -> group id
	subjectToUser map[string]string       // snapshot user external id -> user id (created or existing)
	changes       map[string]*userChange  // by user id
	counts        map[string]int
	conflicts     []syncConflict
	conflictCount int
	skippedNew    int
	audits        []audit.Entry
}

func (a *syncApply) change(userID string) *userChange {
	c := a.changes[userID]
	if c == nil {
		c = &userChange{}
		a.changes[userID] = c
	}
	return c
}

func (a *syncApply) conflict(kind string, u public.SyncUser) {
	a.conflictCount++
	if len(a.conflicts) < maxStoredConflicts {
		a.conflicts = append(a.conflicts, syncConflict{Kind: kind, ExternalID: u.ExternalID, Username: u.Username})
	}
}

func (a *syncApply) auditEntry(action, targetType, targetID string, before, after any) error {
	meta, err := json.Marshal(syncMetadata(a.in.ProviderKey, a.in.RunID))
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	e := audit.Entry{
		OccurredAt: a.in.ObservedAt, Action: action, TargetType: targetType, TargetID: targetID,
		CorrelationID: a.in.RunID, Metadata: meta,
	}
	if before != nil {
		if e.Before, err = json.Marshal(before); err != nil {
			return fmt.Errorf("marshal audit before: %w", err)
		}
	}
	if after != nil {
		if e.After, err = json.Marshal(after); err != nil {
			return fmt.Errorf("marshal audit after: %w", err)
		}
	}
	a.audits = append(a.audits, e)
	return nil
}

func (a *syncApply) run(ctx context.Context) (public.SyncApplyOutput, error) {
	// Lock the run so a run that was declared abandoned cannot commit.
	var outcome string
	if err := a.tx.QueryRow(ctx, `SELECT outcome FROM organization.directory_sync_runs WHERE id = $1 FOR UPDATE`, a.in.RunID).Scan(&outcome); err != nil {
		return public.SyncApplyOutput{}, fmt.Errorf("apply sync: lock run: %w", err)
	}
	if outcome != "running" {
		return public.SyncApplyOutput{}, errors.New("apply sync: run is no longer running")
	}
	steps := []func(context.Context) error{
		a.loadIdentities, a.checkSafeguard, a.applyUsers, a.sweepNotObserved, a.applyStatus,
		a.applyManagers, a.applyGroups, a.applyMemberships, a.writeAuditAndEvents, a.finishRun,
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return public.SyncApplyOutput{}, err
		}
	}
	return public.SyncApplyOutput{Counts: a.counts, ConflictCount: a.conflictCount}, nil
}

func (a *syncApply) loadIdentities(ctx context.Context) error {
	rows, err := a.tx.Query(ctx, `
		SELECT e.external_subject, e.id::text, e.user_id::text, e.username, e.distinguished_name,
		       e.enabled, e.attributes_hash, e.deleted_observed_at IS NOT NULL, u.manager_user_id::text
		FROM organization.external_identities e
		JOIN organization.users u ON u.id = e.user_id
		WHERE e.provider_key = $1`, a.in.ProviderKey)
	if err != nil {
		return fmt.Errorf("apply sync: load identities: %w", err)
	}
	defer rows.Close()
	a.identities = map[string]*identityRow{}
	for rows.Next() {
		var subject string
		var row identityRow
		if err := rows.Scan(&subject, &row.id, &row.userID, &row.username, &row.dn, &row.enabled, &row.hash, &row.deleted, &row.manager); err != nil {
			return fmt.Errorf("apply sync: scan identity: %w", err)
		}
		a.identities[subject] = &row
	}
	return rows.Err()
}

func (a *syncApply) checkSafeguard(context.Context) error {
	snapshot := make(map[string]bool, len(a.in.Users)) // external id -> enabled
	for _, u := range a.in.Users {
		snapshot[u.ExternalID] = u.Enabled
	}
	activeBefore, deactivations := 0, 0
	for subject, row := range a.identities {
		if !row.enabled || row.deleted {
			continue
		}
		activeBefore++
		if enabled, present := snapshot[subject]; !present || !enabled {
			deactivations++
		}
	}
	if deactivations > safeguardMinimum && deactivations*100 > a.in.MaxDeactivationPercent*activeBefore {
		return &public.SafeguardError{ActiveBefore: activeBefore, Deactivations: deactivations, MaxPercent: a.in.MaxDeactivationPercent}
	}
	return nil
}

func ptrEq(p *string, q *string) bool {
	if p == nil || q == nil {
		return p == nil && q == nil
	}
	return *p == *q
}

func strEq(p *string, s string) bool {
	if p == nil {
		return s == ""
	}
	return *p == s
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// sendBatches executes queued statements in chunks. queue is called with a
// fresh batch for each chunk of indexes.
func (a *syncApply) sendBatches(ctx context.Context, n int, queue func(b *pgx.Batch, i int)) error {
	for start := 0; start < n; start += batchSize {
		end := min(start+batchSize, n)
		b := &pgx.Batch{}
		for i := start; i < end; i++ {
			queue(b, i)
		}
		br := a.tx.SendBatch(ctx, b)
		if err := br.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (a *syncApply) applyUsers(ctx context.Context) error {
	var unchanged []string
	var changedIdx, newIdx []int
	for i, u := range a.in.Users {
		row, ok := a.identities[u.ExternalID]
		if !ok {
			newIdx = append(newIdx, i)
			continue
		}
		a.subjectToUser[u.ExternalID] = row.userID
		if row.hash != nil && *row.hash == u.AttributesHash && !row.deleted && row.enabled == u.Enabled {
			unchanged = append(unchanged, u.ExternalID)
			continue
		}
		changedIdx = append(changedIdx, i)
	}
	a.counts["usersObserved"] = len(a.in.Users)

	// Current rows of changed users and owners of every candidate email.
	current := map[string]userRow{}
	if len(changedIdx) > 0 {
		ids := make([]string, 0, len(changedIdx))
		for _, i := range changedIdx {
			ids = append(ids, a.identities[a.in.Users[i].ExternalID].userID)
		}
		rows, err := a.tx.Query(ctx, `
			SELECT id::text, display_name, given_name, family_name, primary_email, employee_number
			FROM organization.users WHERE id = ANY($1::text[]::uuid[])`, ids)
		if err != nil {
			return fmt.Errorf("apply sync: load users: %w", err)
		}
		for rows.Next() {
			var id string
			var ur userRow
			if err := rows.Scan(&id, &ur.displayName, &ur.givenName, &ur.familyName, &ur.email, &ur.employeeNo); err != nil {
				rows.Close()
				return fmt.Errorf("apply sync: scan user: %w", err)
			}
			current[id] = ur
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("apply sync: load users: %w", err)
		}
	}
	candidates := map[string]struct{}{}
	for _, i := range newIdx {
		if e := a.in.Users[i].Email; e != nil {
			candidates[strings.ToLower(*e)] = struct{}{}
		}
	}
	for _, i := range changedIdx {
		u := a.in.Users[i]
		cur := current[a.identities[u.ExternalID].userID]
		if u.Email != nil && !ptrEqFold(u.Email, cur.email) {
			candidates[strings.ToLower(*u.Email)] = struct{}{}
		}
	}
	owner := map[string]string{}
	if len(candidates) > 0 {
		keys := make([]string, 0, len(candidates))
		for k := range candidates {
			keys = append(keys, k)
		}
		rows, err := a.tx.Query(ctx, `
			SELECT lower(primary_email), id::text FROM organization.users
			WHERE lower(primary_email) = ANY($1::text[])`, keys)
		if err != nil {
			return fmt.Errorf("apply sync: load email owners: %w", err)
		}
		for rows.Next() {
			var email, id string
			if err := rows.Scan(&email, &id); err != nil {
				rows.Close()
				return fmt.Errorf("apply sync: scan email owner: %w", err)
			}
			owner[email] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("apply sync: load email owners: %w", err)
		}
	}

	// Existing identities whose hash differs or that reappeared.
	type update struct {
		idx          int
		identityID   string
		hash         string
		userID       string
		userChanged  bool
		desiredEmail *string
	}
	var updates []update
	for _, i := range changedIdx {
		u := a.in.Users[i]
		row := a.identities[u.ExternalID]
		cur := current[row.userID]
		desiredEmail := u.Email
		withheld := false
		if !ptrEqFold(cur.email, desiredEmail) {
			if desiredEmail != nil {
				if o, taken := owner[strings.ToLower(*desiredEmail)]; taken && o != row.userID {
					a.conflict("email_in_use", u)
					withheld = true
					desiredEmail = cur.email
				}
			}
			if !withheld {
				if cur.email != nil && owner[strings.ToLower(*cur.email)] == row.userID {
					delete(owner, strings.ToLower(*cur.email))
				}
				if desiredEmail != nil {
					owner[strings.ToLower(*desiredEmail)] = row.userID
				}
			}
		}
		hash := u.AttributesHash
		if withheld {
			hash += emailWithheldMark // never equals the real hash, so the next run re-evaluates
		}
		ch := &userChange{}
		if cur.displayName != u.DisplayName {
			ch.field("displayName")
		}
		if !ptrEq(cur.givenName, u.GivenName) {
			ch.field("givenName")
		}
		if !ptrEq(cur.familyName, u.FamilyName) {
			ch.field("familyName")
		}
		if !ptrEq(cur.email, desiredEmail) {
			ch.field("primaryEmail")
		}
		if !ptrEq(cur.employeeNo, u.EmployeeNumber) {
			ch.field("employeeNumber")
		}
		if !strEq(row.username, u.Username) {
			ch.field("username")
		}
		if row.enabled != u.Enabled {
			ch.field("enabled")
		}
		userChanged := len(ch.fields) > 0
		identityChanged := !strEq(row.username, u.Username) || !strEq(row.dn, u.DistinguishedName) ||
			row.enabled != u.Enabled || row.deleted || row.hash == nil || *row.hash != hash
		if !userChanged && !identityChanged {
			unchanged = append(unchanged, u.ExternalID)
			continue
		}
		ch.updated = true
		a.changes[row.userID] = ch
		updates = append(updates, update{idx: i, identityID: row.id, hash: hash, userID: row.userID, userChanged: userChanged, desiredEmail: desiredEmail})
	}
	a.counts["usersUpdated"] = len(updates)
	err := a.sendBatches(ctx, len(updates), func(b *pgx.Batch, n int) {
		up := updates[n]
		u := a.in.Users[up.idx]
		if up.userChanged {
			b.Queue(`
				UPDATE organization.users
				SET display_name = $2, given_name = $3, family_name = $4, primary_email = $5, employee_number = $6, updated_at = $7
				WHERE id = $1`, up.userID, u.DisplayName, u.GivenName, u.FamilyName, up.desiredEmail, u.EmployeeNumber, a.in.ObservedAt)
		}
		b.Queue(`
			UPDATE organization.external_identities
			SET username = $2, distinguished_name = $3, enabled = $4, attributes_hash = $5,
			    deleted_observed_at = NULL, last_seen_at = $6, updated_at = $6
			WHERE id = $1`, up.identityID, nilIfEmpty(u.Username), nilIfEmpty(u.DistinguishedName), u.Enabled, up.hash, a.in.ObservedAt)
	})
	if err != nil {
		return fmt.Errorf("apply sync: update users: %w", err)
	}

	// New accounts.
	var creations []int
	for _, i := range newIdx {
		u := a.in.Users[i]
		if u.Email != nil {
			key := strings.ToLower(*u.Email)
			if _, taken := owner[key]; taken {
				a.conflict("email_in_use", u)
				a.skippedNew++
				continue
			}
			owner[key] = "new"
		}
		creations = append(creations, i)
	}
	newIDs := make([]string, len(creations))
	err = a.sendBatches(ctx, len(creations), func(b *pgx.Batch, n int) {
		u := a.in.Users[creations[n]]
		status := "inactive"
		if u.Enabled {
			status = "active"
		}
		b.Queue(`
			WITH u AS (
				INSERT INTO organization.users
					(display_name, given_name, family_name, primary_email, employee_number, status, status_source, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, 'directory', $7, $7)
				RETURNING id)
			INSERT INTO organization.external_identities
				(user_id, provider_key, external_subject, username, distinguished_name, enabled, last_seen_at, attributes_hash, created_at, updated_at)
			SELECT id, $8, $9, $10, $11, $12, $7, $13, $7, $7 FROM u
			RETURNING user_id::text`,
			u.DisplayName, u.GivenName, u.FamilyName, u.Email, u.EmployeeNumber, status, a.in.ObservedAt,
			a.in.ProviderKey, u.ExternalID, nilIfEmpty(u.Username), nilIfEmpty(u.DistinguishedName), u.Enabled, u.AttributesHash,
		).QueryRow(func(row pgx.Row) error { return row.Scan(&newIDs[n]) })
	})
	if err != nil {
		return fmt.Errorf("apply sync: create users: %w", err)
	}
	for n, i := range creations {
		u := a.in.Users[i]
		a.subjectToUser[u.ExternalID] = newIDs[n]
		a.changes[newIDs[n]] = &userChange{created: true}
		status := "inactive"
		if u.Enabled {
			status = "active"
		}
		if err := a.auditEntry("organization.user.created_from_directory", "user", newIDs[n], nil,
			map[string]string{"status": status, "statusSource": "directory"}); err != nil {
			return err
		}
	}
	a.counts["usersCreated"] = len(creations)
	a.counts["usersUnchanged"] = len(a.in.Users) - len(creations) - len(updates) - a.skippedNew

	if len(unchanged) > 0 {
		if _, err := a.tx.Exec(ctx, `
			UPDATE organization.external_identities SET last_seen_at = $3
			WHERE provider_key = $1 AND external_subject = ANY($2::text[])`,
			a.in.ProviderKey, unchanged, a.in.ObservedAt); err != nil {
			return fmt.Errorf("apply sync: bump last_seen_at: %w", err)
		}
	}
	return nil
}

func ptrEqFold(p, q *string) bool {
	if p == nil || q == nil {
		return p == nil && q == nil
	}
	return strings.EqualFold(*p, *q)
}

func (a *syncApply) sweepNotObserved(ctx context.Context) error {
	subjects := make([]string, 0, len(a.in.Users))
	for _, u := range a.in.Users {
		subjects = append(subjects, u.ExternalID)
	}
	rows, err := a.tx.Query(ctx, `
		UPDATE organization.external_identities
		SET enabled = false, deleted_observed_at = $2, updated_at = $2
		WHERE provider_key = $1 AND deleted_observed_at IS NULL AND NOT (external_subject = ANY($3::text[]))
		RETURNING user_id::text`, a.in.ProviderKey, a.in.ObservedAt, subjects)
	if err != nil {
		return fmt.Errorf("apply sync: sweep not observed: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("apply sync: scan swept identity: %w", err)
		}
		n++
		if _, ok := a.changes[id]; !ok {
			a.changes[id] = &userChange{}
		}
	}
	a.counts["usersNotObserved"] = n
	return rows.Err()
}

// applyStatus applies the status rule to every user touched by the user pass
// or the sweep and revokes the sessions of users that stop being active.
func (a *syncApply) applyStatus(ctx context.Context) error {
	touched := make([]string, 0, len(a.changes))
	for id, c := range a.changes {
		if !c.created {
			touched = append(touched, id)
		}
	}
	if len(touched) == 0 {
		return nil
	}
	sort.Strings(touched)
	type flip struct{ id, prevSource string }
	read := func(sql string) ([]flip, error) {
		rows, err := a.tx.Query(ctx, sql, touched, a.in.ObservedAt)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []flip
		for rows.Next() {
			var f flip
			if err := rows.Scan(&f.id, &f.prevSource); err != nil {
				return nil, err
			}
			out = append(out, f)
		}
		return out, rows.Err()
	}
	deactivated, err := read(`
		WITH c AS (
			SELECT u.id, u.status_source AS prev_source FROM organization.users u
			WHERE u.id = ANY($1::text[]::uuid[]) AND u.status = 'active'
			  AND NOT EXISTS (SELECT 1 FROM organization.external_identities e
			                  WHERE e.user_id = u.id AND e.enabled AND e.deleted_observed_at IS NULL)
			FOR UPDATE OF u)
		UPDATE organization.users u SET status = 'inactive', status_source = 'directory', updated_at = $2
		FROM c WHERE u.id = c.id RETURNING u.id::text, c.prev_source`)
	if err != nil {
		return fmt.Errorf("apply sync: deactivate users: %w", err)
	}
	activated, err := read(`
		WITH c AS (
			SELECT u.id, u.status_source AS prev_source FROM organization.users u
			WHERE u.id = ANY($1::text[]::uuid[]) AND u.status = 'inactive' AND u.status_source = 'directory'
			  AND EXISTS (SELECT 1 FROM organization.external_identities e
			              WHERE e.user_id = u.id AND e.enabled AND e.deleted_observed_at IS NULL)
			FOR UPDATE OF u)
		UPDATE organization.users u SET status = 'active', status_source = 'directory', updated_at = $2
		FROM c WHERE u.id = c.id RETURNING u.id::text, c.prev_source`)
	if err != nil {
		return fmt.Errorf("apply sync: activate users: %w", err)
	}
	type state struct {
		Status       string `json:"status"`
		StatusSource string `json:"statusSource"`
	}
	for _, f := range deactivated {
		c := a.change(f.id)
		c.statusChanged = true
		c.field("status")
		if err := a.auditEntry("organization.user.status_changed", "user", f.id,
			state{"active", f.prevSource}, state{"inactive", "directory"}); err != nil {
			return err
		}
		n, err := authentication.RevokeUserSessions(ctx, a.tx, f.id, "user_deactivated", "directory-sync", a.in.RunID, a.in.ObservedAt)
		if err != nil {
			return fmt.Errorf("apply sync: revoke sessions: %w", err)
		}
		a.counts["sessionsRevoked"] += n
	}
	for _, f := range activated {
		c := a.change(f.id)
		c.statusChanged = true
		c.field("status")
		if err := a.auditEntry("organization.user.status_changed", "user", f.id,
			state{"inactive", f.prevSource}, state{"active", "directory"}); err != nil {
			return err
		}
	}
	a.counts["usersDeactivated"] = len(deactivated)
	a.counts["usersActivated"] = len(activated)
	return nil
}

func (a *syncApply) applyManagers(ctx context.Context) error {
	var ids, managers []string
	for _, u := range a.in.Users {
		userID, ok := a.subjectToUser[u.ExternalID]
		if !ok {
			continue // skipped by a conflict
		}
		desired := ""
		switch {
		case u.ManagerUnresolved:
			a.conflict("manager_unresolved", u)
		case u.ManagerExternalID != nil:
			if mid, found := a.subjectToUser[*u.ManagerExternalID]; !found {
				a.conflict("manager_unresolved", u)
			} else if mid != userID { // a self-reference stays null without a conflict
				desired = mid
			}
		}
		current := ""
		if row, existing := a.identities[u.ExternalID]; existing && row.manager != nil {
			current = *row.manager
		}
		if desired == current {
			continue
		}
		ids = append(ids, userID)
		managers = append(managers, desired)
		c := a.change(userID)
		c.field("managerUserId")
		if !c.created && !c.updated {
			c.updated = true
			a.counts["usersUpdated"]++
			a.counts["usersUnchanged"]--
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := a.tx.Exec(ctx, `
		UPDATE organization.users u SET manager_user_id = NULLIF(v.m, '')::uuid, updated_at = $3
		FROM unnest($1::text[], $2::text[]) AS v(id, m)
		WHERE u.id = v.id::uuid`, ids, managers, a.in.ObservedAt); err != nil {
		return fmt.Errorf("apply sync: set managers: %w", err)
	}
	return nil
}

type groupRow struct {
	id          string
	displayName string
	description *string
	deleted     bool
}

func (a *syncApply) applyGroups(ctx context.Context) error {
	rows, err := a.tx.Query(ctx, `
		SELECT external_id, id::text, display_name, description, deleted_observed_at IS NOT NULL
		FROM organization.directory_groups WHERE provider_key = $1`, a.in.ProviderKey)
	if err != nil {
		return fmt.Errorf("apply sync: load groups: %w", err)
	}
	existing := map[string]*groupRow{}
	for rows.Next() {
		var ext string
		var g groupRow
		if err := rows.Scan(&ext, &g.id, &g.displayName, &g.description, &g.deleted); err != nil {
			rows.Close()
			return fmt.Errorf("apply sync: scan group: %w", err)
		}
		existing[ext] = &g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("apply sync: load groups: %w", err)
	}

	a.groupIDs = make(map[string]string, len(a.in.Groups))
	var unchanged []string
	var updates, creations []int
	for i, g := range a.in.Groups {
		row, ok := existing[g.ExternalID]
		if !ok {
			creations = append(creations, i)
			continue
		}
		a.groupIDs[g.ExternalID] = row.id
		if row.deleted || row.displayName != strings.TrimSpace(g.DisplayName) || !ptrEq(row.description, normalizedDescription(g.Description)) {
			updates = append(updates, i)
		} else {
			unchanged = append(unchanged, row.id)
		}
	}
	a.counts["groupsObserved"] = len(a.in.Groups)
	a.counts["groupsUpdated"] = len(updates)
	a.counts["groupsCreated"] = len(creations)
	err = a.sendBatches(ctx, len(updates), func(b *pgx.Batch, n int) {
		g := a.in.Groups[updates[n]]
		b.Queue(`
			UPDATE organization.directory_groups
			SET display_name = $2, description = $3, last_observed_at = $4, deleted_observed_at = NULL, updated_at = $4
			WHERE id = $1`, a.groupIDs[g.ExternalID], strings.TrimSpace(g.DisplayName), normalizedDescription(g.Description), a.in.ObservedAt)
	})
	if err != nil {
		return fmt.Errorf("apply sync: update groups: %w", err)
	}
	created := make([]string, len(creations))
	err = a.sendBatches(ctx, len(creations), func(b *pgx.Batch, n int) {
		g := a.in.Groups[creations[n]]
		b.Queue(`
			INSERT INTO organization.directory_groups
				(provider_key, external_id, display_name, description, first_observed_at, last_observed_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5, $5, $5) RETURNING id::text`,
			a.in.ProviderKey, g.ExternalID, strings.TrimSpace(g.DisplayName), normalizedDescription(g.Description), a.in.ObservedAt,
		).QueryRow(func(row pgx.Row) error { return row.Scan(&created[n]) })
	})
	if err != nil {
		return fmt.Errorf("apply sync: create groups: %w", err)
	}
	for n, i := range creations {
		a.groupIDs[a.in.Groups[i].ExternalID] = created[n]
	}
	if len(unchanged) > 0 {
		if _, err := a.tx.Exec(ctx, `
			UPDATE organization.directory_groups SET last_observed_at = $2 WHERE id = ANY($1::text[]::uuid[])`,
			unchanged, a.in.ObservedAt); err != nil {
			return fmt.Errorf("apply sync: bump groups: %w", err)
		}
	}
	externalIDs := make([]string, 0, len(a.in.Groups))
	for _, g := range a.in.Groups {
		externalIDs = append(externalIDs, g.ExternalID)
	}
	tag, err := a.tx.Exec(ctx, `
		UPDATE organization.directory_groups SET deleted_observed_at = $2, updated_at = $2
		WHERE provider_key = $1 AND deleted_observed_at IS NULL AND NOT (external_id = ANY($3::text[]))`,
		a.in.ProviderKey, a.in.ObservedAt, externalIDs)
	if err != nil {
		return fmt.Errorf("apply sync: mark groups not observed: %w", err)
	}
	a.counts["groupsNotObserved"] = int(tag.RowsAffected())
	return nil
}

func normalizedDescription(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

func (a *syncApply) applyMemberships(ctx context.Context) error {
	observed := make([]string, 0, len(a.in.Groups))
	memberGroups, memberUsers := []string{}, []string{}
	parents, children := []string{}, []string{}
	for _, g := range a.in.Groups {
		gid := a.groupIDs[g.ExternalID]
		observed = append(observed, gid)
		a.counts["unresolvedMembers"] += g.UnresolvedMembers
		seen := map[string]struct{}{}
		for _, ext := range g.MemberUserIDs {
			uid, ok := a.subjectToUser[ext]
			if !ok {
				a.counts["unresolvedMembers"]++
				continue
			}
			if _, dup := seen[uid]; dup {
				continue
			}
			seen[uid] = struct{}{}
			memberGroups = append(memberGroups, gid)
			memberUsers = append(memberUsers, uid)
		}
		seenChild := map[string]struct{}{}
		for _, ext := range g.MemberGroupIDs {
			cid, ok := a.groupIDs[ext]
			if !ok {
				a.counts["unresolvedMembers"]++
				continue
			}
			if _, dup := seenChild[cid]; dup || cid == gid {
				continue
			}
			seenChild[cid] = struct{}{}
			parents = append(parents, gid)
			children = append(children, cid)
		}
	}
	ts := a.in.ObservedAt

	// Memberships: freshness bump, close, open.
	if _, err := a.tx.Exec(ctx, `
		UPDATE organization.directory_group_memberships m SET last_observed_at = $1
		FROM unnest($2::text[], $3::text[]) AS d(g, u)
		WHERE m.group_id = d.g::uuid AND m.user_id = d.u::uuid AND m.observed_until IS NULL`,
		ts, memberGroups, memberUsers); err != nil {
		return fmt.Errorf("apply sync: bump memberships: %w", err)
	}
	tag, err := a.tx.Exec(ctx, `
		WITH d AS (SELECT g::uuid AS g, u::uuid AS u FROM unnest($2::text[], $3::text[]) AS t(g, u))
		UPDATE organization.directory_group_memberships m SET observed_until = $1
		WHERE m.observed_until IS NULL
		  AND (m.group_id IN (SELECT id FROM organization.directory_groups WHERE provider_key = $5 AND deleted_observed_at IS NOT NULL)
		       OR (m.group_id = ANY($4::text[]::uuid[])
		           AND NOT EXISTS (SELECT 1 FROM d WHERE d.g = m.group_id AND d.u = m.user_id)))`,
		ts, memberGroups, memberUsers, observed, a.in.ProviderKey)
	if err != nil {
		return fmt.Errorf("apply sync: close memberships: %w", err)
	}
	a.counts["membershipsClosed"] = int(tag.RowsAffected())
	tag, err = a.tx.Exec(ctx, `
		WITH d AS (SELECT g::uuid AS g, u::uuid AS u FROM unnest($2::text[], $3::text[]) AS t(g, u))
		INSERT INTO organization.directory_group_memberships (group_id, user_id, observed_from, last_observed_at)
		SELECT d.g, d.u, $1, $1 FROM d
		WHERE NOT EXISTS (SELECT 1 FROM organization.directory_group_memberships m
		                  WHERE m.group_id = d.g AND m.user_id = d.u AND m.observed_until IS NULL)`,
		ts, memberGroups, memberUsers)
	if err != nil {
		return fmt.Errorf("apply sync: open memberships: %w", err)
	}
	a.counts["membershipsOpened"] = int(tag.RowsAffected())

	// Nesting: same maintenance over direct group-in-group edges.
	if _, err := a.tx.Exec(ctx, `
		UPDATE organization.directory_group_nesting n SET last_observed_at = $1
		FROM unnest($2::text[], $3::text[]) AS d(p, c)
		WHERE n.parent_group_id = d.p::uuid AND n.child_group_id = d.c::uuid AND n.observed_until IS NULL`,
		ts, parents, children); err != nil {
		return fmt.Errorf("apply sync: bump nesting: %w", err)
	}
	tag, err = a.tx.Exec(ctx, `
		WITH d AS (SELECT p::uuid AS p, c::uuid AS c FROM unnest($2::text[], $3::text[]) AS t(p, c)),
		     gone AS (SELECT id FROM organization.directory_groups WHERE provider_key = $5 AND deleted_observed_at IS NOT NULL)
		UPDATE organization.directory_group_nesting n SET observed_until = $1
		WHERE n.observed_until IS NULL
		  AND (n.parent_group_id IN (SELECT id FROM gone) OR n.child_group_id IN (SELECT id FROM gone)
		       OR (n.parent_group_id = ANY($4::text[]::uuid[])
		           AND NOT EXISTS (SELECT 1 FROM d WHERE d.p = n.parent_group_id AND d.c = n.child_group_id)))`,
		ts, parents, children, observed, a.in.ProviderKey)
	if err != nil {
		return fmt.Errorf("apply sync: close nesting: %w", err)
	}
	a.counts["nestingClosed"] = int(tag.RowsAffected())
	tag, err = a.tx.Exec(ctx, `
		WITH d AS (SELECT p::uuid AS p, c::uuid AS c FROM unnest($2::text[], $3::text[]) AS t(p, c))
		INSERT INTO organization.directory_group_nesting (parent_group_id, child_group_id, observed_from, last_observed_at)
		SELECT d.p, d.c, $1, $1 FROM d
		WHERE NOT EXISTS (SELECT 1 FROM organization.directory_group_nesting n
		                  WHERE n.parent_group_id = d.p AND n.child_group_id = d.c AND n.observed_until IS NULL)`,
		ts, parents, children)
	if err != nil {
		return fmt.Errorf("apply sync: open nesting: %w", err)
	}
	a.counts["nestingOpened"] = int(tag.RowsAffected())
	return nil
}

// writeAuditAndEvents inserts the collected audit entries and one
// UserSynchronized event per created or changed user, all in the sync
// transaction.
func (a *syncApply) writeAuditAndEvents(ctx context.Context) error {
	var eventUsers []string
	for id, c := range a.changes {
		if c.created || len(c.fields) > 0 || c.statusChanged {
			eventUsers = append(eventUsers, id)
		}
	}
	sort.Strings(eventUsers)
	total := len(a.audits) + len(eventUsers)
	if total == 0 {
		return nil
	}
	rows, err := a.tx.Query(ctx, `SELECT uuidv7()::text FROM generate_series(1, $1::int)`, total)
	if err != nil {
		return fmt.Errorf("apply sync: generate ids: %w", err)
	}
	ids := make([]string, 0, total)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("apply sync: scan id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("apply sync: generate ids: %w", err)
	}
	next := 0
	for _, e := range a.audits {
		e.ID = ids[next]
		next++
		if err := audit.Insert(ctx, a.tx, e); err != nil {
			return err
		}
	}
	type payload struct {
		UserID        string   `json:"userId"`
		ProviderKey   string   `json:"providerKey"`
		Created       bool     `json:"created"`
		ChangedFields []string `json:"changedFields"`
		StatusChanged bool     `json:"statusChanged"`
	}
	for _, id := range eventUsers {
		c := a.changes[id]
		fields := make([]string, 0, len(c.fields))
		for f := range c.fields {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		raw, err := json.Marshal(payload{UserID: id, ProviderKey: a.in.ProviderKey, Created: c.created, ChangedFields: fields, StatusChanged: c.statusChanged})
		if err != nil {
			return fmt.Errorf("apply sync: marshal event: %w", err)
		}
		if err := events.InsertOutbox(ctx, a.tx, events.OutboxEvent{
			ID: ids[next], EventType: "UserSynchronized", EventVersion: 1, OccurredAt: a.in.ObservedAt,
			CorrelationID: a.in.RunID, Payload: raw,
		}); err != nil {
			return err
		}
		next++
	}
	return nil
}

func (a *syncApply) finishRun(ctx context.Context) error {
	counts, err := json.Marshal(a.counts)
	if err != nil {
		return fmt.Errorf("apply sync: marshal counts: %w", err)
	}
	conflicts := a.conflicts
	if conflicts == nil {
		conflicts = []syncConflict{}
	}
	rawConflicts, err := json.Marshal(conflicts)
	if err != nil {
		return fmt.Errorf("apply sync: marshal conflicts: %w", err)
	}
	tag, err := a.tx.Exec(ctx, `
		UPDATE organization.directory_sync_runs
		SET outcome = 'succeeded', observed_at = $2, finished_at = $3, counts = $4, conflicts = $5, conflict_count = $6
		WHERE id = $1 AND outcome = 'running'`,
		a.in.RunID, a.in.ObservedAt, a.in.FinishedAt, counts, rawConflicts, a.conflictCount)
	if err != nil {
		return fmt.Errorf("apply sync: finish run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("apply sync: finish run: run is not running")
	}
	return nil
}
