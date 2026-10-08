import { AskTuraco, useAi } from '../modules/ai/AiProvider';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { notificationsApi, onNotificationsChanged } from '../modules/notifications/api';
import { unreadLabel } from '../modules/notifications/text';
import { useAsync } from '../platform/api/useAsync';
import { useI18n } from '../platform/i18n/I18nProvider';
import { locales, type Locale } from '../platform/i18n/i18n';
import { Link, navigate, useLocation } from '../platform/router/Router';
import { useSession } from '../platform/session/SessionProvider';
import { sessionDisplayName } from '../platform/session/identity';
import { useTheme } from '../platform/theme/ThemeProvider';
import { NavIcon } from '../platform/ui/NavIcon';
import { CommandPalette } from '../platform/ui/shell/CommandPalette';
import { navigationCommands } from '../platform/ui/shell/paletteCommands';
import { appRoutes, isNavActive } from './routes';
import { shellNavigation } from './shellNavigation';
import type { MessageKey } from '../platform/i18n/i18n';

function readCollapsed(): boolean {
  try {
    return window.localStorage.getItem('turaco.rail.collapsed') === 'true';
  } catch {
    return false;
  }
}

function UserIdentity({ name, method }: { name: string; method?: string | undefined }) {
  const { t } = useI18n();
  const initials =
    name
      .match(/\p{L}+/gu)
      ?.slice(0, 2)
      .map((part) => part[0]?.toUpperCase())
      .join('') || '👤';
  return (
    <>
      <span className="turaco-avatar" aria-hidden="true">
        {initials}
      </span>
      <span className="turaco-user-label">
        <span className="user-name">{name || t('nav.me')}</span>
        <small>
          {method === 'password' || method === 'kerberos' || method === 'emergency'
            ? t(`auth.method.${method}`)
            : t('nav.me')}
        </small>
      </span>
      <span className="turaco-session-name">{name || t('nav.me')}</span>
    </>
  );
}
/** Authenticated layout; routes and API authorization remain unchanged. */
export function Shell({ title, children }: { title: string; children: ReactNode }) {
  const { t, locale, setLocale } = useI18n();
  const { session, logout } = useSession();
  const { can, open: openAi } = useAi();
  const userName = sessionDisplayName(session) ?? '';
  const navigation = shellNavigation(can);
  const { theme, setTheme, density, setDensity, motion, setMotion } = useTheme();
  const { pathname } = useLocation();
  const mainRef = useRef<HTMLElement>(null);
  const railRef = useRef<HTMLElement>(null);
  const headerRef = useRef<HTMLElement>(null);
  const mobileTriggerRef = useRef<HTMLButtonElement>(null);
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [recentPaths, setRecentPaths] = useState<string[]>([]);
  const focusedPath = useRef(pathname);
  const unread = useAsync((signal) => notificationsApi.unreadCount(signal), []);
  const reloadUnread = unread.reload;
  const unreadValue = unread.data?.count ? unreadLabel(unread.data.count, unread.data.max) : null;
  const commands = useMemo(
    () => [
      ...navigationCommands(appRoutes, can, (route) => t(route.titleKey)),
      ...(can('ai.use')
        ? [{ id: 'aiAssistant', label: t('ai.ask'), path: '', action: () => openAi() }]
        : []),
    ],
    [can, t, openAi],
  );
  const activeNavPath = commands
    .filter((command) => isNavActive(command.path, pathname))
    .sort((left, right) => right.path.length - left.path.length)[0]?.path;
  const displayedNavPath =
    pathname.startsWith('/support/') &&
    pathname !== '/support/new' &&
    (can('tickets.view') || can('tickets.manage'))
      ? appRoutes.find((route) => route.id === 'ticketQueue')?.pattern
      : activeNavPath;
  const area =
    navigation.find(({ items }) => items.some((item) => item.pattern === displayedNavPath))
      ?.label ?? 'shell.workspace';
  const themeLabels: Record<typeof theme, MessageKey> = {
    auto: 'shell.themeAuto',
    turaco: 'shell.themeTuraco',
    dark: 'shell.themeDark',
    cyberpunk: 'shell.themeCyberpunk',
  };
  const nextTheme = (
    { auto: 'turaco', turaco: 'dark', dark: 'cyberpunk', cyberpunk: 'auto' } as const
  )[theme];
  const quickCreate = appRoutes.filter(
    (route) =>
      (route.id === 'ticketNew' || route.id === 'catalog') &&
      (!route.requires || route.requires.every(can)),
  );

  useEffect(() => {
    document.title = `${title} – ${t('app.name')}`;
  }, [title, t]);
  useEffect(() => {
    const timer = window.setInterval(reloadUnread, 60_000);
    window.addEventListener('focus', reloadUnread);
    const off = onNotificationsChanged(reloadUnread);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener('focus', reloadUnread);
      off();
    };
  }, [reloadUnread]);
  useEffect(() => {
    setMobileOpen(false);
    // Move focus only on an actual route change; a repeated effect run must not scroll on load.
    if (focusedPath.current !== pathname) mainRef.current?.focus();
    focusedPath.current = pathname;
    setRecentPaths((paths) =>
      commands.some((command) => command.path === pathname)
        ? [pathname, ...paths.filter((path) => path !== pathname)].slice(0, 5)
        : paths,
    );
  }, [pathname, commands]);
  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        setMobileOpen(false);
        setPaletteOpen(true);
      }
      if (event.key === 'Escape' && mobileOpen) {
        setMobileOpen(false);
        requestAnimationFrame(() => mobileTriggerRef.current?.focus());
      }
      if (event.key === 'Escape')
        headerRef.current
          ?.querySelectorAll('details[open]')
          .forEach((item) => item.removeAttribute('open'));
      if (event.key === 'Tab' && mobileOpen && railRef.current) {
        const focusable = [
          ...railRef.current.querySelectorAll<HTMLElement>('a[href],button:not(:disabled)'),
        ].filter((element) => element.getClientRects().length > 0);
        if (!focusable.length) return;
        if (!railRef.current.contains(document.activeElement)) {
          event.preventDefault();
          focusable[0]?.focus();
          return;
        }
        const firstItem = focusable[0];
        const lastItem = focusable[focusable.length - 1];
        if (event.shiftKey && document.activeElement === firstItem) {
          event.preventDefault();
          lastItem?.focus();
        }
        if (!event.shiftKey && document.activeElement === lastItem) {
          event.preventDefault();
          firstItem?.focus();
        }
      }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [mobileOpen]);
  useEffect(() => {
    if (!mobileOpen) return;
    const previous = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = previous;
    };
  }, [mobileOpen]);
  useEffect(() => {
    const onOutside = (event: PointerEvent) => {
      headerRef.current?.querySelectorAll('details[open]').forEach((item) => {
        if (!item.contains(event.target as Node)) item.removeAttribute('open');
      });
    };
    document.addEventListener('pointerdown', onOutside);
    return () => document.removeEventListener('pointerdown', onOutside);
  }, []);

  function toggleCollapsed() {
    const next = !collapsed;
    setCollapsed(next);
    try {
      window.localStorage.setItem('turaco.rail.collapsed', String(next));
    } catch {
      /* Session-only preference. */
    }
  }

  return (
    <div
      className={`shell turaco-shell${collapsed ? ' turaco-shell-collapsed' : ''}${mobileOpen ? ' turaco-shell-mobile-open' : ''}`}
    >
      <a
        className="skip-link"
        href="#main"
        onClick={(event) => {
          event.preventDefault();
          mainRef.current?.focus();
        }}
      >
        {t('nav.skip')}
      </a>
      {mobileOpen && (
        <button
          type="button"
          className="turaco-rail-shade"
          aria-label={t('shell.closeMenu')}
          onClick={() => {
            setMobileOpen(false);
            mobileTriggerRef.current?.focus();
          }}
        />
      )}
      <aside
        ref={railRef}
        className="sidebar turaco-rail"
        aria-label={t('nav.primary')}
        aria-modal={mobileOpen ? true : undefined}
        role={mobileOpen ? 'dialog' : undefined}
      >
        <div className="turaco-rail-brand">
          <span className="turaco-brand-mark" aria-hidden="true">
            ✦
          </span>
          <span className="turaco-brand-name">{t('app.name')}</span>
          <button
            type="button"
            className="turaco-mobile-close"
            aria-label={t('shell.closeMenu')}
            onClick={() => {
              setMobileOpen(false);
              mobileTriggerRef.current?.focus();
            }}
          >
            ×
          </button>
          <button
            type="button"
            className="turaco-rail-collapse"
            onClick={toggleCollapsed}
            aria-label={collapsed ? t('shell.expand') : t('shell.collapse')}
            title={collapsed ? t('shell.expand') : t('shell.collapse')}
          >
            {collapsed ? '»' : '«'}
          </button>
        </div>
        <nav aria-label={t('nav.primary')}>
          {navigation.map(({ label, items }) => {
            return (
              <div className="nav-section turaco-rail-section" key={label}>
                {label && <p className="nav-heading">{t(label)}</p>}
                {items.map((item) => (
                  <Link
                    key={item.id}
                    to={item.pattern}
                    title={collapsed ? t(item.titleKey) : undefined}
                    aria-label={collapsed ? t(item.titleKey) : undefined}
                    aria-current={item.pattern === displayedNavPath ? 'page' : undefined}
                  >
                    <span className="turaco-rail-icon">
                      <NavIcon id={item.id} />
                    </span>
                    <span className="turaco-rail-label">{t(item.titleKey)}</span>
                    {item.id === 'notifications' && unreadValue && (
                      <span
                        className="badge badge-info nav-count"
                        aria-label={t('notifications.unreadCount', { count: unreadValue })}
                      >
                        {unreadValue}
                      </span>
                    )}
                  </Link>
                ))}
              </div>
            );
          })}
        </nav>
        <div className="turaco-rail-footer">
          <Link to="/notifications" className="turaco-rail-bottom">
            <span
              className={`turaco-rail-bottom-dot${unread.error ? ' is-unknown' : ''}`}
              aria-hidden="true"
            />
            <span>
              {unread.error
                ? t('shell.notificationsUnavailable')
                : unread.loading && !unread.data
                  ? t('state.loading')
                  : t('shell.notificationStatus', { count: unreadValue ?? 0 })}
            </span>
          </Link>
          <button
            type="button"
            className="turaco-theme-toggle"
            onClick={() => setTheme(nextTheme)}
            title={t('shell.nextTheme', { theme: t(themeLabels[nextTheme]) })}
            aria-label={t('shell.nextTheme', { theme: t(themeLabels[nextTheme]) })}
          >
            <svg
              viewBox="0 0 24 24"
              width="18"
              height="18"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.6"
              aria-hidden="true"
            >
              <circle cx="12" cy="12" r="4" />
              <path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5" />
            </svg>
          </button>
        </div>
      </aside>
      <div className="content-column turaco-content-column" inert={mobileOpen}>
        <header ref={headerRef} className="appbar turaco-appbar">
          <button
            ref={mobileTriggerRef}
            type="button"
            className="turaco-mobile-trigger"
            aria-label={t('shell.openMenu')}
            aria-expanded={mobileOpen}
            onClick={() => {
              setMobileOpen(true);
              requestAnimationFrame(() =>
                railRef.current?.querySelector<HTMLElement>('a[href]')?.focus(),
              );
            }}
          >
            ☰
          </button>
          <nav className="turaco-appbar-location" aria-label={t('shell.breadcrumb')}>
            <span>{t(area)}</span>
            <span aria-hidden="true">/</span>
            <strong aria-current="page">{title}</strong>
          </nav>
          <div className="turaco-appbar-actions">
            <AskTuraco />
            <button
              type="button"
              className="turaco-search-trigger"
              onClick={() => setPaletteOpen(true)}
              aria-label={t('shell.search')}
            >
              <span aria-hidden="true">⌕</span>
              <span className="turaco-search-label">{t('shell.searchPlaceholder')}</span>
              <kbd>
                {typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
                  ? '⌘ K'
                  : 'Ctrl K'}
              </kbd>
            </button>
            {quickCreate.length > 0 && (
              <details className="turaco-header-menu turaco-create-menu">
                <summary>
                  {t('shell.new')} <span aria-hidden="true">⌄</span>
                </summary>
                <div className="turaco-header-popover" role="group" aria-label={t('shell.new')}>
                  {quickCreate.map((route) => (
                    <button
                      type="button"
                      key={route.id}
                      onClick={(event) => {
                        event.currentTarget.closest('details')?.removeAttribute('open');
                        navigate(route.pattern);
                      }}
                    >
                      {route.id === 'ticketNew' ? t('shell.newTicket') : t('shell.newRequest')}
                    </button>
                  ))}
                </div>
              </details>
            )}
            <Link
              className="turaco-bell"
              to="/notifications"
              aria-label={
                unreadValue
                  ? t('notifications.unreadCount', { count: unreadValue })
                  : t('shell.notifications')
              }
              title={t('shell.notifications')}
            >
              <svg
                viewBox="0 0 24 24"
                width="19"
                height="19"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.7"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden="true"
              >
                <path d="M18 8a6 6 0 0 0-12 0c0 7-3 8-3 9h18c0-1-3-2-3-9ZM10 21h4" />
              </svg>
              {unreadValue && (
                <span className="badge badge-info nav-count" aria-hidden="true">
                  {unreadValue}
                </span>
              )}
            </Link>
            <details className="turaco-header-menu turaco-user-menu">
              <summary aria-label={t('shell.preferences')}>
                <UserIdentity name={userName} method={session?.authMethod} />
                <span aria-hidden="true">⌄</span>
              </summary>
              <div className="turaco-header-popover turaco-preferences">
                <div className="turaco-popover-heading">{t('shell.preferences')}</div>
                <div className="turaco-session-detail">
                  <UserIdentity name={userName} method={session?.authMethod} />
                </div>
                <label>
                  {t('shell.theme')}
                  <select
                    value={theme}
                    onChange={(event) => setTheme(event.target.value as typeof theme)}
                  >
                    <option value="auto">{t('shell.themeAuto')}</option>
                    <option value="turaco">{t('shell.themeTuraco')}</option>
                    <option value="dark">{t('shell.themeDark')}</option>
                    <option value="cyberpunk">{t('shell.themeCyberpunk')}</option>
                  </select>
                </label>
                <label>
                  {t('shell.density')}
                  <select
                    value={density}
                    onChange={(event) => setDensity(event.target.value as typeof density)}
                  >
                    <option value="comfortable">{t('shell.densityComfortable')}</option>
                    <option value="compact">{t('shell.densityCompact')}</option>
                  </select>
                </label>
                <label>
                  {t('shell.motion')}
                  <select
                    value={motion}
                    onChange={(event) => setMotion(event.target.value as typeof motion)}
                  >
                    <option value="auto">{t('shell.motionAuto')}</option>
                    <option value="reduced">{t('shell.motionReduced')}</option>
                  </select>
                </label>
                <label>
                  {t('language')}
                  <select
                    value={locale}
                    onChange={(event) => setLocale(event.target.value as Locale)}
                  >
                    {locales.map((value) => (
                      <option key={value} value={value}>
                        {value === 'de' ? 'Deutsch' : 'English'}
                      </option>
                    ))}
                  </select>
                </label>
                <button type="button" className="turaco-signout" onClick={() => void logout()}>
                  {t('action.logout')}
                </button>
              </div>
            </details>
          </div>
        </header>
        <main id="main" className="content turaco-main" tabIndex={-1} ref={mainRef}>
          <div key={pathname} className="turaco-route-enter">
            {children}
          </div>
        </main>
      </div>
      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        commands={commands}
        recentPaths={recentPaths}
      />
    </div>
  );
}
