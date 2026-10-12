package sla

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/scheduling"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("SLA policy or queue not found")
	ErrConflict = errors.New("SLA version conflict")
	ErrInvalid  = errors.New("invalid SLA policy")
	ErrInUse    = errors.New("SLA policy is assigned to a queue")
	ErrKeyTaken = errors.New("SLA policy key taken")
)
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var waitingReasons = []string{"customer", "vendor", "external_service", "scheduled_change", "hardware"}
var priorities = []string{"low", "normal", "high", "urgent"}
var kinds = []string{"first_response", "resolution"}

type Policy struct {
	ID                  string    `json:"id"`
	Key                 string    `json:"key"`
	Name                string    `json:"name"`
	Description         string    `json:"description"`
	CalendarID          *string   `json:"calendarId"`
	PauseWaitingReasons []string  `json:"pauseWaitingReasons"`
	WarningPercent      int       `json:"warningPercent"`
	EscalationTask      bool      `json:"escalationTask"`
	Status              string    `json:"status"`
	Version             int       `json:"version"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
	Targets             []Target  `json:"targets"`
}
type Input struct {
	Key                 string   `json:"key"`
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	CalendarID          *string  `json:"calendarId"`
	PauseWaitingReasons []string `json:"pauseWaitingReasons"`
	WarningPercent      int      `json:"warningPercent"`
	EscalationTask      bool     `json:"escalationTask"`
}
type Target struct {
	Priority      string `json:"priority"`
	Kind          string `json:"kind"`
	TargetMinutes int    `json:"targetMinutes"`
	TimeBasis     string `json:"timeBasis"`
}
type Queue struct {
	ID       string  `json:"id"`
	Key      string  `json:"key"`
	Name     string  `json:"name"`
	Status   string  `json:"status"`
	Version  int     `json:"version"`
	PolicyID *string `json:"policyId"`
}
type Store struct{ Pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{Pool: pool} }

const columns = `id::text,key,name,description,calendar_id::text,pause_waiting_reasons,warning_percent,escalation_task,status,version,created_at,updated_at`

func scan(row pgx.Row) (Policy, error) {
	var p Policy
	err := row.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.CalendarID, &p.PauseWaitingReasons, &p.WarningPercent, &p.EscalationTask, &p.Status, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	p.Targets = []Target{}
	return p, err
}
func (s *Store) List(ctx context.Context) ([]Policy, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+columns+` FROM servicedesk.sla_policies ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Policy{}
	for rows.Next() {
		p, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func targetsTx(ctx context.Context, tx pgx.Tx, id string) ([]Target, error) {
	rows, err := tx.Query(ctx, `SELECT priority,kind,target_minutes,time_basis FROM servicedesk.sla_targets WHERE policy_id=$1 ORDER BY priority,kind`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Target{}
	for rows.Next() {
		var target Target
		if err := rows.Scan(&target.Priority, &target.Kind, &target.TargetMinutes, &target.TimeBasis); err != nil {
			return nil, err
		}
		out = append(out, target)
	}
	return out, rows.Err()
}
func (s *Store) Get(ctx context.Context, id string) (Policy, error) {
	if !uuidPattern.MatchString(id) {
		return Policy{}, ErrNotFound
	}
	p, err := scan(s.Pool.QueryRow(ctx, `SELECT `+columns+` FROM servicedesk.sla_policies WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT priority,kind,target_minutes,time_basis FROM servicedesk.sla_targets WHERE policy_id=$1 ORDER BY priority,kind`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.Priority, &t.Kind, &t.TargetMinutes, &t.TimeBasis); err != nil {
			return p, err
		}
		p.Targets = append(p.Targets, t)
	}
	return p, rows.Err()
}
func validText(v string, min, max int) bool {
	return utf8.ValidString(v) && utf8.RuneCountInString(v) >= min && utf8.RuneCountInString(v) <= max && strings.TrimSpace(v) == v && !safetext.ContainsUnsafe(v, false)
}
func validInput(in Input) bool {
	if !validText(in.Name, 1, 80) || !validText(in.Description, 0, 2000) || in.WarningPercent < 50 || in.WarningPercent > 95 {
		return false
	}
	if in.CalendarID != nil && !uuidPattern.MatchString(*in.CalendarID) {
		return false
	}
	seen := map[string]bool{}
	for _, r := range in.PauseWaitingReasons {
		if !slices.Contains(waitingReasons, r) || seen[r] {
			return false
		}
		seen[r] = true
	}
	return true
}
func auditPolicy(ctx context.Context, tx pgx.Tx, p Policy, actor, corr, action string, before any) error {
	return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.sla_policy." + action, TargetType: "sla_policy", TargetID: p.ID, Actor: audit.UserActor(actor), CorrelationID: corr, Before: before, After: p})
}

// lockCalendar serializes policy changes with calendar archive and rejects archived calendars.
func lockCalendar(ctx context.Context, tx pgx.Tx, id *string) error {
	if id == nil {
		return nil
	}
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM platform.business_calendars WHERE id=$1 FOR SHARE`, *id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || status == "archived" {
		return ErrInvalid
	}
	return err
}
func dbError(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) {
		if p.Code == "23505" {
			return ErrKeyTaken
		}
		if p.Code == "23503" || p.Code == "23514" {
			return ErrInvalid
		}
	}
	return err
}
func (s *Store) Create(ctx context.Context, in Input, actor, corr string) (Policy, error) {
	if in.PauseWaitingReasons == nil {
		in.PauseWaitingReasons = []string{"customer"}
	}
	if in.WarningPercent == 0 {
		in.WarningPercent = 80
	}
	if !keyPattern.MatchString(in.Key) || !validInput(in) {
		return Policy{}, ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Policy{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockCalendar(ctx, tx, in.CalendarID); err != nil {
		return Policy{}, err
	}
	p, err := scan(tx.QueryRow(ctx, `INSERT INTO servicedesk.sla_policies(key,name,description,calendar_id,pause_waiting_reasons,warning_percent,escalation_task) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+columns, in.Key, in.Name, in.Description, in.CalendarID, in.PauseWaitingReasons, in.WarningPercent, in.EscalationTask))
	if err != nil {
		return Policy{}, dbError(err)
	}
	if err := auditPolicy(ctx, tx, p, actor, corr, "created", nil); err != nil {
		return Policy{}, err
	}
	return p, tx.Commit(ctx)
}
func (s *Store) mutate(ctx context.Context, id string, expected int, actor, corr, action string, change func(context.Context, pgx.Tx, *Policy) error) (Policy, error) {
	if !uuidPattern.MatchString(id) {
		return Policy{}, ErrNotFound
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Policy{}, err
	}
	defer tx.Rollback(ctx)
	p, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM servicedesk.sla_policies WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Policy{}, ErrNotFound
	}
	if err != nil {
		return Policy{}, err
	}
	if p.Version != expected {
		return Policy{}, ErrConflict
	}
	before := p
	var beforeTargets []Target
	if action == "targets_replaced" {
		beforeTargets, err = targetsTx(ctx, tx, id)
		if err != nil {
			return Policy{}, err
		}
	}
	if err := change(ctx, tx, &p); err != nil {
		return Policy{}, err
	}
	p.Version++
	p, err = scan(tx.QueryRow(ctx, `UPDATE servicedesk.sla_policies SET name=$2,description=$3,calendar_id=$4,pause_waiting_reasons=$5,warning_percent=$6,escalation_task=$7,status=$8,version=$9,updated_at=now() WHERE id=$1 RETURNING `+columns, id, p.Name, p.Description, p.CalendarID, p.PauseWaitingReasons, p.WarningPercent, p.EscalationTask, p.Status, p.Version))
	if err != nil {
		return Policy{}, dbError(err)
	}
	if action == "targets_replaced" {
		afterTargets, err := targetsTx(ctx, tx, id)
		if err != nil {
			return Policy{}, err
		}
		before.Targets = beforeTargets
		p.Targets = afterTargets
	}
	if err := auditPolicy(ctx, tx, p, actor, corr, action, before); err != nil {
		return Policy{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Policy{}, err
	}
	return s.Get(ctx, id)
}
func (s *Store) Update(ctx context.Context, id string, expected int, in Input, actor, corr string) (Policy, error) {
	if !validInput(in) {
		return Policy{}, ErrInvalid
	}
	return s.mutate(ctx, id, expected, actor, corr, "updated", func(ctx context.Context, tx pgx.Tx, p *Policy) error {
		if in.Key != "" && in.Key != p.Key || p.Status != "active" {
			return ErrInvalid
		}
		if err := lockCalendar(ctx, tx, in.CalendarID); err != nil {
			return err
		}
		targets, err := targetsTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if !validTargets(targets, in.CalendarID) {
			return ErrInvalid
		}
		if err := validateBounds(ctx, tx, in.CalendarID, targets); err != nil {
			return err
		}
		p.Name = in.Name
		p.Description = in.Description
		p.CalendarID = in.CalendarID
		p.PauseWaitingReasons = in.PauseWaitingReasons
		p.WarningPercent = in.WarningPercent
		p.EscalationTask = in.EscalationTask
		return nil
	})
}
func (s *Store) SetStatus(ctx context.Context, id string, expected int, status, actor, corr string) (Policy, error) {
	if status != "active" && status != "archived" {
		return Policy{}, ErrInvalid
	}
	action := "archived"
	if status == "active" {
		action = "restored"
	}
	return s.mutate(ctx, id, expected, actor, corr, action, func(ctx context.Context, tx pgx.Tx, p *Policy) error {
		if p.Status == status {
			return ErrConflict
		}
		if status == "archived" {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM servicedesk.queues WHERE sla_policy_id=$1`, id).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrInUse
			}
		}
		if status == "active" {
			if err := lockCalendar(ctx, tx, p.CalendarID); err != nil {
				return err
			}
			targets, err := targetsTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := validateBounds(ctx, tx, p.CalendarID, targets); err != nil {
				return err
			}
		}
		p.Status = status
		return nil
	})
}
func validTargets(items []Target, calendarID *string) bool {
	if len(items) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, t := range items {
		if !slices.Contains(priorities, t.Priority) || !slices.Contains(kinds, t.Kind) || t.TargetMinutes < 1 || t.TargetMinutes > 129600 || !slices.Contains([]string{"business", "continuous"}, t.TimeBasis) || t.TimeBasis == "business" && calendarID == nil {
			return false
		}
		k := t.Priority + ":" + t.Kind
		if seen[k] {
			return false
		}
		seen[k] = true
	}
	return true
}

// validateBounds checks every business target against the calendar's real closures.
func validateBounds(ctx context.Context, tx pgx.Tx, calendarID *string, items []Target) error {
	needsBusiness := false
	for _, target := range items {
		if target.TimeBasis == "business" {
			needsBusiness = true
		}
	}
	if !needsBusiness {
		return nil
	}
	if calendarID == nil {
		return ErrInvalid
	}
	var hours []byte
	var tz string
	if err := tx.QueryRow(ctx, `SELECT timezone,weekly_hours FROM platform.business_calendars WHERE id=$1`, *calendarID).Scan(&tz, &hours); err != nil {
		return err
	}
	var weekly map[int][]scheduling.Interval
	if err := json.Unmarshal(hours, &weekly); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT closed_on::text FROM platform.business_calendar_closures WHERE calendar_id=$1 AND closed_on BETWEEN current_date - 1 AND current_date + 401`, *calendarID)
	if err != nil {
		return err
	}
	closures := map[string]struct{}{}
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			rows.Close()
			return err
		}
		closures[day] = struct{}{}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	cal := scheduling.BusinessCalendar{Timezone: tz, WeeklyHours: weekly, Closures: closures}
	for _, target := range items {
		if target.TimeBasis == "business" {
			if _, err := scheduling.AddBusinessDuration(cal, time.Now().UTC(), time.Duration(target.TargetMinutes)*time.Minute); err != nil {
				return ErrInvalid
			}
		}
	}
	return nil
}
func (s *Store) ReplaceTargets(ctx context.Context, id string, expected int, items []Target, actor, corr string) (Policy, error) {
	return s.mutate(ctx, id, expected, actor, corr, "targets_replaced", func(ctx context.Context, tx pgx.Tx, p *Policy) error {
		if p.Status != "active" || !validTargets(items, p.CalendarID) {
			return ErrInvalid
		}
		if err := lockCalendar(ctx, tx, p.CalendarID); err != nil {
			return err
		}
		if err := validateBounds(ctx, tx, p.CalendarID, items); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM servicedesk.sla_targets WHERE policy_id=$1`, id); err != nil {
			return err
		}
		for _, t := range items {
			if _, err := tx.Exec(ctx, `INSERT INTO servicedesk.sla_targets(policy_id,priority,kind,target_minutes,time_basis) VALUES($1,$2,$3,$4,$5)`, id, t.Priority, t.Kind, t.TargetMinutes, t.TimeBasis); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Store) ListQueues(ctx context.Context) ([]Queue, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text,key,name,status,version,sla_policy_id::text FROM servicedesk.queues ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Queue{}
	for rows.Next() {
		var q Queue
		if err := rows.Scan(&q.ID, &q.Key, &q.Name, &q.Status, &q.Version, &q.PolicyID); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
func (s *Store) SetQueuePolicy(ctx context.Context, id string, policyID *string, expected int, actor, corr string) (Queue, error) {
	if !uuidPattern.MatchString(id) || policyID != nil && !uuidPattern.MatchString(*policyID) {
		return Queue{}, ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Queue{}, err
	}
	defer tx.Rollback(ctx)
	var before Queue
	err = tx.QueryRow(ctx, `SELECT id::text,key,name,status,version,sla_policy_id::text FROM servicedesk.queues WHERE id=$1 FOR UPDATE`, id).Scan(&before.ID, &before.Key, &before.Name, &before.Status, &before.Version, &before.PolicyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Queue{}, ErrNotFound
	}
	if err != nil {
		return Queue{}, err
	}
	if before.Version != expected {
		return Queue{}, ErrConflict
	}
	if before.Status != "active" {
		return Queue{}, ErrInvalid
	}
	if policyID != nil {
		var status string
		err = tx.QueryRow(ctx, `SELECT status FROM servicedesk.sla_policies WHERE id=$1 FOR SHARE`, *policyID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) || status != "active" {
			return Queue{}, ErrInvalid
		}
		if err != nil {
			return Queue{}, err
		}
	}
	var q Queue
	err = tx.QueryRow(ctx, `UPDATE servicedesk.queues SET sla_policy_id=$2,version=version+1,updated_at=now() WHERE id=$1 RETURNING id::text,key,name,status,version,sla_policy_id::text`, id, policyID).Scan(&q.ID, &q.Key, &q.Name, &q.Status, &q.Version, &q.PolicyID)
	if err != nil {
		return Queue{}, dbError(err)
	}
	if err := audit.Record(ctx, tx, audit.Change{Action: "servicedesk.queue.sla_policy_changed", TargetType: "ticket_queue", TargetID: id, Actor: audit.UserActor(actor), CorrelationID: corr, Before: before, After: q}); err != nil {
		return Queue{}, err
	}
	return q, tx.Commit(ctx)
}
