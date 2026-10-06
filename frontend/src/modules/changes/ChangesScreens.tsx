import { useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Avatar, FilterBar, Skeleton, StatusBadge, useCountUp } from '../../platform/ui/Workspace';
import { useContextMenu } from '../../platform/ui/ContextMenu';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Dialog } from '../../platform/ui/Dialog';
import { relativeDate } from '../../platform/ui/tableModel';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Checkbox, Select } from '../../platform/ui/Field';
import { DateFilter } from '../../platform/ui/FilterBar';
import { Button } from '../../platform/ui/Button';
import { PageHeader } from '../../platform/ui/PageHeader';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { servicesApi } from '../services/api';
import { infrastructureApi } from '../infrastructure/api';
import { assetsApi } from '../assets/api';
import { changesApi, type ChangeFields } from './api';
import {
  allowedActions,
  resourceLabel,
  changeMetrics,
  lifecycle,
  windowMinutes,
  matchesChangeMetric,
  type ChangeMetricKey,
} from './helpers';
import {
  cancelReasons,
  failReasons,
  kinds,
  resourceTypes,
  risks,
  statuses,
  type Change,
  type ChangeStatus,
  type ResourceType,
} from './types';
const enc = encodeURIComponent;
function Choice({
  label,
  value,
  values,
  prefix,
  onChange,
  all = false,
}: {
  label: string;
  value: string;
  values: readonly string[];
  prefix: string;
  onChange: (value: string) => void;
  all?: boolean;
}) {
  const { t } = useI18n();
  return (
    <label>
      {label}
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        {all && <option value="">{t('filters.all')}</option>}
        {values.map((x) => (
          <option key={x} value={x}>
            {t(`${prefix}.${x}` as MessageKey)}
          </option>
        ))}
      </select>
    </label>
  );
}
const tone: Record<ChangeStatus, 'neutral' | 'success' | 'warning' | 'danger' | 'info'> = {
  draft: 'neutral',
  assessment: 'info',
  pending_approval: 'warning',
  approved: 'success',
  rejected: 'danger',
  scheduled: 'info',
  in_progress: 'info',
  completed: 'success',
  failed: 'danger',
  review: 'warning',
  closed: 'neutral',
  cancelled: 'neutral',
};
function Status({ value }: { value: ChangeStatus }) {
  const { t } = useI18n();
  return <StatusBadge tone={tone[value]}>{t(`changes.status.${value}`)}</StatusBadge>;
}
function Error({ error }: { error: ApiError | undefined }) {
  return error ? <ApiErrorAlert error={error} /> : null;
}
function Field({
  label,
  value,
  onChange,
  type = 'text',
  required = false,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
  required?: boolean;
}) {
  return (
    <label>
      {label}
      <input
        type={type}
        value={value}
        required={required}
        onChange={(e) => onChange(e.target.value)}
      />
    </label>
  );
}
function dateValue(value: string | null): string {
  return value ? value.slice(0, 16) : '';
}
function iso(value: string): string | null {
  return value ? new Date(value).toISOString() : null;
}
function ChangeForm({
  existing,
  onDone,
  onClose,
}: {
  existing?: Change;
  onDone: (id: string) => void;
  onClose?: () => void;
}) {
  const { t } = useI18n();
  const [title, setTitle] = useState(existing?.title ?? '');
  const [description, setDescription] = useState(existing?.description ?? '');
  const [kind, setKind] = useState(existing?.kind ?? 'normal');
  const [risk, setRisk] = useState(existing?.risk ?? 'low');
  const [start, setStart] = useState(dateValue(existing?.windowStart ?? null));
  const [end, setEnd] = useState(dateValue(existing?.windowEnd ?? null));
  const [rollbackPlan, setRollbackPlan] = useState(existing?.rollbackPlan ?? '');
  const [emergencyJustification, setEmergencyJustification] = useState(
    existing?.emergencyJustification ?? '',
  );
  const [owner, setOwner] = useState<Assignee | null>(
    existing?.ownerId ? { id: existing.ownerId, label: existing.ownerId } : null,
  );
  const [ownerId, setOwnerId] = useState(existing?.ownerId ?? '');
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const invalidWindow = !!(start || end) && windowMinutes(start, end) === null;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (invalidWindow) return;
    setBusy(true);
    setError(undefined);
    try {
      const fields: ChangeFields = {
        title,
        description,
        kind,
        risk,
        ownerUserId: owner?.id ?? ownerId,
        rollbackPlan,
        window: { start: iso(start), end: iso(end) },
      };
      const result = existing
        ? await changesApi.update(existing.id, { ...fields, expectedVersion: existing.version })
        : await changesApi.create(fields);
      // The create/update DTO has no emergency justification field. Keep it for the assessment dialog.
      if (emergencyJustification)
        sessionStorage.setItem(`change-emergency-${result.id}`, emergencyJustification);
      onDone(result.id);
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={(e) => void submit(e)} className="form-stack change-form">
      <section className="change-form-section">
        <h3>{t('changes.polish.basics')}</h3>
        <p>{t('changes.polish.basicsHint')}</p>
        <Field label={t('changes.title')} value={title} onChange={setTitle} required />
        <label>
          {t('changes.description')}
          <textarea value={description} onChange={(e) => setDescription(e.target.value)} />
        </label>
        <Choice
          label={t('changes.kind')}
          value={kind}
          onChange={(v) => setKind(v as Change['kind'])}
          values={kinds}
          prefix="changes.kind"
        />
        <Choice
          label={t('changes.risk')}
          value={risk}
          onChange={(v) => setRisk(v as Change['risk'])}
          values={risks}
          prefix="changes.risk"
        />
      </section>
      <section className="change-form-section">
        <h3>{t('changes.polish.planning')}</h3>
        <p>{t('changes.polish.planningHint')}</p>
        <Field
          label={t('changes.windowStart')}
          value={start}
          onChange={setStart}
          type="datetime-local"
        />
        <Field label={t('changes.windowEnd')} value={end} onChange={setEnd} type="datetime-local" />
        <p
          className={invalidWindow ? 'change-validation' : undefined}
          role={invalidWindow ? 'alert' : undefined}
        >
          {t(invalidWindow ? 'changes.polish.windowInvalid' : 'changes.polish.windowHint')}
        </p>
        <label>
          {t('changes.rollbackPlan')}
          <textarea value={rollbackPlan} onChange={(e) => setRollbackPlan(e.target.value)} />
        </label>
        {kind === 'emergency' && (
          <label>
            {t('changes.emergencyJustification')}
            <textarea
              value={emergencyJustification}
              onChange={(e) => setEmergencyJustification(e.target.value)}
            />
          </label>
        )}
        <p>{t('changes.owner')}</p>
        <AssigneePicker
          type="user"
          value={owner}
          onChange={(value) => {
            setOwner(value);
            setOwnerId(value?.id ?? '');
          }}
        />
        <Field label={t('changes.ownerId')} value={ownerId} onChange={setOwnerId} />
      </section>
      <Error error={error} />
      <div className="actions change-form-footer">
        <Button variant="primary" type="submit" disabled={busy || invalidWindow}>
          {t('action.save')}
        </Button>
        {onClose && (
          <Button type="button" onClick={onClose}>
            {t('action.cancel')}
          </Button>
        )}
      </div>
    </form>
  );
}
function Person({ name }: { name?: string | null | undefined }) {
  const { t } = useI18n();
  return (
    <span className="change-person">
      <Avatar name={name ?? null} />
      <span>{name || t('changes.polish.unavailable')}</span>
    </span>
  );
}
function ChangeWindow({ change }: { change: Change }) {
  const { t, locale } = useI18n();
  const minutes = windowMinutes(change.windowStart, change.windowEnd);
  return (
    <span className="change-window">
      <time
        dateTime={change.windowStart ?? undefined}
        title={change.windowStart ? new Date(change.windowStart).toLocaleString(locale) : undefined}
      >
        {change.windowStart
          ? relativeDate(change.windowStart, locale, Date.now())
          : t('changes.polish.unscheduled')}
      </time>
      {minutes !== null && <small>{t('changes.polish.duration', { count: minutes })}</small>}
    </span>
  );
}
function ChangeMetric({
  label,
  value,
  onClick,
  selected,
}: {
  label: string;
  value: number;
  onClick: () => void;
  selected: boolean;
}) {
  const count = useCountUp(value);
  return (
    <button
      type="button"
      className="change-metric"
      onClick={onClick}
      aria-pressed={selected}
      aria-label={`${label}: ${value}`}
    >
      <span>{label}</span>
      <strong aria-hidden="true">{count}</strong>
      <span className="change-metric-arrow" aria-hidden="true">
        ↗
      </span>
    </button>
  );
}
export function ChangesListScreen({ mine = false }: { mine?: boolean }) {
  const { t } = useI18n();
  const { can, session } = useSession();
  const [status, setStatus] = useState('');
  const [risk, setRisk] = useState('');
  const [kind, setKind] = useState('');
  const [windowFrom, setWindowFrom] = useState('');
  const [windowTo, setWindowTo] = useState('');
  const [owner, setOwner] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const [metric, setMetric] = useState<ChangeMetricKey | null>(null);
  const filter = {
    status,
    risk,
    kind,
    windowFrom: iso(windowFrom) ?? '',
    windowTo: iso(windowTo) ?? '',
    ...(mine
      ? { requester: session?.userId ?? '' }
      : owner
        ? { owner: session?.userId ?? '' }
        : {}),
  };
  const list = usePagedList(
    async (cursor, signal) => {
      const page = await changesApi.list(filter, cursor, signal);
      return {
        ...page,
        items: page.items.map((item) => ({
          ...item,
          ownerName: item.ownerId ? page.names?.users[item.ownerId] : undefined,
          requesterName: page.names?.users[item.requesterId],
        })),
      };
    },
    [status, risk, kind, windowFrom, windowTo, owner, mine, session?.userId],
  );
  const title = t(mine ? 'changes.mine' : 'nav.changes');
  const metrics = changeMetrics(list.items, new Date());
  return (
    <div className="changes-workspace">
      <p className="changes-eyebrow">{t('changes.polish.eyebrow')}</p>
      <PageHeader
        title={title}
        intro={t('changes.polish.intro')}
        actions={
          can('changes.manage') ? (
            <Button variant="primary" onClick={() => setShowCreate(true)}>
              {t('changes.create')}
            </Button>
          ) : null
        }
      />
      {!list.loading && !list.error && (
        <>
          <div className="change-metrics">
            <ChangeMetric
              label={t('changes.polish.scheduled')}
              value={metrics.scheduled}
              selected={metric === 'scheduled'}
              onClick={() => setMetric(metric === 'scheduled' ? null : 'scheduled')}
            />
            <ChangeMetric
              label={t('changes.polish.awaiting')}
              value={metrics.awaiting}
              selected={metric === 'awaiting'}
              onClick={() => setMetric(metric === 'awaiting' ? null : 'awaiting')}
            />
            <ChangeMetric
              label={t('changes.status.in_progress')}
              value={metrics.running}
              selected={metric === 'running'}
              onClick={() => setMetric(metric === 'running' ? null : 'running')}
            />
            <ChangeMetric
              label={t('changes.polish.failed')}
              value={metrics.failed}
              selected={metric === 'failed'}
              onClick={() => setMetric(metric === 'failed' ? null : 'failed')}
            />
          </div>
          <p className="change-scope">
            {t('changes.polish.loadedScope', { count: list.items.length })}
          </p>
        </>
      )}
      <FilterBar
        primaryCount={3}
        activeFilters={[
          ...(status
            ? [
                {
                  key: 'status',
                  label: t(`changes.status.${status}` as MessageKey),
                  onRemove: () => setStatus(''),
                },
              ]
            : []),
          ...(risk
            ? [
                {
                  key: 'risk',
                  label: t(`changes.risk.${risk}` as MessageKey),
                  onRemove: () => setRisk(''),
                },
              ]
            : []),
          ...(kind
            ? [
                {
                  key: 'kind',
                  label: t(`changes.kind.${kind}` as MessageKey),
                  onRemove: () => setKind(''),
                },
              ]
            : []),
          ...(windowFrom
            ? [{ key: 'from', label: t('changes.windowFrom'), onRemove: () => setWindowFrom('') }]
            : []),
          ...(windowTo
            ? [{ key: 'to', label: t('changes.windowTo'), onRemove: () => setWindowTo('') }]
            : []),
          ...(owner
            ? [{ key: 'owner', label: t('changes.mineFilter'), onRemove: () => setOwner('') }]
            : []),
          ...(metric
            ? [
                {
                  key: 'metric',
                  label: t(
                    metric === 'scheduled'
                      ? 'changes.polish.scheduled'
                      : metric === 'awaiting'
                        ? 'changes.polish.awaiting'
                        : metric === 'running'
                          ? 'changes.status.in_progress'
                          : 'changes.polish.failed',
                  ),
                  onRemove: () => setMetric(null),
                },
              ]
            : []),
        ]}
      >
        <Select
          label={t('changes.statusLabel')}
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={[
            { value: '', label: t('filters.all') },
            ...statuses.map((value) => ({ value, label: t(`changes.status.${value}`) })),
          ]}
        />
        <Select
          label={t('changes.risk')}
          value={risk}
          onChange={(e) => setRisk(e.target.value)}
          options={[
            { value: '', label: t('filters.all') },
            ...risks.map((value) => ({ value, label: t(`changes.risk.${value}`) })),
          ]}
        />
        <Select
          label={t('changes.kind')}
          value={kind}
          onChange={(e) => setKind(e.target.value)}
          options={[
            { value: '', label: t('filters.all') },
            ...kinds.map((value) => ({ value, label: t(`changes.kind.${value}`) })),
          ]}
        />
        <DateFilter
          label={t('changes.windowFrom')}
          type="datetime-local"
          value={windowFrom}
          onChange={setWindowFrom}
        />
        <DateFilter
          label={t('changes.windowTo')}
          type="datetime-local"
          value={windowTo}
          onChange={setWindowTo}
        />
        {!mine ? (
          <Checkbox
            label={t('changes.mineFilter')}
            checked={!!owner}
            onChange={(e) => setOwner(e.target.checked ? 'mine' : '')}
          />
        ) : null}
      </FilterBar>
      <DataTable
        caption={title}
        filterSummary={[
          status && t(`changes.status.${status}` as MessageKey),
          risk && t(`changes.risk.${risk}` as MessageKey),
          kind && t(`changes.kind.${kind}` as MessageKey),
          windowFrom && t('changes.windowFrom'),
          windowTo && t('changes.windowTo'),
          owner && t('changes.mineFilter'),
          metric &&
            t(
              metric === 'scheduled'
                ? 'changes.polish.scheduled'
                : metric === 'awaiting'
                  ? 'changes.polish.awaiting'
                  : metric === 'running'
                    ? 'changes.status.in_progress'
                    : 'changes.polish.failed',
            ),
        ]
          .filter(Boolean)
          .join(', ')}
        rowActions={(c) => [
          {
            id: 'open',
            label: t('contextMenu.open'),
            onSelect: () => navigate(`/changes/${enc(c.id)}`),
          },
        ]}
        columns={
          [
            {
              key: 'reference',
              sortValue: (c) => c.reference,
              header: t('changes.reference'),
              render: (c: Change) => (
                <Link className="change-reference" to={`/changes/${enc(c.id)}`}>
                  {c.reference}
                </Link>
              ),
            },
            {
              key: 'title',
              sortValue: (c) => c.title,
              header: t('changes.title'),
              render: (c: Change) => (
                <Link className={`change-title change-risk-${c.risk}`} to={`/changes/${enc(c.id)}`}>
                  {c.title}
                </Link>
              ),
            },
            {
              key: 'status',
              sortValue: (c) => t(`changes.status.${c.status}`),
              header: t('changes.statusLabel'),
              render: (c: Change) => <Status value={c.status} />,
            },
            {
              key: 'kind',
              sortValue: (c) => t(`changes.kind.${c.kind}`),
              header: t('changes.kind'),
              render: (c: Change) => (
                <StatusBadge tone={c.kind === 'emergency' ? 'warning' : 'neutral'}>
                  {t(`changes.kind.${c.kind}`)}
                </StatusBadge>
              ),
            },
            {
              key: 'risk',
              sortValue: (c) => risks.indexOf(c.risk),
              header: t('changes.risk'),
              render: (c: Change) => (
                <StatusBadge
                  tone={c.risk === 'high' ? 'danger' : c.risk === 'medium' ? 'warning' : 'success'}
                >
                  {t(`changes.risk.${c.risk}`)}
                </StatusBadge>
              ),
            },
            {
              key: 'window',
              sortValue: (c) => (c.windowStart ? Date.parse(c.windowStart) : null),
              header: t('changes.windowStart'),
              render: (c: Change) => <ChangeWindow change={c} />,
            },
            {
              key: 'owner',
              sortValue: (c) => c.ownerName,
              header: t('changes.owner'),
              render: (c) => <Person name={c.ownerName} />,
            },
            {
              key: 'requester',
              sortValue: (c) => c.requesterName,
              header: t('changes.requester'),
              render: (c) => <Person name={c.requesterName} />,
            },
          ] satisfies Column<(typeof list.items)[number]>[]
        }
        rows={
          metric ? list.items.filter((c) => matchesChangeMetric(c, metric, new Date())) : list.items
        }
        rowKey={(c) => c.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('changes.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {showCreate && (
        <Dialog wide title={t('changes.create')} onClose={() => setShowCreate(false)}>
          <ChangeForm
            onClose={() => setShowCreate(false)}
            onDone={(id) => navigate(`/changes/${enc(id)}`)}
          />
        </Dialog>
      )}
    </div>
  );
}
export function ChangeCreateScreen() {
  const { t } = useI18n();
  return (
    <>
      <p className="changes-eyebrow">{t('changes.polish.eyebrow')}</p>
      <PageHeader title={t('changes.create')} intro={t('changes.polish.intro')} />
      <ChangeForm onDone={(id) => navigate(`/changes/${enc(id)}`)} />
    </>
  );
}
function AffectedDialog({
  id,
  version,
  onClose,
  onDone,
}: {
  id: string;
  version: number;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const [type, setType] = useState<ResourceType>('service');
  const [query, setQuery] = useState('');
  const [targetId, setTargetId] = useState('');
  const [error, setError] = useState<ApiError>();
  const results = useAsync(
    async (signal) => {
      if (type === 'service' && (can('services.view') || can('services.manage')))
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
    setError(undefined);
    try {
      await changesApi.addAffected(id, type, targetId, version);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    }
  };
  return (
    <Dialog title={t('changes.addAffected')} onClose={onClose}>
      <form onSubmit={(e) => void submit(e)} className="form-stack">
        <Choice
          label={t('changes.type')}
          value={type}
          values={resourceTypes}
          prefix="changes.type"
          onChange={(v) => {
            setType(v as ResourceType);
            setTargetId('');
          }}
        />
        <Field label={t('changes.search')} value={query} onChange={setQuery} type="search" />
        {results.data?.map((x) => (
          <Button type="button" key={x.id} onClick={() => setTargetId(x.id)}>
            {x.label}
          </Button>
        ))}
        <Field label={t('changes.targetId')} value={targetId} onChange={setTargetId} required />
        <Error error={results.error ?? error} />
        <Button type="submit">{t('changes.addAffected')}</Button>
      </form>
    </Dialog>
  );
}
function ActionDialog({
  change,
  action,
  onClose,
  onDone,
}: {
  change: Change;
  action: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [risk, setRisk] = useState(change.risk);
  const [approverType, setApproverType] = useState<'user' | 'team'>('user');
  const [approver, setApprover] = useState<Assignee | null>(null);
  const [emergencyJustification, setEmergencyJustification] = useState(
    sessionStorage.getItem(`change-emergency-${change.id}`) ?? '',
  );
  const [emergencyApproval, setEmergencyApproval] = useState(false);
  const [start, setStart] = useState(dateValue(change.windowStart));
  const [end, setEnd] = useState(dateValue(change.windowEnd));
  const [force, setForce] = useState(false);
  const [reason, setReason] = useState(action === 'fail' ? failReasons[0] : cancelReasons[0]);
  const [rollbackDone, setRollbackDone] = useState(false);
  const [outcomeNote, setOutcomeNote] = useState('');
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    const body: Record<string, unknown> = { expectedVersion: change.version };
    if (action === 'assess') {
      body.risk = risk;
      if (emergencyApproval && change.kind === 'emergency')
        body.emergencyJustification = emergencyJustification;
      else if (approver && (risk !== 'low' || change.kind === 'emergency'))
        body[approverType === 'user' ? 'approverUserId' : 'approverTeamId'] = approver.id;
    }
    if (action === 'schedule') body.window = { start: iso(start), end: iso(end) };
    if (action === 'complete' && force) body.force = 'tasks_waived';
    if (action === 'fail') {
      body.reason = reason;
      body.rollbackDone = rollbackDone;
    }
    if (action === 'review') body.outcomeNote = outcomeNote.trim();
    if (action === 'cancel') body.reason = reason;
    try {
      await changesApi.action(change.id, action, body);
      if (action === 'assess') sessionStorage.removeItem(`change-emergency-${change.id}`);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title={t(`changes.action.${action}` as MessageKey)} onClose={onClose}>
      <form onSubmit={(e) => void submit(e)} className="form-stack">
        {action === 'assess' && (
          <>
            <Choice
              label={t('changes.risk')}
              value={risk}
              values={risks}
              prefix="changes.risk"
              onChange={(v) => setRisk(v as Change['risk'])}
            />
            {change.kind === 'emergency' && (
              <label>
                {t('changes.emergencyApproval')}
                <input
                  type="checkbox"
                  checked={emergencyApproval}
                  onChange={(e) => setEmergencyApproval(e.target.checked)}
                />
              </label>
            )}
            {emergencyApproval ? (
              <label>
                {t('changes.emergencyJustification')}
                <textarea
                  value={emergencyJustification}
                  onChange={(e) => setEmergencyJustification(e.target.value)}
                  required
                />
              </label>
            ) : (
              (risk !== 'low' || change.kind === 'emergency') && (
                <>
                  <Choice
                    label={t('changes.approverType')}
                    value={approverType}
                    values={['user', 'team']}
                    prefix="changes.approverType"
                    onChange={(v) => {
                      setApproverType(v as 'user' | 'team');
                      setApprover(null);
                    }}
                  />
                  <AssigneePicker type={approverType} value={approver} onChange={setApprover} />
                </>
              )
            )}
          </>
        )}
        {action === 'schedule' && (
          <>
            <Field
              label={t('changes.windowStart')}
              value={start}
              onChange={setStart}
              type="datetime-local"
            />
            <Field
              label={t('changes.windowEnd')}
              value={end}
              onChange={setEnd}
              type="datetime-local"
            />
          </>
        )}
        {action === 'complete' && (
          <label>
            {t('changes.tasksWaived')}
            <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />
          </label>
        )}
        {(action === 'fail' || action === 'cancel') && (
          <Choice
            label={t('changes.reason')}
            value={reason}
            values={action === 'fail' ? failReasons : cancelReasons}
            prefix="changes.reason"
            onChange={(v) => setReason(v as typeof reason)}
          />
        )}
        {action === 'fail' && (
          <label>
            {t('changes.rollbackDone')}
            <input
              type="checkbox"
              checked={rollbackDone}
              onChange={(e) => setRollbackDone(e.target.checked)}
            />
          </label>
        )}
        {action === 'review' && (
          <label>
            {t('changes.outcomeNote')}
            <textarea
              value={outcomeNote}
              onChange={(e) => setOutcomeNote(e.target.value)}
              required
            />
          </label>
        )}
        <Error error={error} />
        <Button type="submit" disabled={busy || (action === 'review' && !outcomeNote.trim())}>
          {t(`changes.action.${action}` as MessageKey)}
        </Button>
      </form>
    </Dialog>
  );
}
function TaskDialog({
  id,
  version,
  onClose,
  onDone,
}: {
  id: string;
  version: number;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [dueAt, setDueAt] = useState('');
  const [assigneeType, setAssigneeType] = useState<'user' | 'team'>('user');
  const [assignee, setAssignee] = useState<Assignee | null>(null);
  const [error, setError] = useState<ApiError>();
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await changesApi.addTask(id, {
        expectedVersion: version,
        title,
        description,
        ...(dueAt ? { dueAt: iso(dueAt) ?? '' } : {}),
        ...(assignee
          ? { [assigneeType === 'user' ? 'assignedUserId' : 'assignedTeamId']: assignee.id }
          : {}),
      });
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    }
  };
  return (
    <Dialog title={t('changes.addTask')} onClose={onClose}>
      <form onSubmit={(e) => void submit(e)} className="form-stack">
        <Field label={t('changes.title')} value={title} onChange={setTitle} required />
        <label>
          {t('changes.description')}
          <textarea value={description} onChange={(e) => setDescription(e.target.value)} />
        </label>
        <Field label={t('changes.dueAt')} value={dueAt} onChange={setDueAt} type="datetime-local" />
        <Choice
          label={t('changes.assigneeType')}
          value={assigneeType}
          values={['user', 'team']}
          prefix="changes.approverType"
          onChange={(v) => {
            setAssigneeType(v as 'user' | 'team');
            setAssignee(null);
          }}
        />
        <AssigneePicker type={assigneeType} value={assignee} onChange={setAssignee} />
        <Error error={error} />
        <Button type="submit">{t('changes.addTask')}</Button>
      </form>
    </Dialog>
  );
}
export function ChangeDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const menu = useContextMenu();
  const detail = useAsync((signal) => changesApi.get(id, signal), [id]);
  const transitions = usePagedList(
    (cursor, signal) => changesApi.transitions(id, cursor, signal),
    [id],
  );
  const [depth, setDepth] = useState(3);
  const canImpact = can('services.view') || can('services.manage');
  const impact = useAsync(
    (signal) => (canImpact ? changesApi.impact(id, depth, signal) : Promise.resolve(undefined)),
    [id, depth, canImpact],
  );
  const [action, setAction] = useState('');
  const [showAffected, setShowAffected] = useState(false);
  const [showTask, setShowTask] = useState(false);
  const [showEdit, setShowEdit] = useState(false);
  const [mutationError, setMutationError] = useState<ApiError>();
  const c = detail.data;
  const actions = c ? allowedActions(c) : [];
  const primary = actions.find(
    (x) => !['update', 'edit_affected', 'add_task', 'cancel', 'fail'].includes(x),
  );
  const runAction = (x: string) =>
    x === 'update'
      ? setShowEdit(true)
      : x === 'edit_affected'
        ? setShowAffected(true)
        : x === 'add_task'
          ? setShowTask(true)
          : setAction(x);
  const secondary = actions
    .filter((x) => x !== primary)
    .map((x) => ({
      id: x,
      label: t(`changes.action.${x}` as MessageKey),
      danger: x === 'cancel' || x === 'fail',
      onSelect: () => runAction(x),
    }));
  const reload = () => {
    detail.reload();
    transitions.reload();
    impact.reload();
    setAction('');
    setShowAffected(false);
    setShowTask(false);
    setShowEdit(false);
  };
  const label = (node: {
    type: ResourceType;
    id: string;
    name?: string | null;
    reference?: string | null;
    hidden?: boolean;
  }) => resourceLabel(node, (type) => t('changes.restricted', { type: t(`changes.type.${type}`) }));
  const remove = async (type: ResourceType, targetId: string) => {
    setMutationError(undefined);
    try {
      if (!c) return;
      await changesApi.removeAffected(id, type, targetId, c.version);
      reload();
    } catch (cause) {
      setMutationError(asApiError(cause));
    }
  };
  return (
    <div className="changes-workspace">
      <p className="changes-eyebrow">{c?.reference ?? t('changes.polish.eyebrow')}</p>
      <PageHeader
        title={c ? c.title : t('changes.detail')}
        actions={
          <>
            <Link to="/changes">{t('changes.back')}</Link>
            {primary && (
              <Button variant="primary" onClick={() => runAction(primary)}>
                {t(`changes.action.${primary}` as MessageKey)}
              </Button>
            )}
            {secondary.length > 0 && (
              <Button
                aria-label={t('changes.actions')}
                aria-haspopup="menu"
                onClick={(event) =>
                  menu.openAtElement(secondary, event.currentTarget, t('changes.actions'))
                }
              >
                •••
              </Button>
            )}
          </>
        }
      />
      <Error error={detail.error ?? mutationError} />
      {detail.loading && <Skeleton lines={6} />}
      {menu.menu}
      {c && (
        <>
          <div className="change-lifecycle">
            <div className="change-lifecycle-heading">
              <h2>{t('changes.polish.lifecycle')}</h2>
              <Status value={c.status} />
            </div>
            <ol aria-label={t('changes.polish.lifecycle')}>
              {lifecycle.map((step, index) => (
                <li key={step} aria-current={step === c.status ? 'step' : undefined}>
                  <span aria-hidden="true">{String(index + 1).padStart(2, '0')}</span>
                  {t(`changes.status.${step}`)}
                </li>
              ))}
              {['failed', 'rejected', 'cancelled'].includes(c.status) && (
                <li className={`change-lifecycle-${c.status}`} aria-current="step">
                  <span aria-hidden="true">{c.status === 'cancelled' ? '−' : '!'}</span>
                  {t(`changes.status.${c.status}`)}
                </li>
              )}
            </ol>
          </div>
          <div className="change-detail-grid">
            <section className="change-card change-facts">
              <h2>{t('changes.facts')}</h2>
              <dl>
                <dt>{t('changes.statusLabel')}</dt>
                <dd>
                  <Status value={c.status} />
                </dd>
                <dt>{t('changes.kind')}</dt>
                <dd>{t(`changes.kind.${c.kind}`)}</dd>
                <dt>{t('changes.risk')}</dt>
                <dd>{t(`changes.risk.${c.risk}`)}</dd>
                <dt>{t('changes.description')}</dt>
                <dd>{c.description || '—'}</dd>
                <dt>{t('changes.requester')}</dt>
                <dd>
                  <Person name={c.names.users[c.requesterId]} />
                </dd>
                <dt>{t('changes.owner')}</dt>
                <dd>
                  <Person name={c.ownerId ? c.names.users[c.ownerId] : undefined} />
                </dd>
                <dt>{t('changes.windowStart')}</dt>
                <dd>
                  {c.windowStart
                    ? new Intl.DateTimeFormat(locale, {
                        dateStyle: 'medium',
                        timeStyle: 'short',
                      }).format(new Date(c.windowStart))
                    : '—'}
                </dd>
                <dt>{t('changes.windowEnd')}</dt>
                <dd>
                  {c.windowEnd
                    ? new Intl.DateTimeFormat(locale, {
                        dateStyle: 'medium',
                        timeStyle: 'short',
                      }).format(new Date(c.windowEnd))
                    : '—'}
                </dd>
                <dt>{t('changes.approvedWindow')}</dt>
                <dd>
                  {c.approvedWindowStart && c.approvedWindowEnd
                    ? `${new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(c.approvedWindowStart))} – ${new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(c.approvedWindowEnd))}`
                    : '—'}
                </dd>
                {c.emergencyApprovedBy && (
                  <>
                    <dt>{t('changes.emergencyApprover')}</dt>
                    <dd>{c.names.users[c.emergencyApprovedBy] ?? c.emergencyApprovedBy}</dd>
                  </>
                )}
                <dt>{t('changes.rollbackPlan')}</dt>
                <dd>{c.rollbackPlan || '—'}</dd>
                <dt>{t('changes.emergencyJustification')}</dt>
                <dd>{c.emergencyJustification || '—'}</dd>
                <dt>{t('changes.outcomeNote')}</dt>
                <dd>{c.outcomeNote || '—'}</dd>
              </dl>
            </section>
            <section className="change-card">
              <h2>{t('changes.affected')}</h2>
              {!c.affected.length && (
                <p className="change-empty">{t('changes.polish.noResources')}</p>
              )}
              <div className="change-resources">
                {c.affected.map((x) => (
                  <div className="change-resource" key={x.relationshipId}>
                    <span className="change-resource-icon" aria-hidden="true">
                      {{ service: '◇', vm: '▤', asset: '▣', location: '⌖' }[x.type]}
                    </span>
                    <span>
                      <small>{t(`changes.type.${x.type}`)}</small>
                      {label(x)}
                    </span>{' '}
                    {actions.includes('edit_affected') && !x.hidden && (
                      <Button type="submit" onClick={() => void remove(x.type, x.id)}>
                        {t('changes.remove')}
                      </Button>
                    )}
                  </div>
                ))}
              </div>
              {actions.includes('edit_affected') && (
                <Button type="submit" onClick={() => setShowAffected(true)}>
                  {t('changes.addAffected')}
                </Button>
              )}
            </section>
            <section className="change-card change-approval-card">
              <h2>{t('changes.approvals')}</h2>
              {!c.approvals.length && (
                <p className="change-empty">{t('changes.polish.noApprovals')}</p>
              )}
              {c.approvals.map((x) => (
                <p key={x.id}>
                  <StatusBadge
                    tone={
                      x.status === 'approved'
                        ? 'success'
                        : x.status === 'rejected'
                          ? 'danger'
                          : 'warning'
                    }
                  >
                    {t(`approvals.status.${x.status}` as MessageKey)}
                  </StatusBadge>{' '}
                  {x.approverUserId
                    ? (c.names.users[x.approverUserId] ?? x.approverUserId)
                    : (x.approverTeamId ?? '—')}
                  {x.decidedAt
                    ? ` · ${new Intl.DateTimeFormat(locale).format(new Date(x.decidedAt))}`
                    : ''}
                </p>
              ))}
            </section>
            <section className="change-card">
              <h2>{t('changes.tasks')}</h2>
              <p>{t('changes.taskCount', { open: c.tasks.open, total: c.tasks.total })}</p>
              <progress
                className="change-task-progress"
                aria-label={t('changes.tasks')}
                value={c.tasks.total - c.tasks.open}
                max={Math.max(1, c.tasks.total)}
              />
              {!c.tasks.items.length && (
                <p className="change-empty">{t('changes.polish.noTasks')}</p>
              )}
              {c.tasks.items.map((x) => (
                <p className="change-task" key={x.id}>
                  <span
                    className={x.status === 'completed' ? 'change-task-done' : 'change-task-open'}
                    aria-hidden="true"
                  >
                    {x.status === 'completed' ? '✓' : '○'}
                  </span>
                  <Link to={`/tasks/${enc(x.id)}`}>{x.title}</Link> ·{' '}
                  {t(`tasks.status.${x.status}` as MessageKey)}
                </p>
              ))}
              {actions.includes('add_task') && (
                <Button type="submit" onClick={() => setShowTask(true)}>
                  {t('changes.addTask')}
                </Button>
              )}
            </section>
            <section className="change-card change-history">
              <h2>{t('changes.transitions')}</h2>
              <Error error={transitions.error} />
              {transitions.loading && <Skeleton />}
              {!transitions.loading && !transitions.items.length && (
                <p className="change-empty">{t('changes.polish.noHistory')}</p>
              )}
              {transitions.items.map((x) => (
                <p className="change-history-entry" key={x.id}>
                  {new Intl.DateTimeFormat(locale, {
                    dateStyle: 'medium',
                    timeStyle: 'short',
                  }).format(new Date(x.createdAt))}{' '}
                  · {x.fromStatus ? t(`changes.status.${x.fromStatus}` as MessageKey) : '—'} →{' '}
                  {t(`changes.status.${x.toStatus}` as MessageKey)} ·{' '}
                  {t(`changes.action.${x.operation}` as MessageKey)}
                  {x.reason ? ` · ${t(`changes.reason.${x.reason}` as MessageKey)}` : ''}
                </p>
              ))}
              {transitions.hasMore && (
                <Button type="submit" onClick={transitions.loadMore}>
                  {t('action.loadMore')}
                </Button>
              )}
            </section>
            {canImpact && (
              <section className="change-card change-impact">
                <h2>{t('changes.impact')}</h2>
                <Choice
                  label={t('changes.depth')}
                  value={String(depth)}
                  values={['1', '2', '3', '4', '5', '6']}
                  prefix="changes.depthValue"
                  onChange={(v) => setDepth(Number(v))}
                />
                <Error error={impact.error} />
                {impact.loading && <Skeleton />}
                {impact.data && !impact.data.starts.length && (
                  <p className="change-empty">{t('changes.polish.noImpact')}</p>
                )}
                {impact.data && (
                  <>
                    {impact.data.starts.map((start) => (
                      <div key={`${start.type}-${start.id}`}>
                        <h3>{label(start)}</h3>
                        {start.nodes.map((node, index) => (
                          <p key={`${node.type}-${node.id}-${index}`}>
                            {t(`changes.type.${node.type}`)} · {label(node)} · {t('changes.depth')}:{' '}
                            {node.depth}
                            {node.criticality &&
                              ` · ${t('changes.criticality')}: ${t(`services.criticality.${node.criticality}` as MessageKey)}`}
                          </p>
                        ))}
                        {(start.truncated || start.depthLimited || start.nodeLimited) && (
                          <p>{t('changes.truncated')}</p>
                        )}
                      </div>
                    ))}
                    {impact.data.skipped > 0 && (
                      <p>{t('changes.skipped', { count: impact.data.skipped })}</p>
                    )}
                    {impact.data.truncated && <p>{t('changes.truncated')}</p>}
                  </>
                )}
              </section>
            )}
          </div>
          {showEdit && (
            <Dialog wide title={t('changes.edit')} onClose={() => setShowEdit(false)}>
              <ChangeForm existing={c} onDone={reload} onClose={() => setShowEdit(false)} />
            </Dialog>
          )}
          {showAffected && (
            <AffectedDialog
              id={id}
              version={c.version}
              onClose={() => setShowAffected(false)}
              onDone={reload}
            />
          )}
          {showTask && (
            <TaskDialog
              id={id}
              version={c.version}
              onClose={() => setShowTask(false)}
              onDone={reload}
            />
          )}
          {action && (
            <ActionDialog
              change={c}
              action={action}
              onClose={() => setAction('')}
              onDone={reload}
            />
          )}
        </>
      )}
    </div>
  );
}
