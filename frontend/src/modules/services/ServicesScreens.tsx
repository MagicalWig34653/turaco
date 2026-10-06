import { FilterBar } from '../../platform/ui/FilterBar';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate, useLocation } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Table } from '../../platform/ui/Table';
import { assetsApi } from '../assets/api';
import { infrastructureApi } from '../infrastructure/api';
import { servicesApi, type ServiceFields } from './api';
import { impactTarget, impactUrl, nodeLabel } from './helpers';
import {
  criticalities,
  removalReasons,
  retireReasons,
  statusReasons,
  statuses,
  targetTypes,
  type Criticality,
  type Service,
  type ServiceLink,
  type TargetType,
} from './types';
const enc = encodeURIComponent;
function Choice({
  label,
  value,
  values,
  onChange,
  prefix,
  all,
}: {
  label: string;
  value: string;
  values: readonly string[];
  onChange: (v: string) => void;
  prefix: string;
  all?: boolean;
}) {
  const { t } = useI18n();
  return (
    <label>
      {label}
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        {all && <option value="">{t('filters.all')}</option>}
        {values.map((v) => (
          <option key={v} value={v}>
            {t(`${prefix}.${v}` as MessageKey)}
          </option>
        ))}
      </select>
    </label>
  );
}
function ServiceForm({
  service,
  onClose,
  onDone,
}: {
  service?: Service;
  onClose: () => void;
  onDone: (id: string) => void;
}) {
  const { t } = useI18n();
  const [fields, setFields] = useState<ServiceFields>({
    name: service?.name ?? '',
    description: service?.description ?? '',
    ownerUserId: service?.ownerUserId ?? '',
    ownerTeamId: service?.ownerTeamId ?? '',
    supportTeamId: service?.supportTeamId ?? '',
    criticality: service?.criticality ?? 'medium',
  });
  const [status, setStatus] = useState<'operational' | 'planned'>('operational');
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const set = (key: keyof ServiceFields, value: string) => setFields({ ...fields, [key]: value });
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const result = service
        ? await servicesApi.update(service.id, { ...fields, expectedVersion: service.version })
        : await servicesApi.create({ ...fields, status });
      onDone(result.id);
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title={t(service ? 'services.edit' : 'services.create')} onClose={onClose}>
      <form className="form" onSubmit={(e) => void submit(e)}>
        {error && <ApiErrorAlert error={error} />}
        {(['name', 'description', 'ownerUserId', 'ownerTeamId', 'supportTeamId'] as const).map(
          (key) => (
            <label key={key}>
              {t(`services.${key}`)}
              <input
                required={key === 'name'}
                value={fields[key]}
                onChange={(e) => set(key, e.target.value)}
              />
            </label>
          ),
        )}
        <Choice
          label={t('services.criticality')}
          value={fields.criticality}
          values={criticalities}
          prefix="services.criticality"
          onChange={(v) => set('criticality', v as Criticality)}
        />
        {!service && (
          <Choice
            label={t('services.status')}
            value={status}
            values={['operational', 'planned']}
            prefix="services.status"
            onChange={(v) => setStatus(v as typeof status)}
          />
        )}
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
export function ServicesListScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [form, setForm] = useState({
    q: '',
    status: '',
    criticality: '',
    teamId: '',
    ownerUserId: '',
  });
  const [filter, setFilter] = useState(form);
  const [create, setCreate] = useState(false);
  const list = usePagedList((cursor, signal) => servicesApi.list(filter, cursor, signal), [filter]);
  const activeFilters = [
    ...(filter.q
      ? [
          {
            key: 'q',
            label: `${t('services.search')}: ${filter.q}`,
            onRemove: () => {
              setFilter((current) => ({ ...current, q: '' }));
              setForm((current) => ({ ...current, q: '' }));
            },
          },
        ]
      : []),
    ...(filter.status
      ? [
          {
            key: 'status',
            label: t(`services.status.${filter.status}` as MessageKey),
            onRemove: () => {
              setFilter((current) => ({ ...current, status: '' }));
              setForm((current) => ({ ...current, status: '' }));
            },
          },
        ]
      : []),
    ...(filter.criticality
      ? [
          {
            key: 'criticality',
            label: t(`services.criticality.${filter.criticality}` as MessageKey),
            onRemove: () => {
              setFilter((current) => ({ ...current, criticality: '' }));
              setForm((current) => ({ ...current, criticality: '' }));
            },
          },
        ]
      : []),
    ...(filter.ownerUserId
      ? [
          {
            key: 'ownerUserId',
            label: `${t('services.ownerUserId')}: ${filter.ownerUserId}`,
            onRemove: () => {
              setFilter((current) => ({ ...current, ownerUserId: '' }));
              setForm((current) => ({ ...current, ownerUserId: '' }));
            },
          },
        ]
      : []),
    ...(filter.teamId
      ? [
          {
            key: 'teamId',
            label: `${t('services.teamId')}: ${filter.teamId}`,
            onRemove: () => {
              setFilter((current) => ({ ...current, teamId: '' }));
              setForm((current) => ({ ...current, teamId: '' }));
            },
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t('services.list')}
        actions={
          can('services.manage') ? (
            <Button onClick={() => setCreate(true)}>{t('services.create')}</Button>
          ) : undefined
        }
      />
      <FilterBar
        activeFilters={activeFilters}

        role="search"
        onSubmit={(e) => {
          e.preventDefault();
          setFilter({ ...form });
        }}
      >
        <TextField
          label={t('services.search')}
          value={form.q}
          onChange={(e) => setForm({ ...form, q: e.target.value })}
        />
        <Select
          label={t('services.status')}
          value={form.status}
          onChange={(e) => setForm({ ...form, status: e.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            ...statuses.map((value) => ({ value, label: t(`services.status.${value}`) })),
          ]}
        />
        <Select
          label={t('services.criticality')}
          value={form.criticality}
          onChange={(e) => setForm({ ...form, criticality: e.target.value })}
          options={[
            { value: '', label: t('filters.all') },
            ...criticalities.map((value) => ({ value, label: t(`services.criticality.${value}`) })),
          ]}
        />
        <TextField
          label={t('services.ownerUserId')}
          value={form.ownerUserId}
          onChange={(e) => setForm({ ...form, ownerUserId: e.target.value })}
        />
        <TextField
          label={t('services.teamId')}
          value={form.teamId}
          onChange={(e) => setForm({ ...form, teamId: e.target.value })}
        />
        <Button type="submit">{t('filters.apply')}</Button>
      </FilterBar>
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={t('services.list')}
        columns={
          [
            {
              key: 'reference',
              header: t('services.reference'),
              render: (service: Service) => (
                <Link to={`/services/${enc(service.id)}`}>{service.reference}</Link>
              ),
            },
            { key: 'name', header: t('services.name'), render: (service: Service) => service.name },
            {
              key: 'status',
              header: t('services.status'),
              render: (service: Service) => t(`services.status.${service.status}`),
            },
            {
              key: 'criticality',
              header: t('services.criticality'),
              render: (service: Service) => t(`services.criticality.${service.criticality}`),
            },
          ] satisfies Column<Service>[]
        }
        rows={list.items}
        rowKey={(service) => service.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('services.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {create && (
        <ServiceForm
          onClose={() => setCreate(false)}
          onDone={(id) => {
            setCreate(false);
            navigate(`/services/${enc(id)}`);
          }}
        />
      )}
    </>
  );
}
function DependencyDialog({
  id,
  onClose,
  onDone,
}: {
  id: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const [type, setType] = useState<TargetType>('service');
  const [query, setQuery] = useState('');
  const [targetId, setTargetId] = useState('');
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const candidates = useAsync(
    async (signal) => {
      if (type === 'service')
        return (await servicesApi.list({ q: query }, undefined, signal)).items.map((x) => ({
          id: x.id,
          label: `${x.reference} · ${x.name}`,
        }));
      if (type === 'vm' && (can('infrastructure.view') || can('infrastructure.manage')))
        return (await infrastructureApi.vms({ q: query }, undefined, signal)).items.map((x) => ({
          id: x.id,
          label: x.name,
        }));
      if (type === 'asset' && (can('assets.view') || can('assets.manage')))
        return (await assetsApi.list({ q: query }, undefined, signal)).items.map((x) => ({
          id: x.id,
          label: x.reference,
        }));
      return [];
    },
    [type, query],
  );
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await servicesApi.addDependency(id, type, targetId);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('services.addDependency')} onClose={onClose}>
      <form className="form" onSubmit={(e) => void submit(e)}>
        {error && <ApiErrorAlert error={error} />}
        <Choice
          label={t('services.type')}
          value={type}
          values={targetTypes}
          prefix="services.type"
          onChange={(v) => {
            setType(v as TargetType);
            setTargetId('');
            setQuery('');
          }}
        />
        {type !== 'location' && (
          <label>
            {t('services.search')}
            <input value={query} onChange={(e) => setQuery(e.target.value)} />
          </label>
        )}
        {candidates.data?.length ? (
          <label>
            {t('services.pickTarget')}
            <select value={targetId} onChange={(e) => setTargetId(e.target.value)}>
              <option value="">{t('services.enterId')}</option>
              {candidates.data
                .filter((x) => x.id !== id || type !== 'service')
                .map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.label}
                  </option>
                ))}
            </select>
          </label>
        ) : null}
        <label>
          {t('services.targetId')}
          <input required value={targetId} onChange={(e) => setTargetId(e.target.value)} />
        </label>
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
function LinkTable({
  links,
  title,
  canManage,
  onRemove,
}: {
  links: ServiceLink[];
  title: string;
  canManage: boolean;
  onRemove?: ((link: ServiceLink) => void) | undefined;
}) {
  const { t } = useI18n();
  return (
    <>
      <h2>{title}</h2>
      <Table>
        <thead>
          <tr>
            <th>{t('services.type')}</th>
            <th>{t('services.name')}</th>
            <th>{t('services.confidence')}</th>
            {canManage && <th>{t('services.action')}</th>}
          </tr>
        </thead>
        <tbody>
          {links.map((l) => (
            <tr key={l.relationshipId}>
              <td>{t(`services.type.${l.node.type}`)}</td>
              <td>
                {l.node.hidden ? (
                  t('services.restricted', { type: t(`services.type.${l.node.type}`) })
                ) : l.node.type === 'service' ? (
                  <Link to={`/services/${enc(l.node.id)}`}>{nodeLabel(l.node)}</Link>
                ) : (
                  nodeLabel(l.node)
                )}
              </td>
              <td>{t(`services.confidence.${l.confidence}` as MessageKey)}</td>
              {canManage && (
                <td>
                  {onRemove && <Button onClick={() => onRemove(l)}>{t('services.remove')}</Button>}
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </Table>
    </>
  );
}
function LinkSection({
  id,
  direction,
  links: initialLinks,
  nextCursor: initialCursor,
  title,
  canManage,
  onRemove,
}: {
  id: string;
  direction: 'out' | 'in';
  links: ServiceLink[];
  nextCursor?: string | undefined;
  title: string;
  canManage: boolean;
  onRemove?: ((link: ServiceLink) => void) | undefined;
}) {
  const { t } = useI18n();
  const [links, setLinks] = useState(initialLinks);
  const [cursor, setCursor] = useState(initialCursor);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const loadMore = async () => {
    if (!cursor || busy) return;
    const request = new AbortController();
    controller.current = request;
    setBusy(true);
    setError(undefined);
    try {
      const page = await servicesApi.dependencies(id, direction, cursor, request.signal);
      if (request.signal.aborted) return;
      setLinks((current) => [...current, ...page.items]);
      setCursor(page.nextCursor);
    } catch (cause) {
      if (!request.signal.aborted) setError(asApiError(cause));
    } finally {
      if (!request.signal.aborted) setBusy(false);
    }
  };
  return (
    <>
      <LinkTable links={links} title={title} canManage={canManage} onRemove={onRemove} />
      {error && <ApiErrorAlert error={error} onRetry={() => void loadMore()} />}
      {cursor && (
        <Button busy={busy} onClick={() => void loadMore()}>
          {t('action.loadMore')}
        </Button>
      )}
    </>
  );
}
export function ServiceDetailScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const loaded = useAsync((signal) => servicesApi.get(id, signal), [id]);
  const s = loaded.data;
  const [dialog, setDialog] = useState<'edit' | 'status' | 'retire' | 'add' | 'remove'>();
  const [status, setStatus] = useState<'operational' | 'degraded' | 'outage' | 'planned'>(
    'operational',
  );
  const [reason, setReason] = useState('incident');
  const [removing, setRemoving] = useState<ServiceLink>();
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const [revision, setRevision] = useState(0);
  const reloadDetail = () => {
    setRevision((current) => current + 1);
    loaded.reload();
  };
  const run = async () => {
    if (!s) return;
    setBusy(true);
    setError(undefined);
    try {
      if (dialog === 'status') await servicesApi.status(id, status, reason, s.version);
      if (dialog === 'retire') await servicesApi.retire(id, reason, s.version);
      if (dialog === 'remove' && removing)
        await servicesApi.removeDependency(id, removing.relationshipId, reason);
      setDialog(undefined);
      reloadDetail();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <PageHeader title={s ? `${s.reference} · ${s.name}` : t('services.detail')} />
      {loaded.error && <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />}
      {s && !loaded.loading && (
        <>
          <p>
            <Link to="/services">{t('services.back')}</Link> ·{' '}
            <Link to={impactUrl('service', id)}>{t('services.impact')}</Link>
          </p>
          <dl className="facts">
            <dt>{t('services.description')}</dt>
            <dd>{s.description || '–'}</dd>
            <dt>{t('services.status')}</dt>
            <dd>{t(`services.status.${s.status}`)}</dd>
            <dt>{t('services.statusReason')}</dt>
            <dd>{s.statusReason ? t(`services.reason.${s.statusReason}` as MessageKey) : '–'}</dd>
            <dt>{t('services.criticality')}</dt>
            <dd>{t(`services.criticality.${s.criticality}`)}</dd>
            <dt>{t('services.ownerUserId')}</dt>
            <dd>{s.ownerUserId || '–'}</dd>
            <dt>{t('services.ownerTeamId')}</dt>
            <dd>{s.ownerTeamId || '–'}</dd>
            <dt>{t('services.supportTeamId')}</dt>
            <dd>{s.supportTeamId || '–'}</dd>
          </dl>
          {can('services.manage') && s.status !== 'retired' && (
            <>
              <Button onClick={() => setDialog('edit')}>{t('services.edit')}</Button>
              <Button
                onClick={() => {
                  setStatus(s.status as typeof status);
                  setReason('incident');
                  setDialog('status');
                }}
              >
                {t('services.changeStatus')}
              </Button>
              <Button
                onClick={() => {
                  setReason('replaced');
                  setDialog('retire');
                }}
              >
                {t('services.retire')}
              </Button>
              <Button onClick={() => setDialog('add')}>{t('services.addDependency')}</Button>
            </>
          )}
          <LinkSection
            key={`${id}:${revision}:out`}
            id={id}
            direction="out"
            links={s.dependencies}
            nextCursor={s.dependenciesNextCursor}
            title={t('services.dependencies')}
            canManage={can('services.manage') && s.status !== 'retired'}
            onRemove={(l) => {
              setRemoving(l);
              setReason('no_longer_needed');
              setDialog('remove');
            }}
          />
          <LinkSection
            key={`${id}:${revision}:in`}
            id={id}
            direction="in"
            links={s.dependents}
            nextCursor={s.dependentsNextCursor}
            title={t('services.dependents')}
            canManage={false}
          />
          {dialog === 'edit' && (
            <ServiceForm
              service={s}
              onClose={() => setDialog(undefined)}
              onDone={() => {
                setDialog(undefined);
                reloadDetail();
              }}
            />
          )}
          {dialog === 'add' && (
            <DependencyDialog
              id={id}
              onClose={() => setDialog(undefined)}
              onDone={() => {
                setDialog(undefined);
                reloadDetail();
              }}
            />
          )}
          {(dialog === 'status' || dialog === 'retire' || dialog === 'remove') && (
            <Dialog
              title={t(
                dialog === 'status'
                  ? 'services.changeStatus'
                  : dialog === 'retire'
                    ? 'services.retire'
                    : 'services.remove',
              )}
              onClose={() => setDialog(undefined)}
            >
              {error && <ApiErrorAlert error={error} />}
              {dialog === 'status' && (
                <Choice
                  label={t('services.status')}
                  value={status}
                  values={statuses.filter((v) => v !== 'retired')}
                  prefix="services.status"
                  onChange={(v) => setStatus(v as typeof status)}
                />
              )}
              <Choice
                label={t('services.reason')}
                value={reason}
                values={
                  dialog === 'status'
                    ? statusReasons
                    : dialog === 'retire'
                      ? retireReasons
                      : removalReasons
                }
                prefix="services.reason"
                onChange={setReason}
              />
              <div className="dialog-actions">
                <Button onClick={() => setDialog(undefined)}>{t('action.cancel')}</Button>
                <Button variant="primary" busy={busy} onClick={() => void run()}>
                  {t('action.save')}
                </Button>
              </div>
            </Dialog>
          )}
        </>
      )}
    </>
  );
}
export function ImpactScreen() {
  const { t } = useI18n();
  const location = useLocation();
  const target = impactTarget(location.search);
  const [direction, setDirection] = useState<'downstream' | 'upstream'>('downstream');
  const [depth, setDepth] = useState(3);
  const result = useAsync(
    (signal) =>
      target
        ? servicesApi.impact(target.type, target.id, direction, depth, signal)
        : Promise.resolve(undefined),
    [target?.type, target?.id, direction, depth],
  );
  return (
    <>
      <PageHeader title={t('services.impact')} />
      {!target && <p>{t('services.impactMissing')}</p>}
      {target && (
        <>
          <p>
            {t(`services.type.${target.type}`)} ·{' '}
            {result.data
              ? result.data.start.hidden
                ? t('services.restricted', { type: t(`services.type.${result.data.start.type}`) })
                : nodeLabel(result.data.start)
              : target.id}
          </p>
          <Choice
            label={t('services.direction')}
            value={direction}
            values={['downstream', 'upstream']}
            prefix="services.direction"
            onChange={(v) => setDirection(v as typeof direction)}
          />
          <label>
            {t('services.depth')}
            <select value={depth} onChange={(e) => setDepth(Number(e.target.value))}>
              {[1, 2, 3, 4, 5, 6].map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </label>
          {result.error && <ApiErrorAlert error={result.error} onRetry={result.reload} />}
          {result.data && (
            <>
              <p>{t(`services.directionHelp.${direction}`)}</p>
              <Table>
                <thead>
                  <tr>
                    <th>{t('services.type')}</th>
                    <th>{t('services.name')}</th>
                    <th>{t('services.depth')}</th>
                    <th>{t('services.criticality')}</th>
                    <th>{t('services.status')}</th>
                    <th>{t('services.confidence')}</th>
                  </tr>
                </thead>
                <tbody>
                  {result.data.items.map((n) => (
                    <tr key={`${n.type}:${n.id}`}>
                      <td>{t(`services.type.${n.type}`)}</td>
                      <td>
                        {n.hidden
                          ? t('services.restricted', { type: t(`services.type.${n.type}`) })
                          : nodeLabel(n)}
                      </td>
                      <td>{n.depth}</td>
                      <td>{n.criticality ? t(`services.criticality.${n.criticality}`) : '–'}</td>
                      <td>{n.status ? t(`services.status.${n.status}` as MessageKey) : '–'}</td>
                      <td>{t(`services.confidence.${n.confidence}` as MessageKey)}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
              {result.data.truncated && <p>{t('services.impactTruncated')}</p>}
            </>
          )}
        </>
      )}
    </>
  );
}
