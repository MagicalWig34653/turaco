import { useEffect } from 'react';
import type { ReactNode } from 'react';
import { AuditScreen } from '../modules/audit/AuditScreen';
import { LoginScreen } from '../modules/auth/LoginScreen';
import { MeScreen } from '../modules/access/MeScreen';
import { RoleAssignmentsScreen } from '../modules/access/RoleAssignmentsScreen';
import { RoleCreateScreen } from '../modules/access/RoleCreateScreen';
import { RoleDetailScreen } from '../modules/access/RoleDetailScreen';
import { RolesScreen } from '../modules/access/RolesScreen';
import { MyWorkScreen } from '../modules/my-work/MyWorkScreen';
import { TaskCreateScreen } from '../modules/tasks/TaskCreateScreen';
import { TaskDetailScreen } from '../modules/tasks/TaskDetailScreen';
import { TasksScreen } from '../modules/tasks/TasksScreen';
import { DirectorySyncRunScreen } from '../modules/directory/DirectorySyncRunScreen';
import { DirectorySyncScreen } from '../modules/directory/DirectorySyncScreen';
import { I18nProvider, useI18n } from '../platform/i18n/I18nProvider';
import { matchRoute } from '../platform/router/routing';
import { navigate, useLocation } from '../platform/router/Router';
import { SessionProvider, useSession } from '../platform/session/SessionProvider';
import { Home } from './Home';
import { appRoutes, canViewRoute, type RouteId } from './routes';
import { Shell } from './Shell';
import { ForbiddenView, NotFoundView } from './StatusViews';

function renderScreen(id: RouteId, params: Record<string, string>): ReactNode {
  switch (id) {
    case 'home':
      return <Home />;
    case 'me':
      return <MeScreen />;
    case 'myWork':
      return <MyWorkScreen />;
    case 'tasks':
      return <TasksScreen />;
    case 'taskNew':
      return <TaskCreateScreen />;
    case 'taskDetail':
      return <TaskDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'roles':
      return <RolesScreen />;
    case 'roleNew':
      return <RoleCreateScreen />;
    case 'roleDetail':
      return <RoleDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'roleAssignments':
      return <RoleAssignmentsScreen />;
    case 'directorySync':
      return <DirectorySyncScreen />;
    case 'directorySyncRun':
      return <DirectorySyncRunScreen key={params.id} id={params.id ?? ''} />;
    case 'audit':
      return <AuditScreen />;
  }
}

function AuthenticatedApp() {
  const { t } = useI18n();
  const { can } = useSession();
  const { pathname } = useLocation();

  useEffect(() => {
    if (pathname === '/login') navigate('/', { replace: true });
  }, [pathname]);

  const match = matchRoute(appRoutes, pathname);
  if (!match) {
    return (
      <Shell title={t('notFound.title')}>
        <NotFoundView />
      </Shell>
    );
  }
  const allowed = canViewRoute(can, match.route);
  return (
    <Shell title={allowed ? t(match.route.titleKey) : t('forbidden.title')}>
      {allowed ? renderScreen(match.route.id, match.params) : <ForbiddenView />}
    </Shell>
  );
}

function Gate() {
  const { t } = useI18n();
  const { state } = useSession();
  useEffect(() => {
    if (state.status === 'anonymous') document.title = `${t('login.title')} – ${t('app.name')}`;
  }, [state.status, t]);
  if (state.status === 'loading') {
    return (
      <p className="boot" role="status">
        {t('state.loading')}
      </p>
    );
  }
  if (state.status === 'anonymous') return <LoginScreen />;
  return <AuthenticatedApp />;
}

export function App() {
  return (
    <I18nProvider>
      <SessionProvider>
        <Gate />
      </SessionProvider>
    </I18nProvider>
  );
}
