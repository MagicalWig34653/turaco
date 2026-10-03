package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// History reads (F6 slice 4). The change history is derived from the interval and change tables ingestion
// already keeps; there is no per-sync record. Every list is newest first by (occurred_at, key) with the key
// compared bytewise (COLLATE "C"), so the keyset cursor and the merge in the service agree.

func cursorArgs(after *application.HistoryCursor) (at *time.Time, key *string) {
	if after == nil {
		return nil, nil
	}
	return &after.At, &after.Key
}

// deviceMatch is true when an assignment target addresses the Device at the time: all_devices, or a provider group
// the Device was a member of then.
func deviceMatch(kind, group, at string) string {
	return fmt.Sprintf(`(%[1]s = 'all_devices' OR (%[1]s = 'group' AND EXISTS (
		SELECT 1 FROM endpoints.device_group_memberships m
		WHERE m.device_id = $3::uuid AND m.provider = $2 AND m.group_external_id = %[2]s
			AND m.observed_from <= %[3]s AND (m.observed_until IS NULL OR m.observed_until >= %[3]s))))`, kind, group, at)
}

// historyArtifactScope returns the artifacts whose assignment history is read: the one artifact, or the artifacts
// that ever addressed the Device (all_devices, or a group the Device is or was a member of), at most
// application.MaxHistoryArtifacts in id order. truncated is set when more qualified.
func (r *Repository) historyArtifactScope(ctx context.Context, q application.AssignmentEventQuery) (ids []string, truncated bool, err error) {
	if q.ArtifactID != "" {
		return []string{q.ArtifactID}, false, nil
	}
	rows, err := r.q(ctx).Query(ctx, `
		SELECT x.artifact_id::text FROM endpoints.management_assignments x
		WHERE x.provider = $1 AND (x.target_kind = 'all_devices' OR (x.target_kind = 'group' AND x.target_group_external_id IN (
			SELECT g.group_external_id FROM endpoints.device_group_memberships g WHERE g.device_id = $2::uuid AND g.provider = $1)))
		GROUP BY x.artifact_id ORDER BY x.artifact_id LIMIT $3`, q.Provider, q.DeviceID, application.MaxHistoryArtifacts+1)
	if err != nil {
		return nil, false, fmt.Errorf("history scope: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, fmt.Errorf("history scope: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("history scope: %w", err)
	}
	if len(ids) > application.MaxHistoryArtifacts {
		ids, truncated = ids[:application.MaxHistoryArtifacts], true
	}
	return ids, truncated, nil
}

// AssignmentEvents derives the assignment events of one artifact, or of the artifacts addressing a Device. An
// assignment row opens an event at valid_from (added; changed when the previous row of the same provider
// assignment closed at that very instant) and a row that closed without a successor at that instant is a removal.
// The cursor, the Device match and the limit are applied inside the two base scans (opens and closes), so a page
// reads about limit rows per scan and finds the previous row of a change by an index lookup, not by a window over
// the whole history. truncated reports that the Device scope was cut at MaxHistoryArtifacts artifacts.
func (r *Repository) AssignmentEvents(ctx context.Context, q application.AssignmentEventQuery) (rows []application.AssignmentEventRow, truncated bool, err error) {
	scope, truncated, err := r.historyArtifactScope(ctx, q)
	if err != nil {
		return nil, false, err
	}
	if len(scope) == 0 {
		return []application.AssignmentEventRow{}, truncated, nil
	}
	var device *string
	if q.DeviceID != "" {
		device = &q.DeviceID
	}
	at, key := cursorArgs(q.After)
	const cols = `a.id, a.artifact_id, a.provider_assignment_id, a.target_kind, a.target_group_external_id, a.mode, a.intent, a.filter_id, a.filter_mode, a.source`
	res, err := r.q(ctx).Query(ctx, `
		WITH opens AS (
			SELECT a.valid_from AS occurred_at, 'a:' || a.id::text || ':o' AS key,
				CASE WHEN p.id IS NOT NULL AND p.valid_until = a.valid_from THEN 'assignment_changed' ELSE 'assignment_added' END AS kind, `+cols+`,
				CASE WHEN p.valid_until = a.valid_from THEN p.target_kind END AS p_kind, CASE WHEN p.valid_until = a.valid_from THEN p.target_group_external_id END AS p_group,
				CASE WHEN p.valid_until = a.valid_from THEN p.mode END AS p_mode, CASE WHEN p.valid_until = a.valid_from THEN p.intent END AS p_intent,
				CASE WHEN p.valid_until = a.valid_from THEN p.filter_id END AS p_filter, CASE WHEN p.valid_until = a.valid_from THEN p.filter_mode END AS p_fmode
			FROM endpoints.management_assignments a
			LEFT JOIN LATERAL (
				SELECT x.id, x.valid_until, x.target_kind, x.target_group_external_id, x.mode, x.intent, x.filter_id, x.filter_mode
				FROM endpoints.management_assignments x
				WHERE x.artifact_id = a.artifact_id AND x.provider_assignment_id = a.provider_assignment_id AND (x.valid_from, x.id) < (a.valid_from, a.id)
				ORDER BY x.valid_from DESC, x.id DESC LIMIT 1) p ON true
			WHERE a.artifact_id = ANY($1::uuid[])
				AND ($4::timestamptz IS NULL OR (a.valid_from <= $4::timestamptz AND (a.valid_from, ('a:' || a.id::text || ':o') COLLATE "C") < ($4::timestamptz, $5::text COLLATE "C")))
				AND ($3::uuid IS NULL OR (`+deviceMatch("a.target_kind", "a.target_group_external_id", "a.valid_from")+`
					OR (p.valid_until = a.valid_from AND `+deviceMatch("p.target_kind", "p.target_group_external_id", "a.valid_from")+`)))
			ORDER BY a.valid_from DESC, ('a:' || a.id::text || ':o') COLLATE "C" DESC LIMIT $6
		), closes AS (
			SELECT a.valid_until AS occurred_at, 'a:' || a.id::text || ':c' AS key, 'assignment_removed' AS kind, `+cols+`,
				NULL::text AS p_kind, NULL::text AS p_group, NULL::text AS p_mode, NULL::text AS p_intent, NULL::uuid AS p_filter, NULL::text AS p_fmode
			FROM endpoints.management_assignments a
			WHERE a.artifact_id = ANY($1::uuid[]) AND a.valid_until IS NOT NULL
				AND NOT EXISTS (SELECT 1 FROM endpoints.management_assignments n
					WHERE n.artifact_id = a.artifact_id AND n.provider_assignment_id = a.provider_assignment_id
						AND n.valid_from = a.valid_until AND (n.valid_from, n.id) > (a.valid_from, a.id))
				AND ($4::timestamptz IS NULL OR (a.valid_until <= $4::timestamptz AND (a.valid_until, ('a:' || a.id::text || ':c') COLLATE "C") < ($4::timestamptz, $5::text COLLATE "C")))
				AND ($3::uuid IS NULL OR `+deviceMatch("a.target_kind", "a.target_group_external_id", "a.valid_until")+`)
			ORDER BY a.valid_until DESC, ('a:' || a.id::text || ':c') COLLATE "C" DESC LIMIT $6
		), ev AS (
			SELECT * FROM (SELECT * FROM opens UNION ALL SELECT * FROM closes) u
			ORDER BY u.occurred_at DESC, u.key COLLATE "C" DESC LIMIT $6
		)
		SELECT e.key, e.kind, e.occurred_at, e.source, e.artifact_id::text, ar.name, ar.kind, ar.deleted_observed_at IS NOT NULL,
			e.id::text, e.provider_assignment_id, e.target_kind, e.target_group_external_id, e.mode, e.intent, e.filter_id::text, e.filter_mode, f.name,
			e.p_kind, e.p_group, e.p_mode, e.p_intent, e.p_filter::text, e.p_fmode
		FROM ev e
		JOIN endpoints.management_artifacts ar ON ar.id = e.artifact_id
		LEFT JOIN endpoints.management_filters f ON f.id = e.filter_id
		ORDER BY e.occurred_at DESC, e.key COLLATE "C" DESC`,
		scope, q.Provider, device, at, key, q.Limit)
	if err != nil {
		return nil, false, fmt.Errorf("assignment events: %w", err)
	}
	defer res.Close()
	out := []application.AssignmentEventRow{}
	for res.Next() {
		var e application.AssignmentEventRow
		var pKind, pGroup, pMode, pIntent, pFilter, pFMode *string
		if err := res.Scan(&e.Key, &e.Kind, &e.OccurredAt, &e.Source, &e.ArtifactID, &e.ArtifactName, &e.ArtifactKind, &e.ArtifactDeleted,
			&e.AssignmentID, &e.ProviderAssignmentID, &e.Cur.TargetKind, &e.Cur.Group, &e.Cur.Mode, &e.Cur.Intent, &e.Cur.FilterID, &e.Cur.FilterMode, &e.Cur.FilterName,
			&pKind, &pGroup, &pMode, &pIntent, &pFilter, &pFMode); err != nil {
			return nil, false, fmt.Errorf("assignment events: scan: %w", err)
		}
		e.ObservedAt = e.OccurredAt
		if pKind != nil {
			e.Prev = &application.AssignmentValues{TargetKind: *pKind, Group: pGroup, Mode: str(pMode), Intent: str(pIntent), FilterID: pFilter, FilterMode: str(pFMode)}
		}
		out = append(out, e)
	}
	return out, truncated, res.Err()
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ObservationEvents lists the Device's observation state changes: one entry for the first sighting of an artifact
// and one per change of the normalized state or return of a retired observation (the history table is written on
// those only). The cursor and limit apply to the base scan; the previous state is an index lookup per returned row.
func (r *Repository) ObservationEvents(ctx context.Context, deviceID string, after *application.HistoryCursor, limit int) ([]application.ObservationEventRow, error) {
	at, key := cursorArgs(after)
	rows, err := r.q(ctx).Query(ctx, `
		WITH h AS (
			SELECT x.id, x.artifact_id, x.normalized_state, x.raw_status, x.source, x.observed_at, 'o:' || x.id::text AS key
			FROM endpoints.management_observation_history x
			WHERE x.device_id = $1::uuid
				AND ($2::timestamptz IS NULL OR (x.observed_at <= $2::timestamptz AND (x.observed_at, ('o:' || x.id::text) COLLATE "C") < ($2::timestamptz, $3::text COLLATE "C")))
			ORDER BY x.observed_at DESC, ('o:' || x.id::text) COLLATE "C" DESC LIMIT $4
		)
		SELECT h.key, h.observed_at, h.source, h.artifact_id::text, COALESCE(a.name, ''), COALESCE(a.kind, ''), COALESCE(a.deleted_observed_at IS NOT NULL, true),
			h.normalized_state, pv.normalized_state, h.raw_status
		FROM h LEFT JOIN endpoints.management_artifacts a ON a.id = h.artifact_id
		LEFT JOIN LATERAL (
			SELECT p.normalized_state FROM endpoints.management_observation_history p
			WHERE p.artifact_id = h.artifact_id AND p.device_id = $1::uuid AND (p.observed_at, p.id) < (h.observed_at, h.id)
			ORDER BY p.observed_at DESC, p.id DESC LIMIT 1) pv ON true
		ORDER BY h.observed_at DESC, h.key COLLATE "C" DESC`, deviceID, at, key, limit)
	if err != nil {
		return nil, fmt.Errorf("observation events: %w", err)
	}
	defer rows.Close()
	out := []application.ObservationEventRow{}
	for rows.Next() {
		var e application.ObservationEventRow
		if err := rows.Scan(&e.Key, &e.OccurredAt, &e.Source, &e.ArtifactID, &e.ArtifactName, &e.ArtifactKind, &e.ArtifactDeleted, &e.State, &e.PrevState, &e.RawStatus); err != nil {
			return nil, fmt.Errorf("observation events: scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MembershipEvents lists the Device joining and leaving provider groups (the interval table).
func (r *Repository) MembershipEvents(ctx context.Context, deviceID string, after *application.HistoryCursor, limit int) ([]application.MembershipEventRow, error) {
	at, key := cursorArgs(after)
	rows, err := r.q(ctx).Query(ctx, `
		WITH j AS (
			SELECT observed_from AS occurred_at, 'm:' || id::text || ':j' AS key, 'group_joined' AS kind, group_external_id, source
			FROM endpoints.device_group_memberships
			WHERE device_id = $1::uuid
				AND ($2::timestamptz IS NULL OR (observed_from <= $2::timestamptz AND (observed_from, ('m:' || id::text || ':j') COLLATE "C") < ($2::timestamptz, $3::text COLLATE "C")))
			ORDER BY observed_from DESC, ('m:' || id::text || ':j') COLLATE "C" DESC LIMIT $4
		), l AS (
			SELECT observed_until AS occurred_at, 'm:' || id::text || ':l' AS key, 'group_left' AS kind, group_external_id, source
			FROM endpoints.device_group_memberships
			WHERE device_id = $1::uuid AND observed_until IS NOT NULL
				AND ($2::timestamptz IS NULL OR (observed_until <= $2::timestamptz AND (observed_until, ('m:' || id::text || ':l') COLLATE "C") < ($2::timestamptz, $3::text COLLATE "C")))
			ORDER BY observed_until DESC, ('m:' || id::text || ':l') COLLATE "C" DESC LIMIT $4
		)
		SELECT e.key, e.kind, e.occurred_at, e.source, e.group_external_id FROM (SELECT * FROM j UNION ALL SELECT * FROM l) e
		ORDER BY e.occurred_at DESC, e.key COLLATE "C" DESC LIMIT $4`, deviceID, at, key, limit)
	if err != nil {
		return nil, fmt.Errorf("membership events: %w", err)
	}
	defer rows.Close()
	out := []application.MembershipEventRow{}
	for rows.Next() {
		var e application.MembershipEventRow
		if err := rows.Scan(&e.Key, &e.Kind, &e.OccurredAt, &e.Source, &e.GroupExternalID); err != nil {
			return nil, fmt.Errorf("membership events: scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LiveDevicesPage is a keyset page (ascending id after afterID) of the provider's live Devices.
func (r *Repository) LiveDevicesPage(ctx context.Context, provider, afterID string, limit int) ([]application.Device, error) {
	if afterID == "" {
		afterID = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT `+deviceColumns+` FROM endpoints.devices
		WHERE provider = $1 AND deleted_observed_at IS NULL AND id > $2::uuid ORDER BY id LIMIT $3`, provider, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("live devices page: %w", err)
	}
	return r.scanDevices(rows, "live devices page")
}

// CountLiveDevicesAfter counts the provider's live Devices after afterID.
func (r *Repository) CountLiveDevicesAfter(ctx context.Context, provider, afterID string) (int, error) {
	if afterID == "" {
		afterID = "00000000-0000-0000-0000-000000000000"
	}
	var n int
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(*) FROM endpoints.devices
		WHERE provider = $1 AND deleted_observed_at IS NULL AND id > $2::uuid`, provider, afterID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count live devices: %w", err)
	}
	return n, nil
}

func (r *Repository) ArtifactsWithObservations(ctx context.Context, artifactIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(artifactIDs) == 0 {
		return out, nil
	}
	rows, err := r.q(ctx).Query(ctx, `
		SELECT t.id::text FROM unnest($1::uuid[]) AS t(id)
		WHERE EXISTS (SELECT 1 FROM endpoints.management_observations o WHERE o.artifact_id = t.id AND o.retired_at IS NULL)`, artifactIDs)
	if err != nil {
		return nil, fmt.Errorf("artifacts with observations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("artifacts with observations: scan: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ObservationStateSince returns, per pair, when the current observed state began: the newest history row.
func (r *Repository) ObservationStateSince(ctx context.Context, pairs []application.ObsPair) (map[application.ObsPair]time.Time, error) {
	out := map[application.ObsPair]time.Time{}
	if len(pairs) == 0 {
		return out, nil
	}
	arts, devs := splitPairs(pairs)
	rows, err := r.q(ctx).Query(ctx, `
		SELECT t.artifact_id::text, t.device_id::text, h.observed_at
		FROM unnest($1::uuid[], $2::uuid[]) AS t(artifact_id, device_id)
		CROSS JOIN LATERAL (
			SELECT x.observed_at FROM endpoints.management_observation_history x
			WHERE x.artifact_id = t.artifact_id AND x.device_id = t.device_id ORDER BY x.observed_at DESC, x.id DESC LIMIT 1) h`, arts, devs)
	if err != nil {
		return nil, fmt.Errorf("observation state since: %w", err)
	}
	return scanPairTimes(rows, out, "observation state since")
}

// ObservationsRetiredAt returns, per pair, when its observation was retired; pairs without a retired observation
// are absent from the result.
func (r *Repository) ObservationsRetiredAt(ctx context.Context, pairs []application.ObsPair) (map[application.ObsPair]time.Time, error) {
	out := map[application.ObsPair]time.Time{}
	if len(pairs) == 0 {
		return out, nil
	}
	arts, devs := splitPairs(pairs)
	rows, err := r.q(ctx).Query(ctx, `
		SELECT o.artifact_id::text, o.device_id::text, o.retired_at
		FROM unnest($1::uuid[], $2::uuid[]) AS t(artifact_id, device_id)
		JOIN endpoints.management_observations o ON o.artifact_id = t.artifact_id AND o.device_id = t.device_id
		WHERE o.retired_at IS NOT NULL`, arts, devs)
	if err != nil {
		return nil, fmt.Errorf("observations retired at: %w", err)
	}
	return scanPairTimes(rows, out, "observations retired at")
}

func splitPairs(pairs []application.ObsPair) (arts, devs []string) {
	arts, devs = make([]string, len(pairs)), make([]string, len(pairs))
	for i, p := range pairs {
		arts[i], devs[i] = p.ArtifactID, p.DeviceID
	}
	return arts, devs
}

func scanPairTimes(rows pgx.Rows, out map[application.ObsPair]time.Time, what string) (map[application.ObsPair]time.Time, error) {
	defer rows.Close()
	for rows.Next() {
		var p application.ObsPair
		var at time.Time
		if err := rows.Scan(&p.ArtifactID, &p.DeviceID, &at); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", what, err)
		}
		out[p] = at
	}
	return out, rows.Err()
}

// IneffectiveCursor is the device id after which the next assignment_ineffective pass starts ("" = the beginning).
func (r *Repository) IneffectiveCursor(ctx context.Context, provider string) (string, error) {
	var c *string
	err := r.q(ctx).QueryRow(ctx, `SELECT ineffective_cursor::text FROM endpoints.provider_sync_state WHERE provider = $1`, provider).Scan(&c)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && c == nil) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("ineffective cursor: %w", err)
	}
	return *c, nil
}

// SaveIneffectiveCursorTx stores the cursor ("" clears it); the state row is created when the provider never completed a sync.
func (r *Repository) SaveIneffectiveCursorTx(ctx context.Context, tx pgx.Tx, provider, cursor string) error {
	var c *string
	if cursor != "" {
		c = &cursor
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO endpoints.provider_sync_state (provider, ineffective_cursor) VALUES ($1, $2::uuid)
		ON CONFLICT (provider) DO UPDATE SET ineffective_cursor = EXCLUDED.ineffective_cursor`, provider, c); err != nil {
		return fmt.Errorf("save ineffective cursor: %w", err)
	}
	return nil
}
