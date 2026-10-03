import { impactUrl } from '../services/helpers';
import { infrastructureApi } from '../infrastructure/api';
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
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ReasonDialog } from '../../platform/ui/ReasonDialog';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { assetsApi } from './api';
import { AssetStatusBadge } from './AssetsScreen';
import {
  operationsWithAssignee,
  operationsWithReason,
  provisioningStatuses,
  type Asset,
  type AssetDetail,
  type AssetOperation,
  type ProvisioningStatus,
} from './types';

type Dialog =
  | { kind: 'reason'; op: AssetOperation }
  | { kind: 'assign'; op: AssetOperation }
  | { kind: 'edit' };

function AssignDialog({
  asset,
  op,
  onClose,
  onDone,
}: {
  asset: Asset;
  op: AssetOperation;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [type, setType] = useState<'user' | 'team'>('user');
  const [assignee, setAssignee] = useState<Assignee | null>(null);
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!assignee) return;
    setBusy(true);
    setError(undefined);
    try {
      await assetsApi.operate(asset.id, op, {
        expectedVersion: asset.version,
        assigneeType: type,
        assigneeId: assignee.id,
        note: note.trim(),
      });
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t(`assets.op.${op}` as MessageKey)} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <Select
          label={t('assets.assign.type')}
          value={type}
          onChange={(event) => {
            setType(event.target.value as 'user' | 'team');
            setAssignee(null);
          }}
          options={[
            { value: 'user', label: t('assets.assign.user') },
            { value: 'team', label: t('assets.assign.team') },
          ]}
        />
        <AssigneePicker key={type} type={type} value={assignee} onChange={setAssignee} />
        <TextField
          label={t('assets.assign.note')}
          value={note}
          maxLength={500}
          onChange={(event) => setNote(event.target.value)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!assignee}>
            {t(`assets.op.${op}` as MessageKey)}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function EditDialog({
  asset,
  onClose,
  onDone,
}: {
  asset: Asset;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [serial, setSerial] = useState(asset.serialNumber ?? '');
  const [tag, setTag] = useState(asset.assetTag ?? '');
  const [notes, setNotes] = useState(asset.notes ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await assetsApi.update(asset.id, {
        expectedVersion: asset.version,
        serialNumber: serial.trim(),
        assetTag: tag.trim(),
        notes: notes.trim(),
      });
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('assets.action.edit')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
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
        <TextArea
          label={t('assets.field.notes')}
          value={notes}
          maxLength={2000}
          rows={3}
          onChange={(event) => setNotes(event.target.value)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy}>
            {t('assets.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function AssetDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const loaded = useAsync((signal) => assetsApi.get(id, signal), [id]);
  const canSeeLocation =
    (can('infrastructure.view') || can('infrastructure.manage')) &&
    (can('assets.view') || can('assets.manage'));
  const location = useAsync(
    (signal) =>
      canSeeLocation ? infrastructureApi.assetLocation(id, signal) : Promise.resolve(undefined),
    [id, canSeeLocation],
  );
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [busyOp, setBusyOp] = useState<string | null>(null);
  const [actionError, setActionError] = useState<ApiError | undefined>(undefined);

  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const asset: AssetDetail | undefined = loaded.data;
  if (!asset) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }
  const name = (key: string | null) => (key ? (asset.names[key] ?? key) : '–');
  const done = () => {
    setDialog(null);
    setActionError(undefined);
    loaded.reload();
  };

  const run = async (op: AssetOperation) => {
    setBusyOp(op);
    setActionError(undefined);
    try {
      await assetsApi.operate(asset.id, op, { expectedVersion: asset.version });
      done();
    } catch (cause) {
      setActionError(asApiError(cause));
    } finally {
      setBusyOp(null);
    }
  };

  const start = (op: AssetOperation) => {
    if (operationsWithAssignee.includes(op)) setDialog({ kind: 'assign', op });
    else if (operationsWithReason.includes(op)) setDialog({ kind: 'reason', op });
    else void run(op);
  };

  const setProvisioning = async (status: ProvisioningStatus) => {
    setActionError(undefined);
    try {
      await assetsApi.setProvisioning(asset.id, status, asset.version);
      done();
    } catch (cause) {
      setActionError(asApiError(cause));
    }
  };

  const manage = can('assets.manage');
  const ops = asset.allowedOperations as AssetOperation[];
  return (
    <>
      <PageHeader
        title={`${asset.reference} · ${name(asset.productId)}`}
        actions={
          manage ? (
            <>
              <Button onClick={() => setDialog({ kind: 'edit' })}>{t('assets.action.edit')}</Button>
              {ops.map((op) => (
                <Button
                  key={op}
                  variant={op === 'dispose' || op === 'mark_lost' ? 'danger' : 'secondary'}
                  busy={busyOp === op}
                  disabled={busyOp !== null}
                  onClick={() => start(op)}
                >
                  {t(`assets.op.${op}` as MessageKey)}
                </Button>
              ))}
            </>
          ) : null
        }
      />
      <p>
        <Link to={can('assets.view') || manage ? '/assets' : '/my-assets'}>{t('assets.back')}</Link>
      </p>
      {actionError ? <ApiErrorAlert error={actionError} onRetry={loaded.reload} /> : null}
      {(can('services.view') || can('services.manage')) && (
        <p>
          <Link to={impactUrl('asset', asset.id)}>{t('services.impact')}</Link>
        </p>
      )}
      <dl className="facts">
        <dt>{t('assets.col.status')}</dt>
        <dd>
          <AssetStatusBadge status={asset.status} />
          {asset.statusReason ? <> {asset.statusReason}</> : null}
        </dd>
        <dt>{t('assets.field.serial')}</dt>
        <dd>{asset.serialNumber ?? '–'}</dd>
        <dt>{t('assets.field.tag')}</dt>
        <dd>{asset.assetTag ?? '–'}</dd>
        <dt>{t('assets.field.ownership')}</dt>
        <dd>{t(`assets.ownership.${asset.ownershipType}`)}</dd>
        <dt>{t('assets.field.provisioning')}</dt>
        <dd>
          {manage ? (
            <Select
              label={t('assets.field.provisioning')}
              value={asset.provisioningStatus}
              onChange={(event) => void setProvisioning(event.target.value as ProvisioningStatus)}
              options={provisioningStatuses.map((value) => ({
                value,
                label: t(`assets.provisioning.${value}`),
              }))}
            />
          ) : (
            t(`assets.provisioning.${asset.provisioningStatus}`)
          )}
        </dd>
        <dt>{t('assets.field.location')}</dt>
        <dd>{name(asset.locationId)}</dd>
        <dt>{t('assets.field.purchasedAt')}</dt>
        <dd>{asset.purchasedAt ? formatDateTime(locale, asset.purchasedAt) : '–'}</dd>
        <dt>{t('assets.field.warrantyUntil')}</dt>
        <dd>{asset.warrantyUntil ? asset.warrantyUntil.slice(0, 10) : '–'}</dd>
        {asset.notes ? (
          <>
            <dt>{t('assets.field.notes')}</dt>
            <dd className="preline">{asset.notes}</dd>
          </>
        ) : null}
      </dl>
      {canSeeLocation && (
        <section>
          <h2>{t('infra.assetLocation')}</h2>
          {location.error && <ApiErrorAlert error={location.error} onRetry={location.reload} />}
          {location.data &&
            (location.data.placed ? (
              <p>
                {location.data.siteName} →{' '}
                <Link to={`/infrastructure/buildings/${location.data.buildingId}`}>
                  {location.data.buildingName}
                </Link>{' '}
                →{' '}
                <Link to={`/infrastructure/rooms/${location.data.roomId}`}>
                  {location.data.roomName}
                </Link>{' '}
                →{' '}
                <Link to={`/infrastructure/racks/${location.data.rackId}`}>
                  {location.data.rackName}
                </Link>{' '}
                · {location.data.uPosition} U ·{' '}
                {t(location.data.face === 'rear' ? 'infra.rear' : 'infra.front')}
              </p>
            ) : (
              <p>{t('infra.unplaced')}</p>
            ))}
        </section>
      )}
      <section>
        <h2>{t('assets.section.assignments')}</h2>
        {asset.assignments.length === 0 ? (
          <p className="empty">{t('assets.assignments.none')}</p>
        ) : null}
        <ul className="plain-list">
          {asset.assignments.map((a) => (
            <li key={a.id}>
              <strong>{name(a.assigneeId)}</strong> ({t(`assets.assign.${a.assigneeType}`)}) ·{' '}
              {formatDateTime(locale, a.assignedAt)}
              {' – '}
              {a.returnedAt
                ? formatDateTime(locale, a.returnedAt)
                : t('assets.assignments.current')}
              {a.note ? <span className="field-hint"> · {a.note}</span> : null}
            </li>
          ))}
        </ul>
      </section>
      {dialog?.kind === 'reason' ? (
        <ReasonDialog
          title={t(`assets.op.${dialog.op}` as MessageKey)}
          label={t('assets.reason.label')}
          hint={t('assets.reason.hint')}
          confirmLabel={t(`assets.op.${dialog.op}` as MessageKey)}
          danger={dialog.op === 'dispose' || dialog.op === 'mark_lost'}
          onClose={() => setDialog(null)}
          onSubmit={async (reason) => {
            await assetsApi.operate(asset.id, dialog.op, {
              expectedVersion: asset.version,
              reason,
            });
            done();
          }}
        />
      ) : null}
      {dialog?.kind === 'assign' ? (
        <AssignDialog asset={asset} op={dialog.op} onClose={() => setDialog(null)} onDone={done} />
      ) : null}
      {dialog?.kind === 'edit' ? (
        <EditDialog asset={asset} onClose={() => setDialog(null)} onDone={done} />
      ) : null}
    </>
  );
}
