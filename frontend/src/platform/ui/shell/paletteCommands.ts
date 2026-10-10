import type { ModuleEnabled } from '../../modules/model';
import type { AppRoute } from '../../../app/routes';
import { canViewRoute } from '../../../app/routes';
import type { CanFn } from '../../session/permissions';

/** Navigation is today's source; object search providers can contribute commands later. */
export type PaletteCommand = {
  id: string;
  label: string;
  path: string;
  keywords?: string;
  action?: () => void;
  /** Object results (for example `tickets`) are listed under their own heading. */
  group?: string;
  /** An exact hit (a ticket reference) that should lead the list. */
  top?: boolean;
};

/** An object search provider: results for a query, aborted when the query changes. */
export type PaletteSearch = (
  query: string,
  signal: AbortSignal,
) => Promise<{
  items: PaletteCommand[];
  /** Sources that failed (as opposed to having no results). */
  unavailable: boolean;
  /** The query is too short to search objects; nothing was requested. */
  tooShort?: boolean;
}>;

/** Record search starts here; shorter text only filters pages (the server refuses shorter search text). */
export const minObjectQuery = 2;

/** Navigation results first, object results after them; exact object hits lead everything. */
export function arrangeResults(
  navigation: readonly PaletteCommand[],
  objects: readonly PaletteCommand[],
): PaletteCommand[] {
  return [
    ...objects.filter((item) => item.top),
    ...navigation,
    ...objects.filter((item) => !item.top),
  ];
}

export function navigationCommands(
  routes: readonly AppRoute[],
  can: CanFn,
  label: (route: AppRoute) => string,
  enabled?: ModuleEnabled,
): PaletteCommand[] {
  return routes
    .filter((route) => route.nav && canViewRoute(can, route, enabled))
    .map((route) => ({ id: route.id, label: label(route), path: route.pattern }));
}

function fuzzyScore(value: string, query: string): number {
  const haystack = value.toLocaleLowerCase();
  const needle = query.toLocaleLowerCase().trim();
  if (!needle) return 0;
  const exact = haystack.indexOf(needle);
  if (exact >= 0) return exact;
  let cursor = 0;
  let gap = 0;
  for (const letter of needle) {
    const next = haystack.indexOf(letter, cursor);
    if (next < 0) return Number.POSITIVE_INFINITY;
    gap += next - cursor;
    cursor = next + 1;
  }
  return haystack.length + gap;
}

export function filterCommands(
  commands: readonly PaletteCommand[],
  query: string,
): PaletteCommand[] {
  if (!query.trim()) return [...commands];
  return commands
    .map((command) => ({
      command,
      score: fuzzyScore(`${command.label} ${command.keywords ?? ''}`, query),
    }))
    .filter(({ score }) => Number.isFinite(score))
    .sort((a, b) => a.score - b.score || a.command.label.localeCompare(b.command.label))
    .map(({ command }) => command);
}

export function moveCommandSelection(current: number, length: number, key: string): number {
  if (length === 0) return -1;
  if (key === 'Home') return 0;
  if (key === 'End') return length - 1;
  if (key === 'ArrowDown') return (current + 1 + length) % length;
  if (key === 'ArrowUp') return (current - 1 + length) % length;
  return current;
}
