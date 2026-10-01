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

// maxValueBytes bounds the length of every text value (attribute value or DN)
// the adapter passes on. Real names, mail addresses and DNs are far shorter; a
// longer value is a misconfigured or hostile directory. A variable so tests
// can inject a small limit.
var maxValueBytes = 4096

// replacementRune stands in for invalid UTF-8 in sanitized display text.
const replacementRune = "\uFFFD"

// invalidText reports whether a text value is unusable as is: longer than
// maxValueBytes, not valid UTF-8, or containing a control character (NUL and
// the other C0 controls, DEL and the C1 controls). Names and DNs never
// legitimately contain them, and they are the usual carriers of log, header
// and terminal injection.
func invalidText(s string) bool {
	if len(s) > maxValueBytes || !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// truncateRunes cuts s to at most max bytes at a rune boundary.
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// sanitizeText turns a free-text display value into safe text: invalid UTF-8
// becomes U+FFFD, control characters become a space, the result is truncated
// to maxValueBytes at a rune boundary and trimmed. changed reports whether
// anything beyond trimming was altered.
func sanitizeText(s string) (out string, changed bool) {
	if !invalidText(s) {
		return strings.TrimSpace(s), false
	}
	// Bound the work first; invalid bytes can expand to three bytes each.
	s = truncateRunes(s, maxValueBytes)
	s = strings.ToValidUTF8(s, replacementRune)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(truncateRunes(s, maxValueBytes)), true
}

// textReader reads attributes of entries and counts how often values had to
// be sanitized. The counts, never the values, are logged.
type textReader struct {
	sanitized int
}

// display returns the sanitized, trimmed first value of a free-text display
// attribute, or "".
func (r *textReader) display(e *goldap.Entry, attr string) string {
	out, changed := sanitizeText(e.GetEqualFoldAttributeValue(attr))
	if changed {
		r.sanitized++
	}
	return out
}

// identity returns the trimmed first value of an identity-relevant attribute.
// bad is true when the value is not valid text; the value is then "" so bad
// bytes are never passed on.
func identity(e *goldap.Entry, attr string) (v string, bad bool) {
	raw := e.GetEqualFoldAttributeValue(attr)
	if invalidText(raw) {
		return "", true
	}
	return strings.TrimSpace(raw), false
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

// value returns the trimmed first value of an attribute, or "".
func value(e *goldap.Entry, attr string) string {
	return strings.TrimSpace(e.GetEqualFoldAttributeValue(attr))
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

// mapSnapshot converts raw directory entries into a DirectorySnapshot.
//
// An entry without a usable stable ID, or a user without a parseable account
// state, fails the whole snapshot: omitting it would make the consumer treat
// the object as no longer observed, and these values cannot be repaired.
// Everything else degrades per entry so that one bad entry cannot stop every
// synchronization (and with it deactivations):
//
//   - free-text display attributes are sanitized;
//   - a user whose username, mail or employee number is not valid text (or
//     whose username is missing) is marked Invalid and stays in the snapshot;
//   - a DN that is not valid text is not indexed, and references that are not
//     valid text stay unresolved.
//
// DN references (manager, member) resolve only to a unique entry; ambiguous
// references stay unresolved. Only counts are logged, never values or DNs.
func mapSnapshot(sch schema, userEntries []*goldap.Entry, groups []rawGroup, logger *slog.Logger) (public.DirectorySnapshot, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	var probs problems
	var badUserID, badAccountControl, badGroupID int
	var invalidUsers, unindexableDNs, invalidReferences int
	text := &textReader{}

	users := make([]mappedUser, 0, len(userEntries))
	for _, e := range userEntries {
		id, ok := sch.objectID(e)
		if !ok {
			badUserID++
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
		invalid := false
		username, bad := identity(e, sch.usernameAttr)
		if bad || username == "" {
			invalid = true
		}
		email, bad := identity(e, attrMail)
		invalid = invalid || bad
		var employee string
		for _, attr := range sch.employeeAttrs {
			v, bad := identity(e, attr)
			if bad {
				invalid = true
				break
			}
			if employee = v; employee != "" {
				break
			}
		}
		if invalid {
			invalidUsers++
		}
		dn := e.DN
		if invalidText(dn) {
			dn = ""
			unindexableDNs++
		}
		// Not trimmed: a trailing space may be an escaped, significant part of
		// the DN. The DN parser ignores insignificant surrounding spaces.
		managerDN := e.GetEqualFoldAttributeValue(attrManager)
		users = append(users, mappedUser{
			user: public.DirectoryUser{
				ExternalID:        id,
				Username:          username,
				DistinguishedName: dn,
				DisplayName:       firstNonEmpty(text.display(e, attrDisplayName), text.display(e, attrCN), username),
				GivenName:         optional(text.display(e, attrGivenName)),
				FamilyName:        optional(text.display(e, attrSurname)),
				Email:             optional(email),
				EmployeeNumber:    optional(employee),
				Enabled:           enabled,
				Invalid:           invalid,
			},
			managerDN:  managerDN,
			hasManager: strings.TrimSpace(managerDN) != "",
		})
	}
	probs.add("users", badUserID, "entries with missing or invalid "+sch.idAttr)
	probs.add("users", badAccountControl, "entries with missing or invalid "+attrAccountCtl)

	type mappedGroup struct {
		group   public.DirectoryGroup
		dn      string
		members []string
	}
	mappedGroups := make([]mappedGroup, 0, len(groups))
	for _, g := range groups {
		id, ok := sch.objectID(g.entry)
		if !ok {
			badGroupID++
			continue
		}
		dn := g.entry.DN
		if invalidText(dn) {
			dn = ""
			unindexableDNs++
		}
		mappedGroups = append(mappedGroups, mappedGroup{
			group: public.DirectoryGroup{
				ExternalID: id,
				// A group without any name is still observable; its stable ID
				// is the only non-empty identifier it has.
				DisplayName: firstNonEmpty(text.display(g.entry, attrCN), text.display(g.entry, attrDisplayName), id),
				Description: optional(text.display(g.entry, attrDescription)),
			},
			dn:      dn,
			members: g.members,
		})
	}
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
		if u.user.DistinguishedName != "" {
			index.add(u.user.DistinguishedName, dnTarget{id: u.user.ExternalID})
		}
	}
	for _, g := range mappedGroups {
		if g.dn != "" {
			index.add(g.dn, dnTarget{id: g.group.ExternalID, isGroup: true})
		}
	}
	ambiguous := index.ambiguousKeys()

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
				if invalidText(u.managerDN) {
					invalidReferences++
				}
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
				if invalidText(memberDN) {
					invalidReferences++
				}
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

	if text.sanitized+invalidUsers+unindexableDNs+invalidReferences+ambiguous > 0 {
		logger.Warn("ldap: directory data was degraded instead of failing the fetch",
			"sanitizedValues", text.sanitized,
			"invalidUsers", invalidUsers,
			"unindexableDNs", unindexableDNs,
			"invalidReferences", invalidReferences,
			"ambiguousKeys", ambiguous)
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
