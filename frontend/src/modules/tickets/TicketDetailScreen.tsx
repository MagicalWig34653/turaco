import { useState } from 'react';
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
import { Checkbox, Select, TextArea } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
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
  const [comment, setComment] = useState('');
  const [internal, setInternal] = useState(false);
  const [commenting, setCommenting] = useState(false);
  const [commentError, setCommentError] = useState<ApiError | undefined>(undefined);

  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const ticket = loaded.data;
  if (!ticket) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }
  const manage = can('tickets.manage');
  const staff = manage || can('tickets.view');
  const name = (key: string | null) => (key ? (ticket.names[key] ?? key) : '–');
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
    setCommenting(true);
    setCommentError(undefined);
    try {
      await ticketsApi.comment(ticket.id, comment.trim(), internal);
      setComment('');
      loaded.reload();
    } catch (cause) {
      setCommentError(asApiError(cause));
    } finally {
      setCommenting(false);
    }
  };
  const isOwner =
    session?.userId === ticket.reporterId || session?.userId === ticket.affectedUserId;
  const canComment =
    ticket.status !== 'closed' && ticket.status !== 'cancelled' && (manage || isOwner);
  const ops = ticket.allowedOperations as TicketOperation[];
  const textKey = (op: TicketOperation) => `tickets.action.${op}` as MessageKey;
  return (
    <>
      <PageHeader
        title={`${ticket.reference} · ${ticket.title}`}
        actions={
          <>
            {manage ? (
              <Button onClick={() => setDialog({ kind: 'assign' })}>
                {t('tickets.action.assign')}
              </Button>
            ) : null}
            {ops.map((op) => (
              <Button
                key={op}
                variant={op === 'resolve' ? 'primary' : op === 'cancel' ? 'danger' : 'secondary'}
                busy={busyOp === op}
                disabled={busyOp !== null}
                onClick={() => start(op)}
              >
                {t(textKey(op))}
              </Button>
            ))}
          </>
        }
      />
      <p>
        <Link to="/support">{t('tickets.back')}</Link>
      </p>
      {actionError ? <ApiErrorAlert error={actionError} onRetry={loaded.reload} /> : null}
      <dl className="facts">
        <dt>{t('tickets.col.status')}</dt>
        <dd>
          <TicketStatusBadge status={ticket.status} />
          {ticket.waitingReason ? <> {t(`tickets.waiting.${ticket.waitingReason}`)}</> : null}
          {ticket.statusReason ? <> {ticket.statusReason}</> : null}
        </dd>
        <dt>{t('tickets.col.priority')}</dt>
        <dd>
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
          ) : (
            t(`tickets.priority.${ticket.priority}`)
          )}
        </dd>
        <dt>{t('tickets.fact.reporter')}</dt>
        <dd>{name(ticket.reporterId)}</dd>
        {ticket.affectedUserId !== ticket.reporterId ? (
          <>
            <dt>{t('tickets.fact.affected')}</dt>
            <dd>{name(ticket.affectedUserId)}</dd>
          </>
        ) : null}
        <dt>{t('tickets.fact.assignee')}</dt>
        <dd>{ticket.assigneeId ? name(ticket.assigneeId) : t('tickets.fact.unassigned')}</dd>
        {staff && ticket.queueTeamId ? (
          <>
            <dt>{t('tickets.fact.queue')}</dt>
            <dd>{name(ticket.queueTeamId)}</dd>
          </>
        ) : null}
        {ticket.deviceSnapshot ? (
          <>
            <dt>{t('tickets.field.device')}</dt>
            <dd>
              {String(ticket.deviceSnapshot.product ?? '')} (
              {String(ticket.deviceSnapshot.reference ?? '')}
              {ticket.deviceSnapshot.serialNumber
                ? `, ${String(ticket.deviceSnapshot.serialNumber)}`
                : ''}
              )
            </dd>
          </>
        ) : null}
        <dt>{t('tickets.fact.created')}</dt>
        <dd>{formatDateTime(locale, ticket.createdAt)}</dd>
      </dl>
      {can('runbooks.execute') ? (
        <p>
          <Link to={`/runbooks?ticket=${encodeURIComponent(ticket.id)}`}>
            {t('tickets.action.runbook')}
          </Link>
        </p>
      ) : null}
      {sync.data?.enabled ? (
        <section>
          <h2>{t('tickets.section.externalSync')}</h2>
          <p>
            <Badge>
              {sync.data.syncState
                ? t(`tickets.sync.${sync.data.syncState}` as MessageKey)
                : t('tickets.sync.none')}
            </Badge>
            {sync.data.externalId ? ` ${sync.data.externalId}` : ''}
            {sync.data.lastSyncedAt ? ` · ${formatDateTime(locale, sync.data.lastSyncedAt)}` : ''}
          </p>
          {sync.data.lastError ? <p className="preline">{sync.data.lastError}</p> : null}
          {can('tickets.manage') ? (
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
        </section>
      ) : null}
      {known.data && known.data.length > 0 ? (
        <section>
          <h2>{t('tickets.section.knownErrors')}</h2>
          <ul className="plain-list">
            {known.data.map((k) => (
              <li key={k.id}>
                <Link to={`/problems/${encodeURIComponent(k.id)}`}>
                  {k.reference} · {k.title}
                </Link>
                <p className="preline">{k.workaround}</p>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
      {ticket.description ? (
        <section>
          <h2>{t('tickets.field.description')}</h2>
          <p className="preline">{ticket.description}</p>
        </section>
      ) : null}
      {ticket.resolution ? (
        <section>
          <h2>{t('tickets.fact.resolution')}</h2>
          <p className="preline">{ticket.resolution}</p>
          {can('knowledge.manage') ? (
            <p>
              <Link
                to={`/knowledge/new?title=${encodeURIComponent(ticket.title)}&body=${encodeURIComponent(ticket.resolution)}`}
              >
                {t('tickets.action.makeArticle')}
              </Link>
            </p>
          ) : null}
        </section>
      ) : null}
      <section>
        <h2>{t('tickets.section.conversation')}</h2>
        {ticket.comments.length === 0 ? (
          <p className="empty">{t('tickets.comments.none')}</p>
        ) : null}
        <ul className="plain-list">
          {ticket.comments.map((c) => (
            <li key={c.id}>
              <strong>{name(c.authorId)}</strong> · {formatDateTime(locale, c.createdAt)}{' '}
              {c.internal ? <Badge tone="warning">{t('tickets.comment.internal')}</Badge> : null}
              <p className="preline">{c.body}</p>
            </li>
          ))}
        </ul>
        {canComment ? (
          <form className="form" onSubmit={(event) => void submitComment(event)}>
            {commentError ? <ApiErrorAlert error={commentError} /> : null}
            <TextArea
              label={t('tickets.comment.label')}
              value={comment}
              rows={3}
              maxLength={5000}
              onChange={(event) => setComment(event.target.value)}
            />
            {manage ? (
              <Checkbox
                label={t('tickets.comment.internalOnly')}
                description={t('tickets.comment.internalOnly.hint')}
                checked={internal}
                onChange={(event) => setInternal(event.target.checked)}
              />
            ) : null}
            <div className="form-actions">
              <Button type="submit" busy={commenting} disabled={comment.trim() === ''}>
                {t('tickets.comment.send')}
              </Button>
            </div>
          </form>
        ) : null}
      </section>
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
    </>
  );
}
