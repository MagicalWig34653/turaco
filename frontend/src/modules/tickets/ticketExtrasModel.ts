import type { Change } from '../changes/types';
import type { TicketHistoryEntry } from './types';

/** Pure logic for the location correction, @mentions and Change links of a Ticket. */

export const maxMentions = 10;

export type Mentioned = { id: string; label: string };

/** Adds a person once; the server accepts at most ten mentions per note. */
export function addMention(list: readonly Mentioned[], person: Mentioned): Mentioned[] {
  if (list.some((entry) => entry.id === person.id) || list.length >= maxMentions) {
    return [...list];
  }
  return [...list, person];
}

export function removeMention(list: readonly Mentioned[], id: string): Mentioned[] {
  return list.filter((entry) => entry.id !== id);
}

/** The ids to send; mentions only travel with an internal note. */
export function mentionIds(list: readonly Mentioned[], internal: boolean): string[] {
  return internal ? list.map((entry) => entry.id) : [];
}

/** The location still comes from the person's profile until someone corrected it. */
export function locationFromProfile(history: readonly TicketHistoryEntry[] | undefined): boolean {
  return !(history ?? []).some((entry) => entry.kind === 'location_changed');
}

/** Ids of locations named in history entries, so their names can be loaded once. */
export function historyLocationIds(history: readonly TicketHistoryEntry[] | undefined): string[] {
  const ids = new Set<string>();
  for (const entry of history ?? []) {
    if (entry.kind !== 'location_changed') continue;
    if (entry.fromLocationId) ids.add(entry.fromLocationId);
    if (entry.toLocationId) ids.add(entry.toLocationId);
  }
  return [...ids];
}

/** Changes the user can still link: matches the text (reference or title) and is not linked yet. */
export function changeCandidates(
  changes: readonly Pick<Change, 'id' | 'reference' | 'title' | 'status'>[],
  query: string,
  linkedIds: readonly string[],
): Pick<Change, 'id' | 'reference' | 'title' | 'status'>[] {
  const needle = query.trim().toLowerCase();
  return changes.filter(
    (change) =>
      !linkedIds.includes(change.id) &&
      (needle === '' ||
        change.reference.toLowerCase().includes(needle) ||
        change.title.toLowerCase().includes(needle)),
  );
}
