package microsoft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/providerstatus"
)

// ErrAuthFailed means the token endpoint refused the client credentials (a configuration problem, not worth a
// retry): an invalid or expired secret or certificate, an unknown application or an unsuitable scope.
// The OAuth error code (for example invalid_client) is kept in AuthError; the description is dropped because it
// can echo request details.
var ErrAuthFailed = errors.New("microsoft: token request refused")

// AuthError carries the OAuth error code of a refused token request.
type AuthError struct {
	Status int
	Code   string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("microsoft: token request refused: status %d code %s", e.Status, e.Code)
}

func (e *AuthError) Unwrap() error { return ErrAuthFailed }

const (
	// tokenSkew is how long before expiry a cached token is replaced.
	tokenSkew = 5 * time.Minute
	// maxTokenBytes bounds the token response.
	maxTokenBytes = 64 << 10
)

// TokenSource acquires and caches application (client credentials) access tokens for one resource.
// Documentation: https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-client-creds-grant-flow
// (read 2026-10-10): POST {authority}/{tenant}/oauth2/v2.0/token with client_id, scope=<resource>/.default,
// grant_type=client_credentials and either client_secret or client_assertion(+_type); the answer carries
// token_type, expires_in and access_token; errors are 400/401 with error, error_description and error_codes.
type TokenSource struct {
	client   *Client
	tokenURL string
	clientID string
	scope    string
	cred     Credential
	now      func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewTokenSource builds a source. authorityBase is https://login.microsoftonline.com (tests: a local server).
func NewTokenSource(client *Client, authorityBase, tenantID, clientID, scope string, cred Credential, now func() time.Time) *TokenSource {
	if now == nil {
		now = time.Now
	}
	return &TokenSource{
		client: client, clientID: clientID, scope: scope, cred: cred, now: now,
		tokenURL: strings.TrimRight(authorityBase, "/") + "/" + url.PathEscape(tenantID) + "/oauth2/v2.0/token",
	}
}

// Invalidate drops the cached token (after the resource answered 401).
func (t *TokenSource) Invalidate() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token, t.expires = "", time.Time{}
}

// Token returns a valid access token, acquiring a new one when the cached token is missing or about to expire.
// Failures are *TransientError (network, 429, 5xx; honour RetryAfter), *AuthError, or ErrResponseTooLarge and
// similar from the client.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && t.now().Add(tokenSkew).Before(t.expires) {
		return t.token, nil
	}
	form := url.Values{}
	form.Set("client_id", t.clientID)
	form.Set("scope", t.scope)
	form.Set("grant_type", "client_credentials")
	if err := t.cred.Apply(form, t.clientID, t.tokenURL); err != nil {
		return "", &AuthError{Code: "credential_unreadable"}
	}
	resp, err := t.client.Do(ctx, http.MethodPost, t.tokenURL, strings.NewReader(form.Encode()),
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Accept": {"application/json"}})
	if err != nil {
		return "", err
	}
	if resp.Status != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(resp.Body, &e)
		return "", &AuthError{Status: resp.Status, Code: providerstatus.SafeCode(e.Error)}
	}
	var tok struct {
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
		AccessToken string `json:"access_token"`
	}
	if len(resp.Body) > maxTokenBytes || json.Unmarshal(resp.Body, &tok) != nil || tok.AccessToken == "" ||
		(tok.TokenType != "" && !strings.EqualFold(tok.TokenType, "Bearer")) {
		return "", &AuthError{Status: resp.Status, Code: "invalid_token_response"}
	}
	life := time.Duration(tok.ExpiresIn) * time.Second
	if life <= 0 {
		life = 10 * time.Minute
	}
	if life > 24*time.Hour {
		life = 24 * time.Hour
	}
	t.token, t.expires = tok.AccessToken, t.now().Add(life)
	return t.token, nil
}
