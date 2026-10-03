import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../api/client';
import { asApiError } from '../api/useAsync';
import { useI18n } from '../i18n/I18nProvider';
import { ApiErrorAlert } from './ApiErrorAlert';
import { Button } from './Button';
import { Dialog } from './Dialog';
import { TextArea } from './Field';

/**
 * A dialog that asks for a short text (a reason or comment) and runs an action with it. Busy and
 * error state are handled here; the caller closes the dialog when `onSubmit` resolves.
 */
export function ReasonDialog({
  title,
  label,
  hint,
  confirmLabel,
  danger,
  required = true,
  onSubmit,
  onClose,
}: {
  title: string;
  label: string;
  hint?: string | undefined;
  confirmLabel: string;
  danger?: boolean;
  required?: boolean;
  onSubmit: (reason: string) => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await onSubmit(reason.trim());
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={title} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextArea
          label={label}
          hint={hint}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          maxLength={500}
          rows={3}
          required={required}
          autoFocus
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant={danger ? 'danger' : 'primary'}
            busy={busy}
            disabled={required && reason.trim() === ''}
          >
            {confirmLabel}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
