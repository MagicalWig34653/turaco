import { useState, type FormEvent, type ReactNode } from 'react';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import type { ApiError } from '../../platform/api/client';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { infrastructureApi as api } from './api';
import { buildElevation } from './elevation';
import type { Placement } from './types';

type Field = {
  key: string;
  label: string;
  value?: string | undefined;
  type?: string;
  required?: boolean;
};
function Editor({
  title,
  fields,
  submit,
  onClose,
}: {
  title: string;
  fields: Field[];
  submit: (values: Record<string, string>) => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [values, setValues] = useState<Record<string, string>>(
    Object.fromEntries(fields.map((f) => [f.key, f.value ?? ''])),
  );
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const save = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await submit(values);
      onClose();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title={title} onClose={onClose}>
      <form className="form" onSubmit={(event) => void save(event)}>
        {error && <ApiErrorAlert error={error} />}
        {fields.map((f) => (
          <label key={f.key}>
            {f.label}
            {f.key === 'face' ? (
              <select
                value={values[f.key] ?? 'front'}
                onChange={(event) => setValues({ ...values, [f.key]: event.target.value })}
              >
                <option value="front">{t('infra.front')}</option>
                <option value="rear">{t('infra.rear')}</option>
              </select>
            ) : (
              <input
                type={f.type ?? 'text'}
                required={f.required}
                min={f.type === 'number' ? 1 : undefined}
                max={f.key === 'heightU' ? 60 : undefined}
                value={values[f.key] ?? ''}
                onChange={(event) => setValues({ ...values, [f.key]: event.target.value })}
              />
            )}
          </label>
        ))}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy}>
            {t('action.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
function Action({ children, run }: { children: ReactNode; run: () => Promise<unknown> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  return (
    <>
      {error && <ApiErrorAlert error={error} />}
      <Button
        busy={busy}
        onClick={() => {
          setBusy(true);
          setError(undefined);
          void run()
            .catch((cause) => setError(asApiError(cause)))
            .finally(() => setBusy(false));
        }}
      >
        {children}
      </Button>
    </>
  );
}
function Archive({ active, run }: { active: boolean; run: () => Promise<unknown> }) {
  const { t } = useI18n();
  return <Action run={run}>{t(active ? 'infra.archive' : 'infra.unarchive')}</Action>;
}
function More({ hasMore, loadMore }: { hasMore: boolean; loadMore: () => void }) {
  const { t } = useI18n();
  return hasMore ? <Button onClick={loadMore}>{t('action.loadMore')}</Button> : null;
}
function BuildingBranch({
  id,
  active,
  version,
  onChange,
}: {
  id: string;
  active: boolean;
  version: number;
  onChange: () => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button onClick={() => setOpen(!open)}>{t(open ? 'infra.collapse' : 'infra.expand')}</Button>
      {can('infrastructure.manage') && (
        <Archive
          active={active}
          run={async () => {
            await api.archiveBuilding(id, active, version);
            onChange();
          }}
        />
      )}
      {open && <BuildingChildren id={id} />}
    </>
  );
}
function BuildingChildren({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const [create, setCreate] = useState(false);
  const rooms = usePagedList((cursor, signal) => api.rooms(id, cursor, signal), [id]);
  return (
    <>
      {rooms.error && <ApiErrorAlert error={rooms.error} onRetry={rooms.reload} />}
      {can('infrastructure.manage') && (
        <Button onClick={() => setCreate(true)}>{t('infra.createRoom')}</Button>
      )}
      <ul>
        {rooms.items.map((room) => (
          <li key={room.id}>
            <Link to={`/infrastructure/rooms/${room.id}`}>{room.name}</Link>{' '}
            {!room.active && t('infra.archived')}
            <RoomBranch
              id={room.id}
              active={room.active}
              version={room.version}
              onChange={rooms.reload}
            />
          </li>
        ))}
      </ul>
      <More hasMore={rooms.hasMore} loadMore={rooms.loadMore} />
      {create && (
        <Editor
          title={t('infra.createRoom')}
          fields={[
            { key: 'name', label: t('infra.name'), required: true },
            { key: 'floor', label: t('infra.floor') },
          ]}
          submit={async (v) => {
            await api.createRoom(id, { name: v.name ?? '', floor: v.floor ?? '' });
            rooms.reload();
          }}
          onClose={() => setCreate(false)}
        />
      )}
    </>
  );
}
function RoomBranch({
  id,
  active,
  version,
  onChange,
}: {
  id: string;
  active: boolean;
  version: number;
  onChange: () => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button onClick={() => setOpen(!open)}>{t(open ? 'infra.collapse' : 'infra.expand')}</Button>
      {can('infrastructure.manage') && (
        <Archive
          active={active}
          run={async () => {
            await api.archiveRoom(id, active, version);
            onChange();
          }}
        />
      )}
      {open && <RoomChildren id={id} />}
    </>
  );
}
function RoomChildren({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const [create, setCreate] = useState(false);
  const racks = usePagedList((cursor, signal) => api.racks(id, cursor, signal), [id]);
  return (
    <>
      {racks.error && <ApiErrorAlert error={racks.error} onRetry={racks.reload} />}
      {can('infrastructure.manage') && (
        <Button onClick={() => setCreate(true)}>{t('infra.createRack')}</Button>
      )}
      <ul>
        {racks.items.map((rack) => (
          <li key={rack.id}>
            <Link to={`/infrastructure/racks/${rack.id}`}>{rack.name}</Link> · {rack.heightU} U{' '}
            {!rack.active && t('infra.archived')}
            {can('infrastructure.manage') && (
              <Archive
                active={rack.active}
                run={async () => {
                  await api.archiveRack(rack.id, rack.active, rack.version);
                  racks.reload();
                }}
              />
            )}
          </li>
        ))}
      </ul>
      <More hasMore={racks.hasMore} loadMore={racks.loadMore} />
      {create && (
        <Editor
          title={t('infra.createRack')}
          fields={[
            { key: 'name', label: t('infra.name'), required: true },
            {
              key: 'heightU',
              label: t('infra.heightU'),
              value: '42',
              type: 'number',
              required: true,
            },
          ]}
          submit={async (v) => {
            await api.createRack(id, { name: v.name ?? '', heightU: Number(v.heightU) });
            racks.reload();
          }}
          onClose={() => setCreate(false)}
        />
      )}
    </>
  );
}
export function SiteTreeScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const manage = can('infrastructure.manage');
  const loaded = useAsync((signal) => api.tree(manage, signal), [manage]);
  const [create, setCreate] = useState<string>();
  const [globalCreate, setGlobalCreate] = useState(false);
  return (
    <>
      <PageHeader
        title={t('infra.tree')}
        actions={
          manage ? (
            <Button onClick={() => setGlobalCreate(true)}>{t('infra.createBuilding')}</Button>
          ) : undefined
        }
      />
      {loaded.error && <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />}
      {loaded.loading && <p>{t('state.loading')}</p>}
      {loaded.data?.items.length === 0 && <p>{t('infra.empty')}</p>}
      {loaded.data?.items.map((site) => (
        <section key={site.locationId}>
          <h2>{site.name}</h2>
          <p>
            {t('infra.counts', {
              buildings: site.buildings,
              rooms: site.rooms,
              racks: site.racks,
              assets: site.placedAssets,
            })}
          </p>
          {manage && (
            <Button onClick={() => setCreate(site.locationId)}>{t('infra.createBuilding')}</Button>
          )}
          <ul>
            {site.buildingItems.map((building) => (
              <li key={building.id}>
                <Link to={`/infrastructure/buildings/${building.id}`}>{building.name}</Link> ·{' '}
                {t('infra.counts', {
                  buildings: 0,
                  rooms: building.rooms,
                  racks: building.racks,
                  assets: building.placedAssets,
                })}{' '}
                {!building.active && t('infra.archived')}
                <BuildingBranch
                  id={building.id}
                  active={building.active}
                  version={building.version}
                  onChange={loaded.reload}
                />
              </li>
            ))}
          </ul>
        </section>
      ))}
      {globalCreate && (
        <Editor
          title={t('infra.createBuilding')}
          fields={[
            { key: 'siteLocationId', label: t('infra.siteId'), required: true },
            { key: 'name', label: t('infra.name'), required: true },
            { key: 'addressNote', label: t('infra.addressNote') },
          ]}
          submit={async (v) => {
            await api.createBuilding({
              siteLocationId: v.siteLocationId ?? '',
              name: v.name ?? '',
              addressNote: v.addressNote ?? '',
            });
            loaded.reload();
          }}
          onClose={() => setGlobalCreate(false)}
        />
      )}
      {create && (
        <Editor
          title={t('infra.createBuilding')}
          fields={[
            { key: 'name', label: t('infra.name'), required: true },
            { key: 'addressNote', label: t('infra.addressNote') },
          ]}
          submit={async (v) => {
            await api.createBuilding({
              siteLocationId: create,
              name: v.name ?? '',
              addressNote: v.addressNote ?? '',
            });
            loaded.reload();
          }}
          onClose={() => setCreate(undefined)}
        />
      )}
    </>
  );
}
export function BuildingScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const manage = can('infrastructure.manage');
  const record = useAsync((signal) => api.building(id, signal), [id]);
  const rooms = usePagedList((cursor, signal) => api.rooms(id, cursor, signal), [id]);
  const [form, setForm] = useState<'edit' | 'create'>();
  const reload = () => {
    record.reload();
    rooms.reload();
  };
  return (
    <>
      <PageHeader title={record.data?.name ?? t('infra.building')} />
      {record.error && <ApiErrorAlert error={record.error} onRetry={reload} />}
      {rooms.error && <ApiErrorAlert error={rooms.error} onRetry={reload} />}
      {record.data && (
        <>
          <p>
            <Link to="/infrastructure">{t('infra.back')}</Link>
          </p>
          <p>{record.data.addressNote}</p>
          {manage && (
            <>
              <Button onClick={() => setForm('edit')}>{t('infra.edit')}</Button>
              <Archive
                active={record.data.active}
                run={async () => {
                  await api.archiveBuilding(id, record.data!.active, record.data!.version);
                  reload();
                }}
              />
              <Button onClick={() => setForm('create')}>{t('infra.createRoom')}</Button>
            </>
          )}
          <h2>{t('infra.rooms')}</h2>
          <ul>
            {rooms.items.map((room) => (
              <li key={room.id}>
                <Link to={`/infrastructure/rooms/${room.id}`}>{room.name}</Link>{' '}
                {room.floor && `· ${room.floor}`} {!room.active && t('infra.archived')}
              </li>
            ))}
          </ul>
          <More hasMore={rooms.hasMore} loadMore={rooms.loadMore} />
          {form && (
            <Editor
              title={t(form === 'edit' ? 'infra.edit' : 'infra.createRoom')}
              fields={
                form === 'edit'
                  ? [
                      {
                        key: 'name',
                        label: t('infra.name'),
                        value: record.data.name,
                        required: true,
                      },
                      {
                        key: 'addressNote',
                        label: t('infra.addressNote'),
                        value: record.data.addressNote ?? '',
                      },
                    ]
                  : [
                      { key: 'name', label: t('infra.name'), required: true },
                      { key: 'floor', label: t('infra.floor') },
                    ]
              }
              submit={async (v) => {
                if (form === 'edit')
                  await api.updateBuilding(id, {
                    name: v.name ?? '',
                    addressNote: v.addressNote ?? '',
                    expectedVersion: record.data!.version,
                  });
                else await api.createRoom(id, { name: v.name ?? '', floor: v.floor ?? '' });
                reload();
              }}
              onClose={() => setForm(undefined)}
            />
          )}
        </>
      )}
    </>
  );
}
export function RoomScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const manage = can('infrastructure.manage');
  const record = useAsync((signal) => api.room(id, signal), [id]);
  const racks = usePagedList((cursor, signal) => api.racks(id, cursor, signal), [id]);
  const [form, setForm] = useState<'edit' | 'create'>();
  const reload = () => {
    record.reload();
    racks.reload();
  };
  return (
    <>
      <PageHeader title={record.data?.name ?? t('infra.room')} />
      {record.error && <ApiErrorAlert error={record.error} onRetry={reload} />}
      {racks.error && <ApiErrorAlert error={racks.error} onRetry={reload} />}
      {record.data && (
        <>
          <p>
            <Link to={`/infrastructure/buildings/${record.data.buildingId}`}>
              {t('infra.back')}
            </Link>
          </p>
          <p>
            {t('infra.floor')}: {record.data.floor ?? '–'}
          </p>
          {manage && (
            <>
              <Button onClick={() => setForm('edit')}>{t('infra.edit')}</Button>
              <Archive
                active={record.data.active}
                run={async () => {
                  await api.archiveRoom(id, record.data!.active, record.data!.version);
                  reload();
                }}
              />
              <Button onClick={() => setForm('create')}>{t('infra.createRack')}</Button>
            </>
          )}
          <h2>{t('infra.racks')}</h2>
          <ul>
            {racks.items.map((rack) => (
              <li key={rack.id}>
                <Link to={`/infrastructure/racks/${rack.id}`}>{rack.name}</Link> · {rack.heightU} U{' '}
                {!rack.active && t('infra.archived')}
              </li>
            ))}
          </ul>
          <More hasMore={racks.hasMore} loadMore={racks.loadMore} />
          {form && (
            <Editor
              title={t(form === 'edit' ? 'infra.edit' : 'infra.createRack')}
              fields={
                form === 'edit'
                  ? [
                      {
                        key: 'name',
                        label: t('infra.name'),
                        value: record.data.name,
                        required: true,
                      },
                      { key: 'floor', label: t('infra.floor'), value: record.data.floor ?? '' },
                    ]
                  : [
                      { key: 'name', label: t('infra.name'), required: true },
                      {
                        key: 'heightU',
                        label: t('infra.heightU'),
                        type: 'number',
                        value: '42',
                        required: true,
                      },
                    ]
              }
              submit={async (v) => {
                if (form === 'edit')
                  await api.updateRoom(id, {
                    name: v.name ?? '',
                    floor: v.floor ?? '',
                    expectedVersion: record.data!.version,
                  });
                else await api.createRack(id, { name: v.name ?? '', heightU: Number(v.heightU) });
                reload();
              }}
              onClose={() => setForm(undefined)}
            />
          )}
        </>
      )}
    </>
  );
}
const removalReasons = [
  'relocated',
  'replaced',
  'decommissioned',
  'error_correction',
  'other',
] as const;
export function RackScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const manage = can('infrastructure.manage');
  const record = useAsync((signal) => api.rack(id, signal), [id]);
  const [form, setForm] = useState<'rename' | 'place' | 'move' | 'remove'>();
  const [selected, setSelected] = useState<Placement>();
  const [reason, setReason] = useState<string>('relocated');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const rack = record.data;
  const done = () => {
    setForm(undefined);
    setSelected(undefined);
    record.reload();
  };
  const remove = async () => {
    if (!selected) return;
    setBusy(true);
    setError(undefined);
    try {
      await api.remove(selected.id, reason, selected.version);
      done();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <PageHeader title={rack?.name ?? t('infra.rack')} />
      {record.error && <ApiErrorAlert error={record.error} onRetry={record.reload} />}
      {rack && (
        <>
          <p>
            <Link to={`/infrastructure/rooms/${rack.roomId}`}>{t('infra.back')}</Link>
          </p>
          <p>
            {t('infra.heightU')}: {rack.heightU}
          </p>
          {manage && (
            <>
              <Button onClick={() => setForm('rename')}>{t('infra.edit')}</Button>
              <Archive
                active={rack.active}
                run={async () => {
                  await api.archiveRack(id, rack.active, rack.version);
                  record.reload();
                }}
              />
              <Button onClick={() => setForm('place')}>{t('infra.place')}</Button>
            </>
          )}
          <table>
            <caption>{t('infra.elevation')}</caption>
            <thead>
              <tr>
                <th>{t('infra.unit')}</th>
                <th>{t('infra.front')}</th>
                <th>{t('infra.rear')}</th>
              </tr>
            </thead>
            <tbody>
              {buildElevation(rack.heightU, rack.placements).map((row) => (
                <tr key={row.unit}>
                  <th>{row.unit}</th>
                  {(['front', 'rear'] as const).map((face) => {
                    const placement = row[face];
                    return (
                      <td key={face}>
                        {placement ? (
                          <>
                            <Link to={`/assets/${placement.assetId}`}>
                              {placement.assetReference ?? placement.assetId}
                            </Link>{' '}
                            {manage && row.unit === placement.uPosition && (
                              <>
                                <Button
                                  onClick={() => {
                                    setSelected(placement);
                                    setForm('move');
                                  }}
                                >
                                  {t('infra.move')}
                                </Button>
                                <Button
                                  onClick={() => {
                                    setSelected(placement);
                                    setForm('remove');
                                  }}
                                >
                                  {t('infra.remove')}
                                </Button>
                              </>
                            )}
                          </>
                        ) : (
                          t('infra.free')
                        )}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
          {form === 'remove' && (
            <Dialog title={t('infra.remove')} onClose={() => setForm(undefined)}>
              {error && <ApiErrorAlert error={error} />}
              <label>
                {t('infra.reason')}
                <select value={reason} onChange={(event) => setReason(event.target.value)}>
                  {removalReasons.map((code) => (
                    <option key={code} value={code}>
                      {t(`infra.reason.${code}`)}
                    </option>
                  ))}
                </select>
              </label>
              <div className="dialog-actions">
                <Button onClick={() => setForm(undefined)}>{t('action.cancel')}</Button>
                <Button variant="primary" busy={busy} onClick={() => void remove()}>
                  {t('infra.remove')}
                </Button>
              </div>
            </Dialog>
          )}
          {form && form !== 'remove' && (
            <Editor
              title={t(
                form === 'rename' ? 'infra.edit' : form === 'place' ? 'infra.place' : 'infra.move',
              )}
              fields={
                form === 'rename'
                  ? [{ key: 'name', label: t('infra.name'), value: rack.name, required: true }]
                  : [
                      ...(form === 'place'
                        ? [{ key: 'assetId', label: t('infra.assetId'), required: true }]
                        : []),
                      { key: 'rackId', label: t('infra.rackId'), value: id, required: true },
                      {
                        key: 'uPosition',
                        label: t('infra.unit'),
                        type: 'number',
                        value: String(selected?.uPosition ?? 1),
                        required: true,
                      },
                      {
                        key: 'heightU',
                        label: t('infra.heightU'),
                        type: 'number',
                        value: String(selected?.heightU ?? 1),
                        required: true,
                      },
                      {
                        key: 'face',
                        label: t('infra.face'),
                        value: selected?.face ?? 'front',
                        required: true,
                      },
                    ]
              }
              submit={async (v) => {
                if (form === 'rename') await api.renameRack(id, v.name ?? '', rack.version);
                else if (form === 'place')
                  await api.place({
                    rackId: v.rackId ?? id,
                    assetId: v.assetId ?? '',
                    uPosition: Number(v.uPosition),
                    heightU: Number(v.heightU),
                    face: v.face ?? 'front',
                  });
                else if (selected)
                  await api.move(selected.id, {
                    rackId: v.rackId ?? id,
                    uPosition: Number(v.uPosition),
                    heightU: Number(v.heightU),
                    face: v.face ?? 'front',
                    expectedVersion: selected.version,
                  });
                done();
              }}
              onClose={() => setForm(undefined)}
            />
          )}
        </>
      )}
    </>
  );
}
