import { api, isAbortError } from '../platform/api/client';
import type { PaletteCommand, PaletteSearch } from '../platform/ui/shell/paletteCommands';
import { minObjectQuery } from '../platform/ui/shell/paletteCommands';
import { searchTickets, type TicketLabels } from '../modules/tickets/paletteSearch';

/**
 * Object search for the command palette across the sources the caller may use. Every source is
 * authorized by its own endpoint; the permission checks here only avoid pointless requests. A
 * failing source marks the result "unavailable" without hiding the others.
 */

export type SearchSources = {
  tickets: boolean;
  problems: boolean;
  incidents: boolean;
  knowledge: boolean;
  devices: boolean;
  people: boolean;
};

type Can = (permission: string) => boolean;
type Enabled = (module: string) => boolean;

/** Which sources apply, from permissions and module switches. */
export function searchSources(can: Can, enabled: Enabled): SearchSources {
  const desk = enabled('servicedesk');
  return {
    tickets: desk,
    problems: desk && (can('tickets.view') || can('tickets.manage') || can('problems.manage')),
    incidents: desk,
    knowledge: enabled('knowledge'),
    devices: enabled('endpoints') && (can('endpoints.view') || can('endpoints.manage')),
    people:
      enabled('endpoints') &&
      can('organization.view') &&
      can('organization.directory.view') &&
      (can('endpoint.management.view') || can('endpoints.manage')),
  };
}

export const perSourceLimit = 4;

type Row = { id: string; reference?: string; title?: string; name?: string; displayName?: string };

/** Case-insensitive match on reference or title, for lists the server cannot search. */
export function matchRows<T extends { reference: string; title: string }>(
  rows: readonly T[],
  query: string,
  limit = perSourceLimit,
): T[] {
  const needle = query.trim().toLocaleLowerCase();
  if (!needle) return [];
  return rows
    .filter(
      (row) =>
        row.reference.toLocaleLowerCase().includes(needle) ||
        row.title.toLocaleLowerCase().includes(needle),
    )
    .slice(0, limit);
}

const command = (group: string, row: Row, path: string, label: string): PaletteCommand => ({
  id: `${group}-${row.id}`,
  label,
  path,
  keywords: label,
  group,
});

export const objectSearchGroups = [
  'tickets',
  'problems',
  'incidents',
  'knowledge',
  'devices',
  'people',
] as const;

export function buildObjectSearch(
  sources: SearchSources,
  labels: TicketLabels,
): PaletteSearch | undefined {
  if (!Object.values(sources).some(Boolean)) return undefined;
  return async (query, signal) => {
    const text = query.trim();
    if ([...text].length < minObjectQuery) return { items: [], unavailable: false, tooShort: true };
    let unavailable = false;
    const guard = <T>(promise: Promise<T>, fallback: T): Promise<T> =>
      promise.catch((cause: unknown) => {
        if (isAbortError(cause)) throw cause;
        unavailable = true;
        return fallback;
      });
    const enc = encodeURIComponent;
    const [tickets, problems, incidents, knowledge, devices, people] = await Promise.all([
      sources.tickets
        ? guard(
            searchTickets(text, signal, labels).then((found) => {
              if (found.unavailable) unavailable = true;
              return found.items;
            }),
            [] as PaletteCommand[],
          )
        : [],
      sources.problems
        ? guard(
            api
              .get<{ items: Array<Row & { reference: string; title: string }> }>('/problems', {
                signal,
                query: { limit: 100 },
              })
              .then((page) =>
                matchRows(
                  page.items as Array<
                    Required<Pick<Row, 'id'>> & { reference: string; title: string }
                  >,
                  text,
                ).map((row) =>
                  command(
                    'problems',
                    row,
                    `/problems/${enc(row.id)}`,
                    `${row.reference} · ${row.title}`,
                  ),
                ),
              ),
            [] as PaletteCommand[],
          )
        : [],
      sources.incidents
        ? guard(
            api
              .get<{ items: Array<{ id: string; reference: string; title: string }> }>(
                '/major-incidents',
                { signal, query: { limit: 100 } },
              )
              .then((page) =>
                matchRows(page.items, text).map((row) =>
                  command(
                    'incidents',
                    row,
                    `/incidents/${enc(row.id)}`,
                    `${row.reference} · ${row.title}`,
                  ),
                ),
              ),
            [] as PaletteCommand[],
          )
        : [],
      sources.knowledge
        ? guard(
            api
              .get<{ items: Array<{ id: string; reference: string; title: string }> }>(
                '/knowledge-articles',
                { signal, query: { q: text, limit: perSourceLimit } },
              )
              .then((page) =>
                page.items.map((row) =>
                  command(
                    'knowledge',
                    row,
                    `/knowledge/${enc(row.id)}`,
                    `${row.reference} · ${row.title}`,
                  ),
                ),
              ),
            [] as PaletteCommand[],
          )
        : [],
      sources.devices
        ? guard(
            api
              .get<{ items: Array<{ id: string; name: string; serialNumber: string | null }> }>(
                '/devices',
                { signal, query: { q: text, limit: perSourceLimit } },
              )
              .then((page) =>
                page.items.map((row) =>
                  command(
                    'devices',
                    row,
                    `/devices/${enc(row.id)}`,
                    row.serialNumber ? `${row.name} · ${row.serialNumber}` : row.name,
                  ),
                ),
              ),
            [] as PaletteCommand[],
          )
        : [],
      sources.people
        ? guard(
            api
              .get<{
                items: Array<{
                  id: string;
                  displayName: string;
                  primaryEmail?: string | null;
                  status?: string;
                }>;
              }>('/users', { signal, query: { q: text, limit: perSourceLimit } })
              .then((page) =>
                page.items
                  .filter((row) => row.status === undefined || row.status === 'active')
                  .map((row) =>
                    command(
                      'people',
                      row,
                      `/endpoints/users/${enc(row.id)}`,
                      row.primaryEmail
                        ? `${row.displayName} · ${row.primaryEmail}`
                        : row.displayName,
                    ),
                  ),
              ),
            [] as PaletteCommand[],
          )
        : [],
    ]);
    return {
      items: [...tickets, ...problems, ...incidents, ...knowledge, ...devices, ...people],
      unavailable,
    };
  };
}
