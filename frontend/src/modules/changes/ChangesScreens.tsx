import { useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Dialog } from '../../platform/ui/Dialog';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { Button } from '../../platform/ui/Button';
import { PageHeader } from '../../platform/ui/PageHeader';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { servicesApi } from '../services/api';
import { infrastructureApi } from '../infrastructure/api';
import { assetsApi } from '../assets/api';
import { changesApi, type ChangeFields } from './api';
import { allowedActions, resourceLabel } from './helpers';
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
  return <Badge tone={tone[value]}>{t(`changes.status.${value}`)}</Badge>;
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
  const submit = async (e: FormEvent) => {
    e.preventDefault();
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
    <form onSubmit={(e) => void submit(e)} className="form-stack">
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
      <Field
        label={t('changes.windowStart')}
        value={start}
        onChange={setStart}
        type="datetime-local"
      />
      <Field label={t('changes.windowEnd')} value={end} onChange={setEnd} type="datetime-local" />
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
      <Error error={error} />
      <div className="actions">
        <Button type="submit" disabled={busy}>
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
export function ChangesListScreen({ mine = false }: { mine?: boolean }) {
  const { t, locale } = useI18n();
  const { can, session } = useSession();
  const [status, setStatus] = useState('');
  const [risk, setRisk] = useState('');
  const [kind, setKind] = useState('');
  const [windowFrom, setWindowFrom] = useState('');
  const [windowTo, setWindowTo] = useState('');
  const [owner, setOwner] = useState('');
  const [showCreate, setShowCreate] = useState(false);
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
    (cursor, signal) => changesApi.list(filter, cursor, signal),
    [status, risk, kind, windowFrom, windowTo, owner, mine, session?.userId],
  );
  const title = t(mine ? 'changes.mine' : 'nav.changes');
  return (
    <>
      <PageHeader
        title={title}
        actions={
          can('changes.manage') ? (
            <Button variant="primary" onClick={() => setShowCreate(true)}>
              {t('changes.create')}
            </Button>
          ) : null
        }
      />
      <form className="filters changes-filters" role="search" onSubmit={(e) => e.preventDefault()}>
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
        <TextField
          label={t('changes.windowFrom')}
          type="datetime-local"
          value={windowFrom}
          onChange={(e) => setWindowFrom(e.target.value)}
        />
        <TextField
          label={t('changes.windowTo')}
          type="datetime-local"
          value={windowTo}
          onChange={(e) => setWindowTo(e.target.value)}
        />
        {!mine ? (
          <Checkbox
            label={t('changes.mineFilter')}
            checked={!!owner}
            onChange={(e) => setOwner(e.target.checked ? 'mine' : '')}
          />
        ) : null}
      </form>
      <DataTable
        caption={title}
        columns={
          [
            {
              key: 'reference',
              header: t('changes.reference'),
              render: (c: Change) => <Link to={`/changes/${enc(c.id)}`}>{c.reference}</Link>,
            },
            { key: 'title', header: t('changes.title'), render: (c: Change) => c.title },
            {
              key: 'status',
              header: t('changes.statusLabel'),
              render: (c: Change) => <Status value={c.status} />,
            },
            {
              key: 'kind',
              header: t('changes.kind'),
              render: (c: Change) => t(`changes.kind.${c.kind}`),
            },
            {
              key: 'risk',
              header: t('changes.risk'),
              render: (c: Change) => t(`changes.risk.${c.risk}`),
            },
            {
              key: 'window',
              header: t('changes.windowStart'),
              render: (c: Change) => (
                <time dateTime={c.windowStart ?? undefined}>
                  {c.windowStart
                    ? new Intl.DateTimeFormat(locale, {
                        dateStyle: 'medium',
                        timeStyle: 'short',
                      }).format(new Date(c.windowStart))
                    : '—'}
                </time>
              ),
            },
          ] satisfies Column<Change>[]
        }
        rows={list.items}
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
        <Dialog title={t('changes.create')} onClose={() => setShowCreate(false)}>
          <ChangeForm
            onClose={() => setShowCreate(false)}
            onDone={(id) => navigate(`/changes/${enc(id)}`)}
          />
        </Dialog>
      )}
    </>
  );
}
export function ChangeCreateScreen() {
  const { t } = useI18n();
  return (
    <>
      <PageHeader title={t('changes.create')} />
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
    <>
      <PageHeader
        title={c ? `${c.reference} · ${c.title}` : t('changes.detail')}
        actions={<Link to="/changes">{t('changes.back')}</Link>}
      />
      <Error error={detail.error ?? mutationError} />
      {detail.loading && <p>{t('state.loading')}</p>}
      {c && (
        <>
          <section>
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
              <dd>{c.names.users[c.requesterId] ?? c.requesterId}</dd>
              <dt>{t('changes.owner')}</dt>
              <dd>{c.ownerId ? (c.names.users[c.ownerId] ?? c.ownerId) : '—'}</dd>
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
          <section>
            <h2>{t('changes.actions')}</h2>
            <div className="actions">
              {actions.map((x) => (
                <Button
                  type="submit"
                  key={x}
                  onClick={() =>
                    x === 'update'
                      ? setShowEdit(true)
                      : x === 'edit_affected'
                        ? setShowAffected(true)
                        : x === 'add_task'
                          ? setShowTask(true)
                          : setAction(x)
                  }
                >
                  {t(`changes.action.${x}` as MessageKey)}
                </Button>
              ))}
            </div>
          </section>
          <section>
            <h2>{t('changes.affected')}</h2>
            {c.affected.map((x) => (
              <p key={x.relationshipId}>
                {t(`changes.type.${x.type}`)}: {label(x)}{' '}
                {actions.includes('edit_affected') && !x.hidden && (
                  <Button type="submit" onClick={() => void remove(x.type, x.id)}>
                    {t('changes.remove')}
                  </Button>
                )}
              </p>
            ))}
            {actions.includes('edit_affected') && (
              <Button type="submit" onClick={() => setShowAffected(true)}>
                {t('changes.addAffected')}
              </Button>
            )}
          </section>
          <section>
            <h2>{t('changes.approvals')}</h2>
            {c.approvals.map((x) => (
              <p key={x.id}>
                {x.status} ·{' '}
                {x.approverUserId
                  ? (c.names.users[x.approverUserId] ?? x.approverUserId)
                  : (x.approverTeamId ?? '—')}
                {x.decidedAt
                  ? ` · ${new Intl.DateTimeFormat(locale).format(new Date(x.decidedAt))}`
                  : ''}
              </p>
            ))}
          </section>
          <section>
            <h2>{t('changes.tasks')}</h2>
            <p>{t('changes.taskCount', { open: c.tasks.open, total: c.tasks.total })}</p>
            {c.tasks.items.map((x) => (
              <p key={x.id}>
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
          <section>
            <h2>{t('changes.transitions')}</h2>
            <Error error={transitions.error} />
            {transitions.items.map((x) => (
              <p key={x.id}>
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
            <section>
              <h2>{t('changes.impact')}</h2>
              <Choice
                label={t('changes.depth')}
                value={String(depth)}
                values={['1', '2', '3', '4', '5', '6']}
                prefix="changes.depthValue"
                onChange={(v) => setDepth(Number(v))}
              />
              <Error error={impact.error} />
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
          {showEdit && (
            <Dialog title={t('changes.edit')} onClose={() => setShowEdit(false)}>
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
    </>
  );
}
