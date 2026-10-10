// Package entra is the Microsoft Entra ID adapter of the OpenID Connect sign-in (ADR-0035, slice E-A). It builds the
// authorization URL, exchanges the code with the client credential and validates the ID token. The validated ID
// token is the only identity source: no userinfo call, no refresh token, no group claims. Signature verification is
// RS256 only with keys from the JWKS of the configured authority; claim parsing stays in this package.
package entra

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/microsoft"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

const (
	// AuthorityHost is the only host the global cloud adapter talks to.
	AuthorityHost = "login.microsoftonline.com"
	// GraphHost is the Microsoft Graph host, called only for the hybrid source-anchor lookup (delegated User.Read).
	GraphHost = "graph.microsoft.com"

	jwksMaxAge         = 24 * time.Hour
	jwksRefreshMinGap  = 5 * time.Minute
	maxIDTokenBytes    = 16 << 10
	maxIATAge          = 10 * time.Minute
	consumerTenantGUID = "9188040d-6c67-4c5b-b112-36a304b66dad"
)

var guid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Provider implements authentication.OIDCProvider for Entra ID.
type Provider struct {
	cfg    config.EntraConfig
	client *microsoft.Client
	cred   microsoft.Credential
	now    func() time.Time
	// base is the authority base URL; graphBase the Microsoft Graph base URL. Tests point both at a fake server.
	base      string
	graphBase string

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	keysFetched time.Time
	lastRefresh time.Time
}

var _ authentication.OIDCProvider = (*Provider)(nil)

// New builds the provider for the global cloud. client must allow AuthorityHost.
func New(cfg config.EntraConfig, client *microsoft.Client, cred microsoft.Credential, now func() time.Time) *Provider {
	if now == nil {
		now = time.Now
	}
	return &Provider{cfg: cfg, client: client, cred: cred, now: now, base: "https://" + AuthorityHost, graphBase: "https://" + GraphHost}
}

// scope is the requested scope. User.Read is added only when the hybrid match is configured, so installations
// without ENTRA_LINK_DIRECTORY_PROVIDER_KEY never ask for (or receive) a Graph token.
func (p *Provider) scope() string {
	if p.cfg.LinkDirectoryProviderKey != "" {
		return "openid profile User.Read"
	}
	return "openid profile"
}

// authority is the tenant segment of the endpoints.
func (p *Provider) authority() string {
	if p.cfg.TenantMode == config.EntraTenantMultiRestricted {
		return "organizations"
	}
	return p.cfg.TenantID
}

func (p *Provider) endpoint(path string) string { return p.base + "/" + p.authority() + path }

// AuthorizeURL implements authentication.OIDCProvider.
func (p *Provider) AuthorizeURL(state, nonce, codeChallenge string) string {
	q := url.Values{}
	q.Set("client_id", p.cfg.ClientID)
	q.Set("response_type", "code")
	q.Set("response_mode", "query")
	q.Set("redirect_uri", p.cfg.RedirectURL)
	q.Set("scope", p.scope())
	q.Set("prompt", "select_account")
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	return p.endpoint("/oauth2/v2.0/authorize") + "?" + q.Encode()
}

// Exchange implements authentication.OIDCProvider.
func (p *Provider) Exchange(ctx context.Context, code, verifier, nonce string) (authentication.VerifiedIdentity, error) {
	tokenURL := p.endpoint("/oauth2/v2.0/token")
	form := url.Values{}
	form.Set("client_id", p.cfg.ClientID)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", p.cfg.RedirectURL)
	form.Set("code_verifier", verifier)
	form.Set("scope", p.scope())
	if err := p.cred.Apply(form, p.cfg.ClientID, tokenURL); err != nil {
		return authentication.VerifiedIdentity{}, authentication.ErrOIDCUnavailable
	}
	resp, err := p.client.Do(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()),
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Accept": {"application/json"}})
	if err != nil {
		var te *microsoft.TransientError
		if errors.As(err, &te) {
			return authentication.VerifiedIdentity{}, authentication.ErrOIDCUnavailable
		}
		return authentication.VerifiedIdentity{}, authentication.ErrOIDCInvalid
	}
	if resp.Status != http.StatusOK {
		// invalid_grant (code reuse, PKCE mismatch), invalid_client (expired credential): never shown to the user.
		return authentication.VerifiedIdentity{}, authentication.ErrOIDCInvalid
	}
	var tok struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if json.Unmarshal(resp.Body, &tok) != nil || tok.IDToken == "" {
		return authentication.VerifiedIdentity{}, authentication.ErrOIDCInvalid
	}
	ident, err := p.validate(ctx, tok.IDToken, nonce)
	if err != nil {
		return ident, err
	}
	// The access token is used once, in memory, and never stored or logged. Only a member of the home tenant that
	// the directory provider is bound to is ever looked up: a foreign tenant controls its own onPremisesImmutableId.
	if p.cfg.LinkDirectoryProviderKey != "" && !ident.Guest && ident.TenantID == strings.ToLower(p.cfg.TenantID) &&
		tok.AccessToken != "" && strings.EqualFold(tok.TokenType, "Bearer") {
		ident.SourceAnchor = p.sourceAnchor(ctx, tok.AccessToken)
	}
	return ident, nil
}

// sourceAnchor reads onPremisesImmutableId and onPremisesSyncEnabled of the signed-in user from Graph /me and
// returns the objectGUID (lower-case, canonical form) it encodes. It returns "" for a cloud-only user, a user
// whose synchronization is not enabled, an anchor that is not a GUID and any failure: a missing anchor can only
// make the match fail, never succeed.
func (p *Provider) sourceAnchor(ctx context.Context, accessToken string) string {
	resp, err := p.client.Do(ctx, http.MethodGet, p.graphBase+"/v1.0/me?$select=onPremisesImmutableId,onPremisesSyncEnabled", nil,
		http.Header{"Authorization": {"Bearer " + accessToken}, "Accept": {"application/json"}})
	if err != nil || resp.Status != http.StatusOK {
		return ""
	}
	var me struct {
		ImmutableID *string `json:"onPremisesImmutableId"`
		SyncEnabled *bool   `json:"onPremisesSyncEnabled"`
	}
	if json.Unmarshal(resp.Body, &me) != nil || me.ImmutableID == nil || me.SyncEnabled == nil || !*me.SyncEnabled {
		return ""
	}
	return decodeSourceAnchor(*me.ImmutableID)
}

// decodeSourceAnchor decodes the base64 source anchor of an Active Directory objectGUID (16 bytes in the mixed-endian
// layout of AD) to the canonical UUID text that directory synchronization stores as external subject. Any other
// anchor (a custom attribute) yields "".
func decodeSourceAnchor(s string) string {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(s); err != nil {
			return ""
		}
	}
	if len(b) != 16 {
		return ""
	}
	var u [16]byte
	u[0], u[1], u[2], u[3] = b[3], b[2], b[1], b[0]
	u[4], u[5] = b[5], b[4]
	u[6], u[7] = b[7], b[6]
	copy(u[8:], b[8:])
	h := hex.EncodeToString(u[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type claims struct {
	Issuer   string          `json:"iss"`
	Audience json.RawMessage `json:"aud"`
	Azp      string          `json:"azp"`
	Exp      *float64        `json:"exp"`
	Nbf      *float64        `json:"nbf"`
	Iat      *float64        `json:"iat"`
	Nonce    string          `json:"nonce"`
	Oid      string          `json:"oid"`
	Tid      string          `json:"tid"`
	Ver      string          `json:"ver"`
	Name     string          `json:"name"`
	Email    string          `json:"email"`
	Username string          `json:"preferred_username"`
	Acct     *float64        `json:"acct"`
	Idp      string          `json:"idp"`
}

func (p *Provider) validate(ctx context.Context, raw, nonce string) (authentication.VerifiedIdentity, error) {
	invalid := authentication.VerifiedIdentity{}
	if len(raw) > maxIDTokenBytes {
		return invalid, authentication.ErrOIDCInvalid
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return invalid, authentication.ErrOIDCInvalid
	}
	var h header
	if !decodeSegment(parts[0], &h) || h.Alg != "RS256" || h.Kid == "" || len(h.Kid) > 256 {
		return invalid, authentication.ErrOIDCInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) == 0 {
		return invalid, authentication.ErrOIDCInvalid
	}
	key, err := p.keyFor(ctx, h.Kid)
	if err != nil {
		return invalid, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
		return invalid, authentication.ErrOIDCInvalid
	}
	var c claims
	if !decodeSegment(parts[1], &c) {
		return invalid, authentication.ErrOIDCInvalid
	}
	return p.checkClaims(c, nonce)
}

func (p *Provider) checkClaims(c claims, nonce string) (authentication.VerifiedIdentity, error) {
	invalid := authentication.VerifiedIdentity{}
	tid, oid := strings.ToLower(c.Tid), strings.ToLower(c.Oid)
	if !guid.MatchString(tid) || !guid.MatchString(oid) || c.Ver != "2.0" {
		return invalid, authentication.ErrOIDCInvalid
	}
	// The issuer is bound to the token's own tenant, so a token of tenant B cannot pass as tenant A.
	if c.Issuer != "https://"+AuthorityHost+"/"+tid+"/v2.0" {
		return invalid, authentication.ErrOIDCInvalid
	}
	if tid == consumerTenantGUID || !allowed(p.cfg.AllowedTenantIDs, tid) {
		return invalid, authentication.ErrOIDCTenantNotAllowed
	}
	if !audienceIs(c.Audience, p.cfg.ClientID) || (c.Azp != "" && !strings.EqualFold(c.Azp, p.cfg.ClientID)) {
		return invalid, authentication.ErrOIDCInvalid
	}
	now := p.now()
	skew := p.cfg.MaxClockSkew
	if c.Exp == nil || c.Iat == nil || nonce == "" {
		return invalid, authentication.ErrOIDCInvalid
	}
	exp, iat := time.Unix(int64(*c.Exp), 0), time.Unix(int64(*c.Iat), 0)
	if !now.Before(exp.Add(skew)) || iat.After(now.Add(skew)) || now.Sub(iat) > maxIATAge+skew {
		return invalid, authentication.ErrOIDCInvalid
	}
	if c.Nbf != nil && time.Unix(int64(*c.Nbf), 0).After(now.Add(skew)) {
		return invalid, authentication.ErrOIDCInvalid
	}
	if subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1 {
		return invalid, authentication.ErrOIDCInvalid
	}
	// B2B guests: the acct claim, and as a second signal an idp that differs from the issuer.
	guest := (c.Acct != nil && *c.Acct == 1) || (c.Idp != "" && c.Idp != c.Issuer)
	email := c.Email
	if email == "" && strings.Contains(c.Username, "@") {
		email = c.Username
	}
	member := c.Acct != nil && *c.Acct == 0 && (c.Idp == "" || c.Idp == c.Issuer)
	return authentication.VerifiedIdentity{TenantID: tid, ObjectID: oid, Guest: guest, MemberConfirmed: member, DisplayName: c.Name, Email: email}, nil
}

func allowed(list []string, tid string) bool {
	for _, t := range list {
		if t == tid {
			return true
		}
	}
	return false
}

func audienceIs(raw json.RawMessage, clientID string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return strings.EqualFold(one, clientID)
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil && len(many) == 1 {
		return strings.EqualFold(many[0], clientID)
	}
	return false
}

func decodeSegment(seg string, dst any) bool {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, dst) == nil
}

// keyFor returns the signing key for kid. An unknown kid triggers one JWKS refresh, at most once per five minutes
// per process, so a forged kid cannot be used to hammer Entra; a failed refresh keeps the cached keys.
func (p *Provider) keyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	stale := p.keys == nil || now.Sub(p.keysFetched) > jwksMaxAge
	if k, ok := p.keys[kid]; ok && !stale {
		return k, nil
	}
	if stale || now.Sub(p.lastRefresh) >= jwksRefreshMinGap {
		p.lastRefresh = now
		if err := p.refreshLocked(ctx); err != nil {
			if stale {
				p.keys = nil
			}
			if k, ok := p.keys[kid]; ok {
				return k, nil
			}
			return nil, authentication.ErrOIDCUnavailable
		}
		if k, ok := p.keys[kid]; ok {
			return k, nil
		}
	}
	return nil, authentication.ErrOIDCInvalid
}

func (p *Provider) refreshLocked(ctx context.Context) error {
	resp, err := p.client.Do(ctx, http.MethodGet, p.endpoint("/discovery/v2.0/keys"), nil, http.Header{"Accept": {"application/json"}})
	if err != nil || resp.Status != http.StatusOK {
		return errors.New("jwks fetch failed")
	}
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if json.Unmarshal(resp.Body, &doc) != nil {
		return errors.New("jwks decode failed")
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || k.Kid == "" {
			continue
		}
		nb, err1 := base64.RawURLEncoding.DecodeString(k.N)
		eb, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(nb) < 256 || len(eb) == 0 || len(eb) > 4 {
			continue
		}
		e := int(new(big.Int).SetBytes(eb).Int64())
		if e < 3 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}
	}
	if len(keys) == 0 {
		return errors.New("jwks holds no usable key")
	}
	p.keys, p.keysFetched = keys, p.now()
	return nil
}

// EndSessionURL is the Entra end-session endpoint of the configured authority.
func EndSessionURL(cfg config.EntraConfig) string {
	authority := cfg.TenantID
	if cfg.TenantMode == config.EntraTenantMultiRestricted {
		authority = "organizations"
	}
	return "https://" + AuthorityHost + "/" + authority + "/oauth2/v2.0/logout"
}

// PostLogoutURL is where Entra sends the browser after sign-out: the login page of the host of the registered
// redirect URL (never derived from request headers). It must be registered on the app registration.
func PostLogoutURL(cfg config.EntraConfig) string {
	u, err := url.Parse(cfg.RedirectURL)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/login"
}
