import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { assetsApi } from '../assets/api';
import { IncidentBanner } from '../incidents/IncidentBanner';
import { Suggestions } from '../knowledge/Suggestions';
import { ticketsApi } from './api';

/** Raising a ticket asks for little: what is wrong, and optionally which of my devices. */
export function TicketCreateScreen() {
  const { t } = useI18n();
  const devices = useAsync(async (signal) => assetsApi.mine(undefined, signal), []);
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [assetId, setAssetId] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const created = await ticketsApi.create({
        title: title.trim(),
        description: description.trim(),
        ...(assetId ? { assetId } : {}),
      });
      navigate(`/support/${encodeURIComponent(created.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  const names = devices.data?.productNames ?? {};
  return (
    <>
      <PageHeader title={t('tickets.create.title')} intro={t('tickets.create.intro')} />
      <p>
        <Link to="/support">{t('tickets.back')}</Link>
      </p>
      <IncidentBanner />
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextField
          label={t('tickets.field.title')}
          value={title}
          maxLength={200}
          required
          autoFocus
          onChange={(event) => setTitle(event.target.value)}
        />
        <Suggestions text={title} />
        <TextArea
          label={t('tickets.field.description')}
          hint={t('tickets.field.description.hint')}
          value={description}
          rows={5}
          maxLength={5000}
          onChange={(event) => setDescription(event.target.value)}
        />
        {devices.data && devices.data.items.length > 0 ? (
          <Select
            label={t('tickets.field.device')}
            hint={t('tickets.field.device.hint')}
            value={assetId}
            onChange={(event) => setAssetId(event.target.value)}
            options={[
              { value: '', label: t('tickets.field.device.none') },
              ...devices.data.items.map((a) => ({
                value: a.id,
                label: `${names[a.productId] ?? a.reference} (${a.reference})`,
              })),
            ]}
          />
        ) : null}
        <div className="form-actions">
          <Button type="submit" variant="primary" busy={busy} disabled={title.trim() === ''}>
            {t('tickets.create.submit')}
          </Button>
        </div>
      </form>
    </>
  );
}
