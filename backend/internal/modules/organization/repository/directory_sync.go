package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

var _ application.DirectorySyncStore = (*Repository)(nil)

const (
	maxStoredConflicts = 100
	batchSize          = 500
	syncActor          = "directory-sync"
	emailUniqueIndex   = "users_primary_email_unique"
)

// StartRun implements application.DirectorySyncStore. It blocks while an
// applying run holds its row lock (see ApplySnapshot).
func (r *Repository) StartRun(ctx context.Context, providerKey string, trigger application.SyncTrigger, jobID string, startedAt, abandonedBefore time.Time) (string, error) {
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
		INSERT INTO organization.directory_sync_runs (provider_key, trigger, started_at, job_id)
		VALUES ($1, $2, $3, $4) RETURNING id::text`, providerKey, string(trigger), startedAt, nilIfEmpty(jobID)).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "directory_sync_runs_running_unique" {
		return "", application.ErrSyncAlreadyRunning
	}
	if err != nil {
		return "", fmt.Errorf("start sync run: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("start sync run: commit: %w", err)
	}
	return id, nil
}

// ProviderKeyChanged implements application.DirectorySyncStore. Only
// providers that have sync runs count as directory providers, so identities
// of other kinds never block a first sync.
func (r *Repository) ProviderKeyChanged(ctx context.Context, providerKey string) (bool, error) {
	var changed bool
	err := r.pool.QueryRow(ctx, `
		SELECT NOT EXISTS (SELECT 1 FROM organization.external_identities WHERE provider_key = $1)
		   AND EXISTS (SELECT 1 FROM organization.external_identities e
		               WHERE e.provider_key <> $1 AND e.deleted_observed_at IS NULL
		                 AND EXISTS (SELECT 1 FROM organization.directory_sync_runs r WHERE r.provider_key = e.provider_key))`,
		providerKey).Scan(&changed)
	if err != nil {
		return false, fmt.Errorf("check provider key: %w", err)
	}
	return changed, nil
}

// FinishRun implements application.DirectorySyncStore.
func (r *Repository) FinishRun(ctx context.Context, f application.SyncRunFinish) error {
	tag, err := r.pool.Exec(ctx, `
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
	return nil
}

func syncMetadata(providerKey, runID string) map[string]any {
	return map[string]any{"actor": syncActor, "providerKey": providerKey, "runId": runID}
}

// ApplySnapshot implements application.DirectorySyncStore. Everything happens
// in one transaction; see docs/integrations/ldap-ad-sync-design.md section 5.
//
// The run row is locked for the whole transaction so a run declared abandoned
// by StartRun cannot commit, and StartRun waits for an applying run. A worker
// that dies mid-apply leaves the transaction open only until
// idle_in_transaction_session_timeout (2 minutes) ends the session.
func (r *Repository) ApplySnapshot(ctx context.Context, in application.SyncApplyInput) (application.SyncApplyOutput, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return application.SyncApplyOutput{}, fmt.Errorf("apply sync: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, setting := range []string{
		`SET LOCAL idle_in_transaction_session_timeout = '2min'`,
		// Statements run once per parameter set; generic plans for the array
		// parameters would be chosen from unrepresentative statistics.
		`SET LOCAL plan_cache_mode = force_custom_plan`,
	} {
		if _, err := tx.Exec(ctx, setting); err != nil {
			return application.SyncApplyOutput{}, fmt.Errorf("apply sync: session settings: %w", err)
		}
	}
	a := &syncApply{
		tx: tx, in: in,
		subjectToUser: make(map[string]string, len(in.Users)),
		changes:       map[string]*userChange{},
		counts:        zeroCounts(),
	}
	out, err := a.run(ctx)
	if err != nil {
		return application.SyncApplyOutput{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return application.SyncApplyOutput{}, fmt.Errorf("apply sync: commit: %w", err)
	}
	return out, nil
}

func zeroCounts() map[string]int {
	m := map[string]int{}
	for _, k := range []string{
		"usersObserved", "usersCreated", "usersUpdated", "usersUnchanged", "usersNotObserved", "usersSweepWithheld",
		"usersActivated", "usersDeactivated", "sessionsRevoked",
		"groupsObserved", "groupsCreated", "groupsUpdated", "groupsNotObserved", "groupsSweepWithheld",
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

// userFieldChanged reports whether a column of organization.users changes
// (identity-only changes such as username or enabled do not touch the row).
func (c *userChange) userFieldChanged() bool {
	for _, f := range []string{"displayName", "givenName", "familyName", "primaryEmail", "employeeNumber"} {
		if _, ok := c.fields[f]; ok {
			return true
		}
	}
	return false
}

type syncConflict struct {
	Kind       string `json:"kind"`
	ExternalID string `json:"externalId"`
	Username   string `json:"username"`
}

type syncApply struct {
	tx pgx.Tx
	in application.SyncApplyInput
	// observed is the observation time: the fetch time clamped to the
	// provider's previous successful observation.
	observed time.Time

	identities    map[string]*identityRow // by external subject
	groupIDs      map[string]string       // snapshot group external id -> group id
	subjectToUser map[string]string       // snapshot user external id -> user id (created or existing)
	changes       map[string]*userChange  // by user id
	counts        map[string]int
	conflicts     []syncConflict
	conflictCount int
	skippedNew    int
	audits        []audit.Change // written with audit.Record at the end of the transaction
	// statusAudits are the status-change events built by changeUserStatus
	// (user_status.go); they are inserted with pre-generated ids.
	statusAudits []audit.Entry
	revocations  []string // users that left active; revoked at the end of the transaction

	userSweepWithheld  bool
	groupSweepWithheld bool
	withheldGroupIDs   []string
	sweepFacts         map[string]int // audit metadata of a withheld sweep
}

func (a *syncApply) change(userID string) *userChange {
	c := a.changes[userID]
	if c == nil {
		c = &userChange{}
		a.changes[userID] = c
	}
	return c
}

func (a *syncApply) conflict(kind, externalID, username string) {
	a.conflictCount++
	if len(a.conflicts) < maxStoredConflicts {
		a.conflicts = append(a.conflicts, syncConflict{Kind: kind, ExternalID: externalID, Username: username})
	}
}

func (a *syncApply) auditEntry(action, targetType, targetID string, before, after any) {
	a.queueAudit(action, targetType, targetID, before, after, syncMetadata(a.in.ProviderKey, a.in.RunID))
}

// queueAudit collects an audit event attributed to the directory-sync system
// actor; it is written by audit.Record in writeAuditAndEvents.
func (a *syncApply) queueAudit(action, targetType, targetID string, before, after any, meta map[string]any) {
	a.audits = append(a.audits, audit.Change{
		Action: action, TargetType: targetType, TargetID: targetID,
		Actor: audit.SystemActor(syncActor), CorrelationID: a.in.RunID,
		Before: before, After: after, Metadata: meta, OccurredAt: a.observed,
	})
}

func (a *syncApply) run(ctx context.Context) (application.SyncApplyOutput, error) {
	// Lock the run so a run that was declared abandoned cannot commit.
	var outcome string
	if err := a.tx.QueryRow(ctx, `SELECT outcome FROM organization.directory_sync_runs WHERE id = $1 FOR UPDATE`, a.in.RunID).Scan(&outcome); err != nil {
		return application.SyncApplyOutput{}, fmt.Errorf("apply sync: lock run: %w", err)
	}
	if outcome != "running" {
		return application.SyncApplyOutput{}, errors.New("apply sync: run is no longer running")
	}
	// Freshness never moves backwards, even when the clock does.
	if err := a.tx.QueryRow(ctx, `
		SELECT greatest($2::timestamptz, coalesce(max(observed_at), $2::timestamptz))
		FROM organization.directory_sync_runs
		WHERE provider_key = $1 AND outcome IN ('succeeded', 'sweep_withheld')`,
		a.in.ProviderKey, a.in.FetchedAt).Scan(&a.observed); err != nil {
		return application.SyncApplyOutput{}, fmt.Errorf("apply sync: observation time: %w", err)
	}
	a.observed = a.observed.UTC()

	steps := []func(context.Context) error{
		a.loadIdentities, a.decideUserSweep, a.applyUsers, a.sweepNotObserved, a.applyStatus,
		a.applyManagers, a.applyGroups, a.applyMemberships, a.auditSweepWithheld,
		a.writeAuditAndEvents, a.revokeSessions,
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return application.SyncApplyOutput{}, err
		}
	}
	outcome = application.SyncOutcomeSucceeded
	if a.userSweepWithheld || a.groupSweepWithheld {
		outcome = application.SyncOutcomeSweepWithheld
	}
	if err := a.finishRun(ctx, outcome); err != nil {
		return application.SyncApplyOutput{}, err
	}
	return application.SyncApplyOutput{Outcome: outcome, Counts: a.counts, ConflictCount: a.conflictCount}, nil
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

// decideUserSweep applies the sweep safeguard to identities missing from the
// snapshot. Explicit disables are not "missing" and are always applied.
func (a *syncApply) decideUserSweep(context.Context) error {
	present := make(map[string]struct{}, len(a.in.Users))
	for _, u := range a.in.Users {
		present[u.ExternalID] = struct{}{}
	}
	activeBefore, missingActive, sweepable := 0, 0, 0
	for subject, row := range a.identities {
		if row.deleted {
			continue
		}
		_, inSnapshot := present[subject]
		if !inSnapshot {
			sweepable++
		}
		if !row.enabled {
			continue
		}
		activeBefore++
		if !inSnapshot {
			missingActive++
		}
	}
	if application.SweepWithheld(missingActive, activeBefore, a.in.MaxMissingPercent) {
		a.userSweepWithheld = true
		a.counts["usersSweepWithheld"] = sweepable
		a.sweepFacts = map[string]int{"usersActiveBefore": activeBefore, "usersMissing": missingActive}
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

func isEmailUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == emailUniqueIndex
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

// sendChunk runs rows [from, to) as one batch inside a savepoint, so a failure
// leaves the transaction usable.
func (a *syncApply) sendChunk(ctx context.Context, from, to int, queue func(b *pgx.Batch, i int)) error {
	sp, err := a.tx.Begin(ctx)
	if err != nil {
		return err
	}
	b := &pgx.Batch{}
	for i := from; i < to; i++ {
		queue(b, i)
	}
	if err := sp.SendBatch(ctx, b).Close(); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	return sp.Commit(ctx)
}

// sendGuarded is sendBatches for statements that can violate the unique email
// index because another transaction took the address after the owners were
// read. A failing chunk is rolled back to its savepoint and retried row by
// row; onEmailConflict(i) runs for a row that violates the index and returns
// whether to retry that row (after changing what queue sends for it).
func (a *syncApply) sendGuarded(ctx context.Context, n int, queue func(b *pgx.Batch, i int), onEmailConflict func(i int) bool) error {
	for start := 0; start < n; start += batchSize {
		end := min(start+batchSize, n)
		err := a.sendChunk(ctx, start, end, queue)
		if err == nil {
			continue
		}
		if !isEmailUnique(err) {
			return err
		}
		for i := start; i < end; i++ {
			err := a.sendChunk(ctx, i, i+1, queue)
			if isEmailUnique(err) {
				if !onEmailConflict(i) {
					continue
				}
				err = a.sendChunk(ctx, i, i+1, queue)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// emailKeys returns PostgreSQL's lower() of every address. Comparison keys are
// never computed in Go: the unique index uses lower(), and Go's case mapping
// differs for some characters.
func (a *syncApply) emailKeys(ctx context.Context, emails map[string]struct{}) (map[string]string, error) {
	keys := map[string]string{}
	if len(emails) == 0 {
		return keys, nil
	}
	list := make([]string, 0, len(emails))
	for e := range emails {
		list = append(list, e)
	}
	rows, err := a.tx.Query(ctx, `SELECT c.e, lower(c.e) FROM unnest($1::text[]) AS c(e)`, list)
	if err != nil {
		return nil, fmt.Errorf("apply sync: email keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e, k string
		if err := rows.Scan(&e, &k); err != nil {
			return nil, fmt.Errorf("apply sync: scan email key: %w", err)
		}
		keys[e] = k
	}
	return keys, rows.Err()
}

func (a *syncApply) emailOwners(ctx context.Context, keys map[string]string) (map[string]string, error) {
	owner := map[string]string{}
	if len(keys) == 0 {
		return owner, nil
	}
	list := make([]string, 0, len(keys))
	for _, k := range keys {
		list = append(list, k)
	}
	rows, err := a.tx.Query(ctx, `
		SELECT lower(primary_email), id::text FROM organization.users
		WHERE lower(primary_email) = ANY($1::text[])`, list)
	if err != nil {
		return nil, fmt.Errorf("apply sync: load email owners: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			return nil, fmt.Errorf("apply sync: scan email owner: %w", err)
		}
		owner[key] = id
	}
	return owner, rows.Err()
}

func (a *syncApply) currentUsers(ctx context.Context, userIDs []string) (map[string]userRow, error) {
	current := map[string]userRow{}
	if len(userIDs) == 0 {
		return current, nil
	}
	rows, err := a.tx.Query(ctx, `
		SELECT id::text, display_name, given_name, family_name, primary_email, employee_number
		FROM organization.users WHERE id = ANY($1::uuid[])`, userIDs)
	if err != nil {
		return nil, fmt.Errorf("apply sync: load users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var ur userRow
		if err := rows.Scan(&id, &ur.displayName, &ur.givenName, &ur.familyName, &ur.email, &ur.employeeNo); err != nil {
			return nil, fmt.Errorf("apply sync: scan user: %w", err)
		}
		current[id] = ur
	}
	return current, rows.Err()
}

// identityUpdate is one existing directory account whose stored state changes.
type identityUpdate struct {
	idx          int
	identityID   string
	userID       string
	hash         *string // nil: store NULL so the next run re-evaluates
	desiredEmail *string
	curEmail     *string
	change       *userChange
}

func (a *syncApply) applyUsers(ctx context.Context) error {
	var unchanged []string // external subjects that only need last_seen_at
	var changedIdx, newIdx, invalidChanged []int
	for i, u := range a.in.Users {
		row, ok := a.identities[u.ExternalID]
		if !ok {
			if u.Invalid {
				a.conflict(application.ConflictInvalidAttributes, u.ExternalID, "")
				a.skippedNew++
				continue
			}
			newIdx = append(newIdx, i)
			continue
		}
		a.subjectToUser[u.ExternalID] = row.userID
		switch {
		case u.Invalid:
			// Observed, but no attribute value is applied: the stored values stay.
			a.conflict(application.ConflictInvalidAttributes, u.ExternalID, derefOrEmpty(row.username))
			if row.enabled == u.Enabled && !row.deleted {
				unchanged = append(unchanged, u.ExternalID)
			} else {
				invalidChanged = append(invalidChanged, i)
			}
		case row.hash != nil && *row.hash == u.AttributesHash && !row.deleted && row.enabled == u.Enabled:
			unchanged = append(unchanged, u.ExternalID)
		default:
			changedIdx = append(changedIdx, i)
		}
	}
	a.counts["usersObserved"] = len(a.in.Users)

	changedUserIDs := make([]string, 0, len(changedIdx))
	for _, i := range changedIdx {
		changedUserIDs = append(changedUserIDs, a.identities[a.in.Users[i].ExternalID].userID)
	}
	current, err := a.currentUsers(ctx, changedUserIDs)
	if err != nil {
		return err
	}
	candidates := map[string]struct{}{}
	for _, i := range newIdx {
		if e := a.in.Users[i].Email; e != nil {
			candidates[*e] = struct{}{}
		}
	}
	for _, i := range changedIdx {
		u := a.in.Users[i]
		cur := current[a.identities[u.ExternalID].userID]
		if u.Email != nil && !ptrEq(u.Email, cur.email) {
			candidates[*u.Email] = struct{}{}
		}
		if cur.email != nil {
			candidates[*cur.email] = struct{}{}
		}
	}
	keyOf, err := a.emailKeys(ctx, candidates)
	if err != nil {
		return err
	}
	owner, err := a.emailOwners(ctx, keyOf)
	if err != nil {
		return err
	}
	keyPtr := func(email *string) *string {
		if email == nil {
			return nil
		}
		k := keyOf[*email]
		return &k
	}

	// Existing identities whose hash differs or that reappeared.
	var updates []identityUpdate
	for _, i := range changedIdx {
		u := a.in.Users[i]
		row := a.identities[u.ExternalID]
		cur := current[row.userID]
		desiredEmail := u.Email
		hash := &u.AttributesHash
		if !ptrEq(cur.email, desiredEmail) {
			desiredKey := keyPtr(desiredEmail)
			ownerID := ""
			if desiredKey != nil {
				ownerID = owner[*desiredKey]
			}
			if application.DecideEmailChange(desiredKey, ownerID, row.userID) == application.EmailConflict {
				a.conflict(application.ConflictEmailInUse, u.ExternalID, u.Username)
				desiredEmail = cur.email
				hash = nil // never equals the real hash: the next run re-evaluates
			} else {
				if curKey := keyPtr(cur.email); curKey != nil && owner[*curKey] == row.userID {
					delete(owner, *curKey)
				}
				if desiredKey != nil {
					owner[*desiredKey] = row.userID
				}
			}
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
		identityChanged := !strEq(row.username, u.Username) || !strEq(row.dn, u.DistinguishedName) ||
			row.enabled != u.Enabled || row.deleted || !ptrEq(row.hash, hash)
		if !ch.userFieldChanged() && !identityChanged {
			unchanged = append(unchanged, u.ExternalID)
			continue
		}
		ch.updated = true
		a.changes[row.userID] = ch
		updates = append(updates, identityUpdate{idx: i, identityID: row.id, userID: row.userID, hash: hash,
			desiredEmail: desiredEmail, curEmail: cur.email, change: ch})
	}
	err = a.sendGuarded(ctx, len(updates), func(b *pgx.Batch, n int) {
		up := updates[n]
		u := a.in.Users[up.idx]
		if up.change.userFieldChanged() {
			b.Queue(`
				UPDATE organization.users
				SET display_name = $2, given_name = $3, family_name = $4, primary_email = $5, employee_number = $6, updated_at = $7
				WHERE id = $1`, up.userID, u.DisplayName, u.GivenName, u.FamilyName, up.desiredEmail, u.EmployeeNumber, a.observed)
		}
		b.Queue(`
			UPDATE organization.external_identities
			SET username = $2, distinguished_name = $3, enabled = $4, attributes_hash = $5,
			    deleted_observed_at = NULL, last_seen_at = $6, updated_at = $6
			WHERE id = $1`, up.identityID, nilIfEmpty(u.Username), nilIfEmpty(u.DistinguishedName), u.Enabled, up.hash, a.observed)
	}, func(n int) bool {
		// Another transaction took the address after it was read as free:
		// keep the stored address and re-evaluate on the next run.
		up := &updates[n]
		a.conflict(application.ConflictEmailInUse, a.in.Users[up.idx].ExternalID, a.in.Users[up.idx].Username)
		up.desiredEmail, up.hash = up.curEmail, nil
		delete(up.change.fields, "primaryEmail")
		return true
	})
	if err != nil {
		return fmt.Errorf("apply sync: update users: %w", err)
	}
	for _, up := range updates {
		if _, changed := up.change.fields["primaryEmail"]; changed {
			a.auditEntry("organization.user.primary_email_changed", "user", up.userID,
				map[string]*string{"primaryEmail": up.curEmail}, map[string]*string{"primaryEmail": up.desiredEmail})
		}
	}

	// Invalid accounts that still change state: enabled and reappearance only.
	err = a.sendBatches(ctx, len(invalidChanged), func(b *pgx.Batch, n int) {
		u := a.in.Users[invalidChanged[n]]
		row := a.identities[u.ExternalID]
		b.Queue(`
			UPDATE organization.external_identities
			SET enabled = $2, deleted_observed_at = NULL, last_seen_at = $3, updated_at = $3
			WHERE id = $1`, row.id, u.Enabled, a.observed)
	})
	if err != nil {
		return fmt.Errorf("apply sync: update invalid accounts: %w", err)
	}
	for _, i := range invalidChanged {
		u := a.in.Users[i]
		ch := &userChange{updated: true}
		if a.identities[u.ExternalID].enabled != u.Enabled {
			ch.field("enabled")
		}
		a.changes[a.identities[u.ExternalID].userID] = ch
	}

	// New accounts.
	var creations []int
	for _, i := range newIdx {
		u := a.in.Users[i]
		if u.Email != nil {
			key := keyOf[*u.Email]
			if application.DecideEmailChange(&key, owner[key], "") == application.EmailConflict {
				a.conflict(application.ConflictEmailInUse, u.ExternalID, u.Username)
				a.skippedNew++
				continue
			}
			owner[key] = "new"
		}
		creations = append(creations, i)
	}
	newIDs := make([]string, len(creations))
	err = a.sendGuarded(ctx, len(creations), func(b *pgx.Batch, n int) {
		u := a.in.Users[creations[n]]
		status := statusInactive
		if u.Enabled {
			status = statusActive
		}
		newIDs[n] = ""
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
			u.DisplayName, u.GivenName, u.FamilyName, u.Email, u.EmployeeNumber, status, a.observed,
			a.in.ProviderKey, u.ExternalID, nilIfEmpty(u.Username), nilIfEmpty(u.DistinguishedName), u.Enabled, u.AttributesHash,
		).QueryRow(func(row pgx.Row) error { return row.Scan(&newIDs[n]) })
	}, func(n int) bool {
		u := a.in.Users[creations[n]]
		a.conflict(application.ConflictEmailInUse, u.ExternalID, u.Username)
		a.skippedNew++
		return false
	})
	if err != nil {
		return fmt.Errorf("apply sync: create users: %w", err)
	}
	created := 0
	for n, i := range creations {
		if newIDs[n] == "" {
			continue // skipped by an email conflict raised during the insert
		}
		created++
		u := a.in.Users[i]
		a.subjectToUser[u.ExternalID] = newIDs[n]
		a.changes[newIDs[n]] = &userChange{created: true}
		status := statusInactive
		if u.Enabled {
			status = statusActive
		}
		a.auditEntry("organization.user.created_from_directory", "user", newIDs[n], nil,
			statusState{status, statusSourceDirectory})
	}
	a.counts["usersCreated"] = created
	a.counts["usersUpdated"] = len(updates) + len(invalidChanged)
	a.counts["usersUnchanged"] = len(a.in.Users) - created - a.counts["usersUpdated"] - a.skippedNew

	if len(unchanged) > 0 {
		if _, err := a.tx.Exec(ctx, `
			UPDATE organization.external_identities SET last_seen_at = $3
			WHERE provider_key = $1 AND external_subject = ANY($2::text[])`,
			a.in.ProviderKey, unchanged, a.observed); err != nil {
			return fmt.Errorf("apply sync: bump last_seen_at: %w", err)
		}
	}
	return nil
}

func derefOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// sweepNotObserved marks this provider's identities missing from the snapshot
// as no longer observed, unless the safeguard withheld the sweep.
func (a *syncApply) sweepNotObserved(ctx context.Context) error {
	if a.userSweepWithheld {
		return nil
	}
	subjects := make([]string, 0, len(a.in.Users))
	for _, u := range a.in.Users {
		subjects = append(subjects, u.ExternalID)
	}
	rows, err := a.tx.Query(ctx, `
		UPDATE organization.external_identities
		SET enabled = false, deleted_observed_at = $2, updated_at = $2
		WHERE provider_key = $1 AND deleted_observed_at IS NULL AND NOT (external_subject = ANY($3::text[]))
		RETURNING user_id::text`, a.in.ProviderKey, a.observed, subjects)
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
// or the sweep through changeUserStatus. Sessions of users that leave active
// are revoked at the end of the transaction.
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
	sc := statusChangeContext{Metadata: syncMetadata(a.in.ProviderKey, a.in.RunID), CorrelationID: a.in.RunID, At: a.observed}
	deactivated, err := changeUserStatus(ctx, a.tx, touched, statusTransition{
		From: statusActive, To: statusInactive, ToSource: statusSourceDirectory, DirectoryEnabled: false}, sc)
	if err != nil {
		return fmt.Errorf("apply sync: deactivate users: %w", err)
	}
	activated, err := changeUserStatus(ctx, a.tx, touched, statusTransition{
		From: statusInactive, FromSource: statusSourceDirectory, To: statusActive, ToSource: statusSourceDirectory, DirectoryEnabled: true}, sc)
	if err != nil {
		return fmt.Errorf("apply sync: activate users: %w", err)
	}
	for _, f := range append(deactivated, activated...) {
		c := a.change(f.UserID)
		c.statusChanged = true
		c.field("status")
		a.statusAudits = append(a.statusAudits, f.Audit)
		if f.LeftActive {
			a.revocations = append(a.revocations, f.UserID)
		}
	}
	a.counts["usersDeactivated"] = len(deactivated)
	a.counts["usersActivated"] = len(activated)
	return nil
}

func (a *syncApply) applyManagers(ctx context.Context) error {
	var ids []string
	var managers []*string
	for _, u := range a.in.Users {
		userID, ok := a.subjectToUser[u.ExternalID]
		if !ok || u.Invalid {
			continue // skipped by a conflict, or keeps its stored values
		}
		var desired *string
		switch {
		case u.ManagerUnresolved:
			a.conflict(application.ConflictManagerUnresolved, u.ExternalID, u.Username)
		case u.ManagerExternalID != nil:
			if mid, found := a.subjectToUser[*u.ManagerExternalID]; !found {
				a.conflict(application.ConflictManagerUnresolved, u.ExternalID, u.Username)
			} else if mid != userID { // a self-reference stays null without a conflict
				desired = &mid
			}
		}
		var current *string
		if row, existing := a.identities[u.ExternalID]; existing {
			current = row.manager
		}
		if ptrEq(desired, current) {
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
		UPDATE organization.users u SET manager_user_id = v.m, updated_at = $3
		FROM unnest($1::uuid[], $2::uuid[]) AS v(id, m)
		WHERE u.id = v.id`, ids, managers, a.observed); err != nil {
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
	inSnapshot := make(map[string]struct{}, len(a.in.Groups))
	var unchanged []string
	var updates, creations []int
	for i, g := range a.in.Groups {
		inSnapshot[g.ExternalID] = struct{}{}
		row, ok := existing[g.ExternalID]
		if !ok {
			creations = append(creations, i)
			continue
		}
		a.groupIDs[g.ExternalID] = row.id
		if row.deleted || row.displayName != g.DisplayName || !ptrEq(row.description, g.Description) {
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
			WHERE id = $1`, a.groupIDs[g.ExternalID], g.DisplayName, g.Description, a.observed)
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
			a.in.ProviderKey, g.ExternalID, g.DisplayName, g.Description, a.observed,
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
			UPDATE organization.directory_groups SET last_observed_at = $2 WHERE id = ANY($1::uuid[])`,
			unchanged, a.observed); err != nil {
			return fmt.Errorf("apply sync: bump groups: %w", err)
		}
	}

	// Groups missing from the snapshot, subject to the same sweep safeguard as users.
	groupsBefore := 0
	var missing []string
	for ext, row := range existing {
		if row.deleted {
			continue
		}
		groupsBefore++
		if _, ok := inSnapshot[ext]; !ok {
			missing = append(missing, row.id)
		}
	}
	if application.SweepWithheld(len(missing), groupsBefore, a.in.MaxMissingPercent) {
		a.groupSweepWithheld = true
		a.withheldGroupIDs = missing
		a.counts["groupsSweepWithheld"] = len(missing)
		if a.sweepFacts == nil {
			a.sweepFacts = map[string]int{}
		}
		a.sweepFacts["groupsBefore"], a.sweepFacts["groupsMissing"] = groupsBefore, len(missing)
		return nil
	}
	if len(missing) > 0 {
		if _, err := a.tx.Exec(ctx, `
			UPDATE organization.directory_groups SET deleted_observed_at = $2, updated_at = $2
			WHERE id = ANY($1::uuid[])`, missing, a.observed); err != nil {
			return fmt.Errorf("apply sync: mark groups not observed: %w", err)
		}
	}
	a.counts["groupsNotObserved"] = len(missing)
	return nil
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
	ts := a.observed
	withheld := a.withheldGroupIDs
	if withheld == nil {
		withheld = []string{}
	}

	// Memberships: freshness bump, close, open. Groups whose sweep was
	// withheld are not in the observed set and keep their rows.
	if _, err := a.tx.Exec(ctx, `
		UPDATE organization.directory_group_memberships m SET last_observed_at = $1
		FROM unnest($2::uuid[], $3::uuid[]) AS d(g, u)
		WHERE m.group_id = d.g AND m.user_id = d.u AND m.observed_until IS NULL`,
		ts, memberGroups, memberUsers); err != nil {
		return fmt.Errorf("apply sync: bump memberships: %w", err)
	}
	tag, err := a.tx.Exec(ctx, `
		WITH d AS (SELECT g, u FROM unnest($2::uuid[], $3::uuid[]) AS t(g, u))
		UPDATE organization.directory_group_memberships m SET observed_until = $1
		WHERE m.observed_until IS NULL
		  AND (m.group_id IN (SELECT id FROM organization.directory_groups WHERE provider_key = $5 AND deleted_observed_at IS NOT NULL)
		       OR (m.group_id = ANY($4::uuid[])
		           AND NOT EXISTS (SELECT 1 FROM d WHERE d.g = m.group_id AND d.u = m.user_id)))`,
		ts, memberGroups, memberUsers, observed, a.in.ProviderKey)
	if err != nil {
		return fmt.Errorf("apply sync: close memberships: %w", err)
	}
	a.counts["membershipsClosed"] = int(tag.RowsAffected())
	tag, err = a.tx.Exec(ctx, `
		WITH d AS (SELECT g, u FROM unnest($2::uuid[], $3::uuid[]) AS t(g, u))
		INSERT INTO organization.directory_group_memberships (group_id, user_id, observed_from, last_observed_at)
		SELECT d.g, d.u, $1, $1 FROM d
		WHERE NOT EXISTS (SELECT 1 FROM organization.directory_group_memberships m
		                  WHERE m.group_id = d.g AND m.user_id = d.u AND m.observed_until IS NULL)`,
		ts, memberGroups, memberUsers)
	if err != nil {
		return fmt.Errorf("apply sync: open memberships: %w", err)
	}
	a.counts["membershipsOpened"] = int(tag.RowsAffected())

	// Nesting: same maintenance over direct group-in-group edges. An edge to a
	// group whose sweep was withheld is not in any snapshot parent's member
	// set, but is left open with it.
	if _, err := a.tx.Exec(ctx, `
		UPDATE organization.directory_group_nesting n SET last_observed_at = $1
		FROM unnest($2::uuid[], $3::uuid[]) AS d(p, c)
		WHERE n.parent_group_id = d.p AND n.child_group_id = d.c AND n.observed_until IS NULL`,
		ts, parents, children); err != nil {
		return fmt.Errorf("apply sync: bump nesting: %w", err)
	}
	tag, err = a.tx.Exec(ctx, `
		WITH d AS (SELECT p, c FROM unnest($2::uuid[], $3::uuid[]) AS t(p, c)),
		     gone AS (SELECT id FROM organization.directory_groups WHERE provider_key = $5 AND deleted_observed_at IS NOT NULL)
		UPDATE organization.directory_group_nesting n SET observed_until = $1
		WHERE n.observed_until IS NULL
		  AND (n.parent_group_id IN (SELECT id FROM gone) OR n.child_group_id IN (SELECT id FROM gone)
		       OR (n.parent_group_id = ANY($4::uuid[])
		           AND NOT (n.child_group_id = ANY($6::uuid[]))
		           AND NOT EXISTS (SELECT 1 FROM d WHERE d.p = n.parent_group_id AND d.c = n.child_group_id)))`,
		ts, parents, children, observed, a.in.ProviderKey, withheld)
	if err != nil {
		return fmt.Errorf("apply sync: close nesting: %w", err)
	}
	a.counts["nestingClosed"] = int(tag.RowsAffected())
	tag, err = a.tx.Exec(ctx, `
		WITH d AS (SELECT p, c FROM unnest($2::uuid[], $3::uuid[]) AS t(p, c))
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

// auditSweepWithheld records a withheld sweep, in the sync transaction.
func (a *syncApply) auditSweepWithheld(context.Context) error {
	if !a.userSweepWithheld && !a.groupSweepWithheld {
		return nil
	}
	meta := syncMetadata(a.in.ProviderKey, a.in.RunID)
	meta["maxMissingPercent"] = a.in.MaxMissingPercent
	meta["usersSweepWithheld"] = a.counts["usersSweepWithheld"]
	meta["groupsSweepWithheld"] = a.counts["groupsSweepWithheld"]
	for k, v := range a.sweepFacts {
		meta[k] = v
	}
	a.queueAudit("organization.directory_sync.sweep_withheld", "directory_sync_run", a.in.RunID, nil, nil, meta)
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
	total := len(a.statusAudits) + len(eventUsers)
	if total == 0 && len(a.audits) == 0 {
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
	for _, c := range a.audits {
		if err := audit.Record(ctx, a.tx, c); err != nil {
			return err
		}
	}
	for _, e := range a.statusAudits {
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
			ID: ids[next], EventType: "UserSynchronized", EventVersion: 1, OccurredAt: a.observed,
			CorrelationID: a.in.RunID, Payload: raw,
		}); err != nil {
			return err
		}
		next++
	}
	return nil
}

// revokeSessions revokes the sessions of users that left active, last, so the
// locks on platform.sessions are held only briefly.
func (a *syncApply) revokeSessions(ctx context.Context) error {
	n, err := revokeSessionsOfLeftActive(ctx, a.tx, a.revocations, syncActor, a.in.RunID, a.observed)
	if err != nil {
		return fmt.Errorf("apply sync: %w", err)
	}
	a.counts["sessionsRevoked"] += n
	return nil
}

func (a *syncApply) finishRun(ctx context.Context, outcome string) error {
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
	finishedAt := a.observed
	if a.in.Now != nil {
		finishedAt = a.in.Now()
	}
	tag, err := a.tx.Exec(ctx, `
		UPDATE organization.directory_sync_runs
		SET outcome = $2, observed_at = $3, finished_at = $4, counts = $5, conflicts = $6, conflict_count = $7
		WHERE id = $1 AND outcome = 'running'`,
		a.in.RunID, outcome, a.observed, finishedAt, counts, rawConflicts, a.conflictCount)
	if err != nil {
		return fmt.Errorf("apply sync: finish run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("apply sync: finish run: run is not running")
	}
	return nil
}
