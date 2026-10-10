import { appRoutes } from '../../app/routes';
import { en } from '../../platform/i18n/messages.en';
import type { MessageKey } from '../../platform/i18n/i18n';
import { matchRoute } from '../../platform/router/routing';
import type { SetupChecklist, SetupItem, HealthResult, HealthStatus } from './types';

export type Tone = 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'unknown';

export const healthStatuses: readonly HealthStatus[] = [
  'ok',
  'stale',
  'fake',
  'not_configured',
  'disabled',
  'failing',
  'unknown',
];

/**
 * Status meaning only, never brand colors. Everything but OK is visibly distinct: a fake adapter,
 * a missing configuration and a stale result are warnings, a failing check is a danger, a check
 * that is off on purpose is neutral and one never observed is unknown.
 */
export function statusTone(status: HealthStatus): Tone {
  switch (status) {
    case 'ok':
      return 'success';
    case 'stale':
    case 'fake':
    case 'not_configured':
      return 'warning';
    case 'failing':
      return 'danger';
    case 'disabled':
      return 'neutral';
    default:
      return 'unknown';
  }
}

/** Attention covers the statuses that need an administrator (the server counts the same ones). */
export function needsAttention(status: HealthStatus): boolean {
  return status === 'failing' || status === 'stale' || status === 'not_configured';
}

export function statusKey(status: string): MessageKey {
  return messageOr(`health.status.${status}`, 'health.status.unknown');
}

/** A catalog key if it exists, otherwise the fallback (unknown check keys, codes or setup items). */
export function messageOr(key: string, fallback: MessageKey): MessageKey {
  return Object.hasOwn(en, key) ? (key as MessageKey) : fallback;
}

/** Counts of results per status, in the fixed display order, zero counts omitted. */
export function statusCounts(items: readonly Pick<HealthResult, 'status'>[]) {
  return healthStatuses
    .map((status) => ({ status, count: items.filter((item) => item.status === status).length }))
    .filter((entry) => entry.count > 0);
}

/** Server routes that differ from the frontend path (the contract predates the screens). */
const routeAliases: Record<string, string> = {
  '/admin/people': '/admin/users',
  '/admin/directory': '/admin/directory-sync',
};

/** A server-supplied route as an in-app link, or undefined when this client has no such screen. */
export function resolveAppRoute(route: string | undefined): string | undefined {
  if (!route || !route.startsWith('/') || route.startsWith('//')) return undefined;
  const target = routeAliases[route] ?? route;
  return matchRoute(appRoutes, target) ? target : undefined;
}

/** Setup items in the server's order. */
export function orderedItems(list: Pick<SetupChecklist, 'items'> | undefined): SetupItem[] {
  return [...(list?.items ?? [])].sort((a, b) => a.order - b.order);
}

/** Items that are finished for the checklist: derived done, or deliberately skipped or confirmed. */
export function settledCount(list: Pick<SetupChecklist, 'open' | 'total'>): number {
  return Math.max(0, list.total - list.open);
}

export function setupTone(item: Pick<SetupItem, 'state' | 'attention'>): Tone {
  if (item.attention) return 'warning';
  switch (item.state) {
    case 'done':
    case 'confirmed':
      return 'success';
    case 'skipped':
      return 'neutral';
    default:
      return 'info';
  }
}

/** Skip is offered for open items, confirm only where the server allows it (the modules item). */
export const confirmableKeys: readonly string[] = ['modules'];

export type SetupActions = { skip: boolean; confirm: boolean; clear: boolean };

export function setupActions(item: SetupItem, canWrite: boolean): SetupActions {
  if (!canWrite) return { skip: false, confirm: false, clear: false };
  const marked = item.state === 'skipped' || item.state === 'confirmed';
  return {
    skip: item.state === 'todo',
    confirm: item.state === 'todo' && confirmableKeys.includes(item.key),
    clear: marked,
  };
}

/** The version of an existing mark, which the server requires; absent for a first mark. */
export function expectedVersion(item: Pick<SetupItem, 'version'>): number | undefined {
  return item.version;
}

/** Human text for a check's machine error code; every `*_unreadable` code shares one message. */
export function errorTextKey(code: string): MessageKey {
  if (code.endsWith('_unreadable')) return 'health.error.unreadable';
  return messageOr(`health.error.${code}`, 'health.error.generic');
}
