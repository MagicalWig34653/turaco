import { useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select } from '../../platform/ui/Field';
import { deploymentsApi } from './api';
import { cancelReasons, type Deployment } from './types';

export function CancelDialog({
  plan,
  onClose,
  onDone,
}: {
  plan: Pick<Deployment, 'id' | 'name' | 'version' | 'status'>;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await deploymentsApi.cancel(plan.id, reason, plan.version);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('deployments.cancel.title', { name: plan.name })} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        <div className="dialog-body">
          <p>{t('deployments.cancel.text')}</p>
          {plan.status === 'pending_approval' ? <p>{t('deployments.cancel.approvalToo')}</p> : null}
        </div>
        <Select
          label={t('deployments.cancel.reason')}
          value={reason}
          required
          autoFocus
          onChange={(event) => setReason(event.target.value)}
          options={[
            { value: '', label: t('deployments.cancel.choose') },
            ...cancelReasons.map((value) => ({
              value,
              label: t(`deployments.cancelReason.${value}`),
            })),
          ]}
        />
        {error ? <ApiErrorAlert error={error} /> : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="danger" busy={busy} disabled={!reason}>
            {t('deployments.action.cancel')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
