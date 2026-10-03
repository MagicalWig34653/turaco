import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { TextArea } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { requestsApi } from '../requests/api';
import { RequestDetail } from '../requests/RequestDetail';
import { approvalsApi } from './api';
import { ApprovalStatusBadge } from './ApprovalsScreen';
import type { Approval } from './types';

function DecisionDialog({
  approval,
  decision,
  onClose,
  onDone,
}: {
  approval: Approval;
  decision: 'approve' | 'reject';
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [comment, setComment] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await approvalsApi.decide(approval.id, decision, comment.trim(), approval.version);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  const needsComment = decision === 'reject';
  return (
    <Dialog title={t(`approvals.decision.${decision}.title`)} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextArea
          label={t('approvals.comment.label')}
          hint={t(`approvals.comment.hint.${decision}`)}
          value={comment}
          onChange={(event) => setComment(event.target.value)}
          maxLength={1000}
          rows={3}
          required={needsComment}
          autoFocus
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant={needsComment ? 'danger' : 'primary'}
            busy={busy}
            disabled={needsComment && comment.trim() === ''}
          >
            {t(`approvals.${decision}`)}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function ApprovalDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const approval = useAsync((signal) => approvalsApi.get(id, signal), [id]);
  const subjectId =
    approval.data?.subjectType === 'service_request' ? approval.data.subjectId : undefined;
  const request = useAsync(
    (signal) => (subjectId ? requestsApi.get(subjectId, signal) : Promise.resolve(undefined)),
    [subjectId],
  );
  const [dialog, setDialog] = useState<'approve' | 'reject' | null>(null);
  const [decided, setDecided] = useState(false);

  if (approval.error) return <ApiErrorAlert error={approval.error} onRetry={approval.reload} />;
  const current = approval.data;
  if (!current) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }
  return (
    <>
      <PageHeader
        title={current.subjectLabel}
        actions={
          current.status === 'pending' ? (
            <>
              <Button variant="primary" onClick={() => setDialog('approve')}>
                {t('approvals.approve')}
              </Button>
              <Button variant="danger" onClick={() => setDialog('reject')}>
                {t('approvals.reject')}
              </Button>
            </>
          ) : null
        }
      />
      <p>
        <Link to="/approvals">{t('approvals.back')}</Link>
      </p>
      {decided ? <Alert kind="success">{t('approvals.decided')}</Alert> : null}
      <dl className="facts">
        <dt>{t('approvals.col.status')}</dt>
        <dd>
          <ApprovalStatusBadge status={current.status} />
        </dd>
        {current.decidedAt ? (
          <>
            <dt>{t('approvals.fact.decidedAt')}</dt>
            <dd>{formatDateTime(locale, current.decidedAt)}</dd>
          </>
        ) : null}
        {current.decisionComment ? (
          <>
            <dt>{t('approvals.fact.comment')}</dt>
            <dd className="preline">{current.decisionComment}</dd>
          </>
        ) : null}
      </dl>
      {current.subjectType === 'purchase_order' ? (
        <p>
          {can('procurement.view') || can('procurement.manage') ? (
            <Link to={`/procurement/orders/${encodeURIComponent(current.subjectId)}`}>
              {t('approvals.openOrder')}
            </Link>
          ) : (
            t('approvals.orderNoAccess')
          )}
        </p>
      ) : null}
      {request.error ? <ApiErrorAlert error={request.error} onRetry={request.reload} /> : null}
      {request.data ? <RequestDetail request={request.data} onChanged={request.reload} /> : null}
      {dialog ? (
        <DecisionDialog
          approval={current}
          decision={dialog}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            setDecided(true);
            approval.reload();
            request.reload();
          }}
        />
      ) : null}
    </>
  );
}
