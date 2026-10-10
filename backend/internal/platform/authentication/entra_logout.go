package authentication

import (
	"context"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Values of the setting auth.entra_signout_mode.
const (
	EntraSignoutNever      = "never"
	EntraSignoutSharedOnly = "shared_only"
	EntraSignoutAlways     = "always"
)

// LogoutRedirector decides whether logging out must also end an identity-provider session. It is called before
// the Turaco session is revoked and returns the URL the browser must open next, or "" for a plain logout.
type LogoutRedirector interface {
	LogoutRedirect(ctx context.Context, s Session) (string, error)
}

// EntraLogout ends the Entra session on logout: always, never, or only for sessions that were started on a shared
// computer (the sign-in page asks). The end-session URL carries no id token hint, so Entra shows the account
// picker; post_logout_redirect_uri must be registered on the Entra app registration.
type EntraLogout struct {
	pool          *pgxpool.Pool
	endSessionURL string
	postLogoutURL string
	mode          func(ctx context.Context) string
}

// NewEntraLogout builds the redirector. endSessionURL is the end-session endpoint of the configured authority and
// postLogoutURL the registered post-logout redirect (both fixed configuration, never request headers). mode may be
// nil, which means shared_only.
func NewEntraLogout(pool *pgxpool.Pool, endSessionURL, postLogoutURL string, mode func(ctx context.Context) string) *EntraLogout {
	return &EntraLogout{pool: pool, endSessionURL: endSessionURL, postLogoutURL: postLogoutURL, mode: mode}
}

// LogoutRedirect implements LogoutRedirector.
func (e *EntraLogout) LogoutRedirect(ctx context.Context, s Session) (string, error) {
	if s.AuthMethod != methodEntra {
		return "", nil
	}
	mode := EntraSignoutSharedOnly
	if e.mode != nil {
		if m := e.mode(ctx); m != "" {
			mode = m
		}
	}
	switch mode {
	case EntraSignoutNever:
		return "", nil
	case EntraSignoutAlways:
	default:
		var shared bool
		if err := e.pool.QueryRow(ctx, `SELECT shared_workstation FROM platform.sessions WHERE id = $1::uuid`, s.ID).Scan(&shared); err != nil {
			return "", err
		}
		if !shared {
			return "", nil
		}
	}
	u, err := url.Parse(e.endSessionURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("post_logout_redirect_uri", e.postLogoutURL)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
