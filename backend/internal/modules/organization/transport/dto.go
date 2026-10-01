package transport

import (
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

type listResponse[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func toList[A, T any](r application.Result[A], conv func(A) T) listResponse[T] {
	items := make([]T, 0, len(r.Items))
	for _, it := range r.Items {
		items = append(items, conv(it))
	}
	return listResponse[T]{Items: items, NextCursor: r.NextCursor}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

type userDTO struct {
	ID                string  `json:"id"`
	DisplayName       string  `json:"displayName"`
	GivenName         *string `json:"givenName"`
	FamilyName        *string `json:"familyName"`
	PrimaryEmail      *string `json:"primaryEmail"`
	Status            string  `json:"status"`
	DepartmentID      *string `json:"departmentId"`
	PrimaryLocationID *string `json:"primaryLocationId"`
	ManagerUserID     *string `json:"managerUserId"`
	UpdatedAt         string  `json:"updatedAt"`
}

func toUser(u application.User) userDTO {
	return userDTO{u.ID, u.DisplayName, u.GivenName, u.FamilyName, u.PrimaryEmail, u.Status, u.DepartmentID, u.PrimaryLocationID, u.ManagerUserID, ts(u.UpdatedAt)}
}

type teamDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Active    bool   `json:"active"`
	UpdatedAt string `json:"updatedAt"`
}

func toTeam(t application.Team) teamDTO { return teamDTO{t.ID, t.Name, t.Active, ts(t.UpdatedAt)} }

type teamMemberDTO struct {
	UserID      string  `json:"userId"`
	DisplayName string  `json:"displayName"`
	Role        *string `json:"role"`
	Source      string  `json:"source"`
	ValidFrom   string  `json:"validFrom"`
}

func toTeamMember(m application.TeamMember) teamMemberDTO {
	return teamMemberDTO{m.UserID, m.DisplayName, m.Role, m.Source, ts(m.ValidFrom)}
}

type locationDTO struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	ExternalKey *string `json:"externalKey"`
	Active      bool    `json:"active"`
	UpdatedAt   string  `json:"updatedAt"`
}

func toLocation(l application.Location) locationDTO {
	return locationDTO{l.ID, l.Name, l.ExternalKey, l.Active, ts(l.UpdatedAt)}
}

type directoryGroupDTO struct {
	ID                string  `json:"id"`
	ProviderKey       string  `json:"providerKey"`
	ExternalID        string  `json:"externalId"`
	DisplayName       string  `json:"displayName"`
	Description       *string `json:"description"`
	FirstObservedAt   string  `json:"firstObservedAt"`
	LastObservedAt    string  `json:"lastObservedAt"`
	DeletedObservedAt *string `json:"deletedObservedAt"`
}

func toDirectoryGroup(g application.DirectoryGroup) directoryGroupDTO {
	return directoryGroupDTO{g.ID, g.ProviderKey, g.ExternalID, g.DisplayName, g.Description, ts(g.FirstObservedAt), ts(g.LastObservedAt), tsPtr(g.DeletedObservedAt)}
}

type directoryGroupMemberDTO struct {
	UserID         string `json:"userId"`
	DisplayName    string `json:"displayName"`
	ObservedFrom   string `json:"observedFrom"`
	LastObservedAt string `json:"lastObservedAt"`
}

func toDirectoryGroupMember(m application.DirectoryGroupMember) directoryGroupMemberDTO {
	return directoryGroupMemberDTO{m.UserID, m.DisplayName, ts(m.ObservedFrom), ts(m.LastObservedAt)}
}

type externalIdentityDTO struct {
	ProviderKey       string  `json:"providerKey"`
	Username          *string `json:"username"`
	Enabled           bool    `json:"enabled"`
	LastSeenAt        *string `json:"lastSeenAt"`
	DeletedObservedAt *string `json:"deletedObservedAt"`
}

// userDetailDTO is the single-user response; lists keep the smaller userDTO.
type userDetailDTO struct {
	userDTO
	ExternalIdentities []externalIdentityDTO `json:"externalIdentities"`
}

func toUserDetail(u application.User, identities []application.ExternalIdentity) userDetailDTO {
	out := userDetailDTO{userDTO: toUser(u), ExternalIdentities: make([]externalIdentityDTO, 0, len(identities))}
	for _, e := range identities {
		out.ExternalIdentities = append(out.ExternalIdentities, externalIdentityDTO{e.ProviderKey, e.Username, e.Enabled, tsPtr(e.LastSeenAt), tsPtr(e.DeletedObservedAt)})
	}
	return out
}

type syncConflictDTO struct {
	Kind       string `json:"kind"`
	ExternalID string `json:"externalId"`
	Username   string `json:"username"`
}

type syncRunDTO struct {
	ID            string            `json:"id"`
	ProviderKey   string            `json:"providerKey"`
	JobID         *string           `json:"jobId"`
	Trigger       string            `json:"trigger"`
	StartedAt     string            `json:"startedAt"`
	ObservedAt    *string           `json:"observedAt"`
	FinishedAt    *string           `json:"finishedAt"`
	Outcome       string            `json:"outcome"`
	Counts        map[string]int    `json:"counts"`
	Conflicts     []syncConflictDTO `json:"conflicts"`
	ConflictCount int               `json:"conflictCount"`
	Error         *string           `json:"error"`
}

func toSyncRun(r application.DirectorySyncRun) syncRunDTO {
	out := syncRunDTO{
		ID: r.ID, ProviderKey: r.ProviderKey, JobID: r.JobID, Trigger: r.Trigger, StartedAt: ts(r.StartedAt),
		ObservedAt: tsPtr(r.ObservedAt), FinishedAt: tsPtr(r.FinishedAt), Outcome: r.Outcome,
		Counts: r.Counts, Conflicts: make([]syncConflictDTO, 0, len(r.Conflicts)), ConflictCount: r.ConflictCount, Error: r.Error,
	}
	if out.Counts == nil {
		out.Counts = map[string]int{}
	}
	for _, c := range r.Conflicts {
		out.Conflicts = append(out.Conflicts, syncConflictDTO{c.Kind, c.ExternalID, c.Username})
	}
	return out
}

type syncRequestDTO struct {
	JobID   string `json:"jobId"`
	Created bool   `json:"created"`
}
