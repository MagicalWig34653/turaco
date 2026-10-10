import { useRef, useState } from 'react';
import type { DragEvent } from 'react';
import type { ApiError } from '../api/client';
import { asApiError, useAsync } from '../api/useAsync';
import { formatDateTime } from '../format/format';
import { useI18n } from '../i18n/I18nProvider';
import { Alert, Badge } from '../ui/Alert';
import { ApiErrorAlert } from '../ui/ApiErrorAlert';
import { Button } from '../ui/Button';
import { ConfirmDialog } from '../ui/Dialog';
import { Select } from '../ui/Field';
import { Card } from '../ui/Workspace';
import { attachmentsApi } from './api';
import {
  canDownload,
  checkFile,
  formatBytes,
  scanTone,
  uploadAudience,
  type Attachment,
  type AttachmentAudience,
} from './model';

type Props = {
  ownerType: string;
  ownerId: string;
  /** Show the file picker (the server still decides). */
  canUpload: boolean;
  /** Show delete buttons (the server still decides). */
  canDelete: boolean;
  /** Offer the staff-only audience choice. */
  staff?: boolean;
};

/** Reusable attachments panel (ADR-0037). Renders nothing while attachments are not configured. */
export function AttachmentsPanel({ ownerType, ownerId, canUpload, canDelete, staff }: Props) {
  const { t, locale } = useI18n();
  const config = useAsync((signal) => attachmentsApi.config(signal), []);
  const enabled = config.data?.enabled === true;
  const list = useAsync(
    (signal) =>
      enabled
        ? attachmentsApi.list(ownerType, ownerId, signal).then((r) => r.items)
        : Promise.resolve([] as Attachment[]),
    [ownerType, ownerId, enabled],
  );
  const inputRef = useRef<HTMLInputElement>(null);
  const [audience, setAudience] = useState<AttachmentAudience>('all');
  const [busy, setBusy] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [problem, setProblem] = useState<string | undefined>(undefined);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [toDelete, setToDelete] = useState<Attachment | undefined>(undefined);

  if (config.error) {
    if (config.error.code === 'attachments.not_configured') return null;
    return <ApiErrorAlert error={config.error} onRetry={config.reload} />;
  }
  if (!config.data || !config.data.enabled) return null;
  const cfg = config.data;
  const items = list.data ?? [];

  const upload = async (files: FileList | File[]) => {
    setProblem(undefined);
    setError(undefined);
    setBusy(true);
    let count = items.length;
    try {
      for (const file of Array.from(files)) {
        const check = checkFile(file, cfg, count);
        if (check) {
          setProblem(
            t(`attachments.check.${check}`, { name: file.name, max: formatBytes(cfg.maxBytes) }),
          );
          break;
        }
        await attachmentsApi.upload(ownerType, ownerId, file, uploadAudience(!!staff, audience));
        count += 1;
      }
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
      list.reload();
      if (inputRef.current) inputRef.current.value = '';
    }
  };

  const onDrop = (event: DragEvent) => {
    event.preventDefault();
    setDragging(false);
    if (canUpload && !busy) void upload(event.dataTransfer.files);
  };

  const remove = async () => {
    if (!toDelete) return;
    setBusy(true);
    setError(undefined);
    try {
      await attachmentsApi.remove(toDelete.id);
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setToDelete(undefined);
      setBusy(false);
      list.reload();
    }
  };

  return (
    <Card title={t('attachments.title')}>
      <h2>{t('attachments.title')}</h2>
      {list.error ? <ApiErrorAlert error={list.error} onRetry={list.reload} /> : null}
      {error ? <ApiErrorAlert error={error} /> : null}
      {problem ? <Alert kind="warning">{problem}</Alert> : null}
      {!list.error && !list.loading && items.length === 0 ? <p>{t('attachments.empty')}</p> : null}
      {items.length > 0 ? (
        <ul className="attachments-list">
          {items.map((a) => (
            <li key={a.id} className="attachments-item">
              <span className="attachments-name">{a.fileName}</span>
              <span>
                {formatBytes(a.sizeBytes)} · {formatDateTime(locale, a.createdAt)}
              </span>
              <Badge tone={scanTone(a.scanStatus)}>{t(`attachments.scan.${a.scanStatus}`)}</Badge>
              {a.audience === 'privileged' ? (
                <Badge tone="info">{t('attachments.audience.privileged')}</Badge>
              ) : null}
              {canDownload(a) ? (
                <a className="btn btn-secondary" href={attachmentsApi.contentUrl(a.id)} download>
                  {t('attachments.download')}
                </a>
              ) : (
                <span>{t(`attachments.unavailable.${a.scanStatus}`)}</span>
              )}
              {canDelete ? (
                <Button disabled={busy} onClick={() => setToDelete(a)}>
                  {t('attachments.delete')}
                </Button>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}
      {items.some((a) => a.scanStatus === 'pending') ? (
        <Button onClick={list.reload}>{t('action.refresh')}</Button>
      ) : null}
      {canUpload ? (
        <div
          className="attachments-drop"
          data-active={dragging}
          onDragOver={(event) => {
            event.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={onDrop}
        >
          <p>{t('attachments.drop')}</p>
          <p className="field-hint">
            {t('attachments.limits', { max: formatBytes(cfg.maxBytes) })}
          </p>
          {staff ? (
            <Select
              label={t('attachments.audience.label')}
              value={audience}
              onChange={(event) => setAudience(event.target.value as AttachmentAudience)}
              options={[
                { value: 'all', label: t('attachments.audience.all') },
                { value: 'privileged', label: t('attachments.audience.privileged') },
              ]}
            />
          ) : null}
          <input
            ref={inputRef}
            type="file"
            multiple
            hidden
            accept={cfg.allowedTypes.join(',')}
            onChange={(event) => event.target.files && void upload(event.target.files)}
          />
          <Button busy={busy} onClick={() => inputRef.current?.click()}>
            {t('attachments.choose')}
          </Button>
        </div>
      ) : null}
      {toDelete ? (
        <ConfirmDialog
          title={t('attachments.delete.title')}
          message={t('attachments.delete.message', { name: toDelete.fileName })}
          confirmLabel={t('attachments.delete')}
          danger
          busy={busy}
          onConfirm={() => void remove()}
          onCancel={() => setToDelete(undefined)}
        />
      ) : null}
    </Card>
  );
}
