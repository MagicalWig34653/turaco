import { api, ApiError, isAbortError } from '../../platform/api/client';
import { ticketsApi } from './api';
import type { Ticket } from './types';

/**
 * Ticket lookup for pickers and the command palette: a reference (current or earlier number) is
 * resolved exactly, free text goes through the ticket query endpoint, which rejects text shorter
 * than three characters. Short text is answered with a hint and never sent.
 */

/** A reference as people type it: a prefix, a hyphen and a number ("tkt-12", "HR-0003"). */
export const referenceQuery = /^[a-z][a-z0-9]{1,7}-\d{1,9}$/i;
/** The query endpoint refuses shorter search text (`query.invalid_filter`). */
export const minTitleQuery = 3;

export const normalizeReference = (query: string): string => query.trim().toUpperCase();
export const looksLikeReference = (query: string): boolean => referenceQuery.test(query.trim());

export type TicketHit = {
  id: string;
  reference: string;
  title: string;
  status?: string;
  /** The typed number was an earlier reference of this Ticket. */
  alias?: boolean;
};

export type LookupPlan = {
  reference: boolean;
  text: boolean;
  /** Non-empty text that can neither be a reference nor be searched yet. */
  tooShort: boolean;
};

export function planLookup(raw: string): LookupPlan {
  const text = raw.trim();
  const reference = looksLikeReference(text);
  const searchable = text.length >= minTitleQuery;
  return { reference, text: searchable, tooShort: text !== '' && !reference && !searchable };
}

/** Reference hit first, then text hits, without repeating a Ticket. */
export function mergeHits(
  reference: TicketHit | undefined,
  rows: readonly Pick<Ticket, 'id' | 'reference' | 'title' | 'status'>[],
  limit: number,
): TicketHit[] {
  const out: TicketHit[] = [];
  const seen = new Set<string>();
  const push = (hit: TicketHit) => {
    if (seen.has(hit.id) || out.length >= limit) return;
    seen.add(hit.id);
    out.push(hit);
  };
  if (reference) push(reference);
  for (const row of rows)
    push({ id: row.id, reference: row.reference, title: row.title, status: row.status });
  return out;
}

export type TicketLookupResult = {
  hits: TicketHit[];
  tooShort: boolean;
  /** The search failed (not "no results"). */
  unavailable: boolean;
};

export async function lookupTickets(
  raw: string,
  signal: AbortSignal,
  limit = 8,
): Promise<TicketLookupResult> {
  const text = raw.trim();
  const plan = planLookup(text);
  if (!plan.reference && !plan.text)
    return { hits: [], tooShort: plan.tooShort, unavailable: false };
  let unavailable = false;
  const reference = plan.reference
    ? ticketsApi.byReference(normalizeReference(text), signal).then(
        async (found): Promise<TicketHit | undefined> => {
          const detail = await ticketsApi.get(found.ticketId, signal).catch((cause: unknown) => {
            if (isAbortError(cause)) throw cause;
            return undefined;
          });
          return {
            id: found.ticketId,
            reference: found.reference,
            title: detail?.title ?? '',
            ...(detail ? { status: detail.status } : {}),
            alias: found.alias,
          };
        },
        (cause: unknown) => {
          if (isAbortError(cause)) throw cause;
          if (!(cause instanceof ApiError && cause.status === 404)) unavailable = true;
          return undefined;
        },
      )
    : Promise.resolve(undefined);
  const rows = plan.text
    ? api
        .post<{ items: Ticket[] }>(
          '/tickets/query',
          { filter: { v: 1 }, search: text, limit },
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
  const [referenceHit, found] = await Promise.all([reference, rows]);
  return { hits: mergeHits(referenceHit, found, limit), tooShort: false, unavailable };
}
