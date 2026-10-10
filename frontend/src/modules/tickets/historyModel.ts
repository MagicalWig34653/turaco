import type { MessageKey } from '../../platform/i18n/i18n';
import { waitingReasons, type TicketComment, type TicketHistoryEntry } from './types';

/** Pure timeline logic: comments and history entries in one chronological list. */

export type TimelineItem =
  | { type: 'comment'; at: string; comment: TicketComment }
  | { type: 'history'; at: string; entry: TicketHistoryEntry };

const time = (value: string) => {
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? 0 : parsed;
};

/** "created" is the description event itself, so it is not repeated. Unknown kinds are kept. */
export function visibleHistory(entries: readonly TicketHistoryEntry[] | undefined) {
  return (entries ?? []).filter((entry) => entry.kind !== 'created');
}

/** Oldest first; at equal times comments keep their order and follow the change that preceded them. */
export function mergeTimeline(
  comments: readonly TicketComment[],
  entries: readonly TicketHistoryEntry[] | undefined,
): TimelineItem[] {
  const items: TimelineItem[] = [
    ...visibleHistory(entries).map((entry): TimelineItem => ({
      type: 'history',
      at: entry.at,
      entry,
    })),
    ...comments.map((comment): TimelineItem => ({
      type: 'comment',
      at: comment.createdAt,
      comment,
    })),
  ];
  return items
    .map((item, index) => ({ item, index }))
    .sort((a, b) => time(a.item.at) - time(b.item.at) || a.index - b.index)
    .map(({ item }) => item);
}

export type HistoryText = {
  key: MessageKey;
  params: Record<string, string>;
};

/**
 * Message key and parameters for an entry. `name` resolves ids to display names and must return a
 * neutral label for unknown ids; `label` translates enumerations (status, priority).
 */
export function describeHistory(
  entry: TicketHistoryEntry,
  name: (id: string | null | undefined) => string,
  label: (kind: 'status' | 'priority', value: string) => string,
): HistoryText {
  const to = name(entry.toUserId);
  const from = name(entry.fromUserId);
  switch (entry.kind) {
    case 'assigned':
      return { key: 'ticketHistory.assigned', params: { to } };
    case 'unassigned':
      return { key: 'ticketHistory.unassigned', params: { from } };
    case 'reassigned':
      return { key: 'ticketHistory.reassigned', params: { from, to } };
    case 'team_routed':
      return {
        key: 'ticketHistory.teamRouted',
        params: {
          team: name(entry.toTeamId),
          from: entry.fromTeamId ? name(entry.fromTeamId) : '',
        },
      };
    case 'status_changed':
      return {
        key: 'ticketHistory.statusChanged',
        params: {
          from: entry.fromStatus ? label('status', entry.fromStatus) : '',
          to: entry.toStatus ? label('status', entry.toStatus) : '',
        },
      };
    case 'priority_changed':
      return {
        key: 'ticketHistory.priorityChanged',
        params: {
          from: entry.fromPriority ? label('priority', entry.fromPriority) : '',
          to: entry.toPriority ? label('priority', entry.toPriority) : '',
        },
      };
    case 'queue_moved':
      return { key: 'ticketHistory.queueMoved', params: {} };
    case 'location_changed': {
      const toLocation = entry.toLocationId ? name(entry.toLocationId) : '';
      const fromLocation = entry.fromLocationId ? name(entry.fromLocationId) : '';
      if (!toLocation)
        return { key: 'ticketHistory.locationCleared', params: { from: fromLocation } };
      if (!fromLocation) return { key: 'ticketHistory.locationSet', params: { to: toLocation } };
      return {
        key: 'ticketHistory.locationChanged',
        params: { from: fromLocation, to: toLocation },
      };
    }
    default:
      return { key: 'ticketHistory.other', params: {} };
  }
}

/** Entries that change who handles the Ticket; they are highlighted in the thread. */
export const isAssignmentEntry = (entry: Pick<TicketHistoryEntry, 'kind'>) =>
  entry.kind === 'assigned' ||
  entry.kind === 'unassigned' ||
  entry.kind === 'reassigned' ||
  entry.kind === 'team_routed';

/** The operation that caused a change, when it explains an assignment nobody made by hand. */
export function viaKey(via: string | null | undefined): MessageKey | undefined {
  switch (via) {
    case 'start':
      return 'ticketHistory.via.start';
    case 'queue_move':
      return 'ticketHistory.via.queueMove';
    case 'reopen':
      return 'ticketHistory.via.reopen';
    default:
      return undefined;
  }
}

const moveReasons: readonly string[] = ['misrouted', 'different_skill', 'reorganization', 'other'];

export type HistoryReason =
  | { kind: 'waiting'; key: MessageKey }
  | { kind: 'move'; key: MessageKey }
  | { kind: 'text'; text: string };

/** Known reason codes map to their localized label; any other value is free text and shown as is. */
export function historyReason(
  entry: Pick<TicketHistoryEntry, 'reason' | 'kind' | 'toStatus'>,
): HistoryReason | undefined {
  const reason = entry.reason?.trim();
  if (!reason) return undefined;
  if ((waitingReasons as readonly string[]).includes(reason)) {
    return { kind: 'waiting', key: `tickets.waiting.${reason}` as MessageKey };
  }
  if (moveReasons.includes(reason) && entry.kind !== 'status_changed') {
    return { kind: 'move', key: `tickets.move.reason.${reason}` as MessageKey };
  }
  return { kind: 'text', text: reason };
}
