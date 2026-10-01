package ldap

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

const (
	testBindPassword = "s3cr3t-Bind-Pa55word"
	testUserBaseDN   = "OU=Users,DC=example,DC=test"
	testGroupBaseDN  = "OU=Groups,DC=example,DC=test"
	testUserFilter   = "(&(objectCategory=person)(objectClass=user))"
	testGroupFilter  = "(objectClass=group)"
)

func adConfig() config.LDAPConfig {
	return config.LDAPConfig{
		ProviderKey:   "ad",
		URL:           "ldaps://dc.example.test:636",
		BindDN:        "CN=svc-turaco,OU=Service,DC=example,DC=test",
		DirectoryType: config.DirectoryTypeActiveDirectory,
		UserBaseDN:    testUserBaseDN,
		UserFilter:    testUserFilter,
		GroupBaseDN:   testGroupBaseDN,
		GroupFilter:   testGroupFilter,
	}
}

func openLDAPConfig() config.LDAPConfig {
	cfg := adConfig()
	cfg.DirectoryType = config.DirectoryTypeOpenLDAP
	cfg.UserFilter = "(objectClass=inetOrgPerson)"
	cfg.GroupFilter = "(objectClass=groupOfNames)"
	return cfg
}

// encodeGUID is the inverse of decodeGUID: it turns a canonical UUID string
// into the mixed-endian bytes Active Directory stores in objectGUID.
func encodeGUID(t testing.TB, uuid string) string {
	t.Helper()
	raw := strings.ReplaceAll(uuid, "-", "")
	if len(raw) != 32 {
		t.Fatalf("bad test uuid %q", uuid)
	}
	b := make([]byte, 16)
	for i := range b {
		v, err := strconv.ParseUint(raw[2*i:2*i+2], 16, 8)
		if err != nil {
			t.Fatalf("bad test uuid %q", uuid)
		}
		b[i] = byte(v)
	}
	b[0], b[1], b[2], b[3] = b[3], b[2], b[1], b[0]
	b[4], b[5] = b[5], b[4]
	b[6], b[7] = b[7], b[6]
	return string(b)
}

// testID returns a distinct canonical UUID for n.
func testID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", n)
}

func adUserEntry(t testing.TB, dn string, id string, attrs map[string][]string) *goldap.Entry {
	t.Helper()
	m := map[string][]string{attrObjectGUID: {encodeGUID(t, id)}}
	for k, v := range attrs {
		m[k] = v
	}
	return goldap.NewEntry(dn, m)
}

func adUser(t testing.TB, dn, id, sam string, uac string) *goldap.Entry {
	t.Helper()
	return adUserEntry(t, dn, id, map[string][]string{
		attrSAMAccount: {sam},
		attrAccountCtl: {uac},
	})
}

func adGroup(t testing.TB, dn, id, cn string) *goldap.Entry {
	t.Helper()
	return goldap.NewEntry(dn, map[string][]string{
		attrObjectGUID: {encodeGUID(t, id)},
		attrCN:         {cn},
	})
}

func ldapUser(dn, id, uid string, attrs map[string][]string) *goldap.Entry {
	m := map[string][]string{attrEntryUUID: {id}, attrUID: {uid}}
	for k, v := range attrs {
		m[k] = v
	}
	return goldap.NewEntry(dn, m)
}

func ldapGroup(dn, id, cn string) *goldap.Entry {
	return goldap.NewEntry(dn, map[string][]string{attrEntryUUID: {id}, attrCN: {cn}})
}

// recordedSearch is one search request observed by the fake.
type recordedSearch struct {
	baseDN    string
	scope     int
	filter    string
	attrs     []string
	pageSize  uint32 // 0 for non-paged searches
	hasPaging bool
	timeLimit int
}

// fakeDirectory is an in-memory directory behind the directoryConn interface.
type fakeDirectory struct {
	users  []*goldap.Entry
	groups []*goldap.Entry // without member attributes
	// members lists the direct members of a group by group DN.
	members map[string][]string
	// rangeSize > 0 simulates AD range retrieval: groups with more members
	// than rangeSize return "member;range=0-<rangeSize-1>".
	rangeSize int

	dialErr       error
	startTLSErr   error
	bindErr       error
	userSearchErr error
	groupSearchEr error
	rangeSearchEr error
	// userSearchPartial is returned together with userSearchErr, as go-ldap does.
	userSearchPartial []*goldap.Entry
	// blockUsers makes the user search block until the connection is closed.
	blockUsers bool
	// dialBlock makes dial block until released; used for context tests.
	dialBlock chan struct{}
	// pageLen > 0 overrides the number of entries per page; 0 uses the page
	// size requested by the client.
	pageLen int
	// endlessCookie makes every page return a cookie, as a hostile server could.
	endlessCookie bool
	// mutateRange lets a test corrupt range responses.
	mutateRange func(attr *goldap.EntryAttribute)

	mu           sync.Mutex
	dials        int
	closes       int
	startTLS     int
	startTLSCfg  *tls.Config
	boundUser    string
	boundPass    string
	searches     []recordedSearch
	timeouts     []time.Duration
	closedSignal chan struct{}
	closeOnce    sync.Once
}

func newFakeDirectory() *fakeDirectory {
	return &fakeDirectory{members: map[string][]string{}, closedSignal: make(chan struct{})}
}

func (f *fakeDirectory) dial(rawURL string, tlsConfig *tls.Config) (directoryConn, error) {
	f.mu.Lock()
	f.dials++
	block := f.dialBlock
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	return &fakeConn{dir: f}, nil
}

func (f *fakeDirectory) snapshotSearches() []recordedSearch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedSearch(nil), f.searches...)
}

func (f *fakeDirectory) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

type fakeConn struct{ dir *fakeDirectory }

func (c *fakeConn) StartTLS(cfg *tls.Config) error {
	c.dir.mu.Lock()
	defer c.dir.mu.Unlock()
	c.dir.startTLS++
	c.dir.startTLSCfg = cfg
	return c.dir.startTLSErr
}

func (c *fakeConn) Bind(user, pass string) error {
	c.dir.mu.Lock()
	defer c.dir.mu.Unlock()
	c.dir.boundUser, c.dir.boundPass = user, pass
	return c.dir.bindErr
}

func (c *fakeConn) SetTimeout(d time.Duration) {
	c.dir.mu.Lock()
	defer c.dir.mu.Unlock()
	c.dir.timeouts = append(c.dir.timeouts, d)
}

func (c *fakeConn) Close() error {
	c.dir.mu.Lock()
	c.dir.closes++
	c.dir.mu.Unlock()
	c.dir.closeOnce.Do(func() { close(c.dir.closedSignal) })
	return nil
}

func (c *fakeConn) record(req *goldap.SearchRequest, paged bool, size uint32) {
	c.dir.mu.Lock()
	defer c.dir.mu.Unlock()
	c.dir.searches = append(c.dir.searches, recordedSearch{
		baseDN: req.BaseDN, scope: req.Scope, filter: req.Filter,
		attrs: append([]string(nil), req.Attributes...), pageSize: size, hasPaging: paged,
		timeLimit: req.TimeLimit,
	})
}

// pagingOf returns the paging control of a request.
func pagingOf(req *goldap.SearchRequest) (*goldap.ControlPaging, bool) {
	p, ok := goldap.FindControl(req.Controls, goldap.ControlTypePaging).(*goldap.ControlPaging)
	return p, ok && p != nil
}

// page slices entries according to the paging control of the request and
// attaches the response control with the cookie of the next page.
func (c *fakeConn) page(req *goldap.SearchRequest, paging *goldap.ControlPaging, entries []*goldap.Entry) *goldap.SearchResult {
	size := int(paging.PagingSize)
	if c.dir.pageLen > 0 {
		size = c.dir.pageLen
	}
	start := 0
	if len(paging.Cookie) > 0 {
		start, _ = strconv.Atoi(string(paging.Cookie))
	}
	if start > len(entries) {
		start = len(entries)
	}
	end := start + size
	if end > len(entries) {
		end = len(entries)
	}
	var cookie []byte
	if end < len(entries) || c.dir.endlessCookie {
		cookie = []byte(strconv.Itoa(end))
	}
	return &goldap.SearchResult{
		Entries:  entries[start:end],
		Controls: []goldap.Control{&goldap.ControlPaging{PagingSize: paging.PagingSize, Cookie: cookie}},
	}
}

// searchPagedBase serves the user and group searches.
func (c *fakeConn) searchPagedBase(req *goldap.SearchRequest, paging *goldap.ControlPaging) (*goldap.SearchResult, error) {
	d := c.dir
	switch req.BaseDN {
	case testUserBaseDN:
		if d.blockUsers {
			<-d.closedSignal
			return nil, goldap.NewError(goldap.ErrorNetwork, errors.New("connection closed"))
		}
		if d.userSearchErr != nil {
			return &goldap.SearchResult{Entries: d.userSearchPartial}, d.userSearchErr
		}
		return c.page(req, paging, d.users), nil
	case testGroupBaseDN:
		if d.groupSearchEr != nil {
			return nil, d.groupSearchEr
		}
		entries := make([]*goldap.Entry, 0, len(d.groups))
		for _, g := range d.groups {
			entries = append(entries, d.groupWithMembers(g))
		}
		return c.page(req, paging, entries), nil
	}
	return nil, goldap.NewError(goldap.LDAPResultNoSuchObject, errors.New("no such base"))
}

func (f *fakeDirectory) groupWithMembers(g *goldap.Entry) *goldap.Entry {
	members := f.members[g.DN]
	attrs := append([]*goldap.EntryAttribute(nil), g.Attributes...)
	switch {
	case len(members) == 0:
	case f.rangeSize > 0 && len(members) > f.rangeSize:
		attrs = append(attrs, goldap.NewEntryAttribute(fmt.Sprintf("member;range=0-%d", f.rangeSize-1), members[:f.rangeSize]))
	default:
		attrs = append(attrs, goldap.NewEntryAttribute(attrMember, members))
	}
	return &goldap.Entry{DN: g.DN, Attributes: attrs}
}

func (c *fakeConn) Search(req *goldap.SearchRequest) (*goldap.SearchResult, error) {
	if paging, ok := pagingOf(req); ok {
		c.record(req, true, paging.PagingSize)
		return c.searchPagedBase(req, paging)
	}
	c.record(req, false, 0)
	d := c.dir
	if d.rangeSearchEr != nil {
		return nil, d.rangeSearchEr
	}
	if req.Scope != goldap.ScopeBaseObject || len(req.Attributes) != 1 || !strings.HasPrefix(req.Attributes[0], "member;range=") {
		return nil, goldap.NewError(goldap.LDAPResultProtocolError, errors.New("unexpected search"))
	}
	spec := strings.TrimSuffix(strings.TrimPrefix(req.Attributes[0], "member;range="), "-*")
	start, err := strconv.Atoi(spec)
	if err != nil {
		return nil, goldap.NewError(goldap.LDAPResultProtocolError, errors.New("bad range"))
	}
	members := d.members[req.BaseDN]
	if start >= len(members) {
		return &goldap.SearchResult{Entries: []*goldap.Entry{{DN: req.BaseDN}}}, nil
	}
	end := start + d.rangeSize
	name := fmt.Sprintf("member;range=%d-%d", start, end-1)
	if end >= len(members) {
		end = len(members)
		name = fmt.Sprintf("member;range=%d-*", start)
	}
	attr := goldap.NewEntryAttribute(name, members[start:end])
	if d.mutateRange != nil {
		d.mutateRange(attr)
	}
	return &goldap.SearchResult{Entries: []*goldap.Entry{{DN: req.BaseDN, Attributes: []*goldap.EntryAttribute{attr}}}}, nil
}

// newTestSource returns a Source wired to the fake directory.
func newTestSource(t testing.TB, cfg config.LDAPConfig, dir *fakeDirectory, logger *slog.Logger) *Source {
	t.Helper()
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s, err := NewSource(cfg, testBindPassword, logger)
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	s.dial = dir.dial
	return s
}
