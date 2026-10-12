package application

import (
	"context"
	"sort"
	"time"
)

// Report returns aggregated effort per Queue, month or Ticket (design section 7.3). It never groups or filters by
// person. The scope is the Queues the caller may view; a group with fewer distinct contributors than the minimum group
// size is suppressed (only the key is returned), except per-Ticket groups: a Ticket's effort is operational
// documentation already visible on the Ticket to people who can see it. Queue ids are not translated into names here.
func (s *Service) Report(ctx context.Context, p Principal, from, to, groupBy string) (Summary, error) {
	if p.UserID == "" || !p.Report {
		return Summary{}, ErrForbidden
	}
	switch groupBy {
	case GroupQueue, GroupMonth, GroupTicket:
	default:
		return Summary{}, invalid("groupBy must be queue, month or ticket")
	}
	f, err := parseDate(from, "from")
	if err != nil {
		return Summary{}, err
	}
	t, err := parseDate(to, "to")
	if err != nil {
		return Summary{}, err
	}
	if t.Before(f) {
		return Summary{}, invalid("to must not be before from")
	}
	if t.Sub(f) > MaxReportDays*24*time.Hour {
		return Summary{}, invalid("the period spans at most %d days", MaxReportDays)
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return Summary{}, err
	}
	global, queues, err := s.tickets.ViewQueues(ctx, p.UserID)
	if err != nil {
		return Summary{}, err
	}
	out := Summary{From: from, To: to, GroupBy: groupBy, MinGroupSize: st.MinReportGroupSize, Groups: []SummaryGroup{}}
	if !global && len(queues) == 0 {
		return out, nil
	}
	var scope []string
	if !global {
		scope = queues
	}
	rows, err := s.store.Summary(ctx, from, to, groupBy, scope)
	if err != nil {
		return Summary{}, err
	}
	var refs map[string]TicketAccess
	if groupBy == GroupTicket && len(rows) > 0 {
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.Key)
		}
		if refs, err = s.tickets.Access(ctx, p.UserID, ids); err != nil {
			return Summary{}, err
		}
	}
	for _, r := range rows {
		g := SummaryGroup{Key: r.Key}
		if groupBy == GroupTicket {
			a := refs[r.Key]
			if !a.Found || !a.CanView {
				continue // never reveal a Ticket the caller cannot view
			}
			g.Label = a.Reference
		}
		if groupBy != GroupTicket && r.Contributors < st.MinReportGroupSize {
			g.Suppressed = true
		} else {
			g.Minutes, g.Entries, g.Contributors = r.Minutes, r.Entries, r.Contributors
		}
		out.Groups = append(out.Groups, g)
	}
	sort.SliceStable(out.Groups, func(i, j int) bool { return out.Groups[i].Minutes > out.Groups[j].Minutes })
	if len(out.Groups) > MaxReportRows {
		out.Groups, out.Truncated = out.Groups[:MaxReportRows], true
	}
	return out, nil
}
