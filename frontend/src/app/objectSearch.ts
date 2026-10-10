import { api } from '../platform/api/client';
import type { PaletteCommand, PaletteSearch } from '../platform/ui/shell/paletteCommands';
import { minObjectQuery } from '../platform/ui/shell/paletteCommands';

/**
 * Record search for the command palette: one request to the platform search (`GET /search`). Every module
 * authorizes its own hits on the server, so the client neither knows nor guesses what the caller may read; a
 * source that did not answer in time is reported in `unavailable`.
 */

export type SearchHit = {
  type: string;
  id: string;
  reference?: string;
  title: string;
  subtitle?: string;
  exact?: boolean;
};

export type SearchResponse = { items: SearchHit[]; unavailable: string[] };

/** Server result type to palette group (heading key `shell.group.<group>`) and the detail route of a record. */
const sources: Record<string, { group: string; path: (id: string) => string }> = {
  ticket: { group: 'tickets', path: (id) => `/support/${id}` },
  problem: { group: 'problems', path: (id) => `/problems/${id}` },
  major_incident: { group: 'incidents', path: (id) => `/incidents/${id}` },
  change: { group: 'changes', path: (id) => `/changes/${id}` },
  request: { group: 'requests', path: (id) => `/requests/${id}` },
  knowledge: { group: 'knowledge', path: (id) => `/knowledge/${id}` },
  asset: { group: 'assets', path: (id) => `/assets/${id}` },
  device: { group: 'devices', path: (id) => `/devices/${id}` },
  user: { group: 'people', path: (id) => `/endpoints/users/${id}` },
};

/** Groups in the order the palette lists them. */
export const objectSearchGroups = Object.values(sources).map((source) => source.group);

function label(hit: SearchHit): string {
  const head = hit.reference ? `${hit.reference} · ${hit.title}` : hit.title;
  return hit.subtitle && !hit.reference ? `${head} · ${hit.subtitle}` : head;
}

/** Pure mapping of a search response to palette entries; unknown types are ignored, exact hits lead. */
export function searchCommands(response: SearchResponse): PaletteCommand[] {
  const out: PaletteCommand[] = [];
  for (const hit of response.items) {
    const source = sources[hit.type];
    if (!source) continue;
    const text = label(hit);
    out.push({
      id: `${source.group}-${hit.id}`,
      label: text,
      path: source.path(encodeURIComponent(hit.id)),
      keywords: text,
      group: source.group,
      ...(hit.exact ? { top: true } : {}),
    });
  }
  // Keep the groups together in a stable order even when the server interleaves them.
  return out.sort(
    (left, right) =>
      objectSearchGroups.indexOf(left.group ?? '') - objectSearchGroups.indexOf(right.group ?? ''),
  );
}

export function buildObjectSearch(): PaletteSearch {
  return async (query, signal) => {
    const text = query.trim();
    if ([...text].length < minObjectQuery) return { items: [], unavailable: false, tooShort: true };
    const response = await api.get<SearchResponse>('/search', { signal, query: { q: text } });
    return { items: searchCommands(response), unavailable: response.unavailable.length > 0 };
  };
}
