package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// proposalRow is one ai.proposals row.
type proposalRow struct {
	ID             string
	TenantID       string
	UserID         string
	SessionRef     *string
	ToolName       string
	Parameters     json.RawMessage
	ParametersHash string
	TargetType     string
	TargetID       string
	TargetVersion  int
	TargetLabel    *string
	State          string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	ConfirmedAt    *time.Time
	EndedAt        *time.Time
	LeaseExpiresAt *time.Time
	Attempt        int
	OutcomeCode    *string
	ResultType     *string
	ResultID       *string
	Version        int
	// Now is the database time of the read, so expiry is judged on one clock.
	Now time.Time
}

const proposalCols = `id::text, tenant_id, user_id::text, session_ref::text, tool_name, parameters, parameters_hash, target_type,
	target_id::text, target_version, target_label, state, created_at, expires_at, confirmed_at, ended_at, lease_expires_at, attempt,
	outcome_code, result_type, result_id::text, version, now()`

func scanProposal(row pgx.Row) (proposalRow, error) {
	var p proposalRow
	var attempt int16
	var params []byte
	err := row.Scan(&p.ID, &p.TenantID, &p.UserID, &p.SessionRef, &p.ToolName, &params, &p.ParametersHash, &p.TargetType,
		&p.TargetID, &p.TargetVersion, &p.TargetLabel, &p.State, &p.CreatedAt, &p.ExpiresAt, &p.ConfirmedAt, &p.EndedAt,
		&p.LeaseExpiresAt, &attempt, &p.OutcomeCode, &p.ResultType, &p.ResultID, &p.Version, &p.Now)
	p.Attempt = int(attempt)
	if params != nil {
		p.Parameters = params
	}
	return p, err
}

// insertProposal stores a new proposal unless the User already has maxOpen open ones. The per-User advisory lock
// makes the count and the insert one decision under concurrent turns.
func (s *Store) insertProposal(ctx context.Context, tx pgx.Tx, p proposalRow, ttl time.Duration, maxOpen int) (proposalRow, bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ai.proposals:' || $1, 0))`, p.UserID); err != nil {
		return proposalRow{}, false, fmt.Errorf("lock proposals: %w", err)
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ai.proposals WHERE user_id=$1 AND tenant_id=$2
		AND (state='confirmed' OR (state='proposed' AND expires_at > now()))`, p.UserID, p.TenantID).Scan(&open); err != nil {
		return proposalRow{}, false, fmt.Errorf("count open proposals: %w", err)
	}
	if open >= maxOpen {
		return proposalRow{}, false, nil
	}
	out, err := scanProposal(tx.QueryRow(ctx, `INSERT INTO ai.proposals (tenant_id, user_id, session_ref, tool_name, parameters,
		parameters_hash, target_type, target_id, target_version, target_label, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, now() + $11::interval) RETURNING `+proposalCols,
		p.TenantID, p.UserID, p.SessionRef, p.ToolName, []byte(p.Parameters), p.ParametersHash, p.TargetType, p.TargetID, p.TargetVersion,
		p.TargetLabel, fmt.Sprintf("%d seconds", int(ttl.Seconds()))))
	if err != nil {
		return proposalRow{}, false, fmt.Errorf("insert proposal: %w", err)
	}
	return out, true, nil
}

// getProposal returns the proposal only to its creating User in its tenant; anything else is ErrNotFound.
func (s *Store) getProposal(ctx context.Context, q pgx.Row) (proposalRow, error) {
	p, err := scanProposal(q)
	if errors.Is(err, pgx.ErrNoRows) {
		return proposalRow{}, ErrNotFound
	}
	return p, err
}

func (s *Store) findProposal(ctx context.Context, c Caller, id string) (proposalRow, error) {
	return s.getProposal(ctx, s.pool.QueryRow(ctx, `SELECT `+proposalCols+` FROM ai.proposals WHERE id=$1 AND user_id=$2 AND tenant_id=$3`,
		id, c.UserID, c.TenantID))
}

// listProposals returns the User's open proposals and those that ended within the last day, newest first.
func (s *Store) listProposals(ctx context.Context, c Caller, limit int) ([]proposalRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+proposalCols+` FROM ai.proposals WHERE user_id=$1 AND tenant_id=$2
		AND (ended_at IS NULL OR ended_at > now() - interval '1 day') ORDER BY created_at DESC, id DESC LIMIT $3`, c.UserID, c.TenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	defer rows.Close()
	out := []proposalRow{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, fmt.Errorf("scan proposal: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// claimProposal is the single conditional transition proposed -> confirmed (single-use, race-safe): only the
// creating User, only while proposed and unexpired, only with the stored hash. It returns the claim token.
func (s *Store) claimProposal(ctx context.Context, tx pgx.Tx, c Caller, id, hash string, lease time.Duration) (proposalRow, string, bool, error) {
	var token string
	p, err := scanProposal(tx.QueryRow(ctx, `UPDATE ai.proposals SET state='confirmed', confirmed_at=now(), claim_token=uuidv7(),
		lease_expires_at = now() + $5::interval, attempt=1, version=version+1
		WHERE id=$1 AND user_id=$2 AND tenant_id=$3 AND parameters_hash=$4 AND state='proposed' AND expires_at > now()
		RETURNING `+proposalCols, id, c.UserID, c.TenantID, hash, fmt.Sprintf("%d seconds", int(lease.Seconds()))))
	if errors.Is(err, pgx.ErrNoRows) {
		return proposalRow{}, "", false, nil
	}
	if err != nil {
		return proposalRow{}, "", false, fmt.Errorf("claim proposal: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT claim_token::text FROM ai.proposals WHERE id=$1`, id).Scan(&token); err != nil {
		return proposalRow{}, "", false, err
	}
	return p, token, true, nil
}

// reclaimProposal takes over a confirmed proposal whose lease expired without an outcome (the earlier execution
// crashed or timed out), at most maxAttempts times in total. The module call is idempotent per proposal id.
func (s *Store) reclaimProposal(ctx context.Context, tx pgx.Tx, c Caller, id, hash string, lease time.Duration, maxAttempts int) (proposalRow, string, bool, error) {
	p, err := scanProposal(tx.QueryRow(ctx, `UPDATE ai.proposals SET claim_token=uuidv7(), lease_expires_at = now() + $5::interval,
		attempt=attempt+1, version=version+1
		WHERE id=$1 AND user_id=$2 AND tenant_id=$3 AND parameters_hash=$4 AND state='confirmed' AND lease_expires_at <= now() AND attempt < $6
		RETURNING `+proposalCols, id, c.UserID, c.TenantID, hash, fmt.Sprintf("%d seconds", int(lease.Seconds())), maxAttempts))
	if errors.Is(err, pgx.ErrNoRows) {
		return proposalRow{}, "", false, nil
	}
	if err != nil {
		return proposalRow{}, "", false, fmt.Errorf("reclaim proposal: %w", err)
	}
	var token string
	if err := tx.QueryRow(ctx, `SELECT claim_token::text FROM ai.proposals WHERE id=$1`, id).Scan(&token); err != nil {
		return proposalRow{}, "", false, err
	}
	return p, token, true, nil
}

// finishProposal ends a confirmed proposal with executed or failed while the caller still holds the claim. ok=false
// means another attempt took over (a late completer); its own outcome then stands.
func (s *Store) finishProposal(ctx context.Context, tx pgx.Tx, id, token, state, outcome string, result *ResourceRef) (proposalRow, bool, error) {
	var rt, rid *string
	if result != nil {
		rt, rid = &result.Type, &result.ID
	}
	p, err := scanProposal(tx.QueryRow(ctx, `UPDATE ai.proposals SET state=$3, outcome_code=$4, result_type=$5, result_id=$6::uuid, ended_at=now(),
		claim_token=NULL, lease_expires_at=NULL, version=version+1
		WHERE id=$1 AND state='confirmed' AND claim_token=$2::uuid RETURNING `+proposalCols, id, token, state, outcome, rt, rid))
	if errors.Is(err, pgx.ErrNoRows) {
		return proposalRow{}, false, nil
	}
	if err != nil {
		return proposalRow{}, false, fmt.Errorf("finish proposal: %w", err)
	}
	return p, true, nil
}

// dismissProposal is proposed -> dismissed for the creating User while the proposal is unexpired. Parameters are
// removed at once: a dismissed proposal can never run.
func (s *Store) dismissProposal(ctx context.Context, tx pgx.Tx, c Caller, id string) (proposalRow, bool, error) {
	p, err := scanProposal(tx.QueryRow(ctx, `UPDATE ai.proposals SET state='dismissed', outcome_code='dismissed', ended_at=now(),
		parameters=NULL, target_label=NULL, version=version+1
		WHERE id=$1 AND user_id=$2 AND tenant_id=$3 AND state='proposed' AND expires_at > now() RETURNING `+proposalCols, id, c.UserID, c.TenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return proposalRow{}, false, nil
	}
	if err != nil {
		return proposalRow{}, false, fmt.Errorf("dismiss proposal: %w", err)
	}
	return p, true, nil
}

// proposalSweep is what one run of the expiry job changed.
type proposalSweep struct {
	Expired []proposalRow
	Unknown []proposalRow
	Purged  int64
	Deleted int64
}

// sweepProposals expires unconfirmed proposals past their TTL (removing their parameters), fails confirmed ones
// whose execution outcome stayed unknown, removes the parameters of proposals that ended more than keep ago and
// deletes rows that ended more than deleteAfter ago (the audit keeps ids and the hash).
func (s *Store) sweepProposals(ctx context.Context, tx pgx.Tx, unknownAfter, keep, deleteAfter time.Duration, maxAttempts int) (proposalSweep, error) {
	var out proposalSweep
	collect := func(rows pgx.Rows) ([]proposalRow, error) {
		defer rows.Close()
		var list []proposalRow
		for rows.Next() {
			p, err := scanProposal(rows)
			if err != nil {
				return nil, err
			}
			list = append(list, p)
		}
		return list, rows.Err()
	}
	rows, err := tx.Query(ctx, `UPDATE ai.proposals SET state='expired', outcome_code='expired', ended_at=now(), parameters=NULL,
		target_label=NULL, version=version+1 WHERE state='proposed' AND expires_at <= now() RETURNING `+proposalCols)
	if err != nil {
		return out, fmt.Errorf("expire proposals: %w", err)
	}
	if out.Expired, err = collect(rows); err != nil {
		return out, fmt.Errorf("expire proposals: %w", err)
	}
	rows, err = tx.Query(ctx, `UPDATE ai.proposals SET state='failed', outcome_code='ai.execution_unknown', ended_at=now(),
		claim_token=NULL, lease_expires_at=NULL, version=version+1
		WHERE state='confirmed' AND (lease_expires_at <= now() - $1::interval OR (attempt >= $2 AND lease_expires_at <= now()))
		RETURNING `+proposalCols, fmt.Sprintf("%d seconds", int(unknownAfter.Seconds())), maxAttempts)
	if err != nil {
		return out, fmt.Errorf("fail unknown proposals: %w", err)
	}
	if out.Unknown, err = collect(rows); err != nil {
		return out, fmt.Errorf("fail unknown proposals: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE ai.proposals SET parameters=NULL, target_label=NULL
		WHERE parameters IS NOT NULL AND ended_at IS NOT NULL AND ended_at <= now() - $1::interval`, fmt.Sprintf("%d seconds", int(keep.Seconds())))
	if err != nil {
		return out, fmt.Errorf("purge proposal parameters: %w", err)
	}
	out.Purged = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `DELETE FROM ai.proposals WHERE ended_at IS NOT NULL AND ended_at <= now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(deleteAfter.Seconds())))
	if err != nil {
		return out, fmt.Errorf("delete old proposals: %w", err)
	}
	out.Deleted = tag.RowsAffected()
	return out, nil
}
