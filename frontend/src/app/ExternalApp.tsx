import { useEffect } from 'react';
import type { ReactNode } from 'react';
import { ModulesProvider } from '../platform/modules/ModulesProvider';
import { useI18n } from '../platform/i18n/I18nProvider';
import { matchRoute } from '../platform/router/routing';
import { Link, navigate, useLocation } from '../platform/router/Router';
import { useSession } from '../platform/session/SessionProvider';
import { sessionDisplayName } from '../platform/session/identity';
import { ExternalTicketScreen } from '../modules/tickets/ExternalTicketScreen';
import { NotificationsScreen } from '../modules/notifications/NotificationsScreen';
import { TaskDetailScreen } from '../modules/tasks/TaskDetailScreen';
import { TasksScreen } from '../modules/tasks/TasksScreen';
import type { MessageKey } from '../platform/i18n/i18n';

type ExternalRoute = {
  id: 'tasks' | 'task' | 'notifications' | 'ticket';
  pattern: string;
  titleKey: MessageKey;
};

/**
 * The only routes an external account (vendor staff, ADR-0034) has. The server's allow-list is the authority: it
 * answers 404 for everything else, so the application shell, search, views, the AI assistant and presence are not
 * even loaded for these accounts.
 */
const externalRoutes: readonly ExternalRoute[] = [
  { id: 'tasks', pattern: '/tasks', titleKey: 'nav.tasks' },
  { id: 'task', pattern: '/tasks/:id', titleKey: 'nav.tasks' },
  { id: 'notifications', pattern: '/notifications', titleKey: 'nav.notifications' },
  { id: 'ticket', pattern: '/support/:id', titleKey: 'external.ticket.title' },
];

function screenOf(route: ExternalRoute, params: Record<string, string>): ReactNode {
  switch (route.id) {
    case 'tasks':
      return <TasksScreen />;
    case 'task':
      return <TaskDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'notifications':
      return <NotificationsScreen />;
    case 'ticket':
      return <ExternalTicketScreen key={params.id} id={params.id ?? ''} />;
  }
}

/** A deliberately small shell: Tasks, notifications and the Tickets shared with the account. */
export function ExternalApp() {
  const { t } = useI18n();
  const { session, logout } = useSession();
  const { pathname } = useLocation();
  const match = matchRoute(externalRoutes, pathname);
  useEffect(() => {
    if (!match) navigate('/tasks', { replace: true });
  }, [match]);
  useEffect(() => {
    if (match) document.title = `${t(match.route.titleKey)} – ${t('app.name')}`;
  }, [match, t]);
  return (
    <ModulesProvider key={session?.userId}>
      <div className="external-shell">
        <header className="external-bar">
          <strong>{t('app.name')}</strong>
          <nav aria-label={t('external.nav')}>
            <Link to="/tasks">{t('nav.tasks')}</Link>{' '}
            <Link to="/notifications">{t('nav.notifications')}</Link>
          </nav>
          <span className="external-user">{sessionDisplayName(session) ?? ''}</span>
          <button type="button" className="btn btn-secondary" onClick={() => void logout()}>
            {t('action.logout')}
          </button>
        </header>
        <main id="main" tabIndex={-1} className="external-main">
          {match ? screenOf(match.route, match.params) : null}
        </main>
      </div>
    </ModulesProvider>
  );
}
