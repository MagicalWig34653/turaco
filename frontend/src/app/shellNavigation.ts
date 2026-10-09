import type { ModuleEnabled } from '../platform/modules/model';
import type { MessageKey } from '../platform/i18n/i18n';
import type { CanFn } from '../platform/session/permissions';
import { appRoutes, canViewRoute, type AppRoute, type NavGroup, type RouteId } from './routes';

/** Sidebar section keys double as the persisted collapsed-state ids (`PUT /me/sidebar-state`). */
export const sectionKeys = [
  'work',
  'service_desk',
  'assets',
  'infrastructure',
  'security',
  'knowledge',
  'people',
  'admin',
] as const;
export type SectionKey = (typeof sectionKeys)[number];

const labels: Record<SectionKey, MessageKey> = {
  work: 'sidebar.section.work',
  service_desk: 'sidebar.section.service_desk',
  assets: 'sidebar.section.assets',
  infrastructure: 'sidebar.section.infrastructure',
  security: 'sidebar.section.security',
  knowledge: 'sidebar.section.knowledge',
  people: 'sidebar.section.people',
  admin: 'sidebar.section.admin',
};

/** Explicit placement for routes whose registry group does not say where they belong. */
const placement: Partial<Record<RouteId, SectionKey>> = {
  home: 'work',
  myWork: 'work',
  tasks: 'work',
  approvals: 'work',
  notifications: 'work',
  briefing: 'work',
  myTickets: 'service_desk',
  ticketQueue: 'service_desk',
  ticketQueues: 'service_desk',
  catalog: 'service_desk',
  requests: 'service_desk',
  incidents: 'service_desk',
  problems: 'service_desk',
  knowledge: 'knowledge',
  runbooks: 'knowledge',
  me: 'people',
  presenceMine: 'people',
  presenceTeam: 'people',
  myAssets: 'assets',
  myChanges: 'infrastructure',
  myInitiatives: 'infrastructure',
};

/** Fallback by the registry's own navigation group. */
const byGroup: Record<NavGroup, SectionKey> = {
  main: 'work',
  logistics: 'assets',
  endpoints: 'assets',
  infrastructure: 'infrastructure',
  services: 'infrastructure',
  changes: 'infrastructure',
  planning: 'infrastructure',
  security: 'security',
  admin: 'admin',
};

export function sectionOf(route: Pick<AppRoute, 'id' | 'nav'>): SectionKey {
  return placement[route.id] ?? byGroup[route.nav ?? 'main'];
}

export type NavSection = { key: SectionKey; label: MessageKey; items: AppRoute[] };

/** Presentation sections reuse the route registry's visibility rules (permissions and module status) unchanged. */
export function shellNavigation(can: CanFn, enabled?: ModuleEnabled): NavSection[] {
  const sections = new Map<SectionKey, AppRoute[]>(sectionKeys.map((key) => [key, []]));
  for (const route of appRoutes) {
    if (!route.nav || !canViewRoute(can, route, enabled)) continue;
    sections.get(sectionOf(route))?.push(route);
  }
  return sectionKeys
    .map((key) => ({ key, label: labels[key], items: sections.get(key) ?? [] }))
    .filter(({ items }) => items.length);
}
