import { DateFilter, FilterBar } from '../../platform/ui/FilterBar';
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
import { Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Button } from '../../platform/ui/Button';
import { Table } from '../../platform/ui/Table';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { servicesApi } from '../services/api';
import { planningApi } from './api';
import {
  availableActions,
  calendarRange,
  calendarLegend,
  groupCalendarByDay,
  isProposedWindow,
  itemPath,
  localDate,
} from './helpers';
import {
  cancelReasons,
  holdReasons,
  itemTypes,
  removeReasons,
  replanReasons,
  statuses,
  type Initiative,
  type ItemType,
  type Milestone,
} from './types';
const enc = encodeURIComponent;
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
function Status({ value }: { value: string }) {
  const { t } = useI18n();
  return (
    <Badge
      tone={
        value === 'completed' || value === 'approved'
          ? 'success'
          : value === 'cancelled'
            ? 'danger'
            : value === 'on_hold' || value === 'proposed'
              ? 'warning'
              : 'info'
      }
    >
      {t(`planning.status.${value}` as MessageKey)}
    </Badge>
  );
}
function InitiativeForm({
  existing,
  onDone,
  onClose,
}: {
  existing?: Initiative;
  onDone: (id: string) => void;
  onClose?: () => void;
}) {
  const { t } = useI18n();
  const [title, setTitle] = useState(existing?.title ?? '');
  const [goal, setGoal] = useState(existing?.goal ?? '');
  const [owner, setOwner] = useState<Assignee | null>(
    existing ? { id: existing.ownerId, label: existing.ownerId } : null,
  );
  const [ownerId, setOwnerId] = useState(existing?.ownerId ?? '');
  const [targetDate, setTargetDate] = useState(existing?.targetDate ?? '');
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const result = existing
        ? await planningApi.update(existing.id, {
            expectedVersion: existing.version,
            title,
            goal,
            ownerUserId: owner?.id ?? ownerId,
            targetDate,
          })
        : await planningApi.create({ title, goal, ownerUserId: owner?.id ?? ownerId, targetDate });
      onDone(result.id);
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="form-stack" onSubmit={(e) => void submit(e)}>
      <Field label={t('planning.title')} value={title} onChange={setTitle} required />
      <label>
        {t('planning.goal')}
        <textarea value={goal} onChange={(e) => setGoal(e.target.value)} />
      </label>
      <p>{t('planning.owner')}</p>
      <AssigneePicker
        type="user"
        value={owner}
        onChange={(value) => {
          setOwner(value);
          setOwnerId(value?.id ?? '');
        }}
      />
      <Field label={t('planning.ownerId')} value={ownerId} onChange={setOwnerId} />
      <Field
        label={t('planning.targetDate')}
        value={targetDate}
        onChange={setTargetDate}
        type="date"
      />
      {error && <ApiErrorAlert error={error} />}
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
export function InitiativesListScreen({ mine = false }: { mine?: boolean }) {
  const { t, locale } = useI18n();
  const { can, session } = useSession();
  const [status, setStatus] = useState('');
  const [owner, setOwner] = useState('');
  const [q, setQ] = useState('');
  const [targetFrom, setTargetFrom] = useState('');
  const [targetTo, setTargetTo] = useState('');
  const list = usePagedList(
    (cursor, signal) =>
      planningApi.list(
        { status, owner: mine ? (session?.userId ?? '') : owner, q, targetFrom, targetTo },
        cursor,
        signal,
      ),
    [status, owner, q, targetFrom, targetTo, mine, session?.userId],
  );
  const activeFilters = [
    ...(status
      ? [
          {
            key: 'status',
            label: t(`planning.status.${status}` as MessageKey),
            onRemove: () => {
              setStatus('');
            },
          },
        ]
      : []),
    ...(!mine && owner
      ? [
          {
            key: 'owner',
            label: `${t('planning.ownerId')}: ${owner}`,
            onRemove: () => {
              setOwner('');
            },
          },
        ]
      : []),
    ...(q
      ? [
          {
            key: 'q',
            label: `${t('planning.search')}: ${q}`,
            onRemove: () => {
              setQ('');
            },
          },
        ]
      : []),
    ...(targetFrom
      ? [
          {
            key: 'from',
            label: `${t('planning.targetFrom')}: ${targetFrom}`,
            onRemove: () => {
              setTargetFrom('');
            },
          },
        ]
      : []),
    ...(targetTo
      ? [
          {
            key: 'to',
            label: `${t('planning.targetTo')}: ${targetTo}`,
            onRemove: () => {
              setTargetTo('');
            },
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t(mine ? 'planning.mine' : 'planning.list')}
        actions={
          can('planning.manage') ? (
            <Link className="btn btn-primary" to="/initiatives/new">
              {t('planning.create')}
            </Link>
          ) : undefined
        }
      />
      <FilterBar activeFilters={activeFilters}>
        <Select
          label={t('planning.status')}
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          options={[
            { value: '', label: t('filters.all') },
            ...statuses.map((value) => ({
              value,
              label: t(`planning.status.${value}` as MessageKey),
            })),
          ]}
        />
        {!mine ? (
          <TextField
            label={t('planning.ownerId')}
            value={owner}
            onChange={(e) => setOwner(e.target.value)}
          />
        ) : null}
        <TextField
          label={t('planning.search')}
          type="search"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <DateFilter
          label={t('planning.targetFrom')}
          type="date"
          value={targetFrom}
          onChange={setTargetFrom}
        />
        <DateFilter
          label={t('planning.targetTo')}
          type="date"
          value={targetTo}
          onChange={setTargetTo}
        />
      </FilterBar>
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={t(mine ? 'planning.mine' : 'planning.list')}
        columns={
          [
            {
              key: 'reference',
              sortValue: (x) => x.reference,
              header: t('planning.reference'),
              render: (x: Initiative) => (
                <Link to={`/initiatives/${enc(x.id)}`}>{x.reference}</Link>
              ),
            },
            {
              key: 'title',
              sortValue: (x) => x.title,
              header: t('planning.title'),
              render: (x: Initiative) => x.title,
            },
            {
              key: 'status',
              sortValue: (x) => x.status,
              header: t('planning.status'),
              render: (x: Initiative) => <Status value={x.status} />,
            },
            {
              key: 'owner',
              sortValue: (x) => x.ownerId,
              header: t('planning.owner'),
              render: (x: Initiative) => x.ownerId ?? '—',
            },
            {
              key: 'date',
              header: t('planning.targetDate'),
              render: (x: Initiative) =>
                x.targetDate ? (
                  <time dateTime={x.targetDate}>
                    {new Intl.DateTimeFormat(locale).format(new Date(`${x.targetDate}T00:00:00`))}
                  </time>
                ) : (
                  '—'
                ),
            },
          ] satisfies Column<Initiative>[]
        }
        rows={list.items}
        rowKey={(x) => x.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('planning.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
export function InitiativeCreateScreen() {
  const { t } = useI18n();
  return (
    <>
      <PageHeader title={t('planning.create')} />
      <InitiativeForm onDone={(id) => navigate(`/initiatives/${enc(id)}`)} />
    </>
  );
}
function ActionDialog({
  initiative,
  action,
  onClose,
  onDone,
}: {
  initiative: Initiative;
  action: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [approverType, setApproverType] = useState<'user' | 'team'>('user');
  const [approver, setApprover] = useState<Assignee | null>(null);
  const reasons =
    action === 'hold' ? holdReasons : action === 'replan' ? replanReasons : cancelReasons;
  const [reason, setReason] = useState<string>(reasons[0]);
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    const body: Record<string, unknown> = { expectedVersion: initiative.version };
    if (action === 'propose' && approver)
      body[approverType === 'user' ? 'approverUserId' : 'approverTeamId'] = approver.id;
    if (action === 'hold' || action === 'cancel' || action === 'replan') body.reason = reason;
    try {
      await planningApi.action(initiative.id, action, body);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog title={t(`planning.action.${action}` as MessageKey)} onClose={onClose}>
      <form className="form-stack" onSubmit={(e) => void submit(e)}>
        {action === 'propose' && (
          <>
            <label>
              {t('planning.approverType')}
              <select
                value={approverType}
                onChange={(e) => {
                  setApproverType(e.target.value as 'user' | 'team');
                  setApprover(null);
                }}
              >
                <option value="user">{t('planning.approver.user')}</option>
                <option value="team">{t('planning.approver.team')}</option>
              </select>
            </label>
            <AssigneePicker type={approverType} value={approver} onChange={setApprover} />
          </>
        )}
        {(action === 'hold' || action === 'cancel' || action === 'replan') && (
          <label>
            {t('planning.reason')}
            <select value={reason} onChange={(e) => setReason(e.target.value)}>
              {reasons.map((x) => (
                <option key={x} value={x}>
                  {t(`planning.reason.${x}` as MessageKey)}
                </option>
              ))}
            </select>
          </label>
        )}
        {error && <ApiErrorAlert error={error} />}
        <div className="actions">
          <Button type="submit" disabled={busy || (action === 'propose' && !approver)}>
            {t('action.save')}
          </Button>
          <Button type="button" onClick={onClose}>
            {t('action.cancel')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
function MilestoneDialog({
  initiative,
  milestone,
  onClose,
  onDone,
}: {
  initiative: Initiative;
  milestone: Milestone | undefined;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [title, setTitle] = useState(milestone?.title ?? '');
  const [dueDate, setDueDate] = useState(milestone?.dueDate ?? '');
  const [position, setPosition] = useState(String(milestone?.position ?? ''));
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const body = {
        expectedVersion: milestone?.version ?? initiative.version,
        title,
        dueDate,
        ...(position ? { position: Number(position) } : {}),
      };
      if (milestone) await planningApi.updateMilestone(initiative.id, milestone.id, body);
      else await planningApi.addMilestone(initiative.id, body);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog
      title={t(milestone ? 'planning.milestone.edit' : 'planning.milestone.add')}
      onClose={onClose}
    >
      <form className="form-stack" onSubmit={(e) => void submit(e)}>
        <Field label={t('planning.title')} value={title} onChange={setTitle} required />
        <Field
          label={t('planning.dueDate')}
          value={dueDate}
          onChange={setDueDate}
          type="date"
          required
        />
        <Field
          label={t('planning.position')}
          value={position}
          onChange={setPosition}
          type="number"
        />
        {error && <ApiErrorAlert error={error} />}
        <Button type="submit" disabled={busy}>
          {t('action.save')}
        </Button>
      </form>
    </Dialog>
  );
}
function ItemDialog({
  initiative,
  onClose,
  onDone,
}: {
  initiative: Initiative;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [type, setType] = useState<ItemType>('change');
  const [id, setId] = useState('');
  const [error, setError] = useState<ApiError>();
  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(undefined);
    try {
      await planningApi.addItem(initiative.id, type, id, initiative.version);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    }
  }
  return (
    <Dialog title={t('planning.item.add')} onClose={onClose}>
      <form className="form-stack" onSubmit={(e) => void submit(e)}>
        <label>
          {t('planning.type')}
          <select value={type} onChange={(e) => setType(e.target.value as ItemType)}>
            {itemTypes.map((x) => (
              <option key={x} value={x}>
                {t(`planning.type.${x}` as MessageKey)}
              </option>
            ))}
          </select>
        </label>
        <Field label={t('planning.itemId')} value={id} onChange={setId} required />
        {error && <ApiErrorAlert error={error} />}
        <Button type="submit">{t('planning.item.add')}</Button>
      </form>
    </Dialog>
  );
}
export function InitiativeDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const detail = useAsync((signal) => planningApi.get(id, signal), [id]);
  const transitions = usePagedList(
    (cursor, signal) => planningApi.transitions(id, cursor, signal),
    [id],
  );
  const items = usePagedList((cursor, signal) => planningApi.items(id, cursor, signal), [id]);
  const [action, setAction] = useState<string>();
  const [edit, setEdit] = useState(false);
  const [milestone, setMilestone] = useState<Milestone | 'new'>();
  const [addItem, setAddItem] = useState(false);
  const [removeMilestone, setRemoveMilestone] = useState<Milestone>();
  const [removeReason, setRemoveReason] = useState<string>(removeReasons[0]);
  const [error, setError] = useState<ApiError>();
  const data = detail.data;
  function refresh() {
    detail.reload();
    items.reload();
    transitions.reload();
    setAction(undefined);
    setEdit(false);
    setMilestone(undefined);
    setAddItem(false);
    setRemoveMilestone(undefined);
  }
  async function milestoneAction(m: Milestone, operation: string, reason?: string) {
    setError(undefined);
    try {
      await planningApi.milestoneAction(id, m.id, operation, m.version, reason);
      refresh();
    } catch (cause) {
      setError(asApiError(cause));
    }
  }
  if (detail.error) return <ApiErrorAlert error={detail.error} onRetry={detail.reload} />;
  if (!data) return <p>{t('state.loading')}</p>;
  const ops = availableActions(data);
  const canEditPlan = data.status === 'idea' || data.status === 'planning';
  const canUpdateMilestoneProgress =
    can('planning.manage') &&
    ['idea', 'planning', 'approved', 'active', 'on_hold'].includes(data.status);
  return (
    <>
      <PageHeader
        title={`${data.reference} · ${data.title}`}
        actions={
          canEditPlan && ops.includes('update') ? (
            <Button type="submit" onClick={() => setEdit(true)}>
              {t('planning.edit')}
            </Button>
          ) : undefined
        }
      />
      <p>
        <Status value={data.status} />{' '}
        {data.statusReason && t(`planning.reason.${data.statusReason}` as MessageKey)}
      </p>
      <dl>
        <dt>{t('planning.goal')}</dt>
        <dd>{data.goal ?? '—'}</dd>
        <dt>{t('planning.owner')}</dt>
        <dd>{data.names.users[data.ownerId] ?? data.ownerId}</dd>
        <dt>{t('planning.targetDate')}</dt>
        <dd>{data.targetDate ?? '—'}</dd>
        <dt>{t('planning.createdBy')}</dt>
        <dd>{data.names.users[data.createdBy] ?? data.createdBy}</dd>
      </dl>
      <div className="actions">
        {['start-planning', 'propose', 'replan', 'activate', 'hold', 'resume', 'complete', 'cancel']
          .filter((x) => ops.includes(x.replace('-', '_')))
          .map((x) => (
            <Button type="submit" key={x} onClick={() => setAction(x)}>
              {t(`planning.action.${x}` as MessageKey)}
            </Button>
          ))}
      </div>
      {error && <ApiErrorAlert error={error} />}
      <section>
        <h2>{t('planning.progress')}</h2>
        <p>
          {t('planning.progressItems', { count: data.progress.items })} ·{' '}
          {t('planning.progressMilestones', {
            done: data.progress.milestones.done,
            total: data.progress.milestones.total,
            overdue: data.progress.milestones.overdue,
          })}
        </p>
        {data.progress.itemsLimitExceeded && <p>{t('planning.truncated')}</p>}
        {(['changesByStatus', 'tasksByStatus', 'procurementRequestsByStatus'] as const).map(
          (key) => (
            <p key={key}>
              {t(`planning.progress.${key}` as MessageKey)}:{' '}
              {data.progress[key]
                ? Object.entries(data.progress[key])
                    .map(
                      ([status, count]) =>
                        `${t(`${key === 'changesByStatus' ? 'changes.status' : key === 'tasksByStatus' ? 'tasks.status' : 'procurement.need.status'}.${status}` as MessageKey)}: ${count}`,
                    )
                    .join(', ') || '—'
                : t('planning.restricted')}
            </p>
          ),
        )}
      </section>
      <section>
        <h2>{t('planning.approvals')}</h2>
        <ul>
          {data.approvals.map((a) => (
            <li key={a.id}>
              <Link to={`/approvals/${enc(a.id)}`}>
                {t(`approvals.status.${a.status}` as MessageKey)}
              </Link>{' '}
              ·{' '}
              {a.approverUserId
                ? (data.names.users[a.approverUserId] ?? a.approverUserId)
                : a.approverTeamId}
            </li>
          ))}
        </ul>
      </section>
      <section>
        <h2>{t('planning.milestones')}</h2>
        {canEditPlan && ops.includes('edit_milestones') && (
          <Button type="submit" onClick={() => setMilestone('new')}>
            {t('planning.milestone.add')}
          </Button>
        )}
        <Table>
          <thead>
            <tr>
              <th>{t('planning.title')}</th>
              <th>{t('planning.dueDate')}</th>
              <th>{t('planning.status')}</th>
              <th>{t('planning.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {data.milestones.map((m) => (
              <tr key={m.id}>
                <td>{m.title}</td>
                <td>{m.dueDate}</td>
                <td>{m.doneAt ? t('planning.done') : t('planning.open')}</td>
                <td>
                  {canEditPlan && ops.includes('edit_milestones') && (
                    <>
                      <Button type="submit" onClick={() => setMilestone(m)}>
                        {t('planning.edit')}
                      </Button>
                      <Button type="submit" onClick={() => setRemoveMilestone(m)}>
                        {t('planning.milestone.remove')}
                      </Button>
                    </>
                  )}
                  {canUpdateMilestoneProgress && (
                    <Button
                      type="submit"
                      onClick={() => void milestoneAction(m, m.doneAt ? 'reopen' : 'complete')}
                    >
                      {t(m.doneAt ? 'planning.milestone.reopen' : 'planning.milestone.complete')}
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      </section>
      <section>
        <h2>{t('planning.items')}</h2>
        {canEditPlan && ops.includes('edit_items') && (
          <Button type="submit" onClick={() => setAddItem(true)}>
            {t('planning.item.add')}
          </Button>
        )}
        {items.error && <ApiErrorAlert error={items.error} onRetry={items.reload} />}
        <Table>
          <thead>
            <tr>
              <th>{t('planning.type')}</th>
              <th>{t('planning.title')}</th>
              <th>{t('planning.status')}</th>
              <th>{t('planning.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {items.items.map((x) => (
              <tr key={x.relationshipId}>
                <td>{t(`planning.type.${x.type}` as MessageKey)}</td>
                <td>
                  {x.hidden ? (
                    t('planning.restrictedType', {
                      type: t(`planning.type.${x.type}` as MessageKey),
                    })
                  ) : itemPath(x) ? (
                    <Link to={itemPath(x)!}>{x.reference ?? x.title ?? x.id}</Link>
                  ) : (
                    (x.reference ?? x.title ?? x.id)
                  )}
                </td>
                <td>
                  {x.hidden || !x.status
                    ? '—'
                    : t(
                        `${x.type === 'change' ? 'changes.status' : x.type === 'task' ? 'tasks.status' : x.type === 'service' ? 'services.status' : 'procurement.need.status'}.${x.status}` as MessageKey,
                      )}
                </td>
                <td>
                  {canEditPlan && ops.includes('edit_items') && !x.hidden && (
                    <Button
                      type="submit"
                      onClick={() =>
                        void (async () => {
                          setError(undefined);
                          try {
                            await planningApi.removeItem(id, x.type, x.id, data.version);
                            refresh();
                          } catch (cause) {
                            setError(asApiError(cause));
                          }
                        })()
                      }
                    >
                      {t('planning.item.remove')}
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
        {items.hasMore && (
          <Button type="submit" onClick={items.loadMore}>
            {t('action.loadMore')}
          </Button>
        )}
        {data.items.truncated && <p>{t('planning.truncated')}</p>}
      </section>
      <section>
        <h2>{t('planning.transitions')}</h2>
        {transitions.error && (
          <ApiErrorAlert error={transitions.error} onRetry={transitions.reload} />
        )}
        <ol>
          {transitions.items.map((x) => (
            <li key={x.id}>
              {new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(
                new Date(x.createdAt),
              )}{' '}
              · {x.fromStatus ? t(`planning.status.${x.fromStatus}` as MessageKey) : '—'} →{' '}
              <Status value={x.toStatus} /> · {t(`planning.operation.${x.operation}` as MessageKey)}{' '}
              · {x.actorUserId ? (data.names.users[x.actorUserId] ?? x.actorUserId) : x.actorSystem}
            </li>
          ))}
        </ol>
        {transitions.hasMore && (
          <Button type="submit" onClick={transitions.loadMore}>
            {t('action.loadMore')}
          </Button>
        )}
      </section>
      {action && (
        <ActionDialog
          initiative={data}
          action={action}
          onClose={() => setAction(undefined)}
          onDone={refresh}
        />
      )}
      {edit && (
        <Dialog title={t('planning.edit')} onClose={() => setEdit(false)}>
          <InitiativeForm existing={data} onDone={refresh} onClose={() => setEdit(false)} />
        </Dialog>
      )}
      {milestone && (
        <MilestoneDialog
          initiative={data}
          milestone={milestone === 'new' ? undefined : milestone}
          onClose={() => setMilestone(undefined)}
          onDone={refresh}
        />
      )}
      {addItem && (
        <ItemDialog initiative={data} onClose={() => setAddItem(false)} onDone={refresh} />
      )}
      {removeMilestone && (
        <Dialog
          title={t('planning.milestone.remove')}
          onClose={() => setRemoveMilestone(undefined)}
        >
          <label>
            {t('planning.reason')}
            <select value={removeReason} onChange={(e) => setRemoveReason(e.target.value)}>
              {removeReasons.map((x) => (
                <option key={x} value={x}>
                  {t(`planning.reason.${x}` as MessageKey)}
                </option>
              ))}
            </select>
          </label>
          <Button
            type="submit"
            onClick={() => void milestoneAction(removeMilestone, 'remove', removeReason)}
          >
            {t('planning.milestone.remove')}
          </Button>
        </Dialog>
      )}
    </>
  );
}
export function MaintenanceCalendarScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const [anchor, setAnchor] = useState(localDate(new Date()));
  const [view, setView] = useState<'month' | 'week'>('month');
  const initial = calendarRange(anchor, view);
  const [fromDate, setFromDate] = useState(localDate(new Date(initial.from)));
  const [toDate, setToDate] = useState(localDate(new Date(Date.parse(initial.to) - 1)));
  const from = fromDate ? new Date(`${fromDate}T00:00:00`).toISOString() : initial.from;
  const to = toDate
    ? (() => {
        const next = new Date(`${toDate}T00:00:00`);
        next.setDate(next.getDate() + 1);
        return next.toISOString();
      })()
    : initial.to;
  function setPreset(date: string, mode: 'month' | 'week') {
    const next = calendarRange(date, mode);
    setFromDate(localDate(new Date(next.from)));
    setToDate(localDate(new Date(Date.parse(next.to) - 1)));
  }
  const calendar = useAsync((signal) => planningApi.calendar(from, to, signal), [from, to]);
  const groups = groupCalendarByDay(calendar.data?.items ?? []);
  const legend = calendarLegend(calendar.data?.items ?? []);
  const serviceIds = [
    ...new Set(
      (calendar.data?.items ?? []).flatMap((entry) =>
        entry.affected
          .filter((a) => a.type === 'service' && !a.hidden && !a.missing)
          .map((a) => a.id),
      ),
    ),
  ];
  const criticality = useAsync(
    async (signal) => {
      const result: Record<string, string> = {};
      if (!can('services.view') && !can('services.manage')) return result;
      for (let start = 0; start < serviceIds.length; start += 6) {
        const batch = await Promise.allSettled(
          serviceIds.slice(start, start + 6).map((id) => servicesApi.get(id, signal)),
        );
        batch.forEach((entry, index) => {
          const id = serviceIds[start + index];
          if (entry.status === 'fulfilled' && id) result[id] = entry.value.criticality;
        });
      }
      return result;
    },
    [calendar.data, can('services.view'), can('services.manage')],
  );
  return (
    <>
      <PageHeader title={t('planning.calendar')} />
      <FilterBar>
        <label>
          {t('planning.calendar.view')}
          <select
            value={view}
            onChange={(e) => {
              const mode = e.target.value as 'month' | 'week';
              setView(mode);
              setPreset(anchor, mode);
            }}
          >
            <option value="month">{t('planning.calendar.month')}</option>
            <option value="week">{t('planning.calendar.week')}</option>
          </select>
        </label>
        <DateFilter
          label={t('planning.calendar.date')}
          value={anchor}
          onChange={(date) => {
            setAnchor(date);
            if (date) setPreset(date, view);
          }}
          type="date"
        />
        <DateFilter
          label={t('planning.calendar.from')}
          value={fromDate}
          onChange={setFromDate}
          type="date"
        />
        <DateFilter
          label={t('planning.calendar.to')}
          value={toDate}
          onChange={setToDate}
          type="date"
        />
      </FilterBar>
      {calendar.error && <ApiErrorAlert error={calendar.error} onRetry={calendar.reload} />}
      {calendar.loading && <p>{t('state.loading')}</p>}
      {!calendar.loading && calendar.data?.items.length === 0 && (
        <p>{t('planning.calendar.empty')}</p>
      )}
      {calendar.data?.truncated && <p>{t('planning.truncated')}</p>}
      {legend.proposed || legend.firm ? (
        <ul className="calendar-legend" aria-label={t('planning.calendar.legend')}>
          {legend.firm ? (
            <li>
              <span className="calendar-swatch calendar-swatch-firm" aria-hidden="true" />
              {t('planning.calendar.legend.firm')}
            </li>
          ) : null}
          {legend.proposed ? (
            <li>
              <span className="calendar-swatch calendar-swatch-proposed" aria-hidden="true" />
              {t('planning.calendar.legend.proposed')}
            </li>
          ) : null}
        </ul>
      ) : null}
      {Object.entries(groups)
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([day, entries]) => (
          <section key={day}>
            <h2>
              {new Intl.DateTimeFormat(locale, { dateStyle: 'full' }).format(
                new Date(`${day}T00:00:00`),
              )}
            </h2>
            <ul className="calendar-entries">
              {entries.map((x) => (
                <li
                  key={x.changeId}
                  className={
                    isProposedWindow(x)
                      ? 'calendar-entry calendar-entry-proposed'
                      : 'calendar-entry'
                  }
                >
                  {x.title ? (
                    <Link to={`/changes/${enc(x.changeId)}`}>
                      {x.reference} · {x.title}
                    </Link>
                  ) : (
                    x.reference
                  )}{' '}
                  · {x.kind && <Badge>{t(`changes.kind.${x.kind}` as MessageKey)}</Badge>}{' '}
                  {x.risk && <Badge>{t(`changes.risk.${x.risk}` as MessageKey)}</Badge>}{' '}
                  <Badge>{t(`changes.status.${x.status}` as MessageKey)}</Badge>{' '}
                  {isProposedWindow(x) ? (
                    <Badge tone="info">{t('planning.calendar.proposed')}</Badge>
                  ) : null}
                  <p>
                    {new Intl.DateTimeFormat(locale, {
                      dateStyle: 'short',
                      timeStyle: 'short',
                    }).format(new Date(x.windowStart))}{' '}
                    –{' '}
                    {new Intl.DateTimeFormat(locale, {
                      dateStyle: 'short',
                      timeStyle: 'short',
                    }).format(new Date(x.windowEnd))}
                  </p>
                  <p>
                    {x.affected
                      .map((a) =>
                        a.hidden
                          ? t('planning.restrictedType', {
                              type: t(`planning.type.${a.type}` as MessageKey),
                            })
                          : `${a.name ?? a.reference ?? a.id}${criticality.data?.[a.id] ? ` (${t(`services.criticality.${criticality.data[a.id]}` as MessageKey)})` : ''}`,
                      )
                      .join(', ')}
                  </p>
                  {x.initiatives.map((i) => (
                    <Link key={i.id} to={`/initiatives/${enc(i.id)}`}>
                      {i.reference}
                    </Link>
                  ))}
                </li>
              ))}
            </ul>
          </section>
        ))}
    </>
  );
}
