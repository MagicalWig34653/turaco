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
	LastObservedAt string `json:"lastObservedAt"`
}

func toDirectoryGroupMember(m application.DirectoryGroupMember) directoryGroupMemberDTO {
	return directoryGroupMemberDTO{m.UserID, m.DisplayName, ts(m.LastObservedAt)}
}
