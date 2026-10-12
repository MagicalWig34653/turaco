package ai

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// MCPClient is one allowlisted OAuth client of the Turaco MCP server (design M3). It is a public client: there is
// no client secret, the authorization code is protected by PKCE and the exact redirect URI.
type MCPClient struct {
	ID                 string
	ClientID           string
	DisplayName        string
	RedirectURIs       []string
	AllowedDataClasses []DataClass
	Enabled            bool
	ActiveGrants       int
	Version            int
	CreatedAt          string
	UpdatedAt          string
}

// Allows reports whether the client may receive data of class c (design M4).
func (c MCPClient) Allows(cl DataClass) bool { return slices.Contains(c.AllowedDataClasses, cl) }

// MCPClientInput is the writable part of an MCP client.
type MCPClientInput struct {
	DisplayName  string
	RedirectURIs []string
	// AllowedDataClasses nil selects public_reference only (the egress default).
	AllowedDataClasses []DataClass
	Enabled            bool
}

const (
	maxRedirectURIs   = 5
	maxRedirectURILen = 500
)

// ValidateRedirectURI accepts https URIs and http loopback URIs with an explicit port, without fragment or user info
// (design M3). Redirect URIs are later compared by exact string, so the stored form is the submitted form.
func ValidateRedirectURI(raw string) error {
	if raw == "" || len(raw) > maxRedirectURILen || strings.TrimSpace(raw) != raw || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\x00\r\n\t #") {
		return errors.New("redirect URI must be 1 to 500 characters without spaces or a fragment")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.Host == "" {
		return errors.New("redirect URI must be an absolute URL")
	}
	if u.User != nil {
		return errors.New("redirect URI must not contain user information")
	}
	if strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), "localhost") {
		return errors.New("redirect URI must use a numeric loopback address instead of localhost")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if ip == nil || (host != "127.0.0.1" && host != "::1") {
			return errors.New("http redirect URIs are allowed for loopback addresses only")
		}
		if u.Port() == "" {
			return errors.New("a loopback redirect URI needs an explicit port")
		}
		return nil
	}
	return errors.New("redirect URI must use https (or http on a loopback address)")
}

func (in MCPClientInput) normalize() (MCPClientInput, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if n := utf8.RuneCountInString(in.DisplayName); n < 1 || n > 100 || !utf8.ValidString(in.DisplayName) || safetext.ContainsUnsafe(in.DisplayName, false) {
		return in, invalid("displayName must be 1 to 100 characters")
	}
	if len(in.RedirectURIs) < 1 || len(in.RedirectURIs) > maxRedirectURIs {
		return in, invalid("between 1 and %d redirect URIs are required", maxRedirectURIs)
	}
	var uris []string
	for _, u := range in.RedirectURIs {
		if err := ValidateRedirectURI(u); err != nil {
			return in, invalid("%s", err.Error())
		}
		if slices.Contains(uris, u) {
			return in, invalid("redirect URIs must be unique")
		}
		uris = append(uris, u)
	}
	in.RedirectURIs = uris
	classes := in.AllowedDataClasses
	if classes == nil {
		classes = []DataClass{ClassPublicReference}
	}
	var out []DataClass
	for _, cl := range classes {
		if !cl.Valid() {
			return in, invalid("unknown data class")
		}
		if !slices.Contains(out, cl) {
			out = append(out, cl)
		}
	}
	slices.Sort(out)
	if out == nil {
		out = []DataClass{}
	}
	in.AllowedDataClasses = out
	return in, nil
}

const mcpClientCols = `c.id::text, c.client_id, c.display_name, c.redirect_uris, c.allowed_data_classes, c.enabled, c.version,
	c.created_at::text, c.updated_at::text,
	(SELECT count(*) FROM ai.mcp_grants g WHERE g.client_ref = c.id AND g.revoked_at IS NULL AND g.expires_at > now())::int`

const mcpClientHotCols = `c.id::text, c.client_id, c.display_name, c.redirect_uris, c.allowed_data_classes, c.enabled, c.version,
	c.created_at::text, c.updated_at::text, 0::int`

func scanMCPClient(row pgx.Row) (MCPClient, error) {
	var c MCPClient
	var classes []string
	err := row.Scan(&c.ID, &c.ClientID, &c.DisplayName, &c.RedirectURIs, &classes, &c.Enabled, &c.Version, &c.CreatedAt, &c.UpdatedAt, &c.ActiveGrants)
	c.AllowedDataClasses = []DataClass{}
	for _, cl := range classes {
		c.AllowedDataClasses = append(c.AllowedDataClasses, DataClass(cl))
	}
	return c, err
}

// newClientID returns a public client identifier: "tmcp_" and 26 base32 characters (130 random bits).
func newClientID() (string, error) {
	b := make([]byte, 17)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate client id: %w", err)
	}
	enc := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	return "tmcp_" + enc[:26], nil
}

// ListMCPClients returns the client allowlist. Needs ai.settings.view or manage. It ignores the startup gates so
// administrators can prepare clients before switching MCP on.
func (s *Service) ListMCPClients(ctx context.Context, c Caller) ([]MCPClient, error) {
	if err := s.requireAdmin(c, false); err != nil {
		return nil, err
	}
	rows, err := s.store.pool.Query(ctx, `SELECT `+mcpClientCols+` FROM ai.mcp_clients c ORDER BY c.created_at, c.id`)
	if err != nil {
		return nil, fmt.Errorf("list mcp clients: %w", err)
	}
	defer rows.Close()
	out := []MCPClient{}
	for rows.Next() {
		cl, err := scanMCPClient(rows)
		if err != nil {
			return nil, fmt.Errorf("scan mcp client: %w", err)
		}
		out = append(out, cl)
	}
	return out, rows.Err()
}

func mcpClientAudit(c MCPClient) map[string]any {
	return map[string]any{"clientId": c.ClientID, "displayName": c.DisplayName, "redirectUris": c.RedirectURIs,
		"allowedDataClasses": c.AllowedDataClasses, "enabled": c.Enabled, "version": c.Version}
}

// CreateMCPClient adds a client to the allowlist (audited). Needs ai.settings.manage.
func (s *Service) CreateMCPClient(ctx context.Context, c Caller, in MCPClientInput) (MCPClient, error) {
	if err := s.requireAdmin(c, true); err != nil {
		return MCPClient{}, err
	}
	in, err := in.normalize()
	if err != nil {
		return MCPClient{}, err
	}
	clientID, err := newClientID()
	if err != nil {
		return MCPClient{}, err
	}
	var out MCPClient
	err = s.store.inTx(ctx, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO ai.mcp_clients (client_id, display_name, redirect_uris, allowed_data_classes, enabled, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$6) RETURNING id::text`, clientID, in.DisplayName, in.RedirectURIs, classStrings(in.AllowedDataClasses), in.Enabled, c.UserID).Scan(&id); err != nil {
			return fmt.Errorf("insert mcp client: %w", err)
		}
		if out, err = scanMCPClient(tx.QueryRow(ctx, `SELECT `+mcpClientCols+` FROM ai.mcp_clients c WHERE c.id=$1`, id)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "ai.mcp_client.created", TargetType: "ai_mcp_client", TargetID: out.ID,
			Actor: audit.UserActor(c.UserID), CorrelationID: c.CorrelationID, TenantID: c.TenantID, After: mcpClientAudit(out)})
	})
	return out, err
}

// UpdateMCPClient replaces a client's configuration with an optimistic version check. Disabling a client revokes all
// its grants in the same transaction. Needs ai.settings.manage.
func (s *Service) UpdateMCPClient(ctx context.Context, c Caller, id string, expected *int, in MCPClientInput) (MCPClient, error) {
	if err := s.requireAdmin(c, true); err != nil {
		return MCPClient{}, err
	}
	if expected == nil {
		return MCPClient{}, invalid("expectedVersion is required")
	}
	if !uuidRE.MatchString(id) {
		return MCPClient{}, ErrNotFound
	}
	in, err := in.normalize()
	if err != nil {
		return MCPClient{}, err
	}
	var out MCPClient
	err = s.store.inTx(ctx, func(tx pgx.Tx) error {
		before, err := scanMCPClient(tx.QueryRow(ctx, `SELECT `+mcpClientCols+` FROM ai.mcp_clients c WHERE c.id=$1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if before.Version != *expected {
			return ErrVersionConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE ai.mcp_clients SET display_name=$2, redirect_uris=$3, allowed_data_classes=$4, enabled=$5,
			updated_by=$6, updated_at=now(), version=version+1 WHERE id=$1`, id, in.DisplayName, in.RedirectURIs, classStrings(in.AllowedDataClasses), in.Enabled, c.UserID); err != nil {
			return fmt.Errorf("update mcp client: %w", err)
		}
		var revoked int64
		if before.Enabled && !in.Enabled {
			tag, err := tx.Exec(ctx, `UPDATE ai.mcp_grants SET revoked_at=now(), revoked_reason='client_disabled' WHERE client_ref=$1 AND revoked_at IS NULL`, id)
			if err != nil {
				return fmt.Errorf("revoke mcp grants: %w", err)
			}
			revoked = tag.RowsAffected()
		}
		if out, err = scanMCPClient(tx.QueryRow(ctx, `SELECT `+mcpClientCols+` FROM ai.mcp_clients c WHERE c.id=$1`, id)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "ai.mcp_client.updated", TargetType: "ai_mcp_client", TargetID: id,
			Actor: audit.UserActor(c.UserID), CorrelationID: c.CorrelationID, TenantID: c.TenantID,
			Before: mcpClientAudit(before), After: mcpClientAudit(out), Metadata: map[string]any{"revokedGrants": revoked}})
	})
	return out, err
}
