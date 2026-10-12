import { stateLink } from '../modules/sla/slaModel';
import { emptyState, serialize, type Node } from '../platform/ui/query/filterModel';

const openTicketStatuses = ['new', 'open', 'in_progress', 'waiting'];
const openTaskStatuses = ['open', 'in_progress', 'blocked'];

const condition = (field: string, op: string, value?: unknown): Node => ({
  type: 'condition',
  field,
  op,
  ...(value === undefined ? {} : { value }),
});
const and = (...children: Node[]): Node => ({ type: 'group', logic: 'and', children });

/** The list URL that shows exactly the records behind a number (the same `query` parameter the lists read). */
const listLink = (path: string, root: Node) =>
  `${path}?query=${encodeURIComponent(serialize({ ...emptyState(), filter: { v: 1, root } }))}`;

const openTickets = condition('status', 'in', openTicketStatuses);
const openTasks = condition('status', 'in', openTaskStatuses);

/** Every number of the Ticket part of the manager overview opens the Ticket list filtered the same way. */
export const ticketLinks = {
  open: () => listLink('/service-desk', and(openTickets)),
  unassigned: () => listLink('/service-desk', and(openTickets, condition('assignee', 'is_empty'))),
  waiting: () => listLink('/service-desk', and(condition('status', 'equals', 'waiting'))),
  queueOpen: (queueId: string) =>
    listLink('/service-desk', and(openTickets, condition('queue', 'equals', queueId))),
  queueUnassigned: (queueId: string) =>
    listLink(
      '/service-desk',
      and(openTickets, condition('queue', 'equals', queueId), condition('assignee', 'is_empty')),
    ),
  queueWaiting: (queueId: string) =>
    listLink(
      '/service-desk',
      and(condition('status', 'equals', 'waiting'), condition('queue', 'equals', queueId)),
    ),
  assigneeOpen: (userId: string) =>
    listLink('/service-desk', and(openTickets, condition('assignee', 'equals', userId))),
  // SLA numbers (only while the sla switch is on): open Tickets by the derived sla_state, never per assignee.
  atRisk: (queueId?: string) => stateLink(['at_risk'], queueId),
  breached: (queueId?: string) => stateLink(['breached'], queueId),
};

/** The same for Tasks; `dueBefore` is the time the overview was generated, so the list matches the count. */
export const taskLinks = {
  open: () => listLink('/tasks', and(openTasks)),
  overdue: (dueBefore: string) =>
    listLink('/tasks', and(openTasks, condition('due_at', 'before', dueBefore))),
  blocked: () => listLink('/tasks', and(condition('status', 'equals', 'blocked'))),
  unowned: () =>
    listLink(
      '/tasks',
      and(
        openTasks,
        condition('assigned_user', 'is_empty'),
        condition('assigned_team', 'is_empty'),
      ),
    ),
  user: (userId: string) =>
    listLink('/tasks', and(openTasks, condition('assigned_user', 'equals', userId))),
  team: (teamId: string) =>
    listLink('/tasks', and(openTasks, condition('assigned_team', 'equals', teamId))),
};

/** Whole days between an ISO time and now, never negative. */
export function ageInDays(iso: string | null | undefined, now: Date): number {
  if (!iso) return 0;
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return 0;
  return Math.max(0, Math.floor((now.getTime() - then) / 86_400_000));
}
