package ldap

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
)

// rawGroup is a group entry together with its complete direct member list,
// including values fetched through AD range retrieval.
type rawGroup struct {
	entry   *goldap.Entry
	members []string
}

// decodeGUID converts an Active Directory objectGUID to the canonical
// lowercase UUID string. AD stores a GUID as 16 bytes in mixed-endian order:
// the first three fields are little-endian, the remaining bytes are as is.
func decodeGUID(b []byte) (string, bool) {
	if len(b) != 16 {
		return "", false
	}
	var u [16]byte
	u[0], u[1], u[2], u[3] = b[3], b[2], b[1], b[0]
	u[4], u[5] = b[5], b[4]
	u[6], u[7] = b[7], b[6]
	copy(u[8:], b[8:])
	return formatUUID(u), true
}

func formatUUID(u [16]byte) string {
	var dst [36]byte
	hex.Encode(dst[0:8], u[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], u[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], u[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], u[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:], u[10:])
	return string(dst[:])
}

// parseUUID validates the textual 8-4-4-4-12 form (as used by OpenLDAP
// entryUUID) and returns it in lowercase.
func parseUUID(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 36 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return "", false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return "", false
			}
		}
	}
	return strings.ToLower(s), true
}

func (s schema) objectID(e *goldap.Entry) (string, bool) {
	if s.idIsBinaryGUID {
		return decodeGUID(e.GetEqualFoldRawAttributeValue(s.idAttr))
	}
	return parseUUID(e.GetEqualFoldAttributeValue(s.idAttr))
}

// value returns the trimmed first value of an attribute, or "".
func value(e *goldap.Entry, attr string) string {
	return strings.TrimSpace(e.GetEqualFoldAttributeValue(attr))
}

// optional returns nil for an empty value.
func optional(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// maxValueBytes bounds the length of every text value (attribute value or DN)
// the adapter accepts. Real names, mail addresses and DNs are far shorter; a
// longer value is a misconfigured or hostile directory. A variable so tests
// can inject a small limit.
var maxValueBytes = 4096

// invalidText reports whether a text value must be rejected: too long, not
// valid UTF-8, or containing a control character (NUL and the other C0
// controls, DEL and the C1 controls). Names and DNs never legitimately contain
// them, and they are the usual carriers of log, header and terminal injection.
func invalidText(s string) (tooLong, invalid bool) {
	if len(s) > maxValueBytes {
		return true, false
	}
	if !utf8.ValidString(s) {
		return false, true
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false, true
		}
	}
	return false, false
}

// entryTextProblems checks the DN and all attribute values of an entry except
// the attributes skip selects (binary attributes, member lists that are
// checked separately). It reports only whether a problem exists, never which
// value.
func entryTextProblems(e *goldap.Entry, skip func(lowerName string) bool) (tooLong, invalid bool) {
	tooLong, invalid = invalidText(e.DN)
	for _, a := range e.Attributes {
		if skip != nil && skip(strings.ToLower(a.Name)) {
			continue
		}
		for _, v := range a.Values {
			tl, inv := invalidText(v)
			tooLong = tooLong || tl
			invalid = invalid || inv
		}
	}
	return tooLong, invalid
}

// problems counts entries that make the snapshot unusable. Only counts and
// attribute names are reported, never DNs or values.
type problems struct {
	parts []string
}

func (p *problems) add(kind string, count int, what string) {
	if count > 0 {
		p.parts = append(p.parts, fmt.Sprintf("%s: %d %s", kind, count, what))
	}
}

func (p *problems) err() error {
	if len(p.parts) == 0 {
		return nil
	}
	return errors.New("ldap: invalid directory data: " + strings.Join(p.parts, "; "))
}

type mappedUser struct {
	user       public.DirectoryUser
	managerDN  string
	hasManager bool
}

// mapSnapshot converts raw directory entries into a DirectorySnapshot. It is
// all-or-nothing: an entry without a usable stable ID (or, for users, without
// username or account state) fails the whole snapshot, because omitting it
// would make the consumer treat the object as no longer observed.
//
// DN references (manager, member) resolve only to a unique entry; ambiguous
// references stay unresolved and only their number is logged.
func mapSnapshot(sch schema, userEntries []*goldap.Entry, groups []rawGroup, logger *slog.Logger) (public.DirectorySnapshot, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	var probs problems
	var badUserID, badUsername, badAccountControl, badGroupID int
	var userTextInvalid, userTextTooLong, groupTextInvalid, groupTextTooLong, badMembers int

	skipBinaryID := func(name string) bool {
		return sch.idIsBinaryGUID && name == strings.ToLower(sch.idAttr)
	}
	skipMembers := func(name string) bool {
		return name == strings.ToLower(attrMember) || strings.HasPrefix(name, rangePrefix)
	}

	users := make([]mappedUser, 0, len(userEntries))
	for _, e := range userEntries {
		if tooLong, invalid := entryTextProblems(e, skipBinaryID); tooLong || invalid {
			if tooLong {
				userTextTooLong++
			}
			if invalid {
				userTextInvalid++
			}
			continue
		}
		id, ok := sch.objectID(e)
		if !ok {
			badUserID++
			continue
		}
		username := value(e, sch.usernameAttr)
		if username == "" {
			badUsername++
			continue
		}
		enabled := true
		if sch.hasAccountControl {
			uac, err := strconv.ParseInt(value(e, attrAccountCtl), 10, 64)
			if err != nil {
				badAccountControl++
				continue
			}
			enabled = uac&uacAccountDisable == 0
		}
		var employee string
		for _, attr := range sch.employeeAttrs {
			if employee = value(e, attr); employee != "" {
				break
			}
		}
		// Not trimmed: a trailing space may be an escaped, significant part of
		// the DN. The DN parser ignores insignificant surrounding spaces.
		managerDN := e.GetEqualFoldAttributeValue(attrManager)
		users = append(users, mappedUser{
			user: public.DirectoryUser{
				ExternalID:        id,
				Username:          username,
				DistinguishedName: e.DN,
				DisplayName:       firstNonEmpty(value(e, attrDisplayName), value(e, attrCN), username),
				GivenName:         optional(value(e, attrGivenName)),
				FamilyName:        optional(value(e, attrSurname)),
				Email:             optional(value(e, attrMail)),
				EmployeeNumber:    optional(employee),
				Enabled:           enabled,
			},
			managerDN:  managerDN,
			hasManager: strings.TrimSpace(managerDN) != "",
		})
	}
	probs.add("users", userTextInvalid, "entries with an attribute value that is not valid text (invalid UTF-8 or control character)")
	probs.add("users", userTextTooLong, "entries with an attribute value longer than "+strconv.Itoa(maxValueBytes)+" bytes")
	probs.add("users", badUserID, "entries with missing or invalid "+sch.idAttr)
	probs.add("users", badUsername, "entries with missing "+sch.usernameAttr)
	probs.add("users", badAccountControl, "entries with missing or invalid "+attrAccountCtl)

	type mappedGroup struct {
		group   public.DirectoryGroup
		dn      string
		members []string
	}
	mappedGroups := make([]mappedGroup, 0, len(groups))
	for _, g := range groups {
		tooLong, invalid := entryTextProblems(g.entry, func(name string) bool { return skipBinaryID(name) || skipMembers(name) })
		for _, m := range g.members {
			if tl, inv := invalidText(m); tl || inv {
				badMembers++
				break
			}
		}
		if tooLong || invalid {
			if tooLong {
				groupTextTooLong++
			}
			if invalid {
				groupTextInvalid++
			}
			continue
		}
		id, ok := sch.objectID(g.entry)
		if !ok {
			badGroupID++
			continue
		}
		mappedGroups = append(mappedGroups, mappedGroup{
			group: public.DirectoryGroup{
				ExternalID: id,
				// A group without any name is still observable; its stable ID
				// is the only non-empty identifier it has.
				DisplayName: firstNonEmpty(value(g.entry, attrCN), value(g.entry, attrDisplayName), id),
				Description: optional(value(g.entry, attrDescription)),
			},
			dn:      g.entry.DN,
			members: g.members,
		})
	}
	probs.add("groups", groupTextInvalid, "entries with an attribute value that is not valid text (invalid UTF-8 or control character)")
	probs.add("groups", groupTextTooLong, "entries with an attribute value longer than "+strconv.Itoa(maxValueBytes)+" bytes")
	probs.add("groups", badMembers, "entries with a member value that is not valid text or longer than "+strconv.Itoa(maxValueBytes)+" bytes")
	probs.add("groups", badGroupID, "entries with missing or invalid "+sch.idAttr)

	// IDs are unique across users and groups: an object matching both filters
	// would otherwise be written twice with different meanings.
	seen := make(map[string]struct{}, len(users)+len(mappedGroups))
	duplicates := 0
	for _, u := range users {
		if _, dup := seen[u.user.ExternalID]; dup {
			duplicates++
		}
		seen[u.user.ExternalID] = struct{}{}
	}
	for _, g := range mappedGroups {
		if _, dup := seen[g.group.ExternalID]; dup {
			duplicates++
		}
		seen[g.group.ExternalID] = struct{}{}
	}
	probs.add("users and groups", duplicates, "entries with a duplicate "+sch.idAttr)
	if err := probs.err(); err != nil {
		return public.DirectorySnapshot{}, err
	}

	index := newDNIndex(len(users) + len(mappedGroups))
	for _, u := range users {
		index.add(u.user.DistinguishedName, dnTarget{id: u.user.ExternalID})
	}
	for _, g := range mappedGroups {
		index.add(g.dn, dnTarget{id: g.group.ExternalID, isGroup: true})
	}
	if n := index.ambiguousKeys(); n > 0 {
		logger.Warn("ldap: directory contains distinguished names that cannot be told apart; references to them stay unresolved",
			"ambiguousKeys", n)
	}

	snapshot := public.DirectorySnapshot{
		Users:  make([]public.DirectoryUser, 0, len(users)),
		Groups: make([]public.DirectoryGroup, 0, len(mappedGroups)),
	}
	for _, u := range users {
		user := u.user
		if u.hasManager {
			// A manager is always a user; a reference that resolves to a group
			// or is ambiguous is unresolved.
			if t, ok := index.resolve(u.managerDN); ok && !t.isGroup {
				id := t.id
				user.ManagerExternalID = &id
			} else {
				user.ManagerUnresolved = true
			}
		}
		snapshot.Users = append(snapshot.Users, user)
	}
	for _, g := range mappedGroups {
		group := g.group
		userIDs := map[string]struct{}{}
		groupIDs := map[string]struct{}{}
		unresolved := map[string]struct{}{}
		for _, memberDN := range g.members {
			if strings.TrimSpace(memberDN) == "" {
				continue
			}
			t, ok := index.resolve(memberDN)
			switch {
			case !ok:
				unresolved[referenceKey(memberDN)] = struct{}{}
			case !t.isGroup:
				userIDs[t.id] = struct{}{}
			case t.id != group.ExternalID:
				groupIDs[t.id] = struct{}{}
			}
		}
		group.MemberUserIDs = sortedKeys(userIDs)
		group.MemberGroupIDs = sortedKeys(groupIDs)
		group.UnresolvedMembers = len(unresolved)
		snapshot.Groups = append(snapshot.Groups, group)
	}

	sort.Slice(snapshot.Users, func(i, j int) bool { return snapshot.Users[i].ExternalID < snapshot.Users[j].ExternalID })
	sort.Slice(snapshot.Groups, func(i, j int) bool { return snapshot.Groups[i].ExternalID < snapshot.Groups[j].ExternalID })
	return snapshot, nil
}

// sortedKeys returns the keys in order, or nil for an empty set.
func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
