import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ProductPicker, type ProductChoice } from '../products/ProductPicker';
import { assetsApi } from './api';
import { ownershipTypes } from './types';

export function AssetCreateScreen() {
  const { t } = useI18n();
  const [product, setProduct] = useState<ProductChoice | null>(null);
  const [serial, setSerial] = useState('');
  const [tag, setTag] = useState('');
  const [ownership, setOwnership] = useState<string>('owned');
  const [status, setStatus] = useState<'available' | 'received'>('available');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!product) return;
    setBusy(true);
    setError(undefined);
    try {
      const created = await assetsApi.create({
        productId: product.id,
        serialNumber: serial.trim(),
        assetTag: tag.trim(),
        ownershipType: ownership,
        status,
        notes: notes.trim(),
      });
      navigate(`/assets/${encodeURIComponent(created.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <>
      <PageHeader title={t('assets.create.title')} intro={t('assets.create.intro')} />
      <p>
        <Link to="/assets">{t('assets.back')}</Link>
      </p>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <ProductPicker value={product} onChange={setProduct} />
        <TextField
          label={t('assets.field.serial')}
          value={serial}
          maxLength={100}
          onChange={(event) => setSerial(event.target.value)}
        />
        <TextField
          label={t('assets.field.tag')}
          value={tag}
          maxLength={50}
          onChange={(event) => setTag(event.target.value)}
        />
        <Select
          label={t('assets.field.ownership')}
          value={ownership}
          onChange={(event) => setOwnership(event.target.value)}
          options={ownershipTypes.map((value) => ({
            value,
            label: t(`assets.ownership.${value}`),
          }))}
        />
        <Select
          label={t('assets.field.initialStatus')}
          value={status}
          onChange={(event) => setStatus(event.target.value as 'available' | 'received')}
          options={[
            { value: 'available', label: t('assets.status.available') },
            { value: 'received', label: t('assets.status.received') },
          ]}
        />
        <TextArea
          label={t('assets.field.notes')}
          value={notes}
          maxLength={2000}
          rows={3}
          onChange={(event) => setNotes(event.target.value)}
        />
        <div className="form-actions">
          <Button type="submit" variant="primary" busy={busy} disabled={!product}>
            {t('assets.create.submit')}
          </Button>
        </div>
      </form>
    </>
  );
}
