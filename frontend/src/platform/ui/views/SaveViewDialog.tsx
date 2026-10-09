import { useState } from 'react';
import type { ApiError } from '../../api/client';
import { asApiError } from '../../api/useAsync';
import { useI18n } from '../../i18n/I18nProvider';
import { ApiErrorAlert } from '../ApiErrorAlert';
import { Button } from '../Button';
import { Dialog } from '../Dialog';
import { TextArea, TextField } from '../Field';

/** Name and description of a View: for a new View (save, save as) or to rename an existing one. */
export function SaveViewDialog({
  mode,
  initialName = '',
  initialDescription = '',
  onSubmit,
  onClose,
}: {
  mode: 'create' | 'edit';
  initialName?: string;
  initialDescription?: string;
  onSubmit: (name: string, description: string) => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(initialName);
  const [description, setDescription] = useState(initialDescription);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const trimmed = name.trim();
  return (
    <Dialog
      title={mode === 'create' ? t('views.save.titleNew') : t('views.save.titleEdit')}
      onClose={onClose}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!trimmed) return;
          setBusy(true);
          setError(undefined);
          onSubmit(trimmed, description.trim()).then(onClose, (cause: unknown) => {
            setError(asApiError(cause));
            setBusy(false);
          });
        }}
      >
        <TextField
          label={t('views.save.name')}
          value={name}
          maxLength={80}
          required
          autoFocus
          autoComplete="off"
          onChange={(event) => setName(event.target.value)}
        />
        <TextArea
          label={t('views.save.description')}
          value={description}
          maxLength={500}
          rows={3}
          onChange={(event) => setDescription(event.target.value)}
        />
        {mode === 'create' ? <p className="field-hint">{t('views.save.privateHint')}</p> : null}
        {error ? <ApiErrorAlert error={error} /> : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!trimmed}>
            {t('views.save.submit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
