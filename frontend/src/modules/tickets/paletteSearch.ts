import { api, ApiError, isAbortError } from '../../platform/api/client';
import type { PaletteCommand } from '../../platform/ui/shell/paletteCommands';
import { ticketsApi } from './api';
import type { Ticket } from './types';

/** Ticket results for the command palette: reference or earlier number first, then titles. */

import { looksLikeReference, minTitleQuery, normalizeReference } from './ticketLookup';

export {
  looksLikeReference,
  minTitleQuery,
  normalizeReference,
  referenceQuery,
} from './ticketLookup';
export const resultLimit = 5;

type Hit = { ticketId: string; reference: string; alias: boolean };

export type TicketLabels = {
  /** "HR-0003 (earlier number of TKT-000012)" for an alias hit. */
  alias: (old: string, current: string) => string;
};

const ticketPath = (id: string) => `/support/${encodeURIComponent(id)}`;

/** Pure mapping of lookups to palette entries; duplicates by ticket are dropped, reference hits lead. */
export function ticketCommands(
  lookup: { query: string; hit: Hit | undefined },
  rows: readonly Pick<Ticket, 'id' | 'reference' | 'title'>[],
  labels: TicketLabels,
): PaletteCommand[] {
  const out: PaletteCommand[] = [];
  const seen = new Set<string>();
  if (lookup.hit) {
    seen.add(lookup.hit.ticketId);
    const old = normalizeReference(lookup.query);
    out.push({
      id: `ticket-${lookup.hit.ticketId}`,
      label: lookup.hit.alias ? labels.alias(old, lookup.hit.reference) : lookup.hit.reference,
      path: ticketPath(lookup.hit.ticketId),
      keywords: `${old} ${lookup.hit.reference}`,
      group: 'tickets',
      top: true,
    });
  }
  for (const row of rows) {
    if (seen.has(row.id)) continue;
    seen.add(row.id);
    out.push({
      id: `ticket-${row.id}`,
      label: `${row.reference} · ${row.title}`,
      path: ticketPath(row.id),
      keywords: `${row.reference} ${row.title}`,
      group: 'tickets',
    });
  }
  return out;
}

export type TicketSearchResult = { items: PaletteCommand[]; unavailable: boolean };

/**
 * Looks a query up as a ticket reference (current or earlier number) and as free text over
 * the caller's own visible tickets through the query endpoint. A 404 for the reference is a
 * normal "no such ticket"; any other failure marks the search as unavailable instead of
 * pretending there are no results.
 */
export async function searchTickets(
  query: string,
  signal: AbortSignal,
  labels: TicketLabels,
): Promise<TicketSearchResult> {
  const text = query.trim();
  let unavailable = false;
  const lookup = looksLikeReference(text)
    ? ticketsApi.byReference(normalizeReference(text), signal).then(
        (hit): Hit | undefined => hit,
        (cause: unknown) => {
          if (isAbortError(cause)) throw cause;
          if (!(cause instanceof ApiError && cause.status === 404)) unavailable = true;
          return undefined;
        },
      )
    : Promise.resolve(undefined);
  const titles =
    text.length >= minTitleQuery
      ? api
          .post<{ items: Ticket[] }>(
            '/tickets/query',
            { filter: { v: 1 }, search: text, limit: resultLimit },
            { signal },
          )
          .then(
            (page) => page.items,
            (cause: unknown) => {
              if (isAbortError(cause)) throw cause;
              unavailable = true;
              return [] as Ticket[];
            },
          )
      : Promise.resolve([] as Ticket[]);
  const [hit, rows] = await Promise.all([lookup, titles]);
  return { items: ticketCommands({ query: text, hit }, rows, labels), unavailable };
}
