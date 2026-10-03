import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { endpointsApi } from './api';
import { DeviceManagementSection } from './ManagementScreens';
import {
  reasonCodes,
  type DeviceDetail,
  type Finding,
  type Installation,
  type ReasonCode,
} from './types';

function LinkDialog({
  device,
  unlink,
  onClose,
  onDone,
}: {
  device: DeviceDetail;
  unlink: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [assetId, setAssetId] = useState('');
  const [reason, setReason] = useState<ReasonCode>('serial_confirmed');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      if (unlink) await endpointsApi.unlink(device.id, reason, device.version);
      else await endpointsApi.link(device.id, assetId.trim(), reason, device.version);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t(unlink ? 'endpoints.unlink' : 'endpoints.link')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        {!unlink ? (
          <TextField
            label={t('endpoints.assetId')}
            value={assetId}
            required
            autoFocus
            onChange={(e) => setAssetId(e.target.value)}
          />
        ) : null}
        <Select
          label={t('endpoints.reason')}
          value={reason}
          onChange={(e) => setReason(e.target.value as ReasonCode)}
          options={reasonCodes.map((value) => ({ value, label: t(`endpoints.reason.${value}`) }))}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!unlink && !assetId.trim()}>
            {t(unlink ? 'endpoints.unlink' : 'endpoints.link')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function DeviceDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const loaded = useAsync((signal) => endpointsApi.device(id, signal), [id]);
  const [dialog, setDialog] = useState<'link' | 'unlink' | null>(null);
  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const d = loaded.data;
  if (!d)
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  const date = (value: string | null) => (value ? formatDateTime(locale, value) : '–');
  const softwareColumns: Column<Installation>[] = [
    {
      key: 'name',
      header: t('endpoints.softwareName'),
      render: (item) => item.productName ?? item.rawName,
    },
    { key: 'raw', header: t('endpoints.rawName'), render: (item) => item.rawName },
    { key: 'version', header: t('endpoints.version'), render: (item) => item.rawVersion },
    {
      key: 'publisher',
      header: t('endpoints.publisher'),
      render: (item) => item.rawPublisher ?? '–',
    },
    { key: 'observed', header: t('endpoints.observedAt'), render: (item) => date(item.observedAt) },
  ];
  const findingColumns: Column<Finding>[] = [
    {
      key: 'kind',
      header: t('endpoints.findingKind'),
      render: (f) => t(`endpoints.finding.${f.kind}` as MessageKey),
    },
    {
      key: 'status',
      header: t('endpoints.findingStatus'),
      render: (f) => t(`endpoints.findingStatus.${f.status}` as MessageKey),
    },
    { key: 'raised', header: t('endpoints.raisedAt'), render: (f) => date(f.raisedAt) },
  ];
  return (
    <>
      <PageHeader
        title={d.name}
        actions={
          can('endpoints.manage') && !d.deletedObservedAt ? (
            <>
              {!d.assetId ? (
                can('assets.view') ? (
                  <Button onClick={() => setDialog('link')}>{t('endpoints.link')}</Button>
                ) : null
              ) : (
                <>
                  <Button onClick={() => setDialog('unlink')}>{t('endpoints.unlink')}</Button>
                </>
              )}
            </>
          ) : null
        }
      />
      <p>
        <Link to="/devices">{t('endpoints.back')}</Link>
      </p>
      <dl className="facts">
        <dt>{t('endpoints.provider')}</dt>
        <dd>{d.provider}</dd>
        <dt>{t('endpoints.externalId')}</dt>
        <dd>{d.externalId}</dd>
        <dt>{t('endpoints.serialNumber')}</dt>
        <dd>{d.serialNumber ?? '–'}</dd>
        <dt>{t('endpoints.asset')}</dt>
        <dd>
          {d.assetId ? (
            can('assets.view') || can('assets.manage') ? (
              <Link to={`/assets/${encodeURIComponent(d.assetId)}`}>{d.assetId}</Link>
            ) : (
              d.assetId
            )
          ) : (
            '–'
          )}
        </dd>
        <dt>{t('endpoints.assetLinkSource')}</dt>
        <dd>
          {d.assetLinkSource ? t(`endpoints.linkSource.${d.assetLinkSource}` as MessageKey) : '–'}
        </dd>
        <dt>{t('endpoints.autoLinkBlocked')}</dt>
        <dd>{t(d.autoLinkBlocked ? 'endpoints.yes' : 'endpoints.no')}</dd>
        <dt>{t('endpoints.platform')}</dt>
        <dd>{t(`endpoints.platform.${d.osPlatform}` as MessageKey)}</dd>
        <dt>{t('endpoints.osVersion')}</dt>
        <dd>{d.osVersion ?? '–'}</dd>
        <dt>{t('endpoints.manufacturer')}</dt>
        <dd>{d.manufacturer ?? '–'}</dd>
        <dt>{t('endpoints.model')}</dt>
        <dd>{d.model ?? '–'}</dd>
        <dt>{t('endpoints.ownership')}</dt>
        <dd>{t(`endpoints.ownership.${d.ownership}` as MessageKey)}</dd>
        <dt>{t('endpoints.compliance')}</dt>
        <dd>{t(`endpoints.compliance.${d.complianceState}` as MessageKey)}</dd>
        <dt>{t('endpoints.lastCheckinAt')}</dt>
        <dd>{date(d.lastCheckinAt)}</dd>
        <dt>{t('endpoints.source')}</dt>
        <dd>{t(`endpoints.source.${d.source}` as MessageKey)}</dd>
        <dt>{t('endpoints.observedAt')}</dt>
        <dd>{date(d.observedAt)}</dd>
        <dt>{t('endpoints.lastSyncedAt')}</dt>
        <dd>{date(d.lastSyncedAt)}</dd>
        <dt>{t('endpoints.deletedObservedAt')}</dt>
        <dd>{date(d.deletedObservedAt)}</dd>
      </dl>
      <section>
        <h2>{t('endpoints.software')}</h2>
        <DataTable
          caption={t('endpoints.software')}
          columns={softwareColumns}
          rows={d.software}
          rowKey={(item) => item.id}
          emptyText={t('endpoints.softwareEmpty')}
        />
      </section>
      <section>
        <h2>{t('endpoints.openFindings')}</h2>
        <p>
          <Link to={`/endpoint-findings?deviceId=${encodeURIComponent(d.id)}`}>
            {t('endpoints.allFindings')}
          </Link>
        </p>
        <DataTable
          caption={t('endpoints.openFindings')}
          columns={findingColumns}
          rows={d.findings}
          rowKey={(item) => item.id}
          emptyText={t('endpoints.findingsEmpty')}
        />
      </section>
      {(can('endpoint.management.view') || can('endpoints.manage')) &&
      (can('endpoints.view') || can('endpoints.manage')) ? (
        <DeviceManagementSection id={d.id} />
      ) : null}
      {dialog ? (
        <LinkDialog
          device={d}
          unlink={dialog === 'unlink'}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            loaded.reload();
          }}
        />
      ) : null}
    </>
  );
}
