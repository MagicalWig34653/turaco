package application

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxTextBytes bounds every string Organization accepts from a snapshot.
const maxTextBytes = 4096

// SyncUser is a normalized snapshot user together with its attributes hash.
// For an Invalid user every attribute value is cleared; only ExternalID and
// Enabled are meaningful.
type SyncUser struct {
	SnapshotUser
	AttributesHash string
}

// validText reports whether s may be stored: valid UTF-8, no NUL or other
// control characters, and at most maxTextBytes bytes.
func validText(s string) bool {
	if len(s) > maxTextBytes || !utf8.ValidString(s) {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

// sanitizeDisplay makes a free-text display value storable: invalid UTF-8
// becomes U+FFFD, control characters become spaces, the result is truncated at
// a rune boundary and trimmed.
func sanitizeDisplay(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if len(s) > maxTextBytes {
		cut := maxTextBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return strings.TrimSpace(s)
}

func sanitizeDisplayPtr(p *string) *string {
	if p == nil {
		return nil
	}
	return emptyToNil(sanitizeDisplay(*p))
}

func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// identityText normalizes an identity-relevant optional value. ok is false
// when the value is present but not valid text.
func identityText(p *string) (v *string, ok bool) {
	if p == nil {
		return nil, true
	}
	t := strings.TrimSpace(*p)
	if !validText(t) {
		return nil, false
	}
	return emptyToNil(t), true
}

// prepareSnapshot validates the snapshot, sanitizes and normalizes its text
// and computes attributes hashes. A non-empty reason means the snapshot as a
// whole is invalid (ErrInvalidSnapshot); reasons carry indexes, never
// attribute values. Problems of a single entry never invalidate the snapshot:
// display text is sanitized, and invalid identity-relevant text marks the user
// Invalid.
func prepareSnapshot(snap DirectorySnapshot) ([]SyncUser, []SnapshotGroup, string) {
	if len(snap.Users) == 0 {
		return nil, nil, "snapshot contains no users"
	}
	ids := make(map[string]struct{}, len(snap.Users)+len(snap.Groups))
	checkID := func(kind string, i int, id string) string {
		at := kind + "[" + strconv.Itoa(i) + "]"
		if id == "" {
			return at + ": empty external id"
		}
		if !validText(id) {
			return at + ": invalid external id"
		}
		if _, dup := ids[id]; dup {
			return at + ": external id used twice"
		}
		ids[id] = struct{}{}
		return ""
	}
	users := make([]SyncUser, 0, len(snap.Users))
	for i, u := range snap.Users {
		if reason := checkID("users", i, u.ExternalID); reason != "" {
			return nil, nil, reason
		}
		users = append(users, prepareUser(u))
	}
	groups := make([]SnapshotGroup, 0, len(snap.Groups))
	for i, g := range snap.Groups {
		if reason := checkID("groups", i, g.ExternalID); reason != "" {
			return nil, nil, reason
		}
		groups = append(groups, prepareGroup(g))
	}
	return users, groups, ""
}

func prepareUser(u SnapshotUser) SyncUser {
	out := SnapshotUser{ExternalID: u.ExternalID, Enabled: u.Enabled, Invalid: u.Invalid}
	username := strings.TrimSpace(u.Username)
	email, emailOK := identityText(u.Email)
	empNo, empOK := identityText(u.EmployeeNumber)
	display := sanitizeDisplay(u.DisplayName)
	if !validText(username) || !emailOK || !empOK || display == "" {
		out.Invalid = true
	}
	if out.Invalid {
		return SyncUser{SnapshotUser: out}
	}
	out.Username = username
	out.DistinguishedName = sanitizeDisplay(u.DistinguishedName)
	out.DisplayName = display
	out.GivenName = sanitizeDisplayPtr(u.GivenName)
	out.FamilyName = sanitizeDisplayPtr(u.FamilyName)
	out.Email = email
	out.EmployeeNumber = empNo
	if m, ok := identityText(u.ManagerExternalID); ok {
		out.ManagerExternalID = m
		out.ManagerUnresolved = u.ManagerUnresolved
	} else {
		out.ManagerUnresolved = true
	}
	return SyncUser{SnapshotUser: out, AttributesHash: attributesHash(out)}
}

// prepareGroup sanitizes display text and drops member references that are not
// valid text (counted as unresolved). A group without a usable name is shown
// by its external ID.
func prepareGroup(g SnapshotGroup) SnapshotGroup {
	out := SnapshotGroup{ExternalID: g.ExternalID, UnresolvedMembers: g.UnresolvedMembers}
	out.DisplayName = sanitizeDisplay(g.DisplayName)
	if out.DisplayName == "" {
		out.DisplayName = g.ExternalID
	}
	out.Description = sanitizeDisplayPtr(g.Description)
	keep := func(in []string) []string {
		var res []string
		for _, id := range in {
			if id == "" || !validText(id) {
				out.UnresolvedMembers++
				continue
			}
			res = append(res, id)
		}
		return res
	}
	out.MemberUserIDs = keep(g.MemberUserIDs)
	out.MemberGroupIDs = keep(g.MemberGroupIDs)
	return out
}

// attributesHash is SHA-256 over a length-prefixed canonical encoding of every
// directory-owned field except the manager reference, which is resolved
// separately on every run.
func attributesHash(u SnapshotUser) string {
	h := sha256.New()
	str := func(s string) {
		h.Write([]byte(strconv.Itoa(len(s))))
		h.Write([]byte{':'})
		h.Write([]byte(s))
	}
	opt := func(p *string) {
		if p == nil {
			h.Write([]byte("-"))
			return
		}
		h.Write([]byte("+"))
		str(*p)
	}
	str(u.ExternalID)
	str(u.Username)
	str(u.DistinguishedName)
	str(u.DisplayName)
	opt(u.GivenName)
	opt(u.FamilyName)
	opt(u.Email)
	opt(u.EmployeeNumber)
	if u.Enabled {
		h.Write([]byte("E"))
	} else {
		h.Write([]byte("D"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
