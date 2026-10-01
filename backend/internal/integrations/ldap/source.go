package ldap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

const (
	// pageSize is the RFC 2696 paged-results page size.
	pageSize = 500
	// dialTimeout bounds TCP connect and the TLS handshake of ldaps://.
	dialTimeout = 10 * time.Second
	// defaultRequestTimeout applies per request when the context has no deadline.
	defaultRequestTimeout = 5 * time.Minute
	// maxRangeRequests bounds AD range retrieval per group (1500 values per
	// request), so a misbehaving server cannot make a fetch loop forever.
	maxRangeRequests = 10_000
)

var rangePrefix = strings.ToLower(attrMember) + ";range="

// directoryConn is the subset of go-ldap's Client that the adapter uses. It
// exists so Fetch can be tested against a fake directory.
type directoryConn interface {
	StartTLS(*tls.Config) error
	Bind(username, password string) error
	SetTimeout(time.Duration)
	Search(*goldap.SearchRequest) (*goldap.SearchResult, error)
	Close() error
}

// dialFunc opens an unauthenticated connection. For ldaps:// the connection is
// already TLS-protected and verified with tlsConfig.
type dialFunc func(rawURL string, tlsConfig *tls.Config) (directoryConn, error)

func dialLDAP(rawURL string, tlsConfig *tls.Config) (directoryConn, error) {
	conn, err := goldap.DialURL(rawURL,
		goldap.DialWithDialer(&net.Dialer{Timeout: dialTimeout}),
		goldap.DialWithTLSConfig(tlsConfig),
	)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// Source reads directory snapshots from one LDAP or Active Directory server.
// It implements public.DirectorySource. A Source is safe for concurrent use;
// every Fetch opens its own connection.
type Source struct {
	cfg       config.LDAPConfig
	password  secret
	schema    schema
	tlsConfig *tls.Config
	dial      dialFunc
	logger    *slog.Logger
}

var _ public.DirectorySource = (*Source)(nil)

// NewSource validates the configuration and prepares the TLS configuration.
// It performs no network access. bindPassword comes from ReadPasswordFile; it
// is kept in memory only, in a type that redacts itself when formatted or
// logged, and never included in errors.
func NewSource(cfg config.LDAPConfig, bindPassword string, logger *slog.Logger) (*Source, error) {
	if !cfg.Enabled() {
		return nil, errors.New("ldap: directory synchronization is not configured")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	sch, err := schemaFor(cfg.DirectoryType)
	if err != nil {
		return nil, err
	}
	for _, required := range []struct{ name, value string }{
		{"provider key", cfg.ProviderKey},
		{"bind DN", cfg.BindDN},
		{"user base DN", cfg.UserBaseDN},
		{"user filter", cfg.UserFilter},
		{"group base DN", cfg.GroupBaseDN},
		{"group filter", cfg.GroupFilter},
	} {
		if required.value == "" {
			return nil, fmt.Errorf("ldap: %s is required", required.name)
		}
	}
	if bindPassword == "" {
		return nil, errors.New("ldap: bind password is required")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, errors.New("ldap: directory URL is invalid")
	}
	switch u.Scheme {
	case "ldaps":
		if cfg.StartTLS {
			return nil, errors.New("ldap: StartTLS must not be combined with an ldaps URL")
		}
	case "ldap":
		if !cfg.StartTLS {
			// Defence in depth: configuration validation already restricts
			// this to development, but the adapter must not send the bind
			// password in clear text on its own authority.
			if !cfg.AllowPlaintext {
				return nil, errors.New("ldap: plain ldap:// without StartTLS is not allowed (requires explicit plaintext opt-in)")
			}
			logger.Warn("ldap: connection to the directory is not encrypted", "providerKey", cfg.ProviderKey)
		}
	default:
		return nil, errors.New("ldap: directory URL must use ldaps or ldap")
	}
	tlsConfig, err := buildTLSConfig(cfg.URL, cfg.CAFile)
	if err != nil {
		return nil, err
	}
	return &Source{
		cfg:       cfg,
		password:  newSecret(bindPassword),
		schema:    sch,
		tlsConfig: tlsConfig,
		dial:      dialLDAP,
		logger:    logger,
	}, nil
}

// ProviderKey identifies the configured directory provider.
func (s *Source) ProviderKey() string { return s.cfg.ProviderKey }

// Fetch reads every user and group of the configured directory and returns a
// complete snapshot. Any failure, including a cancelled or expired context,
// returns an error and no partial snapshot.
func (s *Source) Fetch(ctx context.Context) (public.DirectorySnapshot, error) {
	started := time.Now()
	users, groups, err := s.fetchRaw(ctx)
	if err != nil {
		return public.DirectorySnapshot{}, err
	}
	snapshot, err := mapSnapshot(s.schema, users, groups, s.logger)
	if err != nil {
		return public.DirectorySnapshot{}, err
	}
	s.logger.InfoContext(ctx, "ldap: fetched directory snapshot",
		"providerKey", s.cfg.ProviderKey,
		"users", len(snapshot.Users),
		"groups", len(snapshot.Groups),
		"duration", time.Since(started),
	)
	return snapshot, nil
}

func (s *Source) fetchRaw(ctx context.Context) ([]*goldap.Entry, []rawGroup, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, opError(ctx, "connect", err)
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, nil, err
	}
	var closeOnce sync.Once
	closeConn := func() { closeOnce.Do(func() { _ = conn.Close() }) }
	defer closeConn()
	// Closing the connection is the only way to interrupt a blocked request.
	stop := context.AfterFunc(ctx, closeConn)
	defer stop()

	if _, err := s.setTimeout(ctx, conn); err != nil {
		return nil, nil, err
	}
	if s.cfg.StartTLS {
		if err := conn.StartTLS(s.tlsConfig); err != nil {
			return nil, nil, opError(ctx, "starttls", err)
		}
	}
	if _, err := s.setTimeout(ctx, conn); err != nil {
		return nil, nil, err
	}
	if err := conn.Bind(s.cfg.BindDN, s.password.reveal()); err != nil {
		return nil, nil, opError(ctx, "bind", err)
	}

	users, err := s.searchPaged(ctx, conn, "search users", s.cfg.UserBaseDN, s.cfg.UserFilter, s.schema.userAttrs, maxUserEntries)
	if err != nil {
		return nil, nil, err
	}
	groupEntries, err := s.searchPaged(ctx, conn, "search groups", s.cfg.GroupBaseDN, s.cfg.GroupFilter, s.schema.groupAttrs, maxGroupEntries)
	if err != nil {
		return nil, nil, err
	}
	groups := make([]rawGroup, 0, len(groupEntries))
	for _, e := range groupEntries {
		members, err := s.groupMembers(ctx, conn, e)
		if err != nil {
			return nil, nil, err
		}
		groups = append(groups, rawGroup{entry: e, members: members})
	}
	return users, groups, nil
}

// connect dials in the background so a cancelled context is honoured even
// while the TCP or TLS handshake is in progress.
func (s *Source) connect(ctx context.Context) (directoryConn, error) {
	type result struct {
		conn directoryConn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := s.dial(s.cfg.URL, s.tlsConfig)
		done <- result{conn, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, opError(ctx, "connect", r.err)
		}
		return r.conn, nil
	case <-ctx.Done():
		go func() {
			if r := <-done; r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, opError(ctx, "connect", ctx.Err())
	}
}

// setTimeout derives the per-request timeout of the connection from the
// context deadline and returns it, so the caller can also pass it to the
// server as the search time limit.
func (s *Source) setTimeout(ctx context.Context, conn directoryConn) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, opError(ctx, "request", err)
	}
	timeout := defaultRequestTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			return 0, opError(ctx, "request", context.DeadlineExceeded)
		}
	}
	conn.SetTimeout(timeout)
	return timeout, nil
}

// serverTimeLimit converts a request timeout to the whole seconds of the LDAP
// search time limit (rounded up, at least 1: 0 would mean "no limit"). The
// server then stops working for a client that has already given up.
func serverTimeLimit(timeout time.Duration) int {
	seconds := int64((timeout + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	if seconds > math.MaxInt32 {
		seconds = math.MaxInt32
	}
	return int(seconds)
}

// searchPaged runs an RFC 2696 paged search and returns all entries. It pages
// by hand instead of using go-ldap's SearchWithPaging so that the number of
// entries and pages is bounded while results arrive: more than limit entries
// is an error, never a truncated result.
func (s *Source) searchPaged(ctx context.Context, conn directoryConn, op, baseDN, filter string, attrs []string, limit int) ([]*goldap.Entry, error) {
	paging := goldap.NewControlPaging(pageSize)
	// Allow for pages that are shorter than requested, but not for a server
	// that keeps returning cookies without making progress.
	maxPages := 2*(limit/pageSize) + 10
	var all []*goldap.Entry
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("ldap: %s: too many result pages", op)
		}
		timeout, err := s.setTimeout(ctx, conn)
		if err != nil {
			return nil, err
		}
		req := goldap.NewSearchRequest(baseDN, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases, 0, serverTimeLimit(timeout), false,
			filter, attrs, []goldap.Control{paging})
		result, err := conn.Search(req)
		if err != nil {
			// Entries received so far are discarded: a partial snapshot must
			// never be used.
			return nil, opError(ctx, op, err)
		}
		entries := nonNilEntries(result)
		if len(entries) > limit-len(all) {
			return nil, fmt.Errorf("ldap: %s: more than %d entries", op, limit)
		}
		all = append(all, entries...)

		cookie := pagingCookie(result)
		if len(cookie) == 0 {
			return all, nil
		}
		paging.SetCookie(cookie)
	}
}

func pagingCookie(result *goldap.SearchResult) []byte {
	if result == nil {
		return nil
	}
	if p, ok := goldap.FindControl(result.Controls, goldap.ControlTypePaging).(*goldap.ControlPaging); ok && p != nil {
		return p.Cookie
	}
	return nil
}

func nonNilEntries(result *goldap.SearchResult) []*goldap.Entry {
	if result == nil {
		return nil
	}
	entries := make([]*goldap.Entry, 0, len(result.Entries))
	for _, e := range result.Entries {
		if e != nil {
			entries = append(entries, e)
		}
	}
	return entries
}

// groupMembers returns all direct member DNs of a group entry. When the server
// limits the number of values per attribute (Active Directory returns
// "member;range=0-1499"), the remaining values are fetched with base-scope
// searches for "member;range=<next>-*" until a range ends with "*".
func (s *Source) groupMembers(ctx context.Context, conn directoryConn, e *goldap.Entry) ([]string, error) {
	var members []string
	var ranged []rangedAttr
	for _, a := range e.Attributes {
		name := strings.ToLower(a.Name)
		switch {
		case name == strings.ToLower(attrMember):
			members = append(members, a.Values...)
		case strings.HasPrefix(name, rangePrefix):
			r, ok := parseRange(name)
			if !ok {
				return nil, errors.New("ldap: search groups: malformed member range attribute")
			}
			r.values = a.Values
			ranged = append(ranged, r)
		}
	}
	switch len(ranged) {
	case 0:
		return members, nil
	case 1:
	default:
		return nil, errors.New("ldap: search groups: more than one member range attribute")
	}
	current := ranged[0]
	if current.low != 0 {
		return nil, errors.New("ldap: search groups: member range does not start at 0")
	}
	members = append(members, current.values...)

	for requests := 0; !current.last; requests++ {
		if requests >= maxRangeRequests {
			return nil, errors.New("ldap: search groups: too many member range requests")
		}
		next := current.high + 1
		if len(current.values) == 0 || next <= current.low {
			return nil, errors.New("ldap: search groups: member range made no progress")
		}
		timeout, err := s.setTimeout(ctx, conn)
		if err != nil {
			return nil, err
		}
		req := goldap.NewSearchRequest(e.DN, goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, serverTimeLimit(timeout), false,
			"(objectClass=*)", []string{attrMember + ";range=" + strconv.Itoa(next) + "-*"}, nil)
		result, err := conn.Search(req)
		if err != nil {
			return nil, opError(ctx, "search group members", err)
		}
		entries := nonNilEntries(result)
		if len(entries) != 1 {
			return nil, errors.New("ldap: search group members: group entry not returned")
		}
		var found []rangedAttr
		for _, a := range entries[0].Attributes {
			name := strings.ToLower(a.Name)
			if !strings.HasPrefix(name, rangePrefix) {
				continue
			}
			r, ok := parseRange(name)
			if !ok {
				return nil, errors.New("ldap: search group members: malformed member range attribute")
			}
			r.values = a.Values
			found = append(found, r)
		}
		if len(found) != 1 || found[0].low != next {
			return nil, errors.New("ldap: search group members: unexpected member range in response")
		}
		current = found[0]
		members = append(members, current.values...)
	}
	return members, nil
}

// rangedAttr is a parsed "member;range=<low>-<high|*>" attribute.
type rangedAttr struct {
	low, high int
	last      bool // the range ends with "*"
	values    []string
}

// parseRange parses a lowercase "member;range=low-high" attribute name.
func parseRange(name string) (rangedAttr, bool) {
	spec := strings.TrimPrefix(name, rangePrefix)
	lowStr, highStr, ok := strings.Cut(spec, "-")
	if !ok {
		return rangedAttr{}, false
	}
	low, err := strconv.Atoi(lowStr)
	if err != nil || low < 0 {
		return rangedAttr{}, false
	}
	if highStr == "*" {
		return rangedAttr{low: low, last: true}, true
	}
	high, err := strconv.Atoi(highStr)
	if err != nil || high < low {
		return rangedAttr{}, false
	}
	return rangedAttr{low: low, high: high}, true
}
