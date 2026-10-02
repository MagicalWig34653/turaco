import { useState } from 'react';
import type { FormEvent } from 'react';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { isoToLocalInput, localInputToIso } from '../../platform/format/format';
import { Button } from '../../platform/ui/Button';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { severities, type BriefingItem, type Severity } from './types';

export type BriefingFormValues = {
  title: string;
  body: string;
  severity: Severity;
  /** RFC 3339, or null when empty. */
  validUntil: string | null;
};

type Props = {
  initial?: Pick<BriefingItem, 'title' | 'body' | 'severity' | 'validUntil'>;
  submitLabel: string;
  busy: boolean;
  onSubmit: (values: BriefingFormValues) => void;
  onCancel: () => void;
};

/** Title, body, severity and expiry of a draft; shared by creating and editing. */
export function BriefingForm({ initial, submitLabel, busy, onSubmit, onCancel }: Props) {
  const { t } = useI18n();
  const [title, setTitle] = useState(initial?.title ?? '');
  const [body, setBody] = useState(initial?.body ?? '');
  const [severity, setSeverity] = useState<Severity>(initial?.severity ?? 'info');
  const [until, setUntil] = useState(isoToLocalInput(initial?.validUntil));

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (title.trim() === '') return;
    onSubmit({ title: title.trim(), body, severity, validUntil: localInputToIso(until) ?? null });
  };

  return (
    <form className="form" onSubmit={submit}>
      <TextField
        label={t('tasks.field.title')}
        value={title}
        onChange={(event) => setTitle(event.target.value)}
        maxLength={200}
        required
        autoFocus
      />
      <TextArea
        label={t('briefing.field.body')}
        hint={t('briefing.field.bodyHint')}
        value={body}
        onChange={(event) => setBody(event.target.value)}
        maxLength={10000}
        rows={8}
      />
      <Select
        label={t('briefing.col.severity')}
        value={severity}
        onChange={(event) => setSeverity(event.target.value as Severity)}
        options={severities.map((value) => ({ value, label: t(`briefing.severity.${value}`) }))}
      />
      <TextField
        label={t('briefing.col.validUntil')}
        hint={t('briefing.field.validUntilHint')}
        type="datetime-local"
        value={until}
        onChange={(event) => setUntil(event.target.value)}
      />
      <div className="form-actions">
        <Button onClick={onCancel}>{t('action.cancel')}</Button>
        <Button type="submit" variant="primary" busy={busy} disabled={title.trim() === ''}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}
