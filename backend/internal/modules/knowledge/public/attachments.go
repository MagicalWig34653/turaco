package public

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// ArticleAttachmentOwner is the owner type of file attachments on Knowledge articles.
const ArticleAttachmentOwner = "knowledge_article"

// ArticleAttachments authorizes the attachments of Knowledge articles for the platform attachment service
// (ADR-0037): everyone who can read the article reads its attachments, editors (knowledge.manage) attach and
// delete.
type ArticleAttachments struct{ svc *application.Service }

// NewArticleAttachments builds the owner for the Knowledge service.
func NewArticleAttachments(svc *application.Service) *ArticleAttachments {
	return &ArticleAttachments{svc: svc}
}

// Access implements attachments.Owner. Reading uses the article's own visibility rules (drafts and retired
// articles only for editors).
func (a *ArticleAttachments) Access(ctx context.Context, p authorization.Principal, articleID string) (attachments.Access, error) {
	manage := p.Has("knowledge.manage")
	_, err := a.svc.Get(ctx, application.Principal{UserID: p.UserID, View: p.Has("knowledge.view"), Manage: manage}, articleID)
	if errors.Is(err, application.ErrNotFound) {
		return attachments.Access{}, attachments.ErrOwnerNotFound
	}
	if err != nil {
		return attachments.Access{}, err
	}
	return attachments.Access{Read: true, Attach: manage, Privileged: manage, Manage: manage}, nil
}
