package public

import "context"

// RecipientReader pages active Users for notifications. The caller must check
// each candidate's effective permission before delivery.
type RecipientReader interface {
	ActiveUserIDs(ctx context.Context, after string, limit int) ([]string, error)
}

type NotificationRecipients struct{ reader RecipientReader }

func NewNotificationRecipients(reader RecipientReader) *NotificationRecipients {
	return &NotificationRecipients{reader: reader}
}

// ActiveUsers pages active User ids. It performs no permission filtering.
func (r *NotificationRecipients) ActiveUsers(ctx context.Context, after string, limit int) ([]string, error) {
	return r.reader.ActiveUserIDs(ctx, after, limit)
}
