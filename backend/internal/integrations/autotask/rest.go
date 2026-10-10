package autotask

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/providerstatus"
)

// REST is the Autotask PSA REST client (Mode real, status unverified until an authenticated call succeeded). It is
// written strictly from the vendor documentation and has not been exercised against a live Autotask database.
//
// Documentation (https://autotask.net/help/developerhelp/Content/APIs/REST/..., all read 2026-10-10):
//   - Zone discovery, API_Calls/REST_ZoneInformation.htm: GET https://webservices.autotask.net/atservicesrest/v1.0/zoneInformation?user=<login>
//     needs no authentication and answers zoneName, url (https://webservices3.autotask.net/atservicesrest/), webUrl, ci.
//   - Authentication, General_Topics/REST_Security_Auth.htm: the headers UserName, Secret, ApiIntegrationCode and
//     Content-Type: application/json on every call; an API-only user; TLS 1.2; failed authentication answers 401.
//   - Limits, General_Topics/REST_Thresholds_Limits.htm: 10,000 requests per hour per database across all integrations,
//     added latency above 50 percent usage, at most 500 records per query, 5 minute execution cut-off. The page names
//     no HTTP status or Retry-After for an exceeded limit, so 429 and 5xx are both treated as transient.
//   - Tickets, Entities/TicketsEntity.htm: /Tickets supports POST (create), PATCH and PUT (update) and /Tickets/query;
//     required on create: companyID, priority, status, title (255) and conditionally queueID and dueDateTime;
//     description 8000, resolution 32000, externalID 50 characters; no delete.
//   - Query, API_Calls/REST_Basic_Query_Calls.htm: GET /Tickets/query?search={"filter":[{"op":"eq","field":...,"value":...}]}
//     answers {"items":[...],"pageDetails":{...}}; Advanced_Query_Features: MaxRecords 1..500, IncludeFields.
//   - Create/update, API_Calls/REST_Creating_Resources_POST.htm and REST_Updating_Data_PATCH.htm: success is HTTP 200
//     with {"itemId": n} for POST; PATCH needs the record id in the body and its answer is not relied on; any
//     non-200 answer is an error with {"errors":[...]}, whose text can echo ticket data and is therefore never kept.
//
// Idempotency. A created ticket carries the Turaco ticket reference in its externalID field. Before creating, the
// client looks the reference up, so a create whose answer was lost (timeout, crash before the mapping was stored)
// finds the ticket on the retry instead of creating a second one.
type REST struct {
	cfg     RESTConfig
	now     func() time.Time
	http    *http.Client
	tracker *providerstatus.Tracker

	secretMu    sync.Mutex
	secretMTime time.Time
	secret      string

	zoneMu   sync.Mutex
	zoneBase *url.URL
	zoneAt   time.Time

	limitMu sync.Mutex
	window  []time.Time
	sem     chan struct{}
}

var _ Gateway = (*REST)(nil)

// RESTConfig configures the client.
type RESTConfig struct {
	Username        string
	SecretFile      string
	IntegrationCode string
	CompanyID       int64
	QueueID         int64
	// StatusMap and PriorityMap map Turaco values (new, open, in_progress, waiting, resolved, closed, cancelled and
	// low, normal, high, urgent) to the tenant's picklist values.
	StatusMap   map[string]int
	PriorityMap map[string]int
	// RatePerHour is the client side ceiling of requests per rolling hour (default 3000).
	RatePerHour int
	// MaxConcurrent bounds simultaneous requests (default 2).
	MaxConcurrent int
	Timeout       time.Duration
	MaxBodyBytes  int64

	// ZoneLookupBase defaults to https://webservices.autotask.net. Tests point it at a local server with Transport
	// and AllowInsecureHTTP; production code never sets them.
	ZoneLookupBase    string
	Transport         http.RoundTripper
	AllowInsecureHTTP bool
	Now               func() time.Time
}

const (
	defaultZoneLookup = "https://webservices.autotask.net"
	zoneTTL           = 12 * time.Hour
	maxTitle          = 255
	maxDescription    = 8000
	maxResolution     = 32000
	maxRefLen         = 50
)

var (
	zoneHost   = regexp.MustCompile(`^webservices[0-9a-z]{0,6}\.autotask\.net$`)
	referenceR = regexp.MustCompile(`^[A-Za-z0-9._-]{1,50}$`)
)

// NewREST builds a client. It sends nothing and reads the secret file once to fail early.
func NewREST(cfg RESTConfig) (*REST, error) {
	switch {
	case cfg.Username == "" || cfg.SecretFile == "" || cfg.IntegrationCode == "":
		return nil, errors.New("autotask: username, secret file and integration code are required")
	case cfg.CompanyID <= 0:
		return nil, errors.New("autotask: company id is required")
	}
	for _, k := range []string{"new", "open", "in_progress", "waiting", "resolved", "closed", "cancelled"} {
		if cfg.StatusMap[k] <= 0 {
			return nil, fmt.Errorf("autotask: status %q is not mapped", k)
		}
	}
	for _, k := range []string{"low", "normal", "high", "urgent"} {
		if cfg.PriorityMap[k] <= 0 {
			return nil, fmt.Errorf("autotask: priority %q is not mapped", k)
		}
	}
	if cfg.RatePerHour <= 0 {
		cfg.RatePerHour = 3000
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 4 << 20
	}
	if cfg.ZoneLookupBase == "" {
		cfg.ZoneLookupBase = defaultZoneLookup
	}
	c := &REST{cfg: cfg, now: cfg.Now, sem: make(chan struct{}, cfg.MaxConcurrent)}
	if c.now == nil {
		c.now = time.Now
	}
	c.tracker = providerstatus.NewTracker(c.now)
	rt := cfg.Transport
	if rt == nil {
		rt = &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: cfg.Timeout, MaxIdleConns: 4, IdleConnTimeout: 60 * time.Second, ForceAttemptHTTP2: true,
		}
	}
	c.http = &http.Client{Transport: rt, Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if _, err := c.readSecret(); err != nil {
		return nil, err
	}
	return c, nil
}

// Status implements providerstatus.Reporter.
func (c *REST) Status() providerstatus.Snapshot { return c.tracker.Status() }

func (c *REST) readSecret() (string, error) {
	c.secretMu.Lock()
	defer c.secretMu.Unlock()
	fi, err := os.Stat(c.cfg.SecretFile)
	if err != nil {
		return "", errors.New("autotask: the API secret file cannot be read")
	}
	if c.secret != "" && fi.ModTime().Equal(c.secretMTime) {
		return c.secret, nil
	}
	data, err := os.ReadFile(c.cfg.SecretFile)
	if err != nil {
		return "", errors.New("autotask: the API secret file cannot be read")
	}
	s := strings.TrimRight(string(data), "\r\n")
	if s == "" || len(data) > 64<<10 || strings.ContainsAny(s, "\r\n") {
		return "", errors.New("autotask: the API secret file is empty or malformed")
	}
	c.secret, c.secretMTime = s, fi.ModTime()
	return s, nil
}

func permanent(format string, args ...any) error {
	return &Error{Message: "autotask: " + fmt.Sprintf(format, args...), Permanent: true}
}

func transient(format string, args ...any) error {
	return &Error{Message: "autotask: " + fmt.Sprintf(format, args...)}
}

// ---- zone ------------------------------------------------------------------------------------------------

// zone returns the zone specific REST base (for example https://webservices3.autotask.net/atservicesrest/v1.0).
func (c *REST) zone(ctx context.Context, force bool) (*url.URL, error) {
	c.zoneMu.Lock()
	defer c.zoneMu.Unlock()
	if !force && c.zoneBase != nil && c.now().Sub(c.zoneAt) < zoneTTL {
		return c.zoneBase, nil
	}
	lookup := strings.TrimRight(c.cfg.ZoneLookupBase, "/") + "/atservicesrest/v1.0/zoneInformation?user=" + url.QueryEscape(c.cfg.Username)
	body, status, err := c.send(ctx, http.MethodGet, lookup, nil, false)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, permanent("zone discovery failed (HTTP %d)", status)
	}
	var z struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(body, &z) != nil {
		return nil, permanent("zone discovery answered invalid json")
	}
	u, err := url.Parse(z.URL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, permanent("zone discovery answered an invalid url")
	}
	if !c.cfg.AllowInsecureHTTP && !zoneURLAllowed(u) {
		return nil, permanent("zone discovery answered a host outside autotask.net")
	}
	base := *u
	base.Path = strings.TrimRight(u.Path, "/") + "/v1.0"
	c.zoneBase, c.zoneAt = &base, c.now()
	return c.zoneBase, nil
}

// zoneURLAllowed accepts only https URLs of webservices<N>.autotask.net on the default port.
func zoneURLAllowed(u *url.URL) bool {
	return u.Scheme == "https" && zoneHost.MatchString(strings.ToLower(u.Hostname())) && u.Port() == ""
}

// ---- transport -------------------------------------------------------------------------------------------

// allow applies the client side request ceiling (a rolling hour) before anything is sent.
func (c *REST) allow() error {
	c.limitMu.Lock()
	defer c.limitMu.Unlock()
	now := c.now()
	cut := now.Add(-time.Hour)
	i := 0
	for i < len(c.window) && !c.window[i].After(cut) {
		i++
	}
	c.window = c.window[i:]
	if len(c.window) >= c.cfg.RatePerHour {
		return transient("the local request ceiling of %d per hour is reached", c.cfg.RatePerHour)
	}
	c.window = append(c.window, now)
	return nil
}

// send performs one HTTP call. Network failures, 429 and 5xx are transient errors; the response body of other
// statuses is returned to the caller.
func (c *REST) send(ctx context.Context, method, rawURL string, payload []byte, authenticated bool) ([]byte, int, error) {
	if err := c.allow(); err != nil {
		return nil, 0, err
	}
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, 0, transient("waiting for a request slot was cancelled")
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, 0, permanent("invalid request")
	}
	if !c.cfg.AllowInsecureHTTP && req.URL.Scheme != "https" {
		return nil, 0, permanent("only https is allowed")
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		secret, err := c.readSecret()
		if err != nil {
			return nil, 0, permanent("%s", strings.TrimPrefix(err.Error(), "autotask: "))
		}
		// Header names as documented: UserName, Secret, ApiIntegrationCode (set directly to keep the spelling).
		req.Header["UserName"] = []string{c.cfg.Username}
		req.Header["Secret"] = []string{secret}
		req.Header["ApiIntegrationCode"] = []string{c.cfg.IntegrationCode}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.tracker.Failure("network")
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, 0, transient("the request timed out or was cancelled")
		}
		return nil, 0, transient("the request failed (network)") // the url.Error text would carry the URL
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBodyBytes+1))
	if err != nil {
		c.tracker.Failure("network")
		return nil, 0, transient("reading the response failed")
	}
	if int64(len(data)) > c.cfg.MaxBodyBytes {
		c.tracker.Failure("response_too_large")
		return nil, 0, permanent("the response is larger than the cap")
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		c.tracker.Failure("http_" + strconv.Itoa(resp.StatusCode))
		return nil, resp.StatusCode, transient("the service answered HTTP %d", resp.StatusCode)
	}
	return data, resp.StatusCode, nil
}

// call performs an authenticated request against the zone, refreshing the zone once when the credentials are
// refused (the database may have moved zones).
func (c *REST) call(ctx context.Context, method, path string, query url.Values, payload any) ([]byte, error) {
	var raw []byte
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, permanent("invalid payload")
		}
		raw = b
	}
	for attempt := 0; attempt < 2; attempt++ {
		base, err := c.zone(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		u := *base
		u.Path = strings.TrimRight(u.Path, "/") + path
		if query != nil {
			u.RawQuery = query.Encode()
		}
		data, status, err := c.send(ctx, method, u.String(), raw, true)
		if err != nil {
			return nil, err
		}
		switch {
		case status == http.StatusUnauthorized && attempt == 0:
			continue
		case status == http.StatusUnauthorized, status == http.StatusForbidden:
			c.tracker.Failure("http_" + strconv.Itoa(status))
			return nil, permanent("the API user was refused (HTTP %d)", status)
		case status != http.StatusOK && status != http.StatusCreated:
			c.tracker.Failure("http_" + strconv.Itoa(status))
			return nil, permanent("the request was rejected (HTTP %d)", status)
		}
		c.tracker.Success()
		return data, nil
	}
	return nil, permanent("the API user was refused (HTTP 401)")
}

// ---- tickets ---------------------------------------------------------------------------------------------

// Upsert implements Gateway. externalID is the Autotask ticket id (decimal); empty creates, after looking the
// Turaco reference up.
func (c *REST) Upsert(ctx context.Context, t Ticket, externalID string) (string, error) {
	if !referenceR.MatchString(t.Reference) {
		return "", permanent("the ticket reference is not a valid external id")
	}
	status, ok := c.cfg.StatusMap[t.Status]
	if !ok {
		return "", permanent("the ticket status is not mapped")
	}
	priority, ok := c.cfg.PriorityMap[t.Priority]
	if !ok {
		return "", permanent("the ticket priority is not mapped")
	}
	if strings.TrimSpace(t.Title) == "" {
		return "", permanent("the ticket has no title")
	}
	if externalID == "" {
		found, err := c.findByReference(ctx, t.Reference)
		if err != nil {
			return "", err
		}
		externalID = found
	}
	fields := map[string]any{"title": clip(t.Title, maxTitle), "status": status, "priority": priority}
	if strings.TrimSpace(t.Description) != "" {
		fields["description"] = clip(t.Description, maxDescription)
	}
	if strings.TrimSpace(t.Resolution) != "" {
		fields["resolution"] = clip(t.Resolution, maxResolution)
	}
	if externalID == "" {
		fields["companyID"], fields["externalID"] = c.cfg.CompanyID, t.Reference
		if c.cfg.QueueID > 0 {
			fields["queueID"] = c.cfg.QueueID
		}
		data, err := c.call(ctx, http.MethodPost, "/Tickets", nil, fields)
		if err != nil {
			return "", err
		}
		var out struct {
			ItemID json.Number `json:"itemId"`
		}
		if json.Unmarshal(data, &out) != nil || !validID(out.ItemID.String()) {
			// The ticket may exist; the retry finds it by its reference.
			return "", transient("the create answer carried no ticket id")
		}
		return out.ItemID.String(), nil
	}
	if !validID(externalID) {
		return "", permanent("the stored external ticket id is invalid")
	}
	id, _ := strconv.ParseInt(externalID, 10, 64)
	fields["id"] = id
	if _, err := c.call(ctx, http.MethodPatch, "/Tickets", nil, fields); err != nil {
		return "", err
	}
	return externalID, nil
}

// findByReference returns the id of the ticket whose externalID is the Turaco reference, or "".
func (c *REST) findByReference(ctx context.Context, reference string) (string, error) {
	search, _ := json.Marshal(map[string]any{
		"filter":        []map[string]any{{"op": "eq", "field": "externalID", "value": reference}},
		"MaxRecords":    2,
		"IncludeFields": []string{"id", "externalID"},
	})
	data, err := c.call(ctx, http.MethodGet, "/Tickets/query", url.Values{"search": {string(search)}}, nil)
	if err != nil {
		return "", err
	}
	var out struct {
		Items []struct {
			ID         json.Number `json:"id"`
			ExternalID string      `json:"externalID"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &out) != nil {
		return "", transient("the lookup answered invalid json")
	}
	var ids []string
	for _, it := range out.Items {
		if it.ExternalID == reference && validID(it.ID.String()) {
			ids = append(ids, it.ID.String())
		}
	}
	switch len(ids) {
	case 0:
		return "", nil
	case 1:
		return ids[0], nil
	}
	return "", permanent("several tickets carry the same Turaco reference")
}

func validID(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == s
}

// clip shortens s to at most n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
