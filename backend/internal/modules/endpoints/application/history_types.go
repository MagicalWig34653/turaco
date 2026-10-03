package application

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// History kinds (F6 slice 4). Only meaningful changes are history: an assignment appearing, changing or
// disappearing, a changed normalized observation state, a Device joining or leaving a provider group. Repeated
// identical syncs add nothing.
const (
	HistoryAssignmentAdded      = "assignment_added"
	HistoryAssignmentChanged    = "assignment_changed"
	HistoryAssignmentRemoved    = "assignment_removed"
	HistoryArtifactRemoved      = "artifact_removed"
	HistoryObservationFirstSeen = "observation_first_seen"
	HistoryObservationChanged   = "observation_changed"
	HistoryGroupJoined          = "group_joined"
	HistoryGroupLeft            = "group_left"
)

// Fields an assignment_changed entry can name.
const (
	ChangeTarget = "target"
	ChangeMode   = "mode"
	ChangeIntent = "intent"
	ChangeFilter = "filter"
)

// AssignmentScopeDevice names which assignment changes a Device history includes: the ones addressing the Device
// itself (all_devices, or a provider group the Device was a member of at the time). Assignments that reach the
// Device only through its User are not derived here.
const AssignmentScopeDevice = "all_devices_and_device_groups"

// HistoryCursor is the keyset position of a history page: entries are ordered newest first by (At, Key) and a page
// continues strictly before the cursor. Key is compared bytewise.
type HistoryCursor struct {
	At  time.Time
	Key string
}

// EncodeHistoryCursor makes the opaque cursor string.
func EncodeHistoryCursor(c HistoryCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%s", c.At.UTC().UnixMicro(), c.Key)))
}

// DecodeHistoryCursor parses a cursor made by EncodeHistoryCursor; an empty string is no cursor.
func DecodeHistoryCursor(s string) (*HistoryCursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) > 120 {
		return nil, ErrInvalidCursor
	}
	micros, key, ok := strings.Cut(string(raw), "|")
	if !ok || key == "" || len(key) > 64 {
		return nil, ErrInvalidCursor
	}
	for _, r := range key {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r == ':' || r == '-') {
			return nil, ErrInvalidCursor
		}
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	return &HistoryCursor{At: time.UnixMicro(n).UTC(), Key: key}, nil
}

// allows reports whether (at, key) sorts strictly after the cursor in the newest-first order (strictly older).
func (c *HistoryCursor) allows(at time.Time, key string) bool {
	if c == nil {
		return true
	}
	if !at.Equal(c.At) {
		return at.Before(c.At)
	}
	return key < c.Key
}

// AssignmentValues are the meaningful fields of an assignment row. FilterName is filled for the current side only.
type AssignmentValues struct {
	TargetKind string
	Group      *string
	Mode       string
	Intent     string
	FilterID   *string
	FilterMode string
	FilterName *string
}

// AssignmentEventQuery selects assignment events newest first. ArtifactID selects one artifact; DeviceID (with
// Provider) the artifacts that address the Device. Exactly one of them is set.
type AssignmentEventQuery struct {
	ArtifactID string
	DeviceID   string
	Provider   string
	After      *HistoryCursor
	Limit      int
}

// AssignmentEventRow is one assignment event as stored: Cur is the row the event is about (for a removal the row that
// was closed), Prev the row it replaced for a change.
type AssignmentEventRow struct {
	Key                  string
	Kind                 string
	OccurredAt           time.Time
	Source               string
	ObservedAt           time.Time
	ArtifactID           string
	ArtifactName         string
	ArtifactKind         string
	ArtifactDeleted      bool
	AssignmentID         string
	ProviderAssignmentID string
	Cur                  AssignmentValues
	Prev                 *AssignmentValues
}

// ObservationEventRow is one change of the normalized observation state of an artifact on a Device.
type ObservationEventRow struct {
	Key             string
	OccurredAt      time.Time
	Source          string
	ArtifactID      string
	ArtifactName    string
	ArtifactKind    string
	ArtifactDeleted bool
	State           string
	PrevState       *string
	RawStatus       string
}

// MembershipEventRow is a Device joining or leaving a provider group.
type MembershipEventRow struct {
	Key             string
	Kind            string
	OccurredAt      time.Time
	Source          string
	GroupExternalID string
}

// HistoryArtifact names the artifact an entry is about.
type HistoryArtifact struct {
	ID      string
	Name    string
	Kind    string
	Deleted bool
}

// AssignmentSnapshot is an assignment as of a history entry. Group is redacted without organization.directory.view.
type AssignmentSnapshot struct {
	TargetKind string
	Group      *GroupRef
	Mode       string
	Intent     string
	FilterMode string
	FilterID   *string
	FilterName *string
	groupExt   *string
}

// AssignmentChange describes an assignment entry. Previous and Changes are set for assignment_changed only.
type AssignmentChange struct {
	AssignmentID         string
	ProviderAssignmentID string
	Current              AssignmentSnapshot
	Previous             *AssignmentSnapshot
	Changes              []string
}

// ObservationChange describes an observation entry. PreviousState is empty for the first sighting.
type ObservationChange struct {
	State         string
	PreviousState string
	RawStatus     string
}

// HistoryEntry is one meaningful change. OccurredAt is when Turaco's synchronization recorded it (the provider
// reports no change time for assignments and group membership; for observations it is the provider's observation
// time); ObservedAt is the provider observation the entry rests on, with Source. Nothing here is a polling record.
type HistoryEntry struct {
	Kind        string
	OccurredAt  time.Time
	Source      string
	ObservedAt  time.Time
	Artifact    *HistoryArtifact
	Assignment  *AssignmentChange
	Observation *ObservationChange
	Group       *GroupRef
	key         string
}

// ArtifactHistory is one page of an artifact's history.
type ArtifactHistory struct {
	Artifact   Artifact
	Items      []HistoryEntry
	NextCursor string
}

// DeviceHistory is one page of a Device's management history. AssignmentScope says which assignment changes are
// derived (AssignmentScopeDevice).
type DeviceHistory struct {
	DeviceID        string
	Items           []HistoryEntry
	NextCursor      string
	AssignmentScope string
}
