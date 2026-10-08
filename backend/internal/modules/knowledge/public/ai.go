// Package public is the Knowledge module's public contract.
package public

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
)

type articleHit struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`
	Title     string `json:"title"`
	Snippet   string `json:"snippet"`
	Audience  string `json:"audience"`
	UpdatedAt string `json:"updatedAt"`
}

type searchResult struct {
	Items []articleHit `json:"items"`
}

// AITools returns the AI Tools of Knowledge. knowledge.search runs the module's own authorization-filtered list
// (published articles only; drafts and retired articles are never offered to the assistant, even to authors).
// Titles and snippets are business_record because internal-audience articles are business content.
func AITools(svc *application.Service) []ai.Tool {
	return []ai.Tool{{
		Name:        "knowledge.search",
		Description: "Search published knowledge articles the user may read. Returns at most 10 hits with id, reference, title and a short snippet; it does not return full article bodies.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string","minLength":1,"maxLength":200,"x-audit":"text","description":"Search words."},"limit":{"type":"integer","minimum":1,"maximum":10,"description":"Number of hits, default 5."}}}`),
		Permission:  "knowledge.view",
		Risk:        ai.RiskRead,
		Output: []ai.Field{
			{Path: "items[].id", Class: ai.ClassPublicReference, MaxLen: 40},
			{Path: "items[].reference", Class: ai.ClassPublicReference, MaxLen: 40},
			{Path: "items[].title", Class: ai.ClassBusinessRecord, MaxLen: 200},
			{Path: "items[].snippet", Class: ai.ClassBusinessRecord, MaxLen: 300},
			{Path: "items[].audience", Class: ai.ClassPublicReference, MaxLen: 20},
			{Path: "items[].updatedAt", Class: ai.ClassPublicReference, MaxLen: 40},
		},
		Handler: func(ctx context.Context, c ai.Caller, input json.RawMessage) (any, error) {
			var in struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, ai.ErrToolInvalid
			}
			if in.Limit == 0 {
				in.Limit = 5
			}
			res, err := svc.List(ctx, application.Principal{UserID: c.UserID, View: c.Has("knowledge.view")}, in.Query, application.StatusPublished, application.Page{Limit: in.Limit})
			switch {
			case errors.Is(err, application.ErrForbidden):
				return nil, ai.ErrToolForbidden
			case err != nil:
				return nil, err
			}
			out := searchResult{Items: []articleHit{}}
			for _, a := range res.Items {
				if len(out.Items) == in.Limit {
					break
				}
				out.Items = append(out.Items, articleHit{ID: a.ID, Reference: a.Reference, Title: a.Title, Snippet: snippet(a), Audience: a.Audience,
					UpdatedAt: a.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")})
			}
			return out, nil
		},
	}}
}

func snippet(a application.Article) string {
	s := strings.TrimSpace(a.Summary)
	if s == "" {
		s = strings.Join(strings.Fields(a.Body), " ")
	}
	if utf8.RuneCountInString(s) > 280 {
		s = string([]rune(s)[:280]) + "…"
	}
	return s
}
