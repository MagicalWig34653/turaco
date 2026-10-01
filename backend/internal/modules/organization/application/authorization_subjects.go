package application

import (
	"context"
	"regexp"
	"strings"
)

// AuthorizationSubjectStore is the storage port behind the authorization
// contract (organization/public): Directory Group expansion and subject
// lookups for platform/authorization.
type AuthorizationSubjectStore interface {
	// GroupIDsOfUser returns the ids of the non-deleted Directory Groups the
	// user currently belongs to, directly or through currently observed
	// nesting (child group -> parent group).
	GroupIDsOfUser(ctx context.Context, userID string) ([]string, error)
	UserExists(ctx context.Context, id string) (bool, error)
	DirectoryGroupObserved(ctx context.Context, id string) (bool, error)
	DisplayNames(ctx context.Context, userIDs, groupIDs []string) (map[string]string, error)
	// ActiveUsers returns id -> true for each given id that is a User with
	// status "active"; every other id is absent.
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	// UsersByUsername returns the distinct ids of Users whose currently
	// observed (not deleted) external identity has the (case-insensitive)
	// username.
	UsersByUsername(ctx context.Context, username string) ([]string, error)
	// UserByEmail returns the User with the (case-insensitive) primary email.
	UserByEmail(ctx context.Context, email string) (id string, found bool, err error)
}

// AuthorizationSubjects serves the questions platform/authorization asks about
// Organization data. Malformed ids are "not found", never errors.
type AuthorizationSubjects struct {
	store AuthorizationSubjectStore
}

func NewAuthorizationSubjects(store AuthorizationSubjectStore) *AuthorizationSubjects {
	return &AuthorizationSubjects{store: store}
}

var subjectUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *AuthorizationSubjects) GroupIDsOfUser(ctx context.Context, userID string) ([]string, error) {
	if !subjectUUID.MatchString(userID) {
		return []string{}, nil
	}
	return s.store.GroupIDsOfUser(ctx, userID)
}

func (s *AuthorizationSubjects) UserExists(ctx context.Context, id string) (bool, error) {
	if !subjectUUID.MatchString(id) {
		return false, nil
	}
	return s.store.UserExists(ctx, id)
}

func (s *AuthorizationSubjects) DirectoryGroupObserved(ctx context.Context, id string) (bool, error) {
	if !subjectUUID.MatchString(id) {
		return false, nil
	}
	return s.store.DirectoryGroupObserved(ctx, id)
}

func (s *AuthorizationSubjects) DisplayNames(ctx context.Context, userIDs, groupIDs []string) (map[string]string, error) {
	keep := func(in []string) []string {
		var out []string
		for _, id := range in {
			if subjectUUID.MatchString(id) {
				out = append(out, id)
			}
		}
		return out
	}
	u, g := keep(userIDs), keep(groupIDs)
	if len(u) == 0 && len(g) == 0 {
		return map[string]string{}, nil
	}
	return s.store.DisplayNames(ctx, u, g)
}

// ActiveUsers returns id -> true for each id that is a User with status
// "active". Malformed ids are inactive.
func (s *AuthorizationSubjects) ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error) {
	var valid []string
	for _, id := range ids {
		if subjectUUID.MatchString(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return map[string]bool{}, nil
	}
	return s.store.ActiveUsers(ctx, valid)
}

// FindUser resolves a user reference: a UUID, a username that is unique across
// providers, or a primary email address. Ambiguous or unknown references are
// not found.
func (s *AuthorizationSubjects) FindUser(ctx context.Context, ref string) (string, bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > 320 {
		return "", false, nil
	}
	if subjectUUID.MatchString(ref) {
		ok, err := s.store.UserExists(ctx, ref)
		if err != nil || !ok {
			return "", false, err
		}
		return strings.ToLower(ref), true, nil
	}
	ids, err := s.store.UsersByUsername(ctx, ref)
	if err != nil {
		return "", false, err
	}
	switch len(ids) {
	case 1:
		return ids[0], true, nil
	case 0:
		if strings.Contains(ref, "@") {
			return s.store.UserByEmail(ctx, ref)
		}
	}
	return "", false, nil
}
