package wiring

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/smtp"
	orgapp "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

// OrganizationConfig configures the People administration of Organization (F14).
type OrganizationConfig struct {
	// BaseURL is EMAIL_BASE_URL: invitation and reset links are built only from it (review rule R7).
	BaseURL string
	// SMTP configures the mail channel for credential links; a disabled configuration means links are not mailed.
	SMTP config.SMTPConfig
}

// Organization returns the Organization repository with the collaborators of the People operations: the access
// guards (dominance rule, last administrator), session and token revocation, local credentials and the credential
// mailer. The plain orgrepository.New(pool) stays valid for reads; operations that need the guards fail closed
// without them.
func Organization(pool *pgxpool.Pool, cfg OrganizationConfig) (*orgrepository.Repository, error) {
	base := orgrepository.New(pool)
	subjects := orgpublic.NewAuthorizationSubjects(base)
	mailer, err := newCredentialMailer(cfg)
	if err != nil {
		return nil, err
	}
	return base.
		WithGuards(roles.NewGuards(pool, subjects), authentication.SessionRevoker{}).
		WithCredentials(authentication.NewLocalCredentials(nil), mailer).
		WithCredentialRemover(localCredentialRemover{}), nil
}

// localCredentialRemover implements orgapp.LocalCredentialRemover: linking a directory identity deletes the password
// of the local account in the same transaction (ADR-0034 review rule R5). Only credentials of kind "local" are
// touched; the emergency account has its own CLI-only lifecycle.
type localCredentialRemover struct{}

func (localCredentialRemover) DeleteLocalCredential(ctx context.Context, tx pgx.Tx, userID string) (int, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM platform.local_credentials WHERE user_id = $1::uuid AND kind = 'local'`, userID)
	if err != nil {
		return 0, fmt.Errorf("delete local credential: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// EntraLinking returns the administrator operations on Entra identities (slice E-B). tenants are the tenants accepted
// for Entra sign-in (empty when Entra sign-in is not configured, which makes linking answer 409). The target is told
// about a link or unlink by email when the mail channel is configured; the notice carries no link.
func EntraLinking(repo *orgrepository.Repository, cfg OrganizationConfig, tenants []string) (*orgapp.EntraLinking, error) {
	m, err := newCredentialMailer(cfg)
	if err != nil {
		return nil, err
	}
	var notifier orgapp.IdentityNotifier
	if m.MailConfigured() {
		notifier = m
	}
	return orgapp.NewEntraLinking(repo, tenants, notifier), nil
}

// identityNoticeTexts are the notice texts (subject, body) per event and locale.
var identityNoticeTexts = map[string]map[string][2]string{
	"en": {
		"linked":   {"A Microsoft sign-in was linked to your Turaco account", "An administrator linked a Microsoft Entra sign-in to your Turaco account. You can now sign in with Microsoft. If you did not expect this, tell your administrator."},
		"unlinked": {"A Microsoft sign-in was removed from your Turaco account", "An administrator removed the Microsoft Entra sign-in from your Turaco account. If you did not expect this, tell your administrator."},
	},
	"de": {
		"linked":   {"Eine Microsoft-Anmeldung wurde mit deinem Turaco-Konto verknüpft", "Eine Administratorin oder ein Administrator hat eine Microsoft-Entra-Anmeldung mit deinem Turaco-Konto verknüpft. Du kannst dich jetzt mit Microsoft anmelden. Falls du das nicht erwartet hast, informiere deine Administration."},
		"unlinked": {"Die Microsoft-Anmeldung wurde von deinem Turaco-Konto entfernt", "Eine Administratorin oder ein Administrator hat die Microsoft-Entra-Anmeldung von deinem Turaco-Konto entfernt. Falls du das nicht erwartet hast, informiere deine Administration."},
	},
}

// NotifyIdentityChange implements orgapp.IdentityNotifier. The address is parsed again here (no CR or LF reaches a
// header) and always comes from the stored profile, never from a request.
func (m *credentialMailer) NotifyIdentityChange(ctx context.Context, to, _ /* displayName */, event string) error {
	if m.mailer == nil {
		return orgapp.ErrMailNotConfigured
	}
	a, err := mail.ParseAddress(to)
	if err != nil || a.Address != to || strings.ContainsAny(to, "\r\n") {
		return orgapp.ErrNoEmail
	}
	texts, ok := identityNoticeTexts[m.locale]
	if !ok {
		texts = identityNoticeTexts["en"]
	}
	t, ok := texts[event]
	if !ok {
		return fmt.Errorf("unknown identity notice event %q", event)
	}
	return m.mailer.Send(ctx, smtp.Message{To: to, Subject: t[0], Text: t[1] + "\n",
		HTML: `<!doctype html><html><body style="font-family:sans-serif"><p>` + html.EscapeString(t[1]) + `</p></body></html>`})
}

// credentialMailer implements orgapp.CredentialMailer.
type credentialMailer struct {
	baseURL string
	locale  string
	mailer  smtp.Mailer
}

var _ orgapp.CredentialMailer = (*credentialMailer)(nil)

func newCredentialMailer(cfg OrganizationConfig) (*credentialMailer, error) {
	m := &credentialMailer{baseURL: strings.TrimRight(cfg.BaseURL, "/"), locale: cfg.SMTP.DefaultLocale}
	if !cfg.SMTP.Enabled() {
		return m, nil
	}
	mc := smtp.Config{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, Security: smtp.Security(cfg.SMTP.Security), Username: cfg.SMTP.Username,
		From: cfg.SMTP.From, Timeout: cfg.SMTP.Timeout}
	if cfg.SMTP.PasswordFile != "" {
		password, err := config.ReadSecretFile(cfg.SMTP.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("read SMTP password file: %w", err)
		}
		mc.Password = password
	}
	if cfg.SMTP.CAFile != "" {
		pem, err := os.ReadFile(cfg.SMTP.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read SMTP CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("SMTP_CA_FILE contains no PEM certificate")
		}
		mc.RootCAs = pool
	}
	var err error
	if m.mailer, err = smtp.New(mc); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *credentialMailer) BaseURLConfigured() bool { return m.baseURL != "" }
func (m *credentialMailer) MailConfigured() bool    { return m.mailer != nil }

// Link puts the token in the URL fragment: it never reaches server logs or the Referer header.
func (m *credentialMailer) Link(token, purpose string) string {
	return m.baseURL + "/set-password#token=" + token + "&purpose=" + purpose
}

// credentialTexts are the mail texts (subject, intro, action) per purpose and locale.
var credentialTexts = map[string]map[string][3]string{
	"en": {
		orgapp.CredentialInvitation: {"You are invited to Turaco", "An administrator created a Turaco account for you. Set your password with the link below. The link can be used once and expires in 7 days.", "Set password"},
		orgapp.CredentialReset:      {"Reset your Turaco password", "A password reset was requested for your Turaco account. Choose a new password with the link below. The link can be used once and expires in 24 hours. If you did not expect this, tell your administrator.", "Choose a new password"},
	},
	"de": {
		orgapp.CredentialInvitation: {"Einladung zu Turaco", "Eine Administratorin oder ein Administrator hat ein Turaco-Konto für dich angelegt. Lege dein Passwort mit dem folgenden Link fest. Der Link ist einmal verwendbar und 7 Tage gültig.", "Passwort festlegen"},
		orgapp.CredentialReset:      {"Turaco-Passwort zurücksetzen", "Für dein Turaco-Konto wurde ein neues Passwort angefordert. Wähle mit dem folgenden Link ein neues Passwort. Der Link ist einmal verwendbar und 24 Stunden gültig. Falls du das nicht erwartet hast, informiere deine Administration.", "Neues Passwort wählen"},
	},
}

// Send mails the link to the stored primary address. The address is parsed again here (no CR or LF reaches a
// header) and is never taken from a request.
func (m *credentialMailer) Send(ctx context.Context, to, _ /* displayName */, purpose, link string) error {
	if m.mailer == nil {
		return orgapp.ErrMailNotConfigured
	}
	a, err := mail.ParseAddress(to)
	if err != nil || a.Address != to || strings.ContainsAny(to, "\r\n") {
		return orgapp.ErrNoEmail
	}
	texts, ok := credentialTexts[m.locale]
	if !ok {
		texts = credentialTexts["en"]
	}
	t := texts[purpose]
	body := t[1] + "\n\n" + t[2] + ": " + link + "\n"
	htmlBody := `<!doctype html><html><body style="font-family:sans-serif"><p>` + html.EscapeString(t[1]) + `</p><p><a href="` +
		html.EscapeString(link) + `">` + html.EscapeString(t[2]) + `</a></p></body></html>`
	return m.mailer.Send(ctx, smtp.Message{To: to, Subject: t[0], Text: body, HTML: htmlBody})
}
