package public

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
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
	return []ai.Tool{summarizeTool(svc), draftInternalNoteTool(svc)}
}

func aiPrincipal(c ai.Caller) application.Principal {
	return application.Principal{UserID: c.UserID, View: c.Has("tickets.view"), Manage: c.Has("tickets.manage")}
}

// noteInput is the input of tickets.draft_internal_note.
type noteInput struct {
	TicketID string `json:"ticketId"`
	Body     string `json:"body"`
}

// draftInternalNoteTool is the first write tool (F12 A-C): it proposes an internal note on a Ticket. Internal notes
// are visible to staff only and send nothing outside Turaco (no requester-visible reply, no mail, no mentions).
// The model only creates an AI Proposal; the note is added after the User confirms it, by AddInternalNoteOnce as
// that User, idempotent per proposal and only while the Ticket still has the version the proposal was made for.
func draftInternalNoteTool(svc *application.Service) ai.Tool {
	decode := func(input json.RawMessage) (noteInput, error) {
		var in noteInput
		if err := json.Unmarshal(input, &in); err != nil {
			return noteInput{}, ai.ErrToolInvalid
		}
		return in, nil
	}
	mapErr := func(err error) error {
		var inv *application.InvalidInputError
		var tr *application.InvalidTransitionError
		switch {
		case errors.Is(err, application.ErrNotFound):
			return ai.ErrToolNotFound
		case errors.Is(err, application.ErrForbidden):
			return ai.ErrToolForbidden
		case errors.Is(err, application.ErrVersionConflict):
			return ai.ErrToolStale
		case errors.As(err, &inv), errors.As(err, &tr), errors.Is(err, application.ErrCommentLimit):
			return ai.ErrToolInvalid
		}
		return err
	}
	return ai.Tool{
		Name:        "tickets.draft_internal_note",
		Description: "Propose an internal note (visible to staff only, never to the requester) on one Ticket. This does not write anything: the user reviews the exact text in Turaco and confirms or dismisses it. Needs the ticket id and the note text.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["ticketId","body"],"properties":{` +
			`"ticketId":{"type":"string","format":"uuid","maxLength":36,"x-audit":"id","description":"The Ticket id."},` +
			`"body":{"type":"string","minLength":1,"maxLength":2000,"x-audit":"text","description":"The complete text of the internal note."}}}`),
		Permission: "tickets.manage",
		Risk:       ai.RiskWrite,
		Target:     &ai.Target{Type: "ticket", Arg: "ticketId"},
		Write: &ai.WriteSpec{
			Effect: ai.EffectInternal,
			Prepare: func(ctx context.Context, c ai.Caller, input json.RawMessage) (ai.Preparation, error) {
				in, err := decode(input)
				if err != nil {
					return ai.Preparation{}, err
				}
				t, err := svc.InternalNoteTarget(ctx, aiPrincipal(c), in.TicketID)
				if err != nil {
					return ai.Preparation{}, mapErr(err)
				}
				return ai.Preparation{TargetVersion: t.Version, TargetLabel: t.Reference + " " + t.Title}, nil
			},
			Execute: func(ctx context.Context, c ai.Caller, input json.RawMessage, x ai.Execution) (ai.ExecutionResult, error) {
				in, err := decode(input)
				if err != nil {
					return ai.ExecutionResult{}, err
				}
				cm, err := svc.AddInternalNoteOnce(ctx, application.Caller{Actor: audit.UserActor(c.UserID), CorrelationID: c.CorrelationID},
					aiPrincipal(c), in.TicketID, in.Body, x.TargetVersion, x.IdempotencyKey)
				if err != nil {
					return ai.ExecutionResult{}, mapErr(err)
				}
				return ai.ExecutionResult{Result: ai.ResourceRef{Type: "ticket_comment", ID: cm.ID}}, nil
			},
		},
	}
}

func summarizeTool(svc *application.Service) ai.Tool {
	return ai.Tool{
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
			d, err := svc.Get(ctx, aiPrincipal(c), in.TicketID)
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
	}
}
