import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { TextArea } from '../../platform/ui/Field';
import { requestsApi } from './api';
import { RequestStatusBadge } from './RequestsScreen';
import type { RequestAction, RequestField, ServiceRequestDetail } from './types';

type ReasonAction = 'cancel' | 'put_on_hold' | 'complete';

/** Human-readable answer; references are shown by name, everything else as entered. */
export function answerText(
  field: RequestField,
  value: unknown,
  names: Record<string, string>,
  yes: string,
  no: string,
): string {
  if (value === undefined || value === null || value === '') return '–';
  if (typeof value === 'boolean') return value ? yes : no;
  if (field.type === 'select') {
    return field.options?.find((o) => o.value === value)?.label ?? String(value);
  }
  if (field.type === 'user' || field.type === 'product')
    return names[String(value)] ?? String(value);
  return String(value);
}

function ReasonDialog({
  request,
  action,
  onClose,
  onDone,
}: {
  request: ServiceRequestDetail;
  action: ReasonAction;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    const text = reason.trim();
    try {
      if (action === 'cancel') await requestsApi.cancel(request.id, text, request.version);
      else if (action === 'put_on_hold') await requestsApi.hold(request.id, text, request.version);
      else await requestsApi.complete(request.id, text, request.version);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t(`requests.reason.${action}.title`)} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextArea
          label={t('requests.reason.label')}
          hint={t('requests.reason.hint')}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          maxLength={500}
          rows={3}
          required
          autoFocus
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant={action === 'cancel' ? 'danger' : 'primary'}
            busy={busy}
            disabled={reason.trim() === ''}
          >
            {t(`requests.action.${action}`)}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Facts, answers, approvals, tasks and the actions the server allows for the viewer. */
export function RequestDetail({
  request,
  onChanged,
}: {
  request: ServiceRequestDetail;
  onChanged: () => void;
}) {
  const { t, locale } = useI18n();
  const [dialog, setDialog] = useState<ReasonAction | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const name = (id: string | null) => (id ? (request.names[id] ?? id) : '–');

  const act = async (action: RequestAction) => {
    if (action === 'cancel' || action === 'put_on_hold' || action === 'complete') {
      setDialog(action);
      return;
    }
    setBusy(true);
    setError(undefined);
    try {
      await requestsApi.resume(request.id, request.version);
      onChanged();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="page-actions">
        {request.actions.map((action) => (
          <Button
            key={action}
            variant={action === 'cancel' ? 'danger' : 'secondary'}
            busy={busy}
            onClick={() => void act(action)}
          >
            {t(`requests.action.${action}`)}
          </Button>
        ))}
      </div>
      {error ? <ApiErrorAlert error={error} onRetry={onChanged} /> : null}
      <dl className="facts">
        <dt>{t('requests.fact.status')}</dt>
        <dd>
          <RequestStatusBadge status={request.status} />
          {request.waitingReason ? <> {t(`requests.waiting.${request.waitingReason}`)}</> : null}
        </dd>
        {request.statusReason ? (
          <>
            <dt>{t('requests.fact.reason')}</dt>
            <dd className="preline">{request.statusReason}</dd>
          </>
        ) : null}
        <dt>{t('requests.fact.requester')}</dt>
        <dd>{name(request.requesterId)}</dd>
        {request.requestedForId !== request.requesterId ? (
          <>
            <dt>{t('requests.fact.requestedFor')}</dt>
            <dd>{name(request.requestedForId)}</dd>
          </>
        ) : null}
        <dt>{t('requests.fact.submitted')}</dt>
        <dd>{formatDateTime(locale, request.submittedAt)}</dd>
        {request.completedAt ? (
          <>
            <dt>{t('requests.fact.completed')}</dt>
            <dd>{formatDateTime(locale, request.completedAt)}</dd>
          </>
        ) : null}
      </dl>

      <section>
        <h2>{t('requests.section.answers')}</h2>
        <dl className="facts">
          {request.fields.map((field) => (
            <div key={field.key} className="fact-row">
              <dt>{field.label}</dt>
              <dd className="preline">
                {answerText(
                  field,
                  request.answers[field.key],
                  request.names,
                  t('requests.answer.yes'),
                  t('requests.answer.no'),
                )}
              </dd>
            </div>
          ))}
        </dl>
      </section>

      <section>
        <h2>{t('requests.section.approvals')}</h2>
        {request.approvals.length === 0 ? (
          <p className="empty">{t('requests.approval.none')}</p>
        ) : null}
        <ul className="plain-list">
          {request.approvals.map((approval) => (
            <li key={approval.id}>
              <strong>{t('requests.approval.step', { step: approval.stepIndex + 1 })}</strong>{' '}
              <Badge
                tone={
                  approval.status === 'approved'
                    ? 'success'
                    : approval.status === 'rejected'
                      ? 'danger'
                      : 'neutral'
                }
              >
                {t(`approvals.status.${approval.status}`)}
              </Badge>
              {approval.decidedByUserId ? (
                <> {t('requests.approval.by', { name: name(approval.decidedByUserId) })}</>
              ) : null}
              {approval.decisionComment ? (
                <p className="preline">{approval.decisionComment}</p>
              ) : null}
            </li>
          ))}
        </ul>
      </section>

      <section>
        <h2>{t('requests.section.tasks')}</h2>
        {request.tasks.length === 0 ? <p className="empty">{t('requests.tasks.none')}</p> : null}
        <ul className="plain-list">
          {request.tasks.map((task) => (
            <li key={task.id}>
              <Link to={`/tasks/${encodeURIComponent(task.id)}`}>{task.title}</Link>{' '}
              <Badge>{t(`tasks.status.${task.status as 'open'}`)}</Badge>{' '}
              <span className="field-hint">
                {t(task.mandatory ? 'requests.task.mandatory' : 'requests.task.optional')}
              </span>
            </li>
          ))}
        </ul>
      </section>

      {dialog ? (
        <ReasonDialog
          request={request}
          action={dialog}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            onChanged();
          }}
        />
      ) : null}
    </>
  );
}
