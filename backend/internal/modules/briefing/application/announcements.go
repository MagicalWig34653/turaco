package application

import (
	"context"
	"time"

	deskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
)

const announcementLimit = 20

// IncidentAnnouncements is the Service Desk read contract for the public status of open Major Incidents.
type IncidentAnnouncements interface {
	PublicOpenIncidents(context.Context) ([]deskpublic.PublicIncident, error)
}

// Announcement is a published briefing item meant for every signed-in User. It carries no author, status or
// internal fields.
type Announcement struct {
	ID          string
	Title       string
	Body        string
	Severity    string
	PublishedAt *time.Time
	ValidUntil  *time.Time
}

// AnnouncementIncident is the public status of an open, non-exercise Major Incident.
type AnnouncementIncident struct {
	ID            string
	Reference     string
	Title         string
	Summary       string
	Status        string
	NextUpdateDue *time.Time
	UpdatedAt     time.Time
}

// Announcements is what every signed-in User may read.
type Announcements struct {
	Items     []Announcement
	Incidents []AnnouncementIncident
	// IncidentsUnavailable is true when the incident source failed; the items are still returned.
	IncidentsUnavailable bool
}

// WithIncidents sets the incident source (nil when Service Desk is off).
func (s *Service) WithIncidents(src IncidentAnnouncements) *Service {
	s.incidents = src
	return s
}

// Announcements returns published, unexpired items with audience "all" and the public status of open,
// non-exercise Major Incidents. It needs no briefing permission: it is the employee-facing part.
func (s *Service) Announcements(ctx context.Context, userID string) (Announcements, error) {
	if userID == "" {
		return Announcements{}, ErrForbidden
	}
	res, err := s.store.List(ctx, ListQuery{PublishedOnly: true, Audience: AudienceAll, Page: Page{Limit: announcementLimit}})
	if err != nil {
		return Announcements{}, err
	}
	out := Announcements{Items: make([]Announcement, 0, len(res.Items)), Incidents: []AnnouncementIncident{}}
	for _, it := range res.Items {
		out.Items = append(out.Items, Announcement{ID: it.ID, Title: it.Title, Body: it.Body, Severity: it.Severity, PublishedAt: it.PublishedAt, ValidUntil: it.ValidUntil})
	}
	if s.incidents == nil {
		return out, nil
	}
	incidents, err := s.incidents.PublicOpenIncidents(ctx)
	if err != nil {
		out.IncidentsUnavailable = true
		return out, nil
	}
	for _, m := range incidents {
		out.Incidents = append(out.Incidents, AnnouncementIncident(m))
	}
	return out, nil
}
