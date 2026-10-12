package ai

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Turaco acts as an OAuth authorization server for exactly one protected resource, the MCP endpoint (ADR-0039,
// design M1 to M8). Codes and tokens are opaque 256-bit random strings; only their SHA-256 hashes are stored.

const (
	// MCPPath is the path of the MCP endpoint; BaseURL + MCPPath is the canonical resource and the token audience.
	MCPPath = "/api/v1/mcp"
	// MCPScope is the only scope: read tools of the MCP server.
	MCPScope = "mcp:read"

	mcpCodeTTL    = 60 * time.Second
	mcpAccessTTL  = 10 * time.Minute
	mcpGrantTTL   = 24 * time.Hour
	maxStateLen   = 500
	mcpTokenBytes = 32
)

var (
	pkceChallengeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	pkceVerifierRE  = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	clientIDRE      = regexp.MustCompile(`^tmcp_[a-z2-7]{26}$`)
)

// Errors of the MCP authorization server.
var (
	// ErrMCPInvalidClient means the client is unknown or disabled or the redirect URI is not registered. The
	// authorization page shows an error and never redirects (RFC 6749 section 4.1.2.1).
	ErrMCPInvalidClient = errors.New("ai: unknown mcp client or redirect uri")
	// ErrMCPInvalidToken is any unusable bearer token (unknown, expired, revoked, wrong audience, disabled client,
	// inactive or external account).
	ErrMCPInvalidToken = errors.New("ai: invalid mcp token")
)

// OAuthError is an OAuth error response (RFC 6749 section 5.2 and 4.1.2.1).
type OAuthError struct {
	Code        string
	Description string
}

func (e *OAuthError) Error() string { return "oauth: " + e.Code + ": " + e.Description }

func oauthErr(code, desc string) error { return &OAuthError{Code: code, Description: desc} }

// MCPMounted reports whether the startup gates allow the MCP server (AI_ENABLED, AI_MCP_ENABLED and a base URL).
func (s *Service) MCPMounted() bool {
	return s.cfg.Enabled && s.cfg.MCPEnabled && s.cfg.BaseURL != ""
}

// MCPIssuer is the OAuth issuer identifier.
func (s *Service) MCPIssuer() string { return s.cfg.BaseURL }

// MCPResource is the canonical resource URI of the MCP endpoint (the only audience).
func (s *Service) MCPResource() string { return s.cfg.BaseURL + MCPPath }

// MCPOpen reports whether MCP is usable right now: the startup gates and the runtime switches ai.settings.enabled
// and ai.settings.mcp_enabled (design M2). The module switch is checked by the HTTP layer. It returns ErrDisabled.
func (s *Service) MCPOpen(ctx context.Context) (Settings, error) {
	if !s.MCPMounted() {
		return Settings{}, ErrDisabled
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	if !st.Enabled || !st.MCPEnabled {
		return Settings{}, ErrDisabled
	}
	return st, nil
}

func newOpaque() (token string, hash []byte, err error) {
	b := make([]byte, mcpTokenBytes)
	if _, err = rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("generate token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	return token, h[:], nil
}

// hashOpaque returns the storage hash of a presented code or token; ok=false for anything that cannot be one.
func hashOpaque(token string) ([]byte, bool) {
	if len(token) != 43 {
		return nil, false
	}
	if b, err := base64.RawURLEncoding.DecodeString(token); err != nil || len(b) != mcpTokenBytes {
		return nil, false
	}
	h := sha256.Sum256([]byte(token))
	return h[:], true
}

// ---- authorization request (consent) ----

// AuthorizeRequest holds the parameters of an authorization request as the browser received them.
type AuthorizeRequest struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
	Scope               string
	State               string
}

// AuthorizePreview is what the consent screen shows.
type AuthorizePreview struct {
	ClientName  string
	ClientID    string
	RedirectURI string
	Tools       []string
	DataClasses []DataClass
	GrantHours  int
	Scope       string
}

func (s *Service) activeClient(ctx context.Context, clientID string) (MCPClient, error) {
	if !clientIDRE.MatchString(clientID) {
		return MCPClient{}, ErrNotFound
	}
	cl, err := scanMCPClient(s.store.pool.QueryRow(ctx, `SELECT `+mcpClientCols+` FROM ai.mcp_clients c WHERE c.client_id=$1 AND c.enabled`, clientID))
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPClient{}, ErrNotFound
	}
	return cl, err
}

// consentGate authorizes the signed-in User for the consent step: MCP open, ai.use, a valid session and tenant.
func (s *Service) consentGate(ctx context.Context, c Caller) error {
	if _, err := s.MCPOpen(ctx); err != nil {
		return err
	}
	if c.UserID == "" || c.TenantID != s.cfg.TenantID || !uuidRE.MatchString(c.SessionID) || !c.Has(PermUse) {
		return ErrForbidden
	}
	return nil
}

// checkAuthorize validates an authorization request. Client and redirect URI come first: until both are proven,
// every error is ErrMCPInvalidClient and nothing may be sent to the redirect URI.
func (s *Service) checkAuthorize(ctx context.Context, req AuthorizeRequest) (MCPClient, error) {
	cl, err := s.activeClient(ctx, req.ClientID)
	if errors.Is(err, ErrNotFound) {
		return MCPClient{}, ErrMCPInvalidClient
	} else if err != nil {
		return MCPClient{}, err
	}
	if req.RedirectURI == "" || !slices.Contains(cl.RedirectURIs, req.RedirectURI) {
		return MCPClient{}, ErrMCPInvalidClient
	}
	switch {
	case req.ResponseType != "code":
		return cl, oauthErr("unsupported_response_type", "response_type must be code")
	case req.CodeChallengeMethod != "S256":
		// No PKCE downgrade: plain and a missing method are refused.
		return cl, oauthErr("invalid_request", "code_challenge_method must be S256")
	case !pkceChallengeRE.MatchString(req.CodeChallenge):
		return cl, oauthErr("invalid_request", "code_challenge must be a base64url SHA-256 value")
	case req.Resource != s.MCPResource():
		return cl, oauthErr("invalid_target", "resource must be the Turaco MCP resource")
	case req.Scope != "" && req.Scope != MCPScope:
		return cl, oauthErr("invalid_scope", "the only scope is "+MCPScope)
	case len(req.State) > maxStateLen || !utf8.ValidString(req.State) || safetext.ContainsUnsafe(req.State, false):
		return cl, oauthErr("invalid_request", "state is too long or contains control characters")
	}
	return cl, nil
}

func (s *Service) offeredTo(c Caller, cl MCPClient) map[string]registered {
	return s.reg.Offered(c, ProviderRecord{AllowedDataClasses: cl.AllowedDataClasses})
}

// PreviewAuthorization validates an authorization request for the consent screen of the signed-in User.
func (s *Service) PreviewAuthorization(ctx context.Context, c Caller, req AuthorizeRequest) (AuthorizePreview, error) {
	if err := s.consentGate(ctx, c); err != nil {
		return AuthorizePreview{}, err
	}
	cl, err := s.checkAuthorize(ctx, req)
	if err != nil {
		return AuthorizePreview{}, err
	}
	out := AuthorizePreview{ClientName: cl.DisplayName, ClientID: cl.ClientID, RedirectURI: req.RedirectURI,
		DataClasses: cl.AllowedDataClasses, GrantHours: int(mcpGrantTTL.Hours()), Scope: MCPScope}
	for name := range s.offeredTo(c, cl) {
		out.Tools = append(out.Tools, name)
	}
	sort.Strings(out.Tools)
	if out.Tools == nil {
		out.Tools = []string{}
	}
	return out, nil
}

func appendQuery(redirect string, params url.Values) string {
	sep := "?"
	if strings.Contains(redirect, "?") {
		sep = "&"
	}
	return redirect + sep + params.Encode()
}

// Authorize records the User's decision and returns the redirect URL for the browser: with a fresh authorization
// code on approval, with error=access_denied otherwise. Consent is never remembered.
func (s *Service) Authorize(ctx context.Context, c Caller, req AuthorizeRequest, approve bool) (string, error) {
	if err := s.consentGate(ctx, c); err != nil {
		return "", err
	}
	cl, err := s.checkAuthorize(ctx, req)
	if err != nil {
		return "", err
	}
	params := url.Values{}
	if req.State != "" {
		params.Set("state", req.State)
	}
	params.Set("iss", s.MCPIssuer())
	meta := map[string]any{"clientId": cl.ClientID, "redirectUri": req.RedirectURI}
	if !approve {
		if err := s.record(ctx, c, "mcp", "ai.mcp.authorization_denied", "ai_mcp_client", cl.ID, meta); err != nil {
			return "", err
		}
		params.Set("error", "access_denied")
		return appendQuery(req.RedirectURI, params), nil
	}
	code, hash, err := newOpaque()
	if err != nil {
		return "", err
	}
	err = s.store.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO ai.mcp_codes (code_hash, client_ref, tenant_id, user_id, redirect_uri, code_challenge, resource, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7, now() + $8::interval)`, hash, cl.ID, c.TenantID, c.UserID, req.RedirectURI, req.CodeChallenge, req.Resource,
			fmt.Sprintf("%d seconds", int(mcpCodeTTL.Seconds()))); err != nil {
			return fmt.Errorf("insert mcp code: %w", err)
		}
		return audit.Record(ctx, tx, audit.Change{Action: "ai.mcp.authorized", TargetType: "ai_mcp_client", TargetID: cl.ID,
			Actor: audit.UserActor(c.UserID), CorrelationID: c.CorrelationID, Via: "mcp", TenantID: c.TenantID, Metadata: meta})
	})
	if err != nil {
		return "", err
	}
	params.Set("code", code)
	return appendQuery(req.RedirectURI, params), nil
}

// ---- token endpoint ----

// TokenRequest holds the form parameters of a token request.
type TokenRequest struct {
	GrantType     string
	Code          string
	RedirectURI   string
	ClientID      string
	CodeVerifier  string
	RefreshToken  string
	Resource      string
	CorrelationID string
}

// TokenResponse is a successful token response.
type TokenResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Scope        string
}

// Token runs the token endpoint (design M6, M7). Errors are *OAuthError for every client-visible outcome.
func (s *Service) Token(ctx context.Context, req TokenRequest) (TokenResponse, error) {
	if req.CorrelationID == "" {
		req.CorrelationID = "mcp:token"
	}
	if _, err := s.MCPOpen(ctx); err != nil {
		return TokenResponse{}, err
	}
	cl, err := s.activeClient(ctx, req.ClientID)
	if errors.Is(err, ErrNotFound) {
		return TokenResponse{}, oauthErr("invalid_client", "unknown or disabled client")
	} else if err != nil {
		return TokenResponse{}, err
	}
	switch req.GrantType {
	case "authorization_code":
		return s.redeemCode(ctx, cl, req)
	case "refresh_token":
		return s.refresh(ctx, cl, req)
	}
	return TokenResponse{}, oauthErr("unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
}

// userUsable checks that the grant's User may still use MCP: an active internal account holding ai.use.
func (s *Service) userUsable(ctx context.Context, userID string) (map[string]struct{}, error) {
	if s.cfg.Account == nil || s.cfg.Permissions == nil {
		return nil, ErrMCPInvalidToken
	}
	active, external, err := s.cfg.Account(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("check mcp account: %w", err)
	}
	if !active || external {
		return nil, ErrMCPInvalidToken
	}
	perms, err := s.cfg.Permissions(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load mcp permissions: %w", err)
	}
	if _, ok := perms[PermUse]; !ok {
		return nil, ErrForbidden
	}
	return perms, nil
}

func pkceMatches(verifier, challenge string) bool {
	if !pkceVerifierRE.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

type issued struct {
	access, refresh string
	expiresIn       int
}

// issueTokens stores a new access and refresh token pair for a grant inside tx.
func issueTokens(ctx context.Context, tx pgx.Tx, grantID, audience string, grantExpires time.Time, now time.Time) (issued, error) {
	access, ah, err := newOpaque()
	if err != nil {
		return issued{}, err
	}
	refresh, rh, err := newOpaque()
	if err != nil {
		return issued{}, err
	}
	accessExp := now.Add(mcpAccessTTL)
	if grantExpires.Before(accessExp) {
		accessExp = grantExpires
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ai.mcp_tokens (token_hash, grant_id, kind, audience, expires_at) VALUES
		($1,$3,'access',$4,$5), ($2,$3,'refresh',$4,$6)`, ah, rh, grantID, audience, accessExp, grantExpires); err != nil {
		return issued{}, fmt.Errorf("insert mcp tokens: %w", err)
	}
	return issued{access: access, refresh: refresh, expiresIn: max(int(accessExp.Sub(now).Seconds()), 1)}, nil
}

func (s *Service) auditGrant(ctx context.Context, tx pgx.Tx, action, grantID, userID, tenant, corr string, meta map[string]any) error {
	return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "ai_mcp_grant", TargetID: grantID,
		Actor: audit.UserActor(userID), CorrelationID: corr, Via: "mcp", TenantID: tenant, Metadata: meta})
}

// revokeGrant ends a grant (idempotent) and audits the revocation as the grant's User.
func (s *Service) revokeGrant(ctx context.Context, grantID, reason, corr string, meta map[string]any) error {
	return s.store.inTx(ctx, func(tx pgx.Tx) error {
		var user, tenant string
		err := tx.QueryRow(ctx, `UPDATE ai.mcp_grants SET revoked_at=now(), revoked_reason=$2 WHERE id=$1 AND revoked_at IS NULL
			RETURNING user_id::text, tenant_id`, grantID, reason).Scan(&user, &tenant)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return fmt.Errorf("revoke mcp grant: %w", err)
		}
		if meta == nil {
			meta = map[string]any{}
		}
		meta["reason"] = reason
		return s.auditGrant(ctx, tx, "ai.mcp.grant.revoked", grantID, user, tenant, corr, meta)
	})
}

func (s *Service) redeemCode(ctx context.Context, cl MCPClient, req TokenRequest) (TokenResponse, error) {
	hash, ok := hashOpaque(req.Code)
	if !ok {
		return TokenResponse{}, oauthErr("invalid_grant", "the authorization code is invalid")
	}
	var out TokenResponse
	var replayedGrant *string
	err := s.store.inTxKeep(ctx, func(tx pgx.Tx) error {
		var clientRef, tenant, user, redirect, challenge, resource string
		var expires time.Time
		// Single use: the first redemption consumes the code whatever its outcome.
		err := tx.QueryRow(ctx, `UPDATE ai.mcp_codes SET used_at=now() WHERE code_hash=$1 AND used_at IS NULL
			RETURNING client_ref::text, tenant_id, user_id::text, redirect_uri, code_challenge, resource, expires_at`, hash).
			Scan(&clientRef, &tenant, &user, &redirect, &challenge, &resource, &expires)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.QueryRow(ctx, `SELECT grant_id::text FROM ai.mcp_codes WHERE code_hash=$1 AND used_at IS NOT NULL`, hash).Scan(&replayedGrant); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			return oauthErr("invalid_grant", "the authorization code is invalid or was already used")
		} else if err != nil {
			return fmt.Errorf("consume mcp code: %w", err)
		}
		now := s.now()
		switch {
		case clientRef != cl.ID:
			return oauthErr("invalid_grant", "the authorization code was issued to another client")
		case !expires.After(now):
			return oauthErr("invalid_grant", "the authorization code has expired")
		case req.RedirectURI != redirect:
			return oauthErr("invalid_grant", "redirect_uri does not match the authorization request")
		case req.Resource != resource || resource != s.MCPResource():
			return oauthErr("invalid_target", "resource must be the Turaco MCP resource of the authorization request")
		case tenant != s.cfg.TenantID:
			return oauthErr("invalid_grant", "the authorization code is invalid")
		case !pkceMatches(req.CodeVerifier, challenge):
			return oauthErr("invalid_grant", "code_verifier does not match the code challenge")
		}
		if _, err := s.userUsable(ctx, user); err != nil {
			if errors.Is(err, ErrMCPInvalidToken) || errors.Is(err, ErrForbidden) {
				return oauthErr("invalid_grant", "the user may not use the MCP server")
			}
			return err
		}
		grantExp := now.Add(mcpGrantTTL)
		var grantID string
		if err := tx.QueryRow(ctx, `INSERT INTO ai.mcp_grants (client_ref, tenant_id, user_id, resource, expires_at) VALUES ($1,$2,$3,$4,$5) RETURNING id::text`,
			cl.ID, tenant, user, resource, grantExp).Scan(&grantID); err != nil {
			return fmt.Errorf("insert mcp grant: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE ai.mcp_codes SET grant_id=$2 WHERE code_hash=$1`, hash, grantID); err != nil {
			return err
		}
		is, err := issueTokens(ctx, tx, grantID, resource, grantExp, now)
		if err != nil {
			return err
		}
		out = TokenResponse{AccessToken: is.access, RefreshToken: is.refresh, ExpiresIn: is.expiresIn, Scope: MCPScope}
		return s.auditGrant(ctx, tx, "ai.mcp.grant.created", grantID, user, tenant, req.CorrelationID,
			map[string]any{"clientId": cl.ClientID, "expiresAt": grantExp.UTC().Format(time.RFC3339)})
	})
	var oe *OAuthError
	if errors.As(err, &oe) {
		// The consumption stays committed even though the request failed: a code is single use.
		if replayedGrant != nil {
			if rerr := s.revokeGrant(ctx, *replayedGrant, "code_replay", req.CorrelationID, map[string]any{"clientId": cl.ClientID}); rerr != nil {
				return TokenResponse{}, rerr
			}
		}
		return TokenResponse{}, err
	}
	return out, err
}

// inTxKeep runs fn in a transaction and commits even when fn returns an *OAuthError, so a consumed code or refresh
// token stays consumed. Other errors roll back.
func (s *Store) inTxKeep(ctx context.Context, fn func(tx pgx.Tx) error) error {
	var kept error
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := fn(tx)
		var oe *OAuthError
		if errors.As(err, &oe) {
			kept = err
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	return kept
}

func (s *Service) refresh(ctx context.Context, cl MCPClient, req TokenRequest) (TokenResponse, error) {
	hash, ok := hashOpaque(req.RefreshToken)
	if !ok {
		return TokenResponse{}, oauthErr("invalid_grant", "the refresh token is invalid")
	}
	var out TokenResponse
	var replayedGrant *string
	err := s.store.inTxKeep(ctx, func(tx pgx.Tx) error {
		var grantID, audience string
		err := tx.QueryRow(ctx, `UPDATE ai.mcp_tokens SET used_at=now() WHERE token_hash=$1 AND kind='refresh' AND used_at IS NULL AND expires_at > now()
			RETURNING grant_id::text, audience`, hash).Scan(&grantID, &audience)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.QueryRow(ctx, `SELECT grant_id::text FROM ai.mcp_tokens WHERE token_hash=$1 AND kind='refresh' AND used_at IS NOT NULL`, hash).Scan(&replayedGrant); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			return oauthErr("invalid_grant", "the refresh token is invalid, expired or was already used")
		} else if err != nil {
			return fmt.Errorf("consume refresh token: %w", err)
		}
		var clientRef, tenant, user string
		var grantExp time.Time
		var revoked bool
		if err := tx.QueryRow(ctx, `SELECT client_ref::text, tenant_id, user_id::text, expires_at, revoked_at IS NOT NULL FROM ai.mcp_grants WHERE id=$1 FOR UPDATE`, grantID).
			Scan(&clientRef, &tenant, &user, &grantExp, &revoked); err != nil {
			return fmt.Errorf("load mcp grant: %w", err)
		}
		now := s.now()
		switch {
		case clientRef != cl.ID:
			return oauthErr("invalid_grant", "the refresh token was issued to another client")
		case revoked || !grantExp.After(now) || tenant != s.cfg.TenantID:
			return oauthErr("invalid_grant", "the grant has ended")
		case req.Resource != "" && req.Resource != audience:
			return oauthErr("invalid_target", "resource must be the Turaco MCP resource")
		case audience != s.MCPResource():
			return oauthErr("invalid_target", "the grant is not for this resource")
		}
		if _, err := s.userUsable(ctx, user); err != nil {
			if errors.Is(err, ErrMCPInvalidToken) || errors.Is(err, ErrForbidden) {
				return oauthErr("invalid_grant", "the user may not use the MCP server")
			}
			return err
		}
		is, err := issueTokens(ctx, tx, grantID, audience, grantExp, now)
		if err != nil {
			return err
		}
		out = TokenResponse{AccessToken: is.access, RefreshToken: is.refresh, ExpiresIn: is.expiresIn, Scope: MCPScope}
		return nil
	})
	var oe *OAuthError
	if errors.As(err, &oe) && replayedGrant != nil {
		// A rotated refresh token was presented again: someone holds a copy. End the whole grant.
		if rerr := s.revokeGrant(ctx, *replayedGrant, "refresh_replay", req.CorrelationID, map[string]any{"clientId": cl.ClientID}); rerr != nil {
			return TokenResponse{}, rerr
		}
	}
	if err != nil {
		return TokenResponse{}, err
	}
	return out, nil
}

// Revoke implements RFC 7009 for the MCP client: an access or refresh token of this client ends its whole grant.
// Unknown tokens are not an error (the endpoint always answers 200 for a known client).
func (s *Service) Revoke(ctx context.Context, clientID, token, corr string) error {
	if corr == "" {
		corr = "mcp:revoke"
	}
	if _, err := s.MCPOpen(ctx); err != nil {
		return err
	}
	cl, err := s.activeClient(ctx, clientID)
	if errors.Is(err, ErrNotFound) {
		return oauthErr("invalid_client", "unknown or disabled client")
	} else if err != nil {
		return err
	}
	hash, ok := hashOpaque(token)
	if !ok {
		return nil
	}
	var grantID string
	err = s.store.pool.QueryRow(ctx, `SELECT t.grant_id::text FROM ai.mcp_tokens t JOIN ai.mcp_grants g ON g.id = t.grant_id
		WHERE t.token_hash=$1 AND g.client_ref=$2`, hash, cl.ID).Scan(&grantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return fmt.Errorf("find mcp token: %w", err)
	}
	return s.revokeGrant(ctx, grantID, "client_revocation", corr, map[string]any{"clientId": cl.ClientID})
}

// ---- bearer authentication ----

// MCPPrincipal is an authenticated MCP request: the Caller built exactly like the web runtime's (design M8) plus the
// client it acts through.
type MCPPrincipal struct {
	Caller   Caller
	GrantID  string
	ClientID string
	Client   MCPClient
}

// AuthenticateMCP resolves a bearer access token for the MCP endpoint. It returns ErrDisabled while MCP is closed,
// ErrMCPInvalidToken for any unusable token and ErrForbidden when the User lacks ai.use.
func (s *Service) AuthenticateMCP(ctx context.Context, token, correlationID string) (MCPPrincipal, error) {
	if _, err := s.MCPOpen(ctx); err != nil {
		return MCPPrincipal{}, err
	}
	hash, ok := hashOpaque(token)
	if !ok {
		return MCPPrincipal{}, ErrMCPInvalidToken
	}
	var grantID, audience, tenant, user, clientID string
	var lastUsed *time.Time
	err := s.store.pool.QueryRow(ctx, `SELECT g.id::text, t.audience, g.tenant_id, g.user_id::text, c.client_id, g.last_used_at
		FROM ai.mcp_tokens t JOIN ai.mcp_grants g ON g.id = t.grant_id JOIN ai.mcp_clients c ON c.id = g.client_ref
		WHERE t.token_hash=$1 AND t.kind='access' AND t.expires_at > now() AND g.revoked_at IS NULL AND g.expires_at > now() AND c.enabled`, hash).
		Scan(&grantID, &audience, &tenant, &user, &clientID, &lastUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPPrincipal{}, ErrMCPInvalidToken
	} else if err != nil {
		return MCPPrincipal{}, fmt.Errorf("authenticate mcp token: %w", err)
	}
	// Audience binding: a token for any other resource is not accepted here, and the data plane must match.
	if audience != s.MCPResource() || tenant != s.cfg.TenantID {
		return MCPPrincipal{}, ErrMCPInvalidToken
	}
	perms, err := s.userUsable(ctx, user)
	if err != nil {
		return MCPPrincipal{}, err
	}
	cl, err := s.activeClient(ctx, clientID)
	if errors.Is(err, ErrNotFound) {
		return MCPPrincipal{}, ErrMCPInvalidToken
	} else if err != nil {
		return MCPPrincipal{}, err
	}
	if lastUsed == nil || s.now().Sub(*lastUsed) > time.Minute {
		if _, err := s.store.pool.Exec(ctx, `UPDATE ai.mcp_grants SET last_used_at=now() WHERE id=$1`, grantID); err != nil {
			return MCPPrincipal{}, fmt.Errorf("touch mcp grant: %w", err)
		}
	}
	return MCPPrincipal{Caller: Caller{UserID: user, TenantID: tenant, SessionID: grantID, CorrelationID: correlationID, Permissions: perms},
		GrantID: grantID, ClientID: clientID, Client: cl}, nil
}

// ---- the User's own grants ----

// MCPGrant is an active grant of the signed-in User.
type MCPGrant struct {
	ID         string
	ClientID   string
	ClientName string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
}

// ListMCPGrants returns the caller's own active grants. Any signed-in User may see and end their own grants.
func (s *Service) ListMCPGrants(ctx context.Context, c Caller) ([]MCPGrant, error) {
	if c.UserID == "" || c.TenantID == "" {
		return nil, ErrForbidden
	}
	rows, err := s.store.pool.Query(ctx, `SELECT g.id::text, c.client_id, c.display_name, g.created_at, g.expires_at, g.last_used_at
		FROM ai.mcp_grants g JOIN ai.mcp_clients c ON c.id = g.client_ref
		WHERE g.user_id=$1 AND g.tenant_id=$2 AND g.revoked_at IS NULL AND g.expires_at > now() ORDER BY g.created_at DESC LIMIT 50`, c.UserID, c.TenantID)
	if err != nil {
		return nil, fmt.Errorf("list mcp grants: %w", err)
	}
	defer rows.Close()
	out := []MCPGrant{}
	for rows.Next() {
		var g MCPGrant
		if err := rows.Scan(&g.ID, &g.ClientID, &g.ClientName, &g.CreatedAt, &g.ExpiresAt, &g.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scan mcp grant: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RevokeMCPGrant ends one of the caller's own grants. Another User's grant is not found.
func (s *Service) RevokeMCPGrant(ctx context.Context, c Caller, id string) error {
	if c.UserID == "" || c.TenantID == "" {
		return ErrForbidden
	}
	if !uuidRE.MatchString(id) {
		return ErrNotFound
	}
	return s.store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ai.mcp_grants SET revoked_at=now(), revoked_reason='user'
			WHERE id=$1 AND user_id=$2 AND tenant_id=$3 AND revoked_at IS NULL`, id, c.UserID, c.TenantID)
		if err != nil {
			return fmt.Errorf("revoke mcp grant: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return s.auditGrant(ctx, tx, "ai.mcp.grant.revoked", id, c.UserID, c.TenantID, c.CorrelationID, map[string]any{"reason": "user"})
	})
}

// PurgeMCP deletes expired codes and tokens and grants that ended more than a day ago (run by ai.sessions.expire).
func (s *Store) PurgeMCP(ctx context.Context) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		for _, q := range []string{
			`DELETE FROM ai.mcp_codes WHERE expires_at <= now() - interval '1 day'`,
			`DELETE FROM ai.mcp_tokens WHERE expires_at <= now() OR (used_at IS NOT NULL AND used_at <= now() - interval '1 day')`,
			`DELETE FROM ai.mcp_grants WHERE (expires_at <= now() - interval '1 day') OR (revoked_at IS NOT NULL AND revoked_at <= now() - interval '1 day')`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				return fmt.Errorf("purge mcp: %w", err)
			}
		}
		return nil
	})
}
