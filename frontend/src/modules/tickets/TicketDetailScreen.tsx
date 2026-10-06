import { useId, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Select, TextArea } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Avatar, Card, Skeleton, StatusBadge, Tabs } from '../../platform/ui/Workspace';
import { useContextMenu } from '../../platform/ui/ContextMenu';
import { TableDate } from '../../platform/ui/TableDate';
import { appendWorkaround, clearSubmittedDraft, primaryTicketOperation } from './workspaceModel';
import { ReasonDialog } from '../../platform/ui/ReasonDialog';
import { Dialog } from '../../platform/ui/Dialog';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { problemsApi } from '../problems/api';
import { ticketsApi } from './api';
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
  const [assignee, setAssignee] = useState<Assignee | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!assignee) return;
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.assign(ticket.id, ticket.version, { assigneeId: assignee.id });
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
        <AssigneePicker type="user" value={assignee} onChange={setAssignee} />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!assignee}>
            {t('tickets.action.assign')}
          </Button>
        </div>
      </form>
    </Dialog>
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
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.operate(ticket.id, 'wait', ticket.version, reason);
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
  const [retrying, setRetrying] = useState(false);
  const [dialog, setDialog] = useState<{
    kind: 'text' | 'wait' | 'assign';
    op?: TicketOperation;
  } | null>(null);
  const [busyOp, setBusyOp] = useState<string | null>(null);
  const [actionError, setActionError] = useState<ApiError | undefined>(undefined);
  const [drafts, setDrafts] = useState({ reply: '', internal: '' });
  const [view, setView] = useState('conversation');
  const paneId = useId();
  const composer = useRef<HTMLFormElement>(null);
  const commentPending = useRef(false);
  const menu = useContextMenu();
  const [internal, setInternal] = useState(false);
  const mode = internal ? 'internal' : 'reply';
  const comment = drafts[mode];
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
  };
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
      await ticketsApi.comment(ticket.id, comment.trim(), internal);
      setDrafts((current) => clearSubmittedDraft(current, submittedMode, submitted));
      loaded.reload();
    } catch (cause) {
      setCommentError(asApiError(cause));
    } finally {
      commentPending.current = false;
      setCommenting(false);
    }
  };
  const isOwner =
    session?.userId === ticket.reporterId || session?.userId === ticket.affectedUserId;
  const canComment =
    ticket.status !== 'closed' && ticket.status !== 'cancelled' && (manage || isOwner);
  const ops = ticket.allowedOperations as TicketOperation[];
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
      </div>
      <PageHeader title={ticket.title} />
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
          {manage ? (
            <Button disabled={busyOp !== null} onClick={() => setDialog({ kind: 'assign' })}>
              {t('tickets.action.assign')}
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
              {ticket.comments.map((c) => (
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
                  </div>
                </li>
              ))}
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
                {manage ? (
                  <div
                    className="incident-composer-mode"
                    role="group"
                    aria-label={t('tickets.comment.label')}
                  >
                    <Button
                      disabled={commenting}
                      aria-pressed={!internal}
                      onClick={() => setInternal(false)}
                    >
                      {t('ticketWorkspace.reply')}
                    </Button>
                    <Button
                      disabled={commenting}
                      aria-pressed={internal}
                      onClick={() => setInternal(true)}
                    >
                      {t('ticketWorkspace.internalNote')}
                    </Button>
                  </div>
                ) : null}
                {commentError ? <ApiErrorAlert error={commentError} /> : null}
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
                    {t('tickets.comment.send')}
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
          <Card title={t('ticketWorkspace.requester')}>
            <h2>{t('ticketWorkspace.requester')}</h2>
            <div className="incident-person">
              <Avatar name={name(ticket.affectedUserId)} />
              <div>
                <strong>{name(ticket.affectedUserId)}</strong>
                <small>{t('tickets.fact.affected')}</small>
              </div>
            </div>
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
              {staff && ticket.queueTeamId ? (
                <>
                  <dt>{t('tickets.fact.queue')}</dt>
                  <dd>{name(ticket.queueTeamId)}</dd>
                </>
              ) : null}
            </dl>
            {manage ? (
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
                              [mode]: appendWorkaround(current[mode], k.workaround ?? ''),
                            }));
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
          {can('runbooks.execute') ? (
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
