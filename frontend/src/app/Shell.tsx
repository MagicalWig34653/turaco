import { useEffect, useRef } from 'react';
import type { ReactNode } from 'react';
import { useAsync } from '../platform/api/useAsync';
import { endpoints } from '../platform/api/endpoints';
import { useI18n } from '../platform/i18n/I18nProvider';
import { locales, type Locale } from '../platform/i18n/i18n';
import { Link, useLocation } from '../platform/router/Router';
import { useSession } from '../platform/session/SessionProvider';
import { Button } from '../platform/ui/Button';
import { isNavActive, visibleNavItems, type AppRoute, type NavGroup } from './routes';

function NavSection({ group, labelKey }: { group: NavGroup; labelKey?: 'nav.admin' }) {
  const { t } = useI18n();
  const { can } = useSession();
  const { pathname } = useLocation();
  const items = visibleNavItems(can, group);
  if (items.length === 0) return null;
  return (
    <div className="nav-section">
      {labelKey ? <p className="nav-heading">{t(labelKey)}</p> : null}
      {items.map((item: AppRoute) => (
        <Link
          key={item.id}
          to={item.pattern}
          aria-current={isNavActive(item.pattern, pathname) ? 'page' : undefined}
        >
          {t(item.titleKey)}
        </Link>
      ))}
    </div>
  );
}

function UserName() {
  const { session, can } = useSession();
  const canViewUser = can('organization.view');
  const userId = session?.userId ?? '';
  const user = useAsync(
    (signal) => (canViewUser ? endpoints.user(userId, signal) : Promise.resolve(undefined)),
    [canViewUser, userId],
  );
  return <span className="user-name">{user.data?.displayName ?? userId}</span>;
}

/** Authenticated layout: skip link, permission-filtered navigation, user menu, language, logout. */
export function Shell({ title, children }: { title: string; children: ReactNode }) {
  const { t, locale, setLocale } = useI18n();
  const { logout } = useSession();
  const { pathname } = useLocation();
  const mainRef = useRef<HTMLElement>(null);
  const first = useRef(true);

  // Route change: update the document title and move focus to the main region so keyboard and
  // screen reader users start at the new content.
  useEffect(() => {
    document.title = `${title} – ${t('app.name')}`;
  }, [title, t]);

  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    mainRef.current?.focus();
  }, [pathname]);

  return (
    <div className="shell">
      <a
        className="skip-link"
        href="#main"
        onClick={(e) => {
          e.preventDefault();
          mainRef.current?.focus();
        }}
      >
        {t('nav.skip')}
      </a>
      <aside className="sidebar">
        <div className="brand">{t('app.name')}</div>
        <nav aria-label={t('nav.primary')}>
          <NavSection group="main" />
          <NavSection group="admin" labelKey="nav.admin" />
        </nav>
      </aside>
      <div className="content-column">
        <header className="appbar">
          <div className="appbar-user" aria-label={t('shell.user')}>
            <UserName />
          </div>
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
          <Button onClick={() => void logout()}>{t('action.logout')}</Button>
        </header>
        <main id="main" className="content" tabIndex={-1} ref={mainRef}>
          {children}
        </main>
      </div>
    </div>
  );
}
