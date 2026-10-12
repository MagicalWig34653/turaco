package businesscalendars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/scheduling"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrCalendarNotFound = errors.New("calendar not found")
	ErrCalendarConflict = errors.New("calendar version conflict")
	ErrCalendarKeyTaken = errors.New("calendar key already exists")
	ErrCalendarInvalid  = errors.New("invalid calendar")
	ErrCalendarInUse    = errors.New("calendar used by active SLA policy")
)
var calendarKey = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var calendarID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Interval = scheduling.Interval
type BusinessCalendar = scheduling.BusinessCalendar

type Closure struct {
	Date string `json:"date"`
	Name string `json:"name"`
}
type Calendar struct {
	ID          string             `json:"id"`
	Key         string             `json:"key"`
	Name        string             `json:"name"`
	Timezone    string             `json:"timezone"`
	WeeklyHours map[int][]Interval `json:"weeklyHours"`
	Status      string             `json:"status"`
	Version     int                `json:"version"`
	CreatedAt   time.Time          `json:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`
	Closures    []Closure          `json:"closures"`
}
type CalendarInput struct {
	Key         string             `json:"key"`
	Name        string             `json:"name"`
	Timezone    string             `json:"timezone"`
	WeeklyHours map[int][]Interval `json:"weeklyHours"`
}
type CalendarStore struct {
	pool  *pgxpool.Pool
	usage CalendarUsage
}

// CalendarUsage is a public contract supplied by a consumer that may depend on calendars.
type CalendarUsage interface {
	ActivePolicyUsesCalendar(context.Context, pgx.Tx, string) (bool, error)
}

func (s *CalendarStore) WithUsage(u CalendarUsage) *CalendarStore { s.usage = u; return s }

func NewCalendarStore(pool *pgxpool.Pool) *CalendarStore { return &CalendarStore{pool: pool} }

func safeCalendarName(s string) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) >= 1 && utf8.RuneCountInString(s) <= 80 && strings.TrimSpace(s) == s && !safetext.ContainsUnsafe(s, false)
}
func validInput(in CalendarInput) error {
	if !safeCalendarName(in.Name) {
		return ErrCalendarInvalid
	}
	if err := (BusinessCalendar{Timezone: in.Timezone, WeeklyHours: in.WeeklyHours}).Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrCalendarInvalid, err)
	}
	return nil
}
func (s *CalendarStore) List(ctx context.Context) ([]Calendar, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text,key,name,timezone,weekly_hours,status,version,created_at,updated_at FROM platform.business_calendars ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Calendar{}
	for rows.Next() {
		c, err := scanCalendar(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}
func scanCalendar(row pgx.Row) (Calendar, error) {
	var c Calendar
	var hours []byte
	err := row.Scan(&c.ID, &c.Key, &c.Name, &c.Timezone, &hours, &c.Status, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(hours, &c.WeeklyHours)
	c.Closures = []Closure{}
	return c, err
}
func (s *CalendarStore) Get(ctx context.Context, id string) (Calendar, error) {
	if !calendarID.MatchString(id) {
		return Calendar{}, ErrCalendarNotFound
	}
	c, err := scanCalendar(s.pool.QueryRow(ctx, `SELECT id::text,key,name,timezone,weekly_hours,status,version,created_at,updated_at FROM platform.business_calendars WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrCalendarNotFound
	}
	if err != nil {
		return c, err
	}
	rows, err := s.pool.Query(ctx, `SELECT closed_on::text,name FROM platform.business_calendar_closures WHERE calendar_id=$1 ORDER BY closed_on`, id)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var cl Closure
		if err := rows.Scan(&cl.Date, &cl.Name); err != nil {
			return c, err
		}
		c.Closures = append(c.Closures, cl)
	}
	return c, rows.Err()
}
func (s *CalendarStore) Create(ctx context.Context, in CalendarInput, actor, corr string) (Calendar, error) {
	var c Calendar
	if !calendarKey.MatchString(in.Key) || validInput(in) != nil {
		return c, ErrCalendarInvalid
	}
	hours, _ := json.Marshal(in.WeeklyHours)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `INSERT INTO platform.business_calendars(key,name,timezone,weekly_hours) VALUES($1,$2,$3,$4) RETURNING id::text,key,name,timezone,weekly_hours,status,version,created_at,updated_at`, in.Key, in.Name, in.Timezone, hours).Scan(&c.ID, &c.Key, &c.Name, &c.Timezone, &hours, &c.Status, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			return c, ErrCalendarKeyTaken
		}
		return c, err
	}
	c.WeeklyHours = in.WeeklyHours
	c.Closures = []Closure{}
	if err := s.record(ctx, tx, c, actor, corr, "created", nil, true); err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}

// mutate locks a calendar and requires its version for every state change.
func (s *CalendarStore) mutate(ctx context.Context, id string, expected int, actor, corr, action string, change func(context.Context, pgx.Tx, *Calendar) (bool, error)) (Calendar, error) {
	var empty Calendar
	if !calendarID.MatchString(id) {
		return empty, ErrCalendarNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	c, err := scanCalendar(tx.QueryRow(ctx, `SELECT id::text,key,name,timezone,weekly_hours,status,version,created_at,updated_at FROM platform.business_calendars WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, ErrCalendarNotFound
	}
	if err != nil {
		return empty, err
	}
	if c.Version != expected {
		return empty, ErrCalendarConflict
	}
	before := c
	workingChanged, err := change(ctx, tx, &c)
	if err != nil {
		return empty, err
	}
	c.Version++
	if err := tx.QueryRow(ctx, `UPDATE platform.business_calendars SET name=$2,timezone=$3,weekly_hours=$4,status=$5,version=$6,updated_at=now() WHERE id=$1 RETURNING updated_at`, id, c.Name, c.Timezone, c.WeeklyHours, c.Status, c.Version).Scan(&c.UpdatedAt); err != nil {
		return empty, err
	}
	if err := s.record(ctx, tx, c, actor, corr, action, before, workingChanged); err != nil {
		return empty, err
	}
	if err := tx.Commit(ctx); err != nil {
		return empty, err
	}
	return s.Get(ctx, id)
}
func (s *CalendarStore) record(ctx context.Context, tx pgx.Tx, c Calendar, actor, corr, action string, before any, changed bool) error {
	if err := audit.Record(ctx, tx, audit.Change{Action: "platform.business_calendar." + action, TargetType: "business_calendar", TargetID: c.ID, Actor: audit.UserActor(actor), CorrelationID: corr, Before: before, After: c}); err != nil {
		return err
	}
	if changed {
		return events.Publish(ctx, tx, events.Publication{Type: "BusinessCalendarChanged", ActorID: &actor, CorrelationID: corr, Payload: map[string]any{"calendarId": c.ID, "version": c.Version}})
	}
	return nil
}
func (s *CalendarStore) Update(ctx context.Context, id string, expected int, in CalendarInput, actor, corr string) (Calendar, error) {
	if err := validInput(in); err != nil {
		return Calendar{}, err
	}
	return s.mutate(ctx, id, expected, actor, corr, "updated", func(_ context.Context, _ pgx.Tx, c *Calendar) (bool, error) {
		if in.Key != "" && in.Key != c.Key {
			return false, ErrCalendarInvalid
		}
		changed := c.Timezone != in.Timezone || !equalHours(c.WeeklyHours, in.WeeklyHours)
		c.Name = in.Name
		c.Timezone = in.Timezone
		c.WeeklyHours = in.WeeklyHours
		return changed, nil
	})
}
func equalHours(a, b map[int][]Interval) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}
func (s *CalendarStore) SetStatus(ctx context.Context, id string, expected int, status, actor, corr string) (Calendar, error) {
	if status != "active" && status != "archived" {
		return Calendar{}, ErrCalendarInvalid
	}
	action := "archived"
	if status == "active" {
		action = "restored"
	}
	return s.mutate(ctx, id, expected, actor, corr, action, func(ctx context.Context, tx pgx.Tx, c *Calendar) (bool, error) {
		if c.Status == status {
			return false, ErrCalendarConflict
		}
		if status == "archived" && s.usage != nil {
			used, err := s.usage.ActivePolicyUsesCalendar(ctx, tx, id)
			if err != nil {
				return false, err
			}
			if used {
				return false, ErrCalendarInUse
			}
		}
		c.Status = status
		return false, nil
	})
}
func (s *CalendarStore) ReplaceClosures(ctx context.Context, id string, expected, year int, closures []Closure, actor, corr string) (Calendar, error) {
	if year < 1 || year > 9999 || len(closures) > 366 {
		return Calendar{}, ErrCalendarInvalid
	}
	seen := map[string]bool{}
	for _, cl := range closures {
		d, e := time.Parse(time.DateOnly, cl.Date)
		if e != nil || d.Year() != year || !safeCalendarName(cl.Name) || seen[cl.Date] {
			return Calendar{}, ErrCalendarInvalid
		}
		seen[cl.Date] = true
	}
	return s.mutate(ctx, id, expected, actor, corr, "closures_replaced", func(ctx context.Context, tx pgx.Tx, c *Calendar) (bool, error) {
		if _, err := tx.Exec(ctx, `DELETE FROM platform.business_calendar_closures WHERE calendar_id=$1 AND EXTRACT(YEAR FROM closed_on)=$2`, id, year); err != nil {
			return false, err
		}
		for _, cl := range closures {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.business_calendar_closures(calendar_id,closed_on,name) VALUES($1,$2,$3)`, id, cl.Date, cl.Name); err != nil {
				return false, err
			}
		}
		return true, nil
	})
}
func (s *CalendarStore) CopyClosures(ctx context.Context, id string, expected, fromYear, toYear int, actor, corr string) (Calendar, error) {
	if fromYear < 1 || toYear < 1 || fromYear > 9999 || toYear > 9999 || fromYear == toYear {
		return Calendar{}, ErrCalendarInvalid
	}
	return s.mutate(ctx, id, expected, actor, corr, "closures_replaced", func(ctx context.Context, tx pgx.Tx, c *Calendar) (bool, error) {
		rows, err := tx.Query(ctx, `SELECT to_char(closed_on,'MM-DD'),name FROM platform.business_calendar_closures WHERE calendar_id=$1 AND EXTRACT(YEAR FROM closed_on)=$2`, id, fromYear)
		if err != nil {
			return false, err
		}
		var list []Closure
		for rows.Next() {
			var suffix, name string
			if err := rows.Scan(&suffix, &name); err != nil {
				rows.Close()
				return false, err
			}
			date := fmt.Sprintf("%04d-%s", toYear, suffix)
			d, e := time.Parse(time.DateOnly, date)
			if e == nil && d.Year() == toYear {
				list = append(list, Closure{date, name})
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.business_calendar_closures WHERE calendar_id=$1 AND EXTRACT(YEAR FROM closed_on)=$2`, id, toYear); err != nil {
			return false, err
		}
		for _, cl := range list {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.business_calendar_closures(calendar_id,closed_on,name) VALUES($1,$2,$3)`, id, cl.Date, cl.Name); err != nil {
				return false, err
			}
		}
		return true, nil
	})
}
