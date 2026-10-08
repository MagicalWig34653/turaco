import type { ModuleEnabled } from '../platform/modules/model';
import type { MessageKey } from '../platform/i18n/i18n';
import type { CanFn } from '../platform/session/permissions';
import { appRoutes, canViewRoute, type AppRoute, type NavGroup } from './routes';

const workspace = new Set(['home', 'myWork', 'approvals', 'briefing', 'notifications', 'tasks']);
const support = new Set(['ticketQueue', 'knowledge', 'incidents', 'problems', 'runbooks']);
const labels: Record<NavGroup, MessageKey> = {
  main: 'shell.personal',
  logistics: 'nav.logistics',
  endpoints: 'nav.endpoints',
  infrastructure: 'nav.infrastructure',
  services: 'nav.services',
  changes: 'nav.changes',
  planning: 'nav.planning',
  security: 'nav.security',
  admin: 'nav.admin',
};

/** Presentation groups reuse the route registry's visibility rules unchanged. */
export function shellNavigation(
  can: CanFn,
  enabled?: ModuleEnabled,
): { label: MessageKey; items: AppRoute[] }[] {
  const groups = new Map<MessageKey, AppRoute[]>([
    ['shell.workspace', []],
    ['shell.personal', []],
    ['shell.serviceDesk', []],
  ]);
  for (const route of appRoutes) {
    if (!route.nav || !canViewRoute(can, route, enabled)) continue;
    const label = workspace.has(route.id)
      ? 'shell.workspace'
      : support.has(route.id)
        ? 'shell.serviceDesk'
        : labels[route.nav];
    const items = groups.get(label) ?? [];
    items.push(route);
    groups.set(label, items);
  }
  return Array.from(groups, ([label, items]) => ({ label, items })).filter(
    ({ items }) => items.length,
  );
}
