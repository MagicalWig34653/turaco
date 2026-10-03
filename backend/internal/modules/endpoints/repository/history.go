package repository

import (
	"context"
	"fmt"
	"time"

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

// AssignmentEvents derives the assignment events of one artifact, or of the artifacts addressing a Device. An
// assignment row opens an event at valid_from (added; changed when the previous row of the same provider
// assignment closed at that very instant) and a row that closed without a successor at that instant is a removal.
func (r *Repository) AssignmentEvents(ctx context.Context, q application.AssignmentEventQuery) ([]application.AssignmentEventRow, error) {
	var artifact, device *string
	if q.ArtifactID != "" {
		artifact = &q.ArtifactID
	}
	if q.DeviceID != "" {
		device = &q.DeviceID
	}
	at, key := cursorArgs(q.After)
	rows, err := r.q(ctx).Query(ctx, `
		WITH scope AS (
			SELECT $1::uuid AS artifact_id WHERE $1::uuid IS NOT NULL
			UNION
			SELECT x.artifact_id FROM endpoints.management_assignments x
			WHERE $3::uuid IS NOT NULL AND x.provider = $2 AND (x.target_kind = 'all_devices' OR (x.target_kind = 'group' AND x.target_group_external_id IN (
				SELECT g.group_external_id FROM endpoints.device_group_memberships g WHERE g.device_id = $3::uuid AND g.provider = $2)))
		), rws AS (
			SELECT a.id, a.artifact_id, a.provider_assignment_id, a.target_kind, a.target_group_external_id, a.mode, a.intent, a.filter_id, a.filter_mode,
				a.source, a.valid_from, a.valid_until,
				lag(a.id) OVER w AS prev_id, lag(a.target_kind) OVER w AS p_kind, lag(a.target_group_external_id) OVER w AS p_group,
				lag(a.mode) OVER w AS p_mode, lag(a.intent) OVER w AS p_intent, lag(a.filter_id) OVER w AS p_filter,
				lag(a.filter_mode) OVER w AS p_fmode, lag(a.valid_until) OVER w AS p_until, lead(a.valid_from) OVER w AS n_from
			FROM endpoints.management_assignments a
			WHERE a.artifact_id IN (SELECT artifact_id FROM scope)
			WINDOW w AS (PARTITION BY a.artifact_id, a.provider_assignment_id ORDER BY a.valid_from, a.id)
		), ev AS (
			SELECT r.valid_from AS occurred_at, 'a:' || r.id::text || ':o' AS key,
				CASE WHEN r.prev_id IS NULL OR r.p_until IS DISTINCT FROM r.valid_from THEN 'assignment_added' ELSE 'assignment_changed' END AS kind, r.*
			FROM rws r
			UNION ALL
			SELECT r.valid_until, 'a:' || r.id::text || ':c', 'assignment_removed', r.*
			FROM rws r WHERE r.valid_until IS NOT NULL AND (r.n_from IS NULL OR r.n_from <> r.valid_until)
		)
		SELECT e.key, e.kind, e.occurred_at, e.source, e.artifact_id::text, ar.name, ar.kind, ar.deleted_observed_at IS NOT NULL,
			e.id::text, e.provider_assignment_id, e.target_kind, e.target_group_external_id, e.mode, e.intent, e.filter_id::text, e.filter_mode, f.name,
			e.p_kind, e.p_group, e.p_mode, e.p_intent, e.p_filter::text, e.p_fmode
		FROM ev e
		JOIN endpoints.management_artifacts ar ON ar.id = e.artifact_id
		LEFT JOIN endpoints.management_filters f ON f.id = e.filter_id
		WHERE ($3::uuid IS NULL OR (`+deviceMatch("e.target_kind", "e.target_group_external_id", "e.occurred_at")+`
				OR (e.kind = 'assignment_changed' AND `+deviceMatch("e.p_kind", "e.p_group", "e.occurred_at")+`)))
			AND ($4::timestamptz IS NULL OR (e.occurred_at, e.key COLLATE "C") < ($4::timestamptz, $5::text COLLATE "C"))
		ORDER BY e.occurred_at DESC, e.key COLLATE "C" DESC LIMIT $6`,
		artifact, q.Provider, device, at, key, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("assignment events: %w", err)
	}
	defer rows.Close()
	out := []application.AssignmentEventRow{}
	for rows.Next() {
		var e application.AssignmentEventRow
		var pKind, pGroup, pMode, pIntent, pFilter, pFMode *string
		if err := rows.Scan(&e.Key, &e.Kind, &e.OccurredAt, &e.Source, &e.ArtifactID, &e.ArtifactName, &e.ArtifactKind, &e.ArtifactDeleted,
			&e.AssignmentID, &e.ProviderAssignmentID, &e.Cur.TargetKind, &e.Cur.Group, &e.Cur.Mode, &e.Cur.Intent, &e.Cur.FilterID, &e.Cur.FilterMode, &e.Cur.FilterName,
			&pKind, &pGroup, &pMode, &pIntent, &pFilter, &pFMode); err != nil {
			return nil, fmt.Errorf("assignment events: scan: %w", err)
		}
		e.ObservedAt = e.OccurredAt
		if pKind != nil {
			e.Prev = &application.AssignmentValues{TargetKind: *pKind, Group: pGroup, Mode: str(pMode), Intent: str(pIntent), FilterID: pFilter, FilterMode: str(pFMode)}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ObservationEvents lists the Device's observation state changes: one entry for the first sighting of an artifact
// and one per change of the normalized state (the history table is written on those only).
func (r *Repository) ObservationEvents(ctx context.Context, deviceID string, after *application.HistoryCursor, limit int) ([]application.ObservationEventRow, error) {
	at, key := cursorArgs(after)
	rows, err := r.q(ctx).Query(ctx, `
		WITH h AS (
			SELECT id, artifact_id, normalized_state, raw_status, source, observed_at, 'o:' || id::text AS key,
				lag(normalized_state) OVER (PARTITION BY artifact_id ORDER BY observed_at, id) AS prev_state
			FROM endpoints.management_observation_history WHERE device_id = $1::uuid
		)
		SELECT h.key, h.observed_at, h.source, h.artifact_id::text, COALESCE(a.name, ''), COALESCE(a.kind, ''), COALESCE(a.deleted_observed_at IS NOT NULL, true),
			h.normalized_state, h.prev_state, h.raw_status
		FROM h LEFT JOIN endpoints.management_artifacts a ON a.id = h.artifact_id
		WHERE ($2::timestamptz IS NULL OR (h.observed_at, h.key COLLATE "C") < ($2::timestamptz, $3::text COLLATE "C"))
		ORDER BY h.observed_at DESC, h.key COLLATE "C" DESC LIMIT $4`, deviceID, at, key, limit)
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
		WITH e AS (
			SELECT observed_from AS occurred_at, 'm:' || id::text || ':j' AS key, 'group_joined' AS kind, group_external_id, source
			FROM endpoints.device_group_memberships WHERE device_id = $1::uuid
			UNION ALL
			SELECT observed_until, 'm:' || id::text || ':l', 'group_left', group_external_id, source
			FROM endpoints.device_group_memberships WHERE device_id = $1::uuid AND observed_until IS NOT NULL
		)
		SELECT e.key, e.kind, e.occurred_at, e.source, e.group_external_id FROM e
		WHERE ($2::timestamptz IS NULL OR (e.occurred_at, e.key COLLATE "C") < ($2::timestamptz, $3::text COLLATE "C"))
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

func (r *Repository) ObservationStateSince(ctx context.Context, deviceID string, artifactIDs []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(artifactIDs) == 0 {
		return out, nil
	}
	rows, err := r.q(ctx).Query(ctx, `
		SELECT DISTINCT ON (artifact_id) artifact_id::text, observed_at FROM endpoints.management_observation_history
		WHERE device_id = $1::uuid AND artifact_id = ANY($2::uuid[]) ORDER BY artifact_id, observed_at DESC, id DESC`, deviceID, artifactIDs)
	if err != nil {
		return nil, fmt.Errorf("observation state since: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, fmt.Errorf("observation state since: scan: %w", err)
		}
		out[id] = at
	}
	return out, rows.Err()
}
