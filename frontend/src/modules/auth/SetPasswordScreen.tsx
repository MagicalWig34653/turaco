import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import { api, isApiError, type ApiError } from '../../platform/api/client';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { locales, type Locale } from '../../platform/i18n/i18n';
import { Alert } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, TextField } from '../../platform/ui/Field';
import {
  checkPassword,
  MIN_LOCAL_PASSWORD_LENGTH,
  parseTokenFragment,
  type TokenFragment,
} from '../organization/adminModel';
import { redeemErrorKey } from './setPasswordModel';

/**
 * Landing page of an invitation or reset link (`/set-password#token=...&purpose=...`). The token is
 * read from the URL fragment once, removed from the address bar and kept only in memory. Setting the
 * password never signs the person in.
 */
export function SetPasswordScreen() {
  const { t, locale, setLocale } = useI18n();
  const [fragment, setFragment] = useState<TokenFragment | null>(() =>
    parseTokenFragment(window.location.hash),
  );
  const [password, setPassword] = useState('');
  const [confirmation, setConfirmation] = useState('');
  const [reveal, setReveal] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [done, setDone] = useState(false);

  useEffect(() => {
    // Keep the secret out of history, screenshots and a shared address bar.
    const strip = () => {
      if (window.location.hash) {
        window.history.replaceState(
          window.history.state,
          '',
          `${window.location.pathname}${window.location.search}`,
        );
      }
    };
    strip();
    // A link pasted into this tab later replaces the token held in memory.
    const onHashChange = () => {
      const next = parseTokenFragment(window.location.hash);
      if (next) {
        setFragment(next);
        setError(undefined);
        setDone(false);
      }
      strip();
    };
    window.addEventListener('hashchange', onHashChange);
    const meta = document.createElement('meta');
    meta.name = 'referrer';
    meta.content = 'no-referrer';
    document.head.appendChild(meta);
    return () => {
      window.removeEventListener('hashchange', onHashChange);
      meta.remove();
    };
  }, []);
  useEffect(() => {
    document.title = `${t('setPassword.title')} – ${t('app.name')}`;
  }, [t]);

  const check = checkPassword(password, confirmation);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!fragment || !check.ok) return;
    setBusy(true);
    setError(undefined);
    try {
      await api.post<void>(
        '/auth/credential-tokens/redeem',
        { token: fragment.token, password },
        { skipUnauthorizedHandler: true },
      );
      setPassword('');
      setConfirmation('');
      setDone(true);
    } catch (cause) {
      setError(isApiError(cause) ? cause : undefined);
      setBusy(false);
    }
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
      <main className="login-card">
        <div className="brand">
          <img className="brand-icon" src="/icon-192.png" alt="" width={36} height={36} />
          {t('app.name')}
        </div>
        <h1 className="login-title">
          {fragment?.purpose === 'reset' ? t('setPassword.title.reset') : t('setPassword.title')}
        </h1>
        {done ? (
          <>
            <Alert kind="success">{t('setPassword.done')}</Alert>
            <a className="btn btn-primary btn-block" href="/">
              {t('setPassword.signIn')}
            </a>
          </>
        ) : !fragment ? (
          <Alert kind="error">{t('setPassword.error.noToken')}</Alert>
        ) : (
          <form onSubmit={(event) => void submit(event)} noValidate>
            <p>{t('setPassword.intro')}</p>
            {error ? <Alert kind="error">{t(redeemErrorKey(error))}</Alert> : null}
            <TextField
              label={t('setPassword.password')}
              type={reveal ? 'text' : 'password'}
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              autoComplete="new-password"
              autoCapitalize="none"
              spellCheck={false}
              maxLength={1024}
              required
              autoFocus
            />
            <TextField
              label={t('setPassword.confirm')}
              type={reveal ? 'text' : 'password'}
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              autoComplete="new-password"
              autoCapitalize="none"
              spellCheck={false}
              maxLength={1024}
              error={confirmation !== '' && !check.matches ? t('setPassword.mismatch') : undefined}
              required
            />
            <Checkbox
              label={t('setPassword.reveal')}
              checked={reveal}
              onChange={(event) => setReveal(event.target.checked)}
            />
            <ul className="adm-hints" aria-label={t('setPassword.policy.title')}>
              <li className={check.longEnough ? 'is-met' : ''}>
                <span aria-hidden="true">{check.longEnough ? '✓' : '•'}</span>{' '}
                {t('setPassword.policy.length', { count: MIN_LOCAL_PASSWORD_LENGTH })}
              </li>
              <li>
                <span aria-hidden="true">•</span> {t('setPassword.policy.common')}
              </li>
              <li>
                <span aria-hidden="true">•</span> {t('setPassword.policy.personal')}
              </li>
            </ul>
            <Button
              type="submit"
              variant="primary"
              busy={busy}
              disabled={!check.ok}
              className="btn-block"
            >
              {t('setPassword.submit')}
            </Button>
            <p className="field-hint">{t('setPassword.noAutoLogin')}</p>
          </form>
        )}
      </main>
    </div>
  );
}
