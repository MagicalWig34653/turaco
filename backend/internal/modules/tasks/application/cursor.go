package application

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

// ListCursor is the keyset position of the shared task order (due date with none last, priority rank, id): the sort
// key of the last returned task. The store decodes it; My Work (public) encodes it per item.
type ListCursor struct {
	Due  string `json:"d"`
	Rank int    `json:"p"`
	ID   string `json:"i"`
}

// PriorityRank is the position of a priority in the shared order (urgent first).
func PriorityRank(priority string) int {
	switch priority {
	case PriorityUrgent:
		return 0
	case PriorityHigh:
		return 1
	case PriorityNormal:
		return 2
	}
	return 3
}

// EncodeCursor returns the list cursor that continues after the task.
func EncodeCursor(t Task) string {
	c := ListCursor{Due: "infinity", ID: t.ID, Rank: PriorityRank(t.Priority)}
	if t.DueAt != nil {
		c.Due = t.DueAt.UTC().Format(time.RFC3339Nano)
	}
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}
