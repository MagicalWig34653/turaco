import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { isApiError } from '../../platform/api/client';
import { endpoints } from '../../platform/api/endpoints';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { locales, type Locale } from '../../platform/i18n/i18n';
import { isKerberosAutoLoginSuppressed } from '../../platform/session/kerberosGuard';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { TextField } from '../../platform/ui/Field';
import { loginErrorMessage } from './loginErrors';

type Mode = 'password' | 'emergency';

/** Generic messages for the failure codes the Entra callback puts into the login page URL. */
const entraErrorKeys: Record<
  string,
  'login.entra.failed' | 'login.entra.notLinked' | 'login.entra.unavailable'
> = {
  entra_failed: 'login.entra.failed',
  entra_not_linked: 'login.entra.notLinked',
  entra_unavailable: 'login.entra.unavailable',
};

function entraErrorFromUrl() {
  try {
    const code = new URLSearchParams(window.location.search).get('error');
    return code ? (entraErrorKeys[code] ?? null) : null;
  } catch {
    return null;
  }
}

function entraStartUrl() {
  // Return to the page the user wanted when it is an in-app path other than the login page.
  const here = window.location.pathname + window.location.search;
  const target = here.startsWith('/') && !here.startsWith('/login') ? here : '/';
  return `/api/v1/auth/entra/start?returnTo=${encodeURIComponent(target)}`;
}

export function LoginScreen() {
  const { t, locale, setLocale } = useI18n();
  const { state, refresh } = useSession();
  const methods = useAsync((signal) => endpoints.authMethods(signal), []);
  const [kerberos, setKerberos] = useState<'idle' | 'trying' | 'done'>('idle');
  const kerberosTried = useRef(false);
  const [mode, setMode] = useState<Mode>('password');
  const [identifier, setIdentifier] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [errorText, setErrorText] = useState<string | null>(null);
  const identifierRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);

  const available = methods.data;
  const entraError = entraErrorFromUrl();

  // Try Kerberos/SPNEGO once; on any non-204 outcome fall back to the form.
  useEffect(() => {
    if (!available?.kerberos || kerberosTried.current) return;
    if (isKerberosAutoLoginSuppressed()) {
      kerberosTried.current = true;
      return;
    }
    kerberosTried.current = true;
    setKerberos('trying');
    endpoints
      .kerberosLogin()
      .then(() => refresh())
      .catch(() => undefined)
      .finally(() => setKerberos('done'));
  }, [available, refresh]);

  const effectiveMode: Mode =
    available && !available.password && available.emergency ? 'emergency' : mode;
  const emergency = effectiveMode === 'emergency';

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setErrorText(null);
    try {
      if (emergency) await endpoints.emergencyLogin({ login: identifier, password });
      else await endpoints.login({ identifier, password });
      setPassword('');
      await refresh();
    } catch (cause) {
      // Never keep a rejected password around.
      setPassword('');
      const failure = loginErrorMessage(
        isApiError(cause) ? cause : { status: -1, retryAfterSeconds: undefined },
      );
      setErrorText(t(failure.key, failure.params));
      passwordRef.current?.focus();
    } finally {
      setBusy(false);
    }
  };

  const switchMode = (next: Mode) => {
    setMode(next);
    setPassword('');
    setErrorText(null);
    window.setTimeout(() => identifierRef.current?.focus(), 0);
  };

  return (
    <div className="login-page">
      <div className="login-language">
        <label className="language">
          {t('language')}
          <select value={locale} onChange={(event) => setLocale(event.target.value as Locale)}>
            {locales.map((value) => (
              <option key={value} value={value}>
                {value === 'de' ? 'Deutsch' : 'English'}
              </option>
            ))}
          </select>
        </label>
      </div>
      <main className={emergency ? 'login-card login-card-emergency' : 'login-card'}>
        <div className="brand">
          <img className="brand-icon" src="/icon-192.png" alt="" width={36} height={36} />
          {t('app.name')}
        </div>
        <h1 className="login-title">{emergency ? t('login.emergency.title') : t('login.title')}</h1>

        {state.status === 'anonymous' && state.expired ? (
          <Alert kind="info">{t('login.sessionExpired')}</Alert>
        ) : null}

        {methods.loading ? <p role="status">{t('state.loading')}</p> : null}
        {methods.error ? <ApiErrorAlert error={methods.error} onRetry={methods.reload} /> : null}

        {kerberos === 'trying' ? <p role="status">{t('login.kerberos.trying')}</p> : null}

        {entraError ? <Alert kind="error">{t(entraError)}</Alert> : null}

        {available && kerberos !== 'trying' && available.entra ? (
          <p className="login-entra">
            <a className="btn btn-primary btn-block" href={entraStartUrl()}>
              {t('login.entra.button')}
            </a>
          </p>
        ) : null}

        {available && kerberos !== 'trying' ? (
          available.password || available.emergency || available.entra ? (
            available.password || available.emergency ? (
              <form onSubmit={(event) => void submit(event)} noValidate>
                {emergency ? (
                  <Alert kind="warning">
                    <strong>{t('login.emergency.warningTitle')}</strong>
                    <p>{t('login.emergency.warning')}</p>
                  </Alert>
                ) : null}
                {errorText ? <Alert kind="error">{errorText}</Alert> : null}
                <TextField
                  ref={identifierRef}
                  label={emergency ? t('login.emergency.login') : t('login.identifier')}
                  hint={emergency ? undefined : t('login.identifier.hint')}
                  value={identifier}
                  onChange={(event) => setIdentifier(event.target.value)}
                  autoComplete="username"
                  autoCapitalize="none"
                  spellCheck={false}
                  maxLength={emergency ? 64 : 256}
                  required
                  autoFocus
                />
                <TextField
                  ref={passwordRef}
                  label={t('login.password')}
                  type="password"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  autoComplete="current-password"
                  maxLength={1024}
                  required
                />
                <Button
                  type="submit"
                  variant={emergency ? 'danger' : 'primary'}
                  busy={busy}
                  disabled={identifier === '' || password === ''}
                  className="btn-block"
                >
                  {busy
                    ? t('login.submitting')
                    : emergency
                      ? t('login.emergency.submit')
                      : t('login.submit')}
                </Button>
                {available.emergency && available.password ? (
                  <p className="login-switch">
                    <Button
                      variant="secondary"
                      className="btn-link"
                      onClick={() => switchMode(emergency ? 'password' : 'emergency')}
                    >
                      {emergency ? t('login.emergency.back') : t('login.emergency.link')}
                    </Button>
                  </p>
                ) : null}
              </form>
            ) : null
          ) : (
            <Alert kind="warning">{t('login.noMethods')}</Alert>
          )
        ) : null}
      </main>
    </div>
  );
}
