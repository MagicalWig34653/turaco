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
import { DateTimeField } from '../../platform/ui/DateTimeField';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { AffectedPicker } from './AffectedPicker';
import { ChangeTicketsCard } from './ChangeTicketsCard';
import { changesApi, type ChangeFields } from './api';
import {
  allowedActions,
  resourceLabel,
  changeMetrics,
  lifecycle,
  windowMinutes,
  matchesChangeMetric,
  candidateKey,
  issuesByStep,
  mergeMissing,
  missingForSubmit,
  readinessFromIssues,
  requiredForSubmit,
  type ChangeMetricKey,
  type ReadinessField,
} from './helpers';
import {
  cancelReasons,
  failReasons,
  kinds,
  risks,
  statuses,
  type AffectedCandidate,
  type Change,
  type ChangeDetail,
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
  return (
    <StatusBadge tone={tone[value]} live={value === 'in_progress'}>
      {t(`changes.status.${value}`)}
    </StatusBadge>
  );
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
  if (type === 'datetime-local')
    return <DateTimeField label={label} value={value} onChange={onChange} required={required} />;
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
/** Field-level 400 details of one step: names exactly what the server found missing. */
function StepIssues({ fields }: { fields: ReadinessField[] }) {
  const { t } = useI18n();
  if (!fields.length) return null;
  return (
    <ul className="change-validation change-issues" role="alert">
      {fields.map((field) => (
        <li key={field}>{t(`changes.ready.${field}`)}</li>
      ))}
    </ul>
  );
}
/** What a draft still lacks before "Submit", each item with a way to fix it. */
function ReadinessChecklist({
  change,
  missing,
  onFix,
}: {
  change: Pick<Change, 'kind' | 'risk'>;
  missing: ReadinessField[];
  onFix?: ((field: ReadinessField) => void) | undefined;
}) {
  const { t } = useI18n();
  return (
    <section className="change-checklist" aria-label={t('changes.ready.title')}>
      <h3>{t('changes.ready.title')}</h3>
      <ul>
        {mergeMissing(requiredForSubmit(change), missing).map((field) => {
          const open = missing.includes(field);
          return (
            <li key={field} className={open ? 'is-missing' : 'is-done'}>
              <span aria-hidden="true">{open ? '✗' : '✓'}</span>
              <span>
                {t(`changes.ready.${field}`)}
                <span className="visually-hidden">
                  {' '}
                  {t(open ? 'changes.ready.stateMissing' : 'changes.ready.stateDone')}
                </span>
              </span>
              {open && onFix ? (
                <Button type="button" onClick={() => onFix(field)}>
                  {t(`changes.ready.fix.${field}`)}
                </Button>
              ) : null}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
function ChangeForm({
  existing,
  onDone,
  onClose,
}: {
  existing?: ChangeDetail;
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
    existing?.ownerId
      ? {
          id: existing.ownerId,
          label: existing.names.users[existing.ownerId] ?? t('changes.polish.unavailable'),
        }
      : null,
  );
  const [affected, setAffected] = useState<AffectedCandidate[]>([]);
  // Set once the Change exists but some affected resources could not be linked.
  const [createdId, setCreatedId] = useState<string>();
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const invalidWindow = !!(start || end) && windowMinutes(start, end) === null;
  const stepIssues = issuesByStep(error?.issues ?? []);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (invalidWindow) return;
    if (createdId) return onDone(createdId);
    setBusy(true);
    setError(undefined);
    let created: string | undefined;
    try {
      const fields: ChangeFields = {
        title,
        description,
        kind,
        risk,
        ownerUserId: owner?.id ?? '',
        rollbackPlan,
        window: { start: iso(start), end: iso(end) },
      };
      const result = existing
        ? await changesApi.update(existing.id, { ...fields, expectedVersion: existing.version })
        : await changesApi.create(fields);
      // The create/update DTO has no emergency justification field. Keep it for the assessment dialog.
      if (emergencyJustification)
        sessionStorage.setItem(`change-emergency-${result.id}`, emergencyJustification);
      if (!existing && affected.length) {
        created = result.id;
        let version = result.version;
        for (const item of affected) {
          await changesApi.addAffected(result.id, item.type, item.id, version);
          version = (await changesApi.get(result.id)).version;
        }
      }
      onDone(result.id);
    } catch (cause) {
      if (created) setCreatedId(created);
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
        <div className="change-form-window">
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
        </div>
        <p
          className={invalidWindow ? 'change-validation' : 'field-hint'}
          role={invalidWindow ? 'alert' : undefined}
        >
          {t(invalidWindow ? 'changes.polish.windowInvalid' : 'changes.polish.windowHint')}
        </p>
        <label>
          {t('changes.rollbackPlan')}
          <textarea value={rollbackPlan} onChange={(e) => setRollbackPlan(e.target.value)} />
          <span className="field-hint">{t('changes.polish.rollbackHint')}</span>
        </label>
        <StepIssues fields={stepIssues.planning} />
        {kind === 'emergency' && (
          <label>
            {t('changes.emergencyJustification')}
            <textarea
              value={emergencyJustification}
              onChange={(e) => setEmergencyJustification(e.target.value)}
            />
          </label>
        )}
      </section>
      {!existing && (
        <section className="change-form-section change-form-resources">
          <h3>{t('changes.polish.resources')}</h3>
          <p>
            {t(
              kind === 'standard'
                ? 'changes.polish.resourcesHintStandard'
                : 'changes.polish.resourcesHint',
            )}
          </p>
          <AffectedPicker selected={affected} onChange={setAffected} />
          <StepIssues fields={stepIssues.resources} />
        </section>
      )}
      <section className="change-form-section change-form-owner">
        <h3>{t('changes.polish.ownership')}</h3>
        <p>{t('changes.polish.ownershipHint')}</p>
        {owner ? (
          <div className="change-owner-selected">
            <Person name={owner.label} />
            <Button type="button" onClick={() => setOwner(null)}>
              {t('changes.polish.changeOwner')}
            </Button>
          </div>
        ) : (
          <AssigneePicker
            type="user"
            label={t('changes.owner')}
            value={owner}
            onChange={setOwner}
          />
        )}
      </section>
      <Error error={error} />
      {createdId ? (
        <p className="change-validation" role="alert">
          {t('changes.polish.partlyCreated')}
        </p>
      ) : null}
      <div className="actions change-form-footer">
        <Button type="button" onClick={onClose ?? (() => window.history.back())}>
          {t('action.cancel')}
        </Button>
        <Button variant="primary" type="submit" disabled={busy || invalidWindow}>
          {createdId
            ? t('changes.polish.openCreated')
            : existing
              ? t('action.save')
              : t('changes.create')}
        </Button>
      </div>
    </form>
  );
}
const operations = new Set([
  'update',
  'edit_affected',
  'submit',
  'assess',
  'schedule',
  'start',
  'complete',
  'fail',
  'review',
  'close',
  'cancel',
  'add_task',
  'create',
  'approval_decided',
  'affected_added',
  'affected_removed',
]);
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
  linked,
  onClose,
  onDone,
}: {
  id: string;
  version: number;
  linked: readonly string[];
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [selected, setSelected] = useState<AffectedCandidate[]>([]);
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!selected.length) return;
    setBusy(true);
    setError(undefined);
    try {
      let current = version;
      for (const item of selected) {
        await changesApi.addAffected(id, item.type, item.id, current);
        current = (await changesApi.get(id)).version;
      }
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('changes.addAffected')} onClose={onClose}>
      <form onSubmit={(e) => void submit(e)} className="form-stack">
        <AffectedPicker selected={selected} onChange={setSelected} linked={linked} />
        <Error error={error} />
        <Button type="submit" variant="primary" busy={busy} disabled={!selected.length}>
          {t('changes.pick.add', { count: selected.length })}
        </Button>
      </form>
    </Dialog>
  );
}
function ActionDialog({
  change,
  action,
  affectedCount,
  onClose,
  onDone,
  onFix,
}: {
  change: Change;
  action: string;
  affectedCount: number;
  onClose: () => void;
  onDone: () => void;
  onFix: (field: ReadinessField) => void;
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
  const checksReadiness = action === 'submit' || action === 'assess';
  // Local check first; fields named by the server's 400 are added so nothing it found is hidden.
  const missing = checksReadiness
    ? mergeMissing(
        missingForSubmit(change, affectedCount),
        readinessFromIssues(error?.issues ?? []),
      )
    : [];
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (missing.length) return;
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
        {checksReadiness ? (
          <ReadinessChecklist change={change} missing={missing} onFix={onFix} />
        ) : null}
        {error && !error.issues.length ? <Error error={error} /> : null}
        <Button
          type="submit"
          disabled={busy || missing.length > 0 || (action === 'review' && !outcomeNote.trim())}
        >
          {t(`changes.action.${action}` as MessageKey)}
        </Button>
        {missing.length > 0 ? (
          <p className="field-hint" role="status">
            {t('changes.ready.blocked', { count: missing.length })}
          </p>
        ) : null}
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
  const { t: translate } = useI18n();
  const operationLabel = (operation: string) =>
    operations.has(operation) ? translate(`changes.action.${operation}` as MessageKey) : '';
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
  // Closes the confirmation and opens the place where the missing item is entered.
  const fixReadiness = (field: ReadinessField) => {
    setAction('');
    if (field === 'affectedResources') setShowAffected(true);
    else setShowEdit(true);
  };
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
            <div className="change-detail-main">
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
                <h2>{t('changes.tasks')}</h2>
                {c.tasks.total > 0 && (
                  <p>{t('changes.taskCount', { open: c.tasks.open, total: c.tasks.total })}</p>
                )}
                {c.tasks.total > 0 && (
                  <progress
                    className="change-task-progress"
                    aria-label={t('changes.tasks')}
                    value={c.tasks.total - c.tasks.open}
                    max={c.tasks.total}
                  />
                )}
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
                              {t(`changes.type.${node.type}`)} · {label(node)} ·{' '}
                              {t('changes.depth')}: {node.depth}
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
            <div className="change-detail-side">
              {c.status === 'draft' &&
              actions.includes('submit') &&
              missingForSubmit(c, c.affected.length).length > 0 ? (
                <ReadinessChecklist
                  change={c}
                  missing={missingForSubmit(c, c.affected.length)}
                  onFix={fixReadiness}
                />
              ) : null}
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
              {can('tickets.view') || can('tickets.manage') ? (
                <ChangeTicketsCard changeId={c.id} />
              ) : null}
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
              <section className="change-card change-history">
                <h2>{t('changes.transitions')}</h2>
                <Error error={transitions.error} />
                {transitions.loading && <Skeleton />}
                {!transitions.loading && !transitions.items.length && (
                  <p className="change-empty">{t('changes.polish.noHistory')}</p>
                )}
                {transitions.items.length > 0 && (
                  <ol className="change-history-list">
                    {transitions.items.map((x) => (
                      <li className="change-history-entry" key={x.id}>
                        <span className="change-history-dot" aria-hidden="true" />
                        <div>
                          <strong>
                            {x.fromStatus
                              ? `${t(`changes.status.${x.fromStatus}` as MessageKey)} → `
                              : ''}
                            {t(`changes.status.${x.toStatus}` as MessageKey)}
                          </strong>
                          <small>
                            {[
                              new Intl.DateTimeFormat(locale, {
                                dateStyle: 'medium',
                                timeStyle: 'short',
                              }).format(new Date(x.createdAt)),
                              operationLabel(x.operation),
                              x.reason ? t(`changes.reason.${x.reason}` as MessageKey) : '',
                            ]
                              .filter(Boolean)
                              .join(' · ')}
                          </small>
                        </div>
                      </li>
                    ))}
                  </ol>
                )}
                {transitions.hasMore && (
                  <Button type="submit" onClick={transitions.loadMore}>
                    {t('action.loadMore')}
                  </Button>
                )}
              </section>
            </div>
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
              linked={c.affected.filter((x) => !x.hidden).map((x) => candidateKey(x))}
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
              affectedCount={c.affected.length}
              onClose={() => setAction('')}
              onDone={reload}
              onFix={fixReadiness}
            />
          )}
        </>
      )}
    </div>
  );
}
