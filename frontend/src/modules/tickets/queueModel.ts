import type { Node } from '../../platform/ui/query/filterModel';
import type { QueueLevel, QueueSubjectType, Ticket, TicketQueue, TicketQueueGrant } from './types';

/** Pure Ticket Queue logic: validation, intake choice, labels and the grants matrix. */

export const queueKeyPattern = /^[a-z][a-z0-9-]{1,30}$/;
export const queuePrefixPattern = /^[A-Z][A-Z0-9]{1,7}$/;
export const queueNameMax = 80;
export const queueDescriptionMax = 500;
export const maxQueueGrants = 200;

export type QueueFormIssue = 'keyInvalid' | 'prefixInvalid' | 'nameRequired' | 'nameTooLong';

/** Client-side mirror of the server's create rules; the server stays the authority. */
export function validateQueueForm(form: {
  key: string;
  prefix: string;
  name: string;
}): Partial<Record<'key' | 'prefix' | 'name', QueueFormIssue>> {
  const issues: Partial<Record<'key' | 'prefix' | 'name', QueueFormIssue>> = {};
  if (!queueKeyPattern.test(form.key)) issues.key = 'keyInvalid';
  if (!queuePrefixPattern.test(form.prefix)) issues.prefix = 'prefixInvalid';
  const name = form.name.trim();
  if (!name) issues.name = 'nameRequired';
  else if ([...name].length > queueNameMax) issues.name = 'nameTooLong';
  return issues;
}

/** A key proposal from a name: lower-case words joined by hyphens, starting with a letter. */
export function suggestKey(name: string): string {
  const slug = name
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^[^a-z]+/, '')
    .replace(/-+$/g, '')
    .slice(0, 31)
    .replace(/-+$/g, '');
  return slug;
}

/** A prefix proposal from a name: up to four upper-case letters or digits, starting with a letter. */
export function suggestPrefix(name: string): string {
  const letters = name
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, '')
    .replace(/^[^A-Z]+/, '');
  return letters.slice(0, 4);
}

/** The next number's look: prefix, dash and a zero-padded 1, for the preview in the create dialog. */
export function previewReference(prefix: string, padding: number): string {
  const width = Math.min(9, Math.max(1, Math.trunc(padding) || 1));
  return `${prefix || '…'}-${'1'.padStart(width, '0')}`;
}

// ---- Choosing a Queue when raising a Ticket ------------------------------------------------

export type IntakeChoice =
  /** Nothing to ask: the server routes to the intake Queue. */
  | { mode: 'default' }
  /** One Queue to raise into: used silently. */
  | { mode: 'single'; queue: TicketQueue }
  /** More than one: the person picks. */
  | { mode: 'choose'; queues: TicketQueue[]; initial: TicketQueue };

/** Queues in `for=create` order (the intake Queue first); archived ones are never offered. */
export function intakeChoice(queues: readonly TicketQueue[] | undefined): IntakeChoice {
  const usable = (queues ?? []).filter((queue) => queue.status === 'active' && queue.canCreate);
  const first = usable[0];
  if (!first) return { mode: 'default' };
  if (usable.length === 1) return { mode: 'single', queue: first };
  const initial = usable.find((queue) => queue.isDefault) ?? first;
  return { mode: 'choose', queues: usable, initial };
}

/** What a person sees as the name of a Queue: staff see the prefix too, employees the plain label. */
export function queueChoiceLabel(queue: TicketQueue, staff: boolean): string {
  if (!staff) return queue.publicLabel || queue.name;
  return `${queue.name} (${queue.prefix})`;
}

/** The Queue a Ticket is shown under: the Queue itself, else the neutral desk label. */
export function ticketQueueName(ticket: Pick<Ticket, 'queue' | 'queueLabel'>): string | undefined {
  return ticket.queue?.name || ticket.queueLabel || undefined;
}

/** Queues a Ticket can be moved to: active ones the caller can raise into, other than its own. */
export function moveTargets(
  queues: readonly TicketQueue[] | undefined,
  currentQueueId: string | undefined,
): TicketQueue[] {
  return (queues ?? []).filter(
    (queue) => queue.status === 'active' && queue.canCreate && queue.id !== currentQueueId,
  );
}

/** Tickets in these states cannot be moved (the server refuses; the UI does not offer it). */
export const unmovableStatuses: readonly string[] = ['resolved', 'closed', 'cancelled'];
export const canOfferMove = (ticket: Pick<Ticket, 'status'>): boolean =>
  !unmovableStatuses.includes(ticket.status);

/** "TKT-000012" shows as the new number; the move result carries it, aliases hold the old ones. */
export function aliasList(ticket: Pick<Ticket, 'aliases' | 'reference'>): string[] {
  // A ticket moved A -> B -> A repeats an alias; each one is listed once.
  return [
    ...new Set((ticket.aliases ?? []).filter((alias) => alias && alias !== ticket.reference)),
  ];
}

/** Extra list filter of the ticket queue: unassigned tickets and the affected person's Location. */
export function ticketExtraFilter(options: {
  unassigned: boolean;
  locationId: string;
}): Node | undefined {
  const children: Node[] = [];
  if (options.unassigned) children.push({ type: 'condition', field: 'assignee', op: 'is_empty' });
  if (options.locationId)
    children.push({
      type: 'condition',
      field: 'location',
      op: 'equals',
      value: options.locationId,
    });
  if (children.length === 0) return undefined;
  return children.length === 1 ? children[0] : { type: 'group', logic: 'and', children };
}

// ---- Grants matrix -------------------------------------------------------------------------

export type MatrixRow = {
  subjectType: QueueSubjectType;
  subjectId: string;
  /** Strongest cumulative level held: '' (none), view, work or manage. */
  access: '' | 'view' | 'work' | 'manage';
  create: boolean;
};

const accessRank = { '': 0, view: 1, work: 2, manage: 3 } as const;
const subjectKey = (row: Pick<MatrixRow, 'subjectType' | 'subjectId'>) =>
  `${row.subjectType}:${row.subjectId}`;

/** Folds grant rows into one matrix row per subject: the strongest cumulative level plus create. */
export function grantsToMatrix(grants: readonly TicketQueueGrant[] | undefined): MatrixRow[] {
  const rows = new Map<string, MatrixRow>();
  for (const grant of grants ?? []) {
    const key = subjectKey(grant);
    const row = rows.get(key) ?? {
      subjectType: grant.subjectType,
      subjectId: grant.subjectId,
      access: '' as MatrixRow['access'],
      create: false,
    };
    if (grant.level === 'create') row.create = true;
    else if (accessRank[grant.level] > accessRank[row.access]) row.access = grant.level;
    rows.set(key, row);
  }
  return [...rows.values()];
}

/** Grant rows to send: the access level (if any) and create (if set), nothing for empty rows. */
export function matrixToGrants(
  rows: readonly MatrixRow[],
): Array<{ subjectType: QueueSubjectType; subjectId: string; level: QueueLevel }> {
  return rows.flatMap((row) => {
    const out: Array<{ subjectType: QueueSubjectType; subjectId: string; level: QueueLevel }> = [];
    if (row.access)
      out.push({ subjectType: row.subjectType, subjectId: row.subjectId, level: row.access });
    if (row.create)
      out.push({ subjectType: row.subjectType, subjectId: row.subjectId, level: 'create' });
    return out;
  });
}

export function addSubject(
  rows: readonly MatrixRow[],
  subject: { subjectType: QueueSubjectType; subjectId: string },
): MatrixRow[] {
  if (rows.some((row) => subjectKey(row) === subjectKey(subject))) return [...rows];
  return [...rows, { ...subject, access: 'view', create: false }];
}

export function removeSubject(
  rows: readonly MatrixRow[],
  subject: Pick<MatrixRow, 'subjectType' | 'subjectId'>,
): MatrixRow[] {
  return rows.filter((row) => subjectKey(row) !== subjectKey(subject));
}

/** Turns "view" or "work" on or off. Work includes view, so switching view off also drops work. */
export function setAccess(
  rows: readonly MatrixRow[],
  subject: Pick<MatrixRow, 'subjectType' | 'subjectId'>,
  level: 'view' | 'work',
  on: boolean,
): MatrixRow[] {
  return rows.map((row) => {
    if (subjectKey(row) !== subjectKey(subject)) return row;
    if (on)
      return { ...row, access: accessRank[row.access] >= accessRank[level] ? row.access : level };
    // Switching "work" off keeps view; switching "view" off removes all access.
    return {
      ...row,
      access: level === 'work' ? (row.access === 'manage' ? 'manage' : 'view') : '',
    };
  });
}

export function setCreate(
  rows: readonly MatrixRow[],
  subject: Pick<MatrixRow, 'subjectType' | 'subjectId'>,
  on: boolean,
): MatrixRow[] {
  return rows.map((row) =>
    subjectKey(row) === subjectKey(subject) ? { ...row, create: on } : row,
  );
}

export type Abilities = {
  read: boolean;
  work: boolean;
  internalComments: boolean;
  create: boolean;
  moveOut: boolean;
  moveIn: boolean;
};

/**
 * What a matrix row allows. Work includes read, internal comments and moving a Ticket out of the
 * Queue; create (independent of read) allows raising Tickets and moving Tickets in.
 */
export function abilitiesOf(row: Pick<MatrixRow, 'access' | 'create'>): Abilities {
  const worker = row.access === 'work' || row.access === 'manage';
  return {
    read: row.access !== '',
    work: worker,
    internalComments: worker,
    create: row.create,
    moveOut: worker,
    moveIn: row.create,
  };
}

/** True when a row would grant nothing; such rows are dropped on save. */
export const isEmptyRow = (row: Pick<MatrixRow, 'access' | 'create'>) =>
  row.access === '' && !row.create;

export function sameMatrix(a: readonly MatrixRow[], b: readonly MatrixRow[]): boolean {
  const norm = (rows: readonly MatrixRow[]) =>
    rows
      .filter((row) => !isEmptyRow(row))
      .map((row) => `${subjectKey(row)}|${row.access}|${row.create}`)
      .sort()
      .join(',');
  return norm(a) === norm(b);
}
