import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import { api, isApiError } from '../../platform/api/client';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { locales, type Locale } from '../../platform/i18n/i18n';
import { Alert } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { TextField } from '../../platform/ui/Field';
import { firstAdministratorErrorKey, firstAdministratorRequest } from './firstAdministratorModel';

export function FirstAdministratorScreen() {
  const { t, locale, setLocale } = useI18n();
  const status = useAsync(
    (signal) =>
      api.get<{ available: boolean }>('/setup/status', { signal, skipUnauthorizedHandler: true }),
    [],
  );
  const [step, setStep] = useState<1 | 2 | 3>(1);
  const [token, setToken] = useState('');
  const [user, setUser] = useState('');
  const [tenant, setTenant] = useState('');
  const [object, setObject] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ReturnType<typeof firstAdministratorErrorKey> | null>(null);

  useEffect(() => {
    document.title = `${t('firstAdmin.title')} – ${t('app.name')}`;
  }, [t]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const body = firstAdministratorRequest(token, user, tenant, object);
    if (!body || busy) {
      setError('firstAdmin.error.invalidRequest');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.post<{ created: true }>('/setup/first-administrator', body, {
        skipUnauthorizedHandler: true,
      });
      setToken('');
      setStep(3);
    } catch (cause) {
      setError(firstAdministratorErrorKey(isApiError(cause) ? cause : { status: 0, code: '' }));
    } finally {
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
        <h1 className="login-title">{t('firstAdmin.title')}</h1>
        {status.loading ? (
          <p role="status">{t('state.loading')}</p>
        ) : status.error ? (
          <Alert kind="error">{t('firstAdmin.error.status')}</Alert>
        ) : status.data?.available === false && step !== 3 ? (
          <Alert kind="info">{t('firstAdmin.unavailable')}</Alert>
        ) : step === 3 ? (
          <Alert kind="success">{t('firstAdmin.done')}</Alert>
        ) : step === 1 ? (
          <form
            onSubmit={(event) => {
              event.preventDefault();
              if (token.trim()) setStep(2);
            }}
          >
            <p>{t('firstAdmin.tokenIntro')}</p>
            <p>
              <code>turaco-admin admin setup-token</code>
            </p>
            <TextField
              label={t('firstAdmin.token')}
              type="password"
              value={token}
              onChange={(event) => setToken(event.target.value)}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              required
              autoFocus
            />
            <Button type="submit" variant="primary" className="btn-block" disabled={!token.trim()}>
              {t('firstAdmin.next')}
            </Button>
          </form>
        ) : (
          <form onSubmit={(event) => void submit(event)}>
            <p>{t('firstAdmin.userIntro')}</p>
            {error ? <Alert kind="error">{t(error)}</Alert> : null}
            <TextField
              label={t('firstAdmin.user')}
              hint={t('firstAdmin.userHint')}
              value={user}
              onChange={(event) => setUser(event.target.value)}
              maxLength={320}
              required
              autoFocus
            />
            <TextField
              label={t('firstAdmin.tenant')}
              hint={t('firstAdmin.entraHint')}
              value={tenant}
              onChange={(event) => setTenant(event.target.value)}
              autoCapitalize="none"
              spellCheck={false}
            />
            <TextField
              label={t('firstAdmin.object')}
              value={object}
              onChange={(event) => setObject(event.target.value)}
              autoCapitalize="none"
              spellCheck={false}
            />
            <Button
              type="submit"
              variant="primary"
              className="btn-block"
              busy={busy}
              disabled={!user.trim() || busy}
            >
              {t('firstAdmin.create')}
            </Button>
            <p className="login-switch">
              <Button
                variant="secondary"
                className="btn-link"
                onClick={() => {
                  setError(null);
                  setStep(1);
                }}
              >
                {t('firstAdmin.back')}
              </Button>
            </p>
          </form>
        )}
        {status.data?.available === false || step === 3 ? (
          <p className="login-switch">
            <a href="/login">{t('firstAdmin.signIn')}</a>
          </p>
        ) : null}
      </main>
    </div>
  );
}
