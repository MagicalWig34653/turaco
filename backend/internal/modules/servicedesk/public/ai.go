package public

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
)

const (
	aiMaxMessages = 20
	aiTimeLayout  = time.RFC3339
)

// ticketSummary is the dedicated egress DTO of tickets.summarize (F12 A13): explicit fields only. Device
// snapshots, routing (queue), external references and user ids never appear.
type ticketSummary struct {
	Reference         string          `json:"reference"`
	Title             string          `json:"title"`
	Status            string          `json:"status"`
	Priority          string          `json:"priority"`
	WaitingReason     *string         `json:"waitingReason"`
	Description       *string         `json:"description"`
	Resolution        *string         `json:"resolution"`
	Reporter          string          `json:"reporter"`
	AffectedUser      string          `json:"affectedUser"`
	Assignee          *string         `json:"assignee"`
	CreatedAt         string          `json:"createdAt"`
	UpdatedAt         string          `json:"updatedAt"`
	Messages          []ticketMessage `json:"messages"`
	MessagesTruncated bool            `json:"messagesTruncated"`
}

type ticketMessage struct {
	Author    string `json:"author"`
	Internal  bool   `json:"internal"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

// AITools returns the AI Tools of the Service Desk (registered by the composition root).
func AITools(svc *application.Service) []ai.Tool {
	return []ai.Tool{{
		Name:        "tickets.summarize",
		Description: "Read one Ticket the user may see (fields, the people on it and the latest comments, including internal notes only if the user may see them) so it can be summarized. Needs the ticket id.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["ticketId"],"properties":{"ticketId":{"type":"string","format":"uuid","maxLength":36,"x-audit":"id","description":"The Ticket id."}}}`),
		Permission:  "tickets.view",
		Risk:        ai.RiskRead,
		Target:      &ai.Target{Type: "ticket", Arg: "ticketId"},
		Output: []ai.Field{
			{Path: "reference", Class: ai.ClassBusinessRecord, MaxLen: 40},
			{Path: "title", Class: ai.ClassBusinessRecord, MaxLen: 200},
			{Path: "status", Class: ai.ClassBusinessRecord, MaxLen: 40},
			{Path: "priority", Class: ai.ClassBusinessRecord, MaxLen: 40},
			{Path: "waitingReason", Class: ai.ClassBusinessRecord, MaxLen: 100},
			{Path: "description", Class: ai.ClassBusinessRecord, MaxLen: 4000},
			{Path: "resolution", Class: ai.ClassBusinessRecord, MaxLen: 2000},
			{Path: "createdAt", Class: ai.ClassBusinessRecord, MaxLen: 40},
			{Path: "updatedAt", Class: ai.ClassBusinessRecord, MaxLen: 40},
			{Path: "reporter", Class: ai.ClassPersonalContact, MaxLen: 100},
			{Path: "affectedUser", Class: ai.ClassPersonalContact, MaxLen: 100},
			{Path: "assignee", Class: ai.ClassPersonalContact, MaxLen: 100},
			{Path: "messagesTruncated", Class: ai.ClassBusinessRecord},
			{Path: "messages[].author", Class: ai.ClassPersonalContact, MaxLen: 100},
			{Path: "messages[].internal", Class: ai.ClassBusinessRecord},
			{Path: "messages[].body", Class: ai.ClassBusinessRecord, MaxLen: 2000},
			{Path: "messages[].createdAt", Class: ai.ClassBusinessRecord, MaxLen: 40},
		},
		Handler: func(ctx context.Context, c ai.Caller, input json.RawMessage) (any, error) {
			var in struct {
				TicketID string `json:"ticketId"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, ai.ErrToolInvalid
			}
			// The module's own authorization decides: the same Get an API call would run, as the requesting User.
			d, err := svc.Get(ctx, application.Principal{UserID: c.UserID, View: c.Has("tickets.view"), Manage: c.Has("tickets.manage")}, in.TicketID)
			switch {
			case errors.Is(err, application.ErrNotFound):
				return nil, ai.ErrToolNotFound
			case errors.Is(err, application.ErrForbidden):
				return nil, ai.ErrToolForbidden
			case err != nil:
				return nil, err
			}
			t := d.Ticket
			out := ticketSummary{Reference: t.Reference, Title: t.Title, Status: t.Status, Priority: t.Priority, WaitingReason: t.WaitingReason,
				Description: t.Description, Resolution: t.Resolution, Reporter: d.Names[t.ReporterID], AffectedUser: d.Names[t.AffectedUserID],
				CreatedAt: t.CreatedAt.UTC().Format(aiTimeLayout), UpdatedAt: t.UpdatedAt.UTC().Format(aiTimeLayout), Messages: []ticketMessage{}}
			if t.AssigneeID != nil {
				if n, ok := d.Names[*t.AssigneeID]; ok {
					out.Assignee = &n
				}
			}
			comments := d.Comments
			if len(comments) > aiMaxMessages {
				comments, out.MessagesTruncated = comments[len(comments)-aiMaxMessages:], true
			}
			for _, cm := range comments {
				out.Messages = append(out.Messages, ticketMessage{Author: d.Names[cm.AuthorID], Internal: cm.Internal, Body: cm.Body, CreatedAt: cm.CreatedAt.UTC().Format(aiTimeLayout)})
			}
			return out, nil
		},
	}}
}
