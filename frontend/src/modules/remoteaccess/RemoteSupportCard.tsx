import { useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { useModules } from '../../platform/modules/ModulesProvider';
import { Link } from '../../platform/router/Router';
import { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { Card, Skeleton } from '../../platform/ui/Workspace';
import { TableDate } from '../../platform/ui/TableDate';
import { endpointsApi } from '../endpoints/api';
import type { Device, DeviceFilters } from '../endpoints/types';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { remoteAccessApi } from './api';
import { activeSession, providerAvailability, startableProviders } from './model';
import { SessionPanel } from './SessionPanel';
import { mapReasons, mismatchReasons, unmapReasons, type Capabilities } from './types';

const emptyFilters: DeviceFilters = {
  platform: '',
  compliance: '',
  q: '',
  linked: '',
  includeDeleted: false,
  managementState: '',
  hasFinding: '',
  osVersion: '',
  lastCheckinOlderThanDays: '',
};

/** Per-provider availability of one device with plain-language reasons. */
function CapabilityList({ caps }: { caps: Capabilities }) {
  const { t } = useI18n();
  if (!caps.enabled) return <p>{t('remoteaccess.reason.provider_disabled')}</p>;
  return (
    <>
      <p className="ra-fresh">
        {t('remoteaccess.lastObserved')}:{' '}
        {caps.observedAt ? <TableDate value={caps.observedAt} /> : t('remoteaccess.unknown')}
        {caps.lastCheckinAt ? (
          <>
            {' · '}
            {t('remoteaccess.lastCheckin')}: <TableDate value={caps.lastCheckinAt} />
          </>
        ) : null}
        {caps.stale ? <Badge tone="warning">{t('remoteaccess.stale')}</Badge> : null}
      </p>
      <ul className="ra-providers">
        {caps.providers.map((p) => {
          const a = providerAvailability(caps, p);
          return (
            <li key={p.provider}>
              <strong>{t(`remoteaccess.provider.${p.provider}` as MessageKey)}</strong>{' '}
              <Badge tone={a.state === 'available' ? 'success' : 'warning'}>
                {t(`remoteaccess.availability.${a.state}` as MessageKey)}
              </Badge>{' '}
              <small>
                {p.mapped
                  ? `${t('remoteaccess.peerMapped')}${p.peerId ? ` (${p.peerId})` : ''}`
                  : t('remoteaccess.peerNotMapped')}
              </small>
              {a.reasons.length > 0 ? (
                <ul className="ra-reasons">
                  {a.reasons.map((key) => (
                    <li key={key}>{t(key)}</li>
                  ))}
                </ul>
              ) : null}
            </li>
          );
        })}
      </ul>
    </>
  );
}

function StartDialog({
  ticketId,
  devices,
  caps,
  onClose,
  onDone,
}: {
  ticketId: string;
  devices: Device[];
  caps: Map<string, Capabilities>;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const startable = devices.filter((d) => startableProviders(caps.get(d.id)).length > 0);
  const [deviceId, setDeviceId] = useState(startable[0]?.id ?? '');
  const providers = startableProviders(caps.get(deviceId));
  const [provider, setProvider] = useState(providers[0]?.provider ?? '');
  const [reason, setReason] = useState('');
  const [note, setNote] = useState('');
  const [needReason, setNeedReason] = useState(false);
  const [needApprover, setNeedApprover] = useState(false);
  const [approverType, setApproverType] = useState<'user' | 'team'>('user');
  const [approver, setApprover] = useState<Assignee | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const effectiveProvider = providers.some((p) => p.provider === provider)
    ? provider
    : (providers[0]?.provider ?? '');

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await remoteAccessApi.request({
        deviceId,
        ticketId,
        provider: effectiveProvider,
        ...(needReason && reason ? { reason } : {}),
        ...(note.trim() ? { note: note.trim() } : {}),
        ...(needApprover && approver
          ? approverType === 'user'
            ? { approverUserId: approver.id }
            : { approverTeamId: approver.id }
          : {}),
      });
      onDone();
    } catch (cause) {
      const failure = asApiError(cause);
      if (failure.code === 'remoteaccess.holder_mismatch') setNeedReason(true);
      if (failure.code === 'remoteaccess.approver_required') setNeedApprover(true);
      setError(failure);
      setBusy(false);
    }
  };
  const missing = (needReason && !reason) || (needApprover && !approver);
  return (
    <Dialog title={t('remoteaccess.start')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        {devices.length > 1 ? (
          <Select
            label={t('remoteaccess.device')}
            value={deviceId}
            onChange={(event) => setDeviceId(event.target.value)}
            options={startable.map((d) => ({ value: d.id, label: d.name }))}
          />
        ) : (
          <p>
            <strong>{devices[0]?.name}</strong>
          </p>
        )}
        <Select
          label={t('remoteaccess.provider')}
          value={effectiveProvider}
          onChange={(event) => setProvider(event.target.value)}
          options={providers.map((p) => ({
            value: p.provider,
            label: t(`remoteaccess.provider.${p.provider}` as MessageKey),
          }))}
        />
        {needReason ? (
          <Select
            label={t('remoteaccess.mismatchReason')}
            hint={t('remoteaccess.mismatchHint')}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            options={[
              { value: '', label: t('remoteaccess.chooseReason') },
              ...mismatchReasons.map((value) => ({
                value,
                label: t(`remoteaccess.mismatch.${value}` as MessageKey),
              })),
            ]}
          />
        ) : null}
        {needApprover ? (
          <fieldset className="ra-approver">
            <legend>{t('remoteaccess.approver')}</legend>
            <p>{t('remoteaccess.approverHint')}</p>
            <Select
              label={t('remoteaccess.approverType')}
              value={approverType}
              onChange={(event) => {
                setApproverType(event.target.value as 'user' | 'team');
                setApprover(null);
              }}
              options={[
                { value: 'user', label: t('remoteaccess.approverUser') },
                { value: 'team', label: t('remoteaccess.approverTeam') },
              ]}
            />
            <AssigneePicker type={approverType} value={approver} onChange={setApprover} />
          </fieldset>
        ) : null}
        <TextArea
          label={t('remoteaccess.note')}
          hint={t('remoteaccess.noteHint')}
          maxLength={500}
          value={note}
          onChange={(event) => setNote(event.target.value)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant="primary"
            busy={busy}
            disabled={!deviceId || !effectiveProvider || missing}
          >
            {t('remoteaccess.start')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/**
 * Remote support card on the Ticket detail. Devices come from the Ticket's device snapshot (serial
 * number) and can be refined by a search; permissions only hide what the backend would refuse.
 */
export function TicketRemoteSupport({
  ticketId,
  deviceSnapshot,
}: {
  ticketId: string;
  deviceSnapshot: Record<string, unknown> | null;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const deviceAccess = can('endpoints.view') || can('endpoints.manage');
  const snapshotSerial =
    typeof deviceSnapshot?.serialNumber === 'string' ? deviceSnapshot.serialNumber : '';
  const [query, setQuery] = useState(snapshotSerial);
  const [draft, setDraft] = useState(snapshotSerial);
  const [dialog, setDialog] = useState(false);
  const found = useAsync(
    async (signal) => {
      if (!deviceAccess || !query.trim()) return [] as Device[];
      const page = await endpointsApi.devices(
        { ...emptyFilters, q: query.trim() },
        undefined,
        signal,
      );
      return page.items.slice(0, 5);
    },
    [query, deviceAccess],
  );
  const devices = found.data ?? [];
  const ids = devices.map((d) => d.id).join(',');
  const caps = useAsync(
    async (signal) =>
      new Map(
        await Promise.all(
          devices.map(
            async (d) => [d.id, await remoteAccessApi.capabilities(d.id, signal)] as const,
          ),
        ),
      ),
    [ids],
  );
  const sessions = useAsync(
    (signal) =>
      remoteAccessApi.sessions({ ticketId }, undefined, signal, 10).catch((cause: unknown) => {
        // Without session permissions the card still explains availability.
        if (cause instanceof ApiError && cause.status === 403) return { items: [] };
        throw cause;
      }),
    [ticketId],
  );
  const active = activeSession(sessions.data?.items ?? []);
  const canStart = can('remote_access.start_attended');
  const anyStartable = devices.some((d) => startableProviders(caps.data?.get(d.id)).length > 0);

  return (
    <Card title={t('remoteaccess.card.title')} className="ra-card">
      <h2>{t('remoteaccess.card.title')}</h2>
      {!deviceAccess ? <p>{t('remoteaccess.needDeviceAccess')}</p> : null}
      {deviceAccess ? (
        <form
          className="ra-search"
          role="search"
          onSubmit={(event) => {
            event.preventDefault();
            setQuery(draft);
          }}
        >
          <TextField
            label={t('remoteaccess.deviceSearch')}
            type="search"
            maxLength={100}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
          />
          <Button type="submit">{t('remoteaccess.search')}</Button>
        </form>
      ) : null}
      {found.loading || caps.loading ? <Skeleton lines={3} /> : null}
      {found.error ? <ApiErrorAlert error={found.error} onRetry={found.reload} /> : null}
      {caps.error ? <ApiErrorAlert error={caps.error} onRetry={caps.reload} /> : null}
      {deviceAccess && !found.loading && !found.error && devices.length === 0 ? (
        <p>{t('remoteaccess.noDevices')}</p>
      ) : null}
      {devices.map((d) => {
        const c = caps.data?.get(d.id);
        return (
          <section key={d.id} className="ra-device" aria-label={d.name}>
            <h3>{d.name}</h3>
            {c ? <CapabilityList caps={c} /> : null}
          </section>
        );
      })}
      {sessions.error ? <ApiErrorAlert error={sessions.error} onRetry={sessions.reload} /> : null}
      {active ? <SessionPanel session={active} onChanged={sessions.reload} /> : null}
      {canStart && !active && devices.length > 0 && !caps.loading ? (
        <>
          {!anyStartable ? <Alert kind="info">{t('remoteaccess.cannotStart')}</Alert> : null}
          <Button variant="primary" disabled={!anyStartable} onClick={() => setDialog(true)}>
            {t('remoteaccess.start')}
          </Button>
        </>
      ) : null}
      {dialog && caps.data ? (
        <StartDialog
          ticketId={ticketId}
          devices={devices}
          caps={caps.data}
          onClose={() => setDialog(false)}
          onDone={() => {
            setDialog(false);
            sessions.reload();
          }}
        />
      ) : null}
    </Card>
  );
}

function MappingDialog({
  deviceId,
  providers,
  unmap,
  onClose,
  onDone,
}: {
  deviceId: string;
  providers: string[];
  unmap: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const reasons = unmap ? unmapReasons : mapReasons;
  const [provider, setProvider] = useState(providers[0] ?? '');
  const [peerId, setPeerId] = useState('');
  const [reason, setReason] = useState<string>(reasons[0]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      if (unmap) await remoteAccessApi.unmapPeer(deviceId, provider, reason);
      else await remoteAccessApi.mapPeer(deviceId, provider, peerId.trim(), reason);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t(unmap ? 'remoteaccess.unmap' : 'remoteaccess.map')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <Select
          label={t('remoteaccess.provider')}
          value={provider}
          onChange={(event) => setProvider(event.target.value)}
          options={providers.map((value) => ({
            value,
            label: t(`remoteaccess.provider.${value}` as MessageKey),
          }))}
        />
        {!unmap ? (
          <TextField
            label={t('remoteaccess.peerId')}
            hint={t('remoteaccess.peerIdHint')}
            value={peerId}
            required
            maxLength={64}
            autoComplete="off"
            autoFocus
            onChange={(event) => setPeerId(event.target.value)}
          />
        ) : null}
        <Select
          label={t('remoteaccess.reason')}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          options={reasons.map((value) => ({
            value,
            label: t(`remoteaccess.${unmap ? 'unmapReason' : 'mapReason'}.${value}` as MessageKey),
          }))}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant="primary"
            busy={busy}
            disabled={!provider || (!unmap && !peerId.trim())}
          >
            {t(unmap ? 'remoteaccess.unmap' : 'remoteaccess.map')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Compact card on the Device detail: availability, recent sessions and mapping management. */
export function DeviceRemoteSupport({ deviceId }: { deviceId: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const admin = can('remote_access.admin');
  const [dialog, setDialog] = useState<'map' | 'unmap' | null>(null);
  const caps = useAsync((signal) => remoteAccessApi.capabilities(deviceId, signal), [deviceId]);
  const sessions = useAsync(
    (signal) =>
      remoteAccessApi.sessions({ deviceId }, undefined, signal, 5).catch((cause: unknown) => {
        if (cause instanceof ApiError && cause.status === 403) return { items: [] };
        throw cause;
      }),
    [deviceId],
  );
  const active = activeSession(sessions.data?.items ?? []);
  const providers = caps.data?.providers.map((p) => p.provider) ?? [];
  const done = () => {
    setDialog(null);
    caps.reload();
  };
  return (
    <Card title={t('remoteaccess.card.title')} className="ra-card">
      <h2>{t('remoteaccess.card.title')}</h2>
      {caps.loading && !caps.data ? <Skeleton lines={3} /> : null}
      {caps.error ? <ApiErrorAlert error={caps.error} onRetry={caps.reload} /> : null}
      {caps.data ? <CapabilityList caps={caps.data} /> : null}
      {active ? (
        <p>
          {t('remoteaccess.activeSession')}: {active.reference} (
          {t(`remoteaccess.status.${active.status}` as MessageKey)})
        </p>
      ) : null}
      <p>{t('remoteaccess.startFromTicket')}</p>
      {admin && providers.length > 0 ? (
        <div className="ra-actions">
          <Button onClick={() => setDialog('map')}>{t('remoteaccess.map')}</Button>
          {caps.data?.providers.some((p) => p.mapped) ? (
            <Button onClick={() => setDialog('unmap')}>{t('remoteaccess.unmap')}</Button>
          ) : null}
        </div>
      ) : null}
      {dialog ? (
        <MappingDialog
          deviceId={deviceId}
          providers={
            dialog === 'unmap'
              ? (caps.data?.providers.filter((p) => p.mapped).map((p) => p.provider) ?? [])
              : providers
          }
          unmap={dialog === 'unmap'}
          onClose={() => setDialog(null)}
          onDone={done}
        />
      ) : null}
    </Card>
  );
}

/**
 * Shown instead of the remote support card while the module is switched off: a disabled action
 * with the reason, and (for module administrators) a link to switch it on.
 */
export function RemoteSupportDisabled() {
  const { t } = useI18n();
  const { can } = useSession();
  return (
    <Card title={t('remoteaccess.card.title')}>
      <h2>{t('remoteaccess.card.title')}</h2>
      <Button disabled aria-describedby="remote-support-disabled-reason">
        {t('remoteaccess.disabled.action')}
      </Button>
      <p id="remote-support-disabled-reason" className="field-hint">
        {t('remoteaccess.disabled.reason')}{' '}
        {can('modules.manage') ? (
          <Link to="/admin/modules">{t('remoteaccess.disabled.admin')}</Link>
        ) : (
          t('remoteaccess.disabled.ask')
        )}
      </p>
    </Card>
  );
}

/** The remote support card of a ticket or device, or its disabled explanation. */
export function RemoteSupportGate({ children }: { children: ReactNode }) {
  const { enabled, loading } = useModules();
  if (loading) return null;
  return enabled('remoteaccess') ? children : <RemoteSupportDisabled />;
}
