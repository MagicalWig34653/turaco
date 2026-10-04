package public

import "context"

// RecipientReader pages active Users for permission-filtered notifications.
// The caller must check each candidate's effective permission before delivery.
type RecipientReader interface {
	ActiveUserIDs(ctx context.Context, after string, limit int) ([]string, error)
}

type NotificationRecipients struct{ reader RecipientReader }

func NewNotificationRecipients(reader RecipientReader) *NotificationRecipients {
	return &NotificationRecipients{reader: reader}
}

func (r *NotificationRecipients) UsersWithPermission(ctx context.Context, _ string, after string, limit int) ([]string, error) {
	return r.reader.ActiveUserIDs(ctx, after, limit)
}
