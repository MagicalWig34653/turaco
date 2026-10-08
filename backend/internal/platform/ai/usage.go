package ai

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// Reservation is a worst-case token reservation against the per-User and the installation budget (design: Conversation runtime, step 5).
type Reservation struct {
	Tenant string
	User   string
	Day    time.Time // UTC date of the reservation
	Tokens int64
}

// Limits are the caps a reservation is checked against.
type Limits struct {
	UserRequestsPerHour      int
	UserRequestsPerDay       int
	UserTokensPerDay         int
	InstallationTokensPerDay int
}

func (s Settings) limits() Limits {
	return Limits{UserRequestsPerHour: s.UserRequestsPerHour, UserRequestsPerDay: s.UserRequestsPerDay,
		UserTokensPerDay: s.UserTokensPerDay, InstallationTokensPerDay: s.InstallationTokensPerDay}
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Reserve checks and reserves in one transaction. Every cap is a conditional upsert/update on its counter row, so
// concurrent turns serialize on the row lock and the cap cannot be overshot. countRequest is true for the first
// provider call of a turn. Rows are always locked in the same order (hour, user day, installation day).
// The error is ErrRateLimited (request caps) or ErrBudgetExceeded (token caps); in both cases nothing is reserved.
func (s *Store) Reserve(ctx context.Context, now time.Time, tenant, user string, tokens int64, countRequest bool, lim Limits) (Reservation, error) {
	day := utcDay(now)
	hour := now.UTC().Truncate(time.Hour)
	res := Reservation{Tenant: tenant, User: user, Day: day, Tokens: tokens}
	if tokens < 0 {
		return Reservation{}, errors.New("reserve: negative tokens")
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if countRequest {
			var n int
			err := tx.QueryRow(ctx, `INSERT INTO ai.usage_hours AS h (tenant_id, user_id, hour, request_count) VALUES ($1,$2,$3,1)
				ON CONFLICT (tenant_id, user_id, hour) DO UPDATE SET request_count = h.request_count + 1
				WHERE h.request_count < $4 RETURNING request_count`, tenant, user, hour, lim.UserRequestsPerHour).Scan(&n)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRateLimited
			} else if err != nil {
				return fmt.Errorf("reserve hour: %w", err)
			}
			err = tx.QueryRow(ctx, `INSERT INTO ai.usage AS u (tenant_id, user_id, day, request_count) VALUES ($1,$2,$3,1)
				ON CONFLICT (tenant_id, user_id, day) DO UPDATE SET request_count = u.request_count + 1
				WHERE u.request_count < $4 RETURNING request_count`, tenant, user, day, lim.UserRequestsPerDay).Scan(&n)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRateLimited
			} else if err != nil {
				return fmt.Errorf("reserve day: %w", err)
			}
		} else if _, err := tx.Exec(ctx, `INSERT INTO ai.usage (tenant_id, user_id, day) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, tenant, user, day); err != nil {
			return fmt.Errorf("ensure usage row: %w", err)
		}
		tag, err := tx.Exec(ctx, `UPDATE ai.usage SET tokens_reserved = tokens_reserved + $4
			WHERE tenant_id=$1 AND user_id=$2 AND day=$3 AND tokens_in + tokens_out + tokens_reserved + $4 <= $5`,
			tenant, user, day, tokens, int64(lim.UserTokensPerDay))
		if err != nil {
			return fmt.Errorf("reserve user tokens: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrBudgetExceeded
		}
		var n int64
		err = tx.QueryRow(ctx, `INSERT INTO ai.installation_usage AS i (tenant_id, day, tokens_reserved) SELECT $1::text, $2::date, $3::bigint WHERE $3::bigint <= $4::bigint
			ON CONFLICT (tenant_id, day) DO UPDATE SET tokens_reserved = i.tokens_reserved + $3::bigint
			WHERE i.tokens_used + i.tokens_reserved + $3::bigint <= $4::bigint RETURNING tokens_reserved`,
			tenant, day, tokens, int64(lim.InstallationTokensPerDay)).Scan(&n)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBudgetExceeded
		} else if err != nil {
			return fmt.Errorf("reserve installation tokens: %w", err)
		}
		return nil
	})
	if err != nil {
		return Reservation{}, err
	}
	return res, nil
}

// Settle replaces the reservation with the actual usage. Usage above the reservation is recorded as reported (the
// counters stay true, so every later reservation sees it and is refused once a cap is reached) and returned as
// overage; the runtime then ends the turn instead of making further calls. A failed provider call settles with zero tokens, which
// releases the reservation fully. costMicro is the estimated cost in millionths of the currency unit.
func (s *Store) Settle(ctx context.Context, r Reservation, tokensIn, tokensOut, costMicro int64) (overage int64, err error) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if over := tokensIn + tokensOut - r.Tokens; over > 0 {
		overage = over
	}
	err = s.inTx(c, func(tx pgx.Tx) error {
		if _, err := tx.Exec(c, `UPDATE ai.usage SET tokens_reserved = GREATEST(tokens_reserved - $4, 0), tokens_in = tokens_in + $5,
			tokens_out = tokens_out + $6, estimated_cost_micro = estimated_cost_micro + $7
			WHERE tenant_id=$1 AND user_id=$2 AND day=$3`, r.Tenant, r.User, r.Day, r.Tokens, tokensIn, tokensOut, costMicro); err != nil {
			return fmt.Errorf("settle user usage: %w", err)
		}
		if _, err := tx.Exec(c, `UPDATE ai.installation_usage SET tokens_reserved = GREATEST(tokens_reserved - $3, 0), tokens_used = tokens_used + $4
			WHERE tenant_id=$1 AND day=$2`, r.Tenant, r.Day, r.Tokens, tokensIn+tokensOut); err != nil {
			return fmt.Errorf("settle installation usage: %w", err)
		}
		return nil
	})
	return overage, err
}

// UsageSnapshot is the aggregated, content-free usage of one User and the installation.
type UsageSnapshot struct {
	Day                   string
	UserRequestsToday     int
	UserRequestsThisHour  int
	UserTokensToday       int64
	InstallationTokens    int64
	EstimatedCostMicro    int64
	UserRequestsTodayLeft int
	UserRequestsHourLeft  int
	UserTokensLeft        int64
	InstallationTokenLeft int64
}

// Snapshot reads the counters (reserved tokens count as used).
func (s *Store) Snapshot(ctx context.Context, now time.Time, tenant, user string, lim Limits) (UsageSnapshot, error) {
	day := utcDay(now)
	out := UsageSnapshot{Day: day.Format(time.DateOnly)}
	var tin, tout, tres int64
	err := s.pool.QueryRow(ctx, `SELECT request_count, tokens_in, tokens_out, tokens_reserved, estimated_cost_micro FROM ai.usage
		WHERE tenant_id=$1 AND user_id=$2 AND day=$3`, tenant, user, day).Scan(&out.UserRequestsToday, &tin, &tout, &tres, &out.EstimatedCostMicro)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return UsageSnapshot{}, fmt.Errorf("usage snapshot: %w", err)
	}
	out.UserTokensToday = tin + tout + tres
	err = s.pool.QueryRow(ctx, `SELECT request_count FROM ai.usage_hours WHERE tenant_id=$1 AND user_id=$2 AND hour=$3`,
		tenant, user, now.UTC().Truncate(time.Hour)).Scan(&out.UserRequestsThisHour)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return UsageSnapshot{}, fmt.Errorf("usage snapshot: %w", err)
	}
	var iu, ir int64
	err = s.pool.QueryRow(ctx, `SELECT tokens_used, tokens_reserved FROM ai.installation_usage WHERE tenant_id=$1 AND day=$2`, tenant, day).Scan(&iu, &ir)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return UsageSnapshot{}, fmt.Errorf("usage snapshot: %w", err)
	}
	out.InstallationTokens = iu + ir
	out.UserRequestsTodayLeft = max(lim.UserRequestsPerDay-out.UserRequestsToday, 0)
	out.UserRequestsHourLeft = max(lim.UserRequestsPerHour-out.UserRequestsThisHour, 0)
	out.UserTokensLeft = max(int64(lim.UserTokensPerDay)-out.UserTokensToday, 0)
	out.InstallationTokenLeft = max(int64(lim.InstallationTokensPerDay)-out.InstallationTokens, 0)
	return out, nil
}

// InstallationUsage is the installation total of one day, for ai.usage.view.
type InstallationUsage struct {
	Day         string
	Users       int
	Requests    int64
	TokensIn    int64
	TokensOut   int64
	CostMicro   int64
	TokensToday int64
}

// UsageSummary aggregates the last days for the installation (counts only; no per-prompt data exists).
func (s *Store) UsageSummary(ctx context.Context, tenant string, days int) ([]InstallationUsage, error) {
	rows, err := s.pool.Query(ctx, `SELECT day::text, count(*), sum(request_count), sum(tokens_in), sum(tokens_out), sum(estimated_cost_micro)
		FROM ai.usage WHERE tenant_id=$1 AND day > (now() AT TIME ZONE 'UTC')::date - $2::int GROUP BY day ORDER BY day DESC`, tenant, days)
	if err != nil {
		return nil, fmt.Errorf("usage summary: %w", err)
	}
	defer rows.Close()
	out := []InstallationUsage{}
	for rows.Next() {
		var u InstallationUsage
		if err := rows.Scan(&u.Day, &u.Users, &u.Requests, &u.TokensIn, &u.TokensOut, &u.CostMicro); err != nil {
			return nil, fmt.Errorf("usage summary: %w", err)
		}
		u.TokensToday = u.TokensIn + u.TokensOut
		out = append(out, u)
	}
	return out, rows.Err()
}

// costMicro estimates the cost from token counts and the provider's price per million tokens.
func costMicro(rec ProviderRecord, in, out int) int64 {
	return int64(math.Round(float64(in)*rec.PriceInPerMTok + float64(out)*rec.PriceOutPerMTok))
}
