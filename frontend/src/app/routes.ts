import type { MessageKey } from '../platform/i18n/i18n';
import { canAll, type CanFn } from '../platform/session/permissions';

export type RouteId =
  | 'home'
  | 'me'
  | 'myWork'
  | 'notifications'
  | 'tasks'
  | 'taskNew'
  | 'taskDetail'
  | 'roles'
  | 'roleNew'
  | 'roleDetail'
  | 'roleAssignments'
  | 'directorySync'
  | 'directorySyncRun'
  | 'audit';

export type NavGroup = 'main' | 'admin';

export type AppRoute = {
  id: RouteId;
  pattern: string;
  titleKey: MessageKey;
  /** Permissions required to see the screen (UI hiding only; the backend enforces). */
  requires?: readonly string[];
  /** Alternative to `requires`: the screen is visible with at least one of these permissions. */
  requiresAny?: readonly string[];
  nav?: NavGroup;
};

const taskViewPermissions = ['tasks.view', 'tasks.manage', 'tasks.work'] as const;

// Static patterns must precede parameterised ones ("/admin/roles/new" before "/admin/roles/:id").
export const appRoutes: readonly AppRoute[] = [
  { id: 'home', pattern: '/', titleKey: 'nav.home', nav: 'main' },
  { id: 'me', pattern: '/me', titleKey: 'nav.me', nav: 'main' },
  {
    id: 'myWork',
    pattern: '/my-work',
    titleKey: 'nav.myWork',
    requiresAny: taskViewPermissions,
    nav: 'main',
  },
  { id: 'notifications', pattern: '/notifications', titleKey: 'nav.notifications', nav: 'main' },
  {
    id: 'tasks',
    pattern: '/tasks',
    titleKey: 'nav.tasks',
    requiresAny: taskViewPermissions,
    nav: 'main',
  },
  {
    id: 'taskNew',
    pattern: '/tasks/new',
    titleKey: 'tasks.create.title',
    requires: ['tasks.manage'],
  },
  {
    id: 'taskDetail',
    pattern: '/tasks/:id',
    titleKey: 'tasks.detail.title',
    requiresAny: taskViewPermissions,
  },
  {
    id: 'roles',
    pattern: '/admin/roles',
    titleKey: 'nav.roles',
    requires: ['platform.roles.view'],
    nav: 'admin',
  },
  {
    id: 'roleNew',
    pattern: '/admin/roles/new',
    titleKey: 'roles.create.title',
    requires: ['platform.roles.manage', 'platform.roles.view'],
  },
  {
    id: 'roleDetail',
    pattern: '/admin/roles/:id',
    titleKey: 'roles.detail.title',
    requires: ['platform.roles.view'],
  },
  {
    id: 'roleAssignments',
    pattern: '/admin/role-assignments',
    titleKey: 'nav.roleAssignments',
    requires: ['platform.roles.view'],
    nav: 'admin',
  },
  {
    id: 'directorySync',
    pattern: '/admin/directory-sync',
    titleKey: 'nav.directorySync',
    requires: ['organization.directory.view', 'organization.view'],
    nav: 'admin',
  },
  {
    id: 'directorySyncRun',
    pattern: '/admin/directory-sync/:id',
    titleKey: 'sync.detail.title',
    requires: ['organization.directory.view', 'organization.view'],
  },
  {
    id: 'audit',
    pattern: '/admin/audit',
    titleKey: 'nav.audit',
    requires: ['platform.audit.view'],
    nav: 'admin',
  },
];

/** UI hiding only: all of `requires` and, when set, at least one of `requiresAny`. */
export function canViewRoute(can: CanFn, route: AppRoute): boolean {
  if (!canAll(can, route.requires)) return false;
  return !route.requiresAny || route.requiresAny.some((permission) => can(permission));
}

/** Navigation entries the current user may see, in declaration order. */
export function visibleNavItems(can: CanFn, group: NavGroup): AppRoute[] {
  return appRoutes.filter((route) => route.nav === group && canViewRoute(can, route));
}

/** A nav entry is active for its own path and every sub-path (detail screens). */
export function isNavActive(pattern: string, pathname: string): boolean {
  if (pattern === '/') return pathname === '/';
  return pathname === pattern || pathname.startsWith(`${pattern}/`);
}
