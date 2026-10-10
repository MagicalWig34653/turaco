import { useModules } from '../../platform/modules/ModulesProvider';
import { AskTuraco } from '../ai/AiProvider';
import { useId, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, Select, TextArea } from '../../platform/ui/Field';
import { AttachmentsPanel } from '../../platform/attachments/AttachmentsPanel';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Avatar, Card, Skeleton, StatusBadge, Tabs } from '../../platform/ui/Workspace';
import { useContextMenu } from '../../platform/ui/ContextMenu';
import { TableDate } from '../../platform/ui/TableDate';
import {
  appendWorkaround,
  clearSubmittedDraft,
  effectiveCommentKind,
  primaryTicketOperation,
  resolveAbilities,
} from './workspaceModel';
import { ReasonDialog } from '../../platform/ui/ReasonDialog';
import { Dialog } from '../../platform/ui/Dialog';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { RemoteSupportGate, TicketRemoteSupport } from '../remoteaccess/RemoteSupportCard';
import { problemsApi } from '../problems/api';
import { incidentsApi } from '../incidents/api';
import { IncidentBadge } from '../incidents/IncidentsScreen';
import { notifySidebarChanged } from '../../platform/ui/views/api';
import { ticketQueuesApi, ticketsApi } from './api';
import { aliasList, canOfferMove, ticketQueueName } from './queueModel';
import { reportedImpactKey } from './impactModel';
import { organizationApi } from '../organization/api';
import { TicketMoveDialog } from './TicketMoveDialog';
import { TicketLocationDialog } from './TicketLocationDialog';
import { TicketChangesCard } from './TicketChangesCard';
import { MentionPicker } from './MentionPicker';
import {
  historyLocationIds,
  locationFromProfile,
  mentionIds,
  type Mentioned,
} from './ticketExtrasModel';
import { DuplicateDialog, LinkToDialog } from './LinkDialogs';
import {
  describeHistory,
  historyReason,
  isAssignmentEntry,
  mergeTimeline,
  viaKey,
} from './historyModel';
import { TicketStatusBadge } from './TicketsScreen';
import { priorities, waitingReasons, type TicketDetail, type TicketOperation } from './types';

const needsText: readonly string[] = ['wait', 'resolve', 'reopen', 'cancel'];

function AssignDialog({
  ticket,
  onClose,
  onDone,
}: {
  ticket: TicketDetail;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const { session } = useSession();
  const [mode, setMode] = useState<'user' | 'team'>('user');
  const [assignee, setAssignee] = useState<Assignee | null>(null);
  const [queue, setQueue] = useState<Assignee | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const target = mode === 'user' ? assignee : queue;
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!target) return;
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.assign(
        ticket.id,
        ticket.version,
        mode === 'user' ? { assigneeId: target.id } : { queueTeamId: target.id },
      );
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('tickets.action.assign')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <fieldset className="assign-mode">
          <legend>{t('tickets.assign.mode')}</legend>
          <label>
            <input
              type="radio"
              name="assign-mode"
              checked={mode === 'user'}
              onChange={() => setMode('user')}
            />{' '}
            {t('tickets.assign.mode.user')}
          </label>
          <label>
            <input
              type="radio"
              name="assign-mode"
              checked={mode === 'team'}
              onChange={() => setMode('team')}
            />{' '}
            {t('tickets.assign.mode.team')}
          </label>
        </fieldset>
        {mode === 'user' ? (
          <>
            {session?.userId ? (
              <Button
                onClick={() =>
                  setAssignee({ id: session.userId, label: session.displayName ?? session.userId })
                }
              >
                {t('tickets.assign.toMe')}
              </Button>
            ) : null}
            {ticket.assigneeId && ticket.assigneeId !== session?.userId ? (
              <Alert kind="warning">{t('tickets.assign.heldByOther')}</Alert>
            ) : null}
            <AssigneePicker presenceHints type="user" value={assignee} onChange={setAssignee} />
          </>
        ) : (
          <AssigneePicker
            type="team"
            label={t('tickets.assign.queue')}
            hint={t('tickets.assign.queue.hint')}
            value={queue}
            onChange={setQueue}
          />
        )}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!target}>
            {t(mode === 'user' ? 'tickets.action.assign' : 'tickets.assign.moveToQueue')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** The Major Incident this ticket is linked to, so staff see the wider outage context. */
function LinkedIncident({ id }: { id: string }) {
  const { t } = useI18n();
  const loaded = useAsync((signal) => incidentsApi.get(id, signal), [id]);
  return (
    <Card title={t('tickets.section.majorIncident')}>
      <h2>{t('tickets.section.majorIncident')}</h2>
      {loaded.error ? (
        <p className="incident-muted">{t('tickets.majorIncident.unavailable')}</p>
      ) : !loaded.data ? (
        <Skeleton lines={2} />
      ) : (
        <>
          <p>
            <span className="incident-reference">{loaded.data.reference}</span>{' '}
            <IncidentBadge status={loaded.data.status} />
          </p>
          <p>
            <Link to={`/incidents/${encodeURIComponent(loaded.data.id)}`}>{loaded.data.title}</Link>
          </p>
          <p className="incident-muted">
            {t('incidents.linked', { count: loaded.data.linkedTickets })}
          </p>
        </>
      )}
    </Card>
  );
}

function WaitDialog({
  ticket,
  onClose,
  onDone,
}: {
  ticket: TicketDetail;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [reason, setReason] = useState<string>('customer');
  const [note, setNote] = useState('');
  const [visible, setVisible] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.operate(ticket.id, 'wait', ticket.version, reason);
      // The note is a second call; the hold itself has already succeeded, so a failure must not undo it.
      if (note.trim()) {
        try {
          await ticketsApi.comment(ticket.id, note.trim(), !visible);
        } catch {
          /* the hold is recorded; the note can be added from the conversation */
        }
      }
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('tickets.action.wait')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <Select
          label={t('tickets.field.waitingReason')}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          options={waitingReasons.map((value) => ({ value, label: t(`tickets.waiting.${value}`) }))}
        />
        <TextArea
          label={t('tickets.wait.note')}
          hint={t('tickets.wait.note.hint')}
          value={note}
          rows={3}
          maxLength={5000}
          onChange={(event) => setNote(event.target.value)}
        />
        {note.trim() ? (
          <Checkbox
            label={t('tickets.wait.note.visible')}
            checked={visible}
            onChange={(event) => setVisible(event.target.checked)}
          />
        ) : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy}>
            {t('tickets.action.wait')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function TicketDetailScreen({ id }: { id: string }) {
  return <TicketWorkspace key={id} id={id} />;
}

function TicketWorkspace({ id }: { id: string }) {
  const { enabled } = useModules();
  const { t, locale } = useI18n();
  const { can, session } = useSession();
  const loaded = useAsync((signal) => ticketsApi.get(id, signal), [id]);
  const staffReader = can('tickets.view') || can('tickets.manage');
  const known = useAsync(
    async (signal) => (staffReader ? (await problemsApi.knownErrors(id, signal)).items : []),
    [id, staffReader],
  );
  const sync = useAsync(
    async (signal) => (staffReader ? await ticketsApi.externalSync(id, signal) : null),
    [id, staffReader],
  );
  // Assignment and status changes with actor and reason; a failure only hides them.
  const history = useAsync(
    async (signal) => (staffReader ? await ticketsApi.history(id, signal) : null),
    [id, staffReader],
  );
  // The caller's level in the Ticket's Queue decides whether "Move to queue" is offered (the server still authorizes).
  const queues = useAsync(
    async (signal) => (staffReader ? (await ticketQueuesApi.list(false, signal)).items : []),
    [staffReader],
  );
  const locationId = loaded.data?.affectedLocationId ?? null;
  const location = useAsync(
    async (signal) => {
      if (!staffReader || !locationId) return null;
      try {
        return (await organizationApi.location(locationId, signal)).name;
      } catch {
        return null; // The name is a convenience; the ticket stays usable without it.
      }
    },
    [locationId, staffReader],
  );
  const historyLocations = historyLocationIds(history.data?.items);
  const historyLocationNames = useAsync(
    async (signal) => {
      const names: Record<string, string> = {};
      await Promise.all(
        historyLocations.map(async (locationId) => {
          try {
            names[locationId] = (await organizationApi.location(locationId, signal)).name;
          } catch {
            // The name is a convenience; the entry falls back to a neutral label.
          }
        }),
      );
      return names;
    },
    [historyLocations.join(',')],
  );
  const [retrying, setRetrying] = useState(false);
  const [mentions, setMentions] = useState<Mentioned[]>([]);
  const [dialog, setDialog] = useState<{
    kind:
      | 'text'
      | 'wait'
      | 'assign'
      | 'move'
      | 'location'
      | 'linkProblem'
      | 'linkIncident'
      | 'duplicate';
    op?: TicketOperation;
  } | null>(null);
  const [busyOp, setBusyOp] = useState<string | null>(null);
  const [actionError, setActionError] = useState<ApiError | undefined>(undefined);
  const [drafts, setDrafts] = useState({ reply: '', internal: '' });
  const [view, setView] = useState('conversation');
  const [workaroundInserted, setWorkaroundInserted] = useState(false);
  const paneId = useId();
  const composer = useRef<HTMLFormElement>(null);
  const commentPending = useRef(false);
  const menu = useContextMenu();
  const [internalChoice, setInternalChoice] = useState(false);
  const [commenting, setCommenting] = useState(false);
  const [commentError, setCommentError] = useState<ApiError | undefined>(undefined);

  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const ticket = loaded.data;
  if (!ticket) {
    return (
      <div className="incident-loading">
        <Skeleton lines={2} />
        <div className="incident-grid">
          <Card>
            <Skeleton lines={8} />
          </Card>
          <Card>
            <Skeleton lines={6} />
          </Card>
        </div>
      </div>
    );
  }
  const manage = can('tickets.manage');
  const staff = manage || can('tickets.view');
  const name = (key: string | null) =>
    key ? (ticket.names[key] ?? t('ticketWorkspace.unknownPerson')) : t('tickets.fact.unassigned');
  const done = () => {
    setDialog(null);
    setActionError(undefined);
    loaded.reload();
    history.reload();
  };
  const queueName = ticketQueueName(ticket);
  const aliases = aliasList(ticket);
  const impactKey = reportedImpactKey(ticket.impact, ticket.patientImpact);
  const queueLevel = queues.data?.find((queue) => queue.id === ticket.queueId)?.level;
  const isOwner =
    session?.userId === ticket.reporterId || session?.userId === ticket.affectedUserId;
  const abilities = resolveAbilities(ticket.abilities, {
    manage,
    isOwner,
    open: ticket.status !== 'closed' && ticket.status !== 'cancelled',
    canMove:
      staffReader &&
      canOfferMove(ticket) &&
      (manage || queueLevel === 'work' || queueLevel === 'manage'),
  });
  // A server that returns abilities already accounts for the Ticket state and Queue level.
  const canMove = abilities.move && canOfferMove(ticket);
  const mode = effectiveCommentKind(abilities, internalChoice);
  const internal = mode === 'internal';
  const comment = drafts[mode];
  const run = async (op: TicketOperation) => {
    setBusyOp(op);
    setActionError(undefined);
    try {
      await ticketsApi.operate(ticket.id, op, ticket.version);
      done();
    } catch (cause) {
      setActionError(asApiError(cause));
    } finally {
      setBusyOp(null);
    }
  };
  const start = (op: TicketOperation) => {
    if (op === 'wait') setDialog({ kind: 'wait' });
    else if (needsText.includes(op)) setDialog({ kind: 'text', op });
    else void run(op);
  };
  const submitComment = async (event: FormEvent) => {
    event.preventDefault();
    if (commentPending.current || !comment.trim()) return;
    commentPending.current = true;
    const submitted = comment;
    const submittedMode = mode;
    setCommenting(true);
    setCommentError(undefined);
    try {
      await ticketsApi.comment(ticket.id, comment.trim(), internal, mentionIds(mentions, internal));
      setDrafts((current) => clearSubmittedDraft(current, submittedMode, submitted));
      if (internal) setMentions([]);
      setWorkaroundInserted(false);
      loaded.reload();
    } catch (cause) {
      setCommentError(asApiError(cause));
    } finally {
      commentPending.current = false;
      setCommenting(false);
    }
  };
  const canComment = abilities.composer;
  const ops = abilities.transition ? (ticket.allowedOperations as TicketOperation[]) : [];
  const textKey = (op: TicketOperation) => `tickets.action.${op}` as MessageKey;
  const primaryOp = primaryTicketOperation(ops);
  const focusComposer = () => {
    setView('conversation');
    window.requestAnimationFrame(() => composer.current?.querySelector('textarea')?.focus());
  };
  return (
    <div className="incident-workspace">
      <Link className="incident-back" to={staffReader ? '/service-desk' : '/support'}>
        ← {t('tickets.back')}
      </Link>
      <div className="incident-heading-meta">
        <span className="incident-reference">{ticket.reference}</span>
        {aliases.length > 0 ? (
          <span className="ticket-aliases" title={t('tickets.aliases.hint')}>
            {t('tickets.aliases.previously')}{' '}
            {aliases.map((alias, index) => (
              <span key={alias}>
                {index > 0 ? ', ' : ''}
                <span className="incident-reference">{alias}</span>
              </span>
            ))}
          </span>
        ) : null}
        <TicketStatusBadge status={ticket.status} />
        <StatusBadge
          tone={
            ticket.priority === 'urgent'
              ? 'danger'
              : ticket.priority === 'high'
                ? 'warning'
                : 'neutral'
          }
        >
          {t(`tickets.priority.${ticket.priority}`)}
        </StatusBadge>
        {ticket.patientImpact ? (
          <StatusBadge tone="warning">{t('tickets.patientImpact')}</StatusBadge>
        ) : null}
      </div>
      <PageHeader title={ticket.title} actions={<AskTuraco context={{ type: 'ticket', id }} />} />
      <div className="incident-identities">
        <span>
          <Avatar name={name(ticket.reporterId)} />
          <span>
            <small>{t('tickets.fact.reporter')}</small>
            <strong>{name(ticket.reporterId)}</strong>
          </span>
        </span>
        <span>
          <Avatar name={ticket.assigneeId ? name(ticket.assigneeId) : null} />
          <span>
            <small>{t('tickets.fact.assignee')}</small>
            <strong>{name(ticket.assigneeId)}</strong>
          </span>
        </span>
        <span className="incident-age">
          <span>
            <small>{t('tickets.fact.created')}</small>
            <TableDate value={ticket.createdAt} />
          </span>
        </span>
      </div>
      {menu.menu}
      {actionError ? <ApiErrorAlert error={actionError} onRetry={loaded.reload} /> : null}
      <div className="incident-mobile-tabs">
        <Tabs
          idPrefix={paneId}
          active={view}
          onChange={setView}
          items={[
            { id: 'conversation', label: t('tickets.section.conversation') },
            { id: 'context', label: t('tickets.context') },
          ]}
        />
      </div>
      <div className={`incident-grid incident-view-${view}`}>
        <div
          className="incident-conversation"
          role="tabpanel"
          aria-labelledby={`${paneId}-tab-conversation`}
          id={`${paneId}-panel-conversation`}
        >
          <Card className="incident-thread" title={t('tickets.section.conversation')}>
            <header className="incident-section-head">
              <h2>{t('tickets.section.conversation')}</h2>
              <span className="incident-count">{ticket.comments.length}</span>
            </header>
            <ol className="incident-timeline">
              <li className="incident-event">
                <Avatar name={name(ticket.reporterId)} />
                <div className="incident-event-body">
                  <header>
                    <strong>{name(ticket.reporterId)}</strong>
                    <TableDate value={ticket.createdAt} />
                  </header>
                  <span className="incident-event-label">{t('tickets.field.description')}</span>
                  <p className="preline">
                    {ticket.description || t('ticketWorkspace.noDescription')}
                  </p>
                </div>
              </li>
              {mergeTimeline(ticket.comments, history.data?.items).map((item) => {
                if (item.type === 'history') {
                  const entry = item.entry;
                  const historyName = (key: string | null | undefined) =>
                    key
                      ? (historyLocationNames.data?.[key] ??
                        (historyLocations.includes(key)
                          ? t('ticketLocation.unknown')
                          : undefined) ??
                        history.data?.names?.[key] ??
                        ticket.names[key] ??
                        t('ticketWorkspace.unknownPerson'))
                      : '';
                  const text = describeHistory(entry, historyName, (kind, value) =>
                    t(
                      (kind === 'status'
                        ? `tickets.status.${value}`
                        : `tickets.priority.${value}`) as MessageKey,
                    ),
                  );
                  const via = viaKey(entry.via);
                  const reason = historyReason(entry);
                  return (
                    <li
                      key={`h-${entry.id}-${entry.kind}`}
                      className={`incident-event ticket-history ${isAssignmentEntry(entry) ? 'ticket-history-assignment' : ''}`}
                    >
                      <Avatar name={entry.actorId ? historyName(entry.actorId) : null} />
                      <div className="incident-event-body">
                        <header>
                          <strong>
                            {entry.actorId ? historyName(entry.actorId) : t('ticketHistory.system')}
                          </strong>
                          <TableDate value={entry.at} />
                        </header>
                        <p>{t(text.key, text.params)}</p>
                        {via ? <small className="incident-muted">{t(via)}</small> : null}
                        {reason ? (
                          <p className="preline ticket-history-reason">
                            <span className="incident-event-label">
                              {t(
                                reason.kind === 'waiting'
                                  ? 'ticketHistory.waitingFor'
                                  : 'ticketHistory.reason',
                              )}
                            </span>{' '}
                            {reason.kind === 'text' ? reason.text : t(reason.key)}
                          </p>
                        ) : null}
                      </div>
                    </li>
                  );
                }
                const c = item.comment;
                return (
                  <li
                    key={c.id}
                    className={`incident-event ${c.internal ? 'incident-event-internal' : ''}`}
                  >
                    <Avatar name={name(c.authorId)} />
                    <div className="incident-event-body">
                      <header>
                        <strong>{name(c.authorId)}</strong>
                        <TableDate value={c.createdAt} />
                      </header>
                      <span className="incident-event-label">
                        {c.internal ? (
                          <StatusBadge tone="warning">{t('tickets.comment.internal')}</StatusBadge>
                        ) : (
                          t('ticketWorkspace.reply')
                        )}
                      </span>
                      <p className="preline">{c.body}</p>
                      {c.mentionedUserIds && c.mentionedUserIds.length > 0 ? (
                        <p className="field-hint ticket-mentions">
                          {t('ticketMention.mentioned', {
                            names: c.mentionedUserIds
                              .map(
                                (userId) =>
                                  `@${ticket.names[userId] ?? t('ticketWorkspace.unknownPerson')}`,
                              )
                              .join(', '),
                          })}
                        </p>
                      ) : null}
                    </div>
                  </li>
                );
              })}
            </ol>
            {ticket.resolution ? (
              <section className="incident-resolution">
                <StatusBadge tone="success">{t('tickets.fact.resolution')}</StatusBadge>
                <p className="preline">{ticket.resolution}</p>
                {can('knowledge.manage') ? (
                  <Link
                    to={`/knowledge/new?title=${encodeURIComponent(ticket.title)}&body=${encodeURIComponent(ticket.resolution)}`}
                  >
                    {t('tickets.action.makeArticle')} →
                  </Link>
                ) : null}
              </section>
            ) : null}
            {canComment ? (
              <form
                ref={composer}
                className={`form incident-composer ${internal ? 'incident-composer-internal' : ''}`}
                onSubmit={(event) => void submitComment(event)}
              >
                {abilities.canChooseKind ? (
                  <div
                    className="incident-composer-mode"
                    role="group"
                    aria-label={t('tickets.comment.label')}
                  >
                    <Button
                      disabled={commenting}
                      aria-pressed={!internal}
                      onClick={() => setInternalChoice(false)}
                    >
                      {t('ticketWorkspace.reply')}
                    </Button>
                    <Button
                      disabled={commenting}
                      aria-pressed={internal}
                      onClick={() => setInternalChoice(true)}
                    >
                      {t('ticketWorkspace.internalNote')}
                    </Button>
                  </div>
                ) : null}
                {commentError ? <ApiErrorAlert error={commentError} /> : null}
                {workaroundInserted && comment.trim() ? (
                  <Alert kind="info">
                    {t(
                      internal
                        ? 'ticketWorkspace.workaroundInsertedInternal'
                        : 'ticketWorkspace.workaroundInsertedPublic',
                    )}
                  </Alert>
                ) : null}
                {internal && staffReader ? (
                  <MentionPicker value={mentions} onChange={setMentions} disabled={commenting} />
                ) : null}
                <TextArea
                  label={t(internal ? 'ticketWorkspace.internalNote' : 'tickets.comment.label')}
                  hint={t(
                    internal ? 'tickets.comment.internalOnly.hint' : 'ticketWorkspace.replyHint',
                  )}
                  value={comment}
                  rows={5}
                  maxLength={5000}
                  onChange={(event) => setDrafts({ ...drafts, [mode]: event.target.value })}
                />
                <div className="incident-composer-footer">
                  <span>{t('ticketWorkspace.draftHint')}</span>
                  <Button
                    type="submit"
                    variant="primary"
                    busy={commenting}
                    disabled={!comment.trim()}
                  >
                    {t(internal ? 'ticketWorkspace.saveNote' : 'ticketWorkspace.sendReply')}
                  </Button>
                </div>
              </form>
            ) : null}
          </Card>
        </div>
        <aside
          className="incident-context"
          role="tabpanel"
          aria-labelledby={`${paneId}-tab-context`}
          id={`${paneId}-panel-context`}
          aria-label={t('tickets.context')}
        >
          <Card className="incident-actions" title={t('ticketWorkspace.actions')}>
            <h2>{t('ticketWorkspace.actions')}</h2>
            <div className="incident-actionbar" aria-label={t('ticketWorkspace.actions')}>
              <div className="incident-actionbar-primary">
                {primaryOp ? (
                  <Button
                    variant="primary"
                    busy={busyOp === primaryOp}
                    disabled={busyOp !== null}
                    onClick={() => start(primaryOp)}
                  >
                    {t(textKey(primaryOp))}
                  </Button>
                ) : null}
                {abilities.assign ? (
                  <Button disabled={busyOp !== null} onClick={() => setDialog({ kind: 'assign' })}>
                    {t('tickets.action.assign')}
                  </Button>
                ) : null}
                {canMove ? (
                  <Button disabled={busyOp !== null} onClick={() => setDialog({ kind: 'move' })}>
                    {t('tickets.action.moveQueue')}
                  </Button>
                ) : null}
                {canComment ? (
                  <Button onClick={focusComposer}>{t('ticketWorkspace.reply')}</Button>
                ) : null}
              </div>
              {ops.some((op) => op !== primaryOp) ? (
                <Button
                  disabled={busyOp !== null}
                  aria-haspopup="menu"
                  onClick={(event) =>
                    menu.openAtElement(
                      ops
                        .filter((op) => op !== primaryOp)
                        .map((op) => ({
                          id: op,
                          label: t(textKey(op)),
                          danger: op === 'cancel',
                          onSelect: () => start(op),
                        })),
                      event.currentTarget,
                      t('ticketWorkspace.actions'),
                    )
                  }
                >
                  {t('ticketWorkspace.more')} <span aria-hidden="true">⋯</span>
                </Button>
              ) : null}
            </div>
          </Card>
          <Card title={t('ticketWorkspace.handling')}>
            <h2>{t('ticketWorkspace.handling')}</h2>
            <dl className="incident-facts">
              <dt>{t('tickets.col.status')}</dt>
              <dd key={ticket.status}>
                <TicketStatusBadge status={ticket.status} />
                {ticket.waitingReason ? (
                  <p>{t(`tickets.waiting.${ticket.waitingReason}`)}</p>
                ) : null}
                {ticket.statusReason ? <p>{ticket.statusReason}</p> : null}
              </dd>
              <dt>{t('tickets.fact.assignee')}</dt>
              <dd>{name(ticket.assigneeId)}</dd>
              {queueName ? (
                <>
                  <dt>{t('tickets.fact.queue')}</dt>
                  <dd>
                    {queueName}
                    {ticket.queue ? (
                      <small className="ticket-queue-prefix"> ({ticket.queue.prefix})</small>
                    ) : null}
                  </dd>
                </>
              ) : null}
              {staff && ticket.queueTeamId ? (
                <>
                  <dt>{t('tickets.fact.routingTeam')}</dt>
                  <dd>{name(ticket.queueTeamId)}</dd>
                </>
              ) : null}
              {aliases.length > 0 ? (
                <>
                  <dt>{t('tickets.aliases.title')}</dt>
                  <dd>
                    <ul className="ticket-alias-list">
                      {aliases.map((alias) => (
                        <li key={alias} className="incident-reference">
                          {alias}
                        </li>
                      ))}
                    </ul>
                    <small>{t('tickets.aliases.hint')}</small>
                  </dd>
                </>
              ) : null}
            </dl>
            {abilities.setPriority ? (
              <Select
                label={t('tickets.col.priority')}
                value={ticket.priority}
                onChange={(event) =>
                  void ticketsApi
                    .setPriority(ticket.id, ticket.version, event.target.value)
                    .then(loaded.reload, (cause: unknown) => setActionError(asApiError(cause)))
                }
                options={priorities.map((value) => ({
                  value,
                  label: t(`tickets.priority.${value}`),
                }))}
              />
            ) : null}
          </Card>
          <Card title={t('ticketWorkspace.requester')}>
            <h2>{t('ticketWorkspace.requester')}</h2>
            <div className="incident-person">
              <Avatar name={name(ticket.affectedUserId)} />
              <div>
                <strong>{name(ticket.affectedUserId)}</strong>
                <small>{t('tickets.fact.affected')}</small>
              </div>
            </div>
            {impactKey ? (
              <p>
                <span className="incident-event-label">{t('tickets.fact.reportedImpact')}</span>{' '}
                <strong>{t(impactKey)}</strong>
              </p>
            ) : null}
            {ticket.affectedLocationId || abilities.setLocation ? (
              <p>
                <span className="incident-event-label">{t('ticketLocation.label')}</span>{' '}
                <strong>
                  {ticket.affectedLocationId
                    ? (location.data ?? t('tickets.personUnknown'))
                    : t('ticketLocation.none')}
                </strong>
                {ticket.affectedLocationId && locationFromProfile(history.data?.items) ? (
                  <small className="ticket-location-source">
                    {' '}
                    ({t('ticketLocation.fromProfile')})
                  </small>
                ) : null}{' '}
                {abilities.setLocation ? (
                  <Button onClick={() => setDialog({ kind: 'location' })}>
                    {t('ticketLocation.edit')}
                  </Button>
                ) : null}
              </p>
            ) : null}
            {ticket.deviceSnapshot ? (
              <div className="incident-device">
                <span className="incident-event-label">{t('tickets.field.device')}</span>
                <strong>{String(ticket.deviceSnapshot.product ?? '')}</strong>
                <span>{String(ticket.deviceSnapshot.reference ?? '')}</span>
                {ticket.deviceSnapshot.serialNumber ? (
                  <span>{String(ticket.deviceSnapshot.serialNumber)}</span>
                ) : null}
              </div>
            ) : null}
          </Card>
          {ticket.majorIncidentId ? <LinkedIncident id={ticket.majorIncidentId} /> : null}
          {abilities.markDuplicate || can('problems.manage') || can('majorincidents.manage') ? (
            <Card title={t('ticketLink.title')}>
              <h2>{t('ticketLink.title')}</h2>
              <div className="link-panel-actions">
                {can('problems.manage') ? (
                  <Button onClick={() => setDialog({ kind: 'linkProblem' })}>
                    {t('ticketLink.toProblem')}
                  </Button>
                ) : null}
                {can('majorincidents.manage') && !ticket.majorIncidentId ? (
                  <Button onClick={() => setDialog({ kind: 'linkIncident' })}>
                    {t('ticketLink.toIncident')}
                  </Button>
                ) : null}
                {abilities.markDuplicate ? (
                  <Button onClick={() => setDialog({ kind: 'duplicate' })}>
                    {t('ticketLink.duplicate')}
                  </Button>
                ) : null}
              </div>
            </Card>
          ) : null}
          {staffReader ? (
            <TicketChangesCard ticketId={ticket.id} canEdit={manage} onChanged={history.reload} />
          ) : null}
          {can('remote_access.view') ? (
            <RemoteSupportGate>
              <TicketRemoteSupport ticketId={ticket.id} deviceSnapshot={ticket.deviceSnapshot} />
            </RemoteSupportGate>
          ) : null}
          {staffReader ? (
            <Card title={t('tickets.section.knownErrors')}>
              <h2>{t('tickets.section.knownErrors')}</h2>
              {known.error ? (
                <ApiErrorAlert error={known.error} onRetry={known.reload} />
              ) : !known.data ? (
                <Skeleton lines={2} />
              ) : known.data.length ? (
                <ul className="incident-known">
                  {known.data.map((k) => (
                    <li key={k.id}>
                      <span className="incident-reference">{k.reference}</span>
                      <Link to={`/problems/${encodeURIComponent(k.id)}`}>{k.title}</Link>
                      <p className="preline">{k.workaround}</p>
                      {canComment && k.workaround ? (
                        <Button
                          disabled={commenting}
                          onClick={() => {
                            setDrafts((current) => ({
                              ...current,
                              [mode]: appendWorkaround(
                                current[mode],
                                k.workaround ?? '',
                                t('ticketWorkspace.workaroundSource', { reference: k.reference }),
                              ),
                            }));
                            setWorkaroundInserted(true);
                            focusComposer();
                          }}
                        >
                          {t('ticketWorkspace.insertWorkaround')} ↗
                        </Button>
                      ) : null}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="incident-muted">{t('ticketWorkspace.noKnownErrors')}</p>
              )}
            </Card>
          ) : null}
          {enabled('knowledge') && can('runbooks.execute') ? (
            <Card className="incident-runbook" title={t('ticketWorkspace.runbook')}>
              <h2>{t('ticketWorkspace.runbook')}</h2>
              <p>{t('ticketWorkspace.runbookHint')}</p>
              <Link to={`/runbooks?ticket=${encodeURIComponent(ticket.id)}`}>
                {t('tickets.action.runbook')} →
              </Link>
            </Card>
          ) : null}
          {sync.error ? (
            <Card>
              <ApiErrorAlert error={sync.error} onRetry={sync.reload} />
            </Card>
          ) : sync.data?.enabled ? (
            <Card title={t('tickets.section.externalSync')}>
              <h2>{t('tickets.section.externalSync')}</h2>
              <Badge>
                {sync.data.syncState
                  ? t(`tickets.sync.${sync.data.syncState}` as MessageKey)
                  : t('tickets.sync.none')}
              </Badge>
              {sync.data.externalId ? (
                <p className="incident-reference">{sync.data.externalId}</p>
              ) : null}
              {sync.data.lastSyncedAt ? (
                <p>
                  <TableDate value={sync.data.lastSyncedAt} />
                </p>
              ) : null}
              {sync.data.lastError ? <p className="preline">{sync.data.lastError}</p> : null}
              {manage ? (
                <Button
                  busy={retrying}
                  onClick={async () => {
                    setRetrying(true);
                    try {
                      await ticketsApi.retryExternalSync(id);
                      sync.reload();
                    } catch (e) {
                      setActionError(asApiError(e));
                    } finally {
                      setRetrying(false);
                    }
                  }}
                >
                  {t('tickets.sync.retry')}
                </Button>
              ) : null}
            </Card>
          ) : null}
          <AttachmentsPanel
            ownerType="ticket"
            ownerId={ticket.id}
            canUpload={canComment}
            canDelete={staffReader}
            staff={staffReader}
          />
          <Card title={t('ticketWorkspace.activity')}>
            <h2>{t('ticketWorkspace.activity')}</h2>
            <ol className="incident-history">
              <li>
                <span>{t('ticketWorkspace.updated')}</span>
                <TableDate value={ticket.updatedAt} />
              </li>
              {ticket.closedAt ? (
                <li>
                  <span>{t('tickets.status.closed')}</span>
                  <TableDate value={ticket.closedAt} />
                </li>
              ) : null}
              {ticket.resolvedAt ? (
                <li>
                  <span>{t('tickets.status.resolved')}</span>
                  <TableDate value={ticket.resolvedAt} />
                </li>
              ) : null}
              <li>
                <span>{t('tickets.fact.created')}</span>
                <time dateTime={ticket.createdAt}>{formatDateTime(locale, ticket.createdAt)}</time>
              </li>
            </ol>
          </Card>
        </aside>
      </div>
      {dialog?.kind === 'assign' ? (
        <AssignDialog ticket={ticket} onClose={() => setDialog(null)} onDone={done} />
      ) : null}
      {dialog?.kind === 'move' ? (
        <TicketMoveDialog
          ticket={ticket}
          onClose={() => setDialog(null)}
          onDone={() => {
            notifySidebarChanged();
            done();
          }}
        />
      ) : null}
      {dialog?.kind === 'location' ? (
        <TicketLocationDialog
          ticket={ticket}
          currentName={ticket.affectedLocationId ? (location.data ?? null) : null}
          onClose={() => setDialog(null)}
          onDone={done}
        />
      ) : null}
      {dialog?.kind === 'linkProblem' || dialog?.kind === 'linkIncident' ? (
        <LinkToDialog
          kind={dialog.kind === 'linkProblem' ? 'problem' : 'incident'}
          ticketId={ticket.id}
          onClose={() => setDialog(null)}
          onDone={done}
        />
      ) : null}
      {dialog?.kind === 'duplicate' ? (
        <DuplicateDialog ticket={ticket} onClose={() => setDialog(null)} onDone={done} />
      ) : null}
      {dialog?.kind === 'wait' ? (
        <WaitDialog ticket={ticket} onClose={() => setDialog(null)} onDone={done} />
      ) : null}
      {dialog?.kind === 'text' && dialog.op ? (
        <ReasonDialog
          title={t(textKey(dialog.op))}
          label={t(dialog.op === 'resolve' ? 'tickets.field.resolution' : 'tickets.field.reason')}
          confirmLabel={t(textKey(dialog.op))}
          danger={dialog.op === 'cancel'}
          onClose={() => setDialog(null)}
          onSubmit={async (text) => {
            await ticketsApi.operate(ticket.id, dialog.op as TicketOperation, ticket.version, text);
            done();
          }}
        />
      ) : null}
    </div>
  );
}
