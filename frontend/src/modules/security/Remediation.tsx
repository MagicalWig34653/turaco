import { useState, type FormEvent } from 'react';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import type { ApiError } from '../../platform/api/client';
import { formatDate, formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Dialog } from '../../platform/ui/Dialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Button } from '../../platform/ui/Button';
import { Table } from '../../platform/ui/Table';
import { AssigneePicker, type Assignee, type AssigneeType } from '../tasks/AssigneePicker';
import { changesApi } from '../changes/api';
import { securityApi } from './api';
import { overviewLinks } from './helpers';

function TaskSection({
  kind,
  id,
  version,
  onCreated,
}: {
  kind: 'advisories' | 'findings';
  id: string;
  version: number;
  onCreated: () => void;
}) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const tasks = useAsync((signal) => securityApi.tasks(kind, id, signal), [kind, id, version]);
  const [open, setOpen] = useState(false);
  const [assigneeType, setAssigneeType] = useState<AssigneeType>('user');
  const [assignee, setAssignee] = useState<Assignee | null>(null);
  const [dueAt, setDueAt] = useState('');
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  async function create(event: FormEvent) {
    event.preventDefault();
    if (!assignee || !dueAt) return;
    setBusy(true);
    setError(undefined);
    try {
      await securityApi.createTask(kind, id, {
        expectedVersion: version,
        ...(assignee
          ? { [assigneeType === 'user' ? 'assignedUserId' : 'assignedTeamId']: assignee.id }
          : {}),
        ...(dueAt ? { dueAt: new Date(dueAt).toISOString() } : {}),
      });
      setOpen(false);
      tasks.reload();
      onCreated();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section>
      <h3>{t('security.remediationTasks')}</h3>
      {can('security.manage') && can('tasks.manage') && (
        <Button type="submit" onClick={() => setOpen(true)}>
          {t('security.createTask')}
        </Button>
      )}
      {tasks.error && <ApiErrorAlert error={tasks.error} onRetry={tasks.reload} />}
      <Table>
        <thead>
          <tr>
            <th>{t('security.task')}</th>
            <th>{t('security.status')}</th>
            <th>{t('security.dueAt')}</th>
            <th>{t('security.assignee')}</th>
          </tr>
        </thead>
        <tbody>
          {(tasks.data?.items ?? []).map((task) => (
            <tr key={task.id}>
              <td>
                {can('tasks.view') || can('tasks.manage') ? (
                  <Link to={`/tasks/${encodeURIComponent(task.id)}`}>{task.id}</Link>
                ) : (
                  task.id
                )}
              </td>
              <td>{t(`tasks.status.${task.status}` as MessageKey)}</td>
              <td>{task.dueAt ? formatDate(locale, task.dueAt) : '—'}</td>
              <td>
                {task.assigneeHidden
                  ? t('security.restrictedAssignee')
                  : (task.assigneeName ?? '—')}
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
      {open && (
        <Dialog title={t('security.createTask')} onClose={() => setOpen(false)}>
          <form className="form-stack" onSubmit={(event) => void create(event)}>
            <label>
              {t('security.assigneeType')}
              <select
                value={assigneeType}
                onChange={(event) => {
                  setAssigneeType(event.target.value as AssigneeType);
                  setAssignee(null);
                }}
              >
                <option value="user">{t('security.user')}</option>
                <option value="team">{t('security.team')}</option>
              </select>
            </label>
            <AssigneePicker
              key={assigneeType}
              type={assigneeType}
              value={assignee}
              onChange={setAssignee}
            />
            <label>
              {t('security.dueAt')}
              <input
                type="datetime-local"
                value={dueAt}
                onChange={(event) => setDueAt(event.target.value)}
                required
              />
            </label>
            {error && <ApiErrorAlert error={error} />}
            <div className="actions">
              <Button type="submit" disabled={busy || !assignee || !dueAt}>
                {t('security.createTask')}
              </Button>
              <Button type="button" onClick={() => setOpen(false)}>
                {t('action.cancel')}
              </Button>
            </div>
          </form>
        </Dialog>
      )}
    </section>
  );
}

export function FindingRemediation({
  id,
  version,
  onCreated,
}: {
  id: string;
  version: number;
  onCreated: () => void;
}) {
  const { t } = useI18n();
  return (
    <section>
      <h2>{t('security.remediation')}</h2>
      <p>{t('security.taskDoesNotRemediate')}</p>
      <TaskSection kind="findings" id={id} version={version} onCreated={onCreated} />
    </section>
  );
}

export function AdvisoryRemediation({
  id,
  version,
  onChanged,
}: {
  id: string;
  version: number;
  onChanged: () => void;
}) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const progress = useAsync((signal) => securityApi.progress(id, signal), [id, version]);
  const changes = useAsync((signal) => securityApi.changes(id, signal), [id, version]);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<{ id: string; reference: string } | null>(null);
  const [query, setQuery] = useState('');
  const [selected, setSelected] = useState('');
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const matches = useAsync(
    (signal) =>
      can('changes.view') || can('changes.manage') || can('changes.execute')
        ? changesApi.list({ q: query }, undefined, signal)
        : Promise.resolve({ items: [] }),
    [query],
  );
  async function link(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await securityApi.linkChange(id, selected, version);
      setAdding(false);
      changes.reload();
      progress.reload();
      onChanged();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  }
  async function remove(changeId: string) {
    setError(undefined);
    try {
      await securityApi.unlinkChange(id, changeId, version);
      setRemoving(null);
      changes.reload();
      progress.reload();
      onChanged();
    } catch (cause) {
      setError(asApiError(cause));
    }
  }
  const p = progress.data;
  return (
    <section>
      <h2>{t('security.remediation')}</h2>
      {progress.error && <ApiErrorAlert error={progress.error} onRetry={progress.reload} />}
      {p && (
        <>
          <p>
            {t('security.residualRisk')}:{' '}
            <Badge
              tone={
                p.residualRisk === 'unknown'
                  ? 'neutral'
                  : p.residualRisk === 'high'
                    ? 'danger'
                    : p.residualRisk === 'medium'
                      ? 'warning'
                      : 'info'
              }
            >
              {t(`security.severity.${p.residualRisk}` as MessageKey)} ·{' '}
              {t('security.derivedByTuraco')}
            </Badge>
          </p>
          {p.residualRisk === 'unknown' && <p>{t('security.residualRiskUnknown')}</p>}
          <p>
            {t('security.progressGeneratedAt')}: {formatDateTime(locale, p.generatedAt)}
          </p>
          <p>
            {t('security.shareRemediated')}: {(p.shareRemediated * 100).toFixed(0)}%
          </p>
          <p>
            {t('security.acceptedRisks')}: {p.acceptedRiskCount} · {t('security.earliestReview')}:{' '}
            {p.earliestRiskReviewBy ? formatDate(locale, p.earliestRiskReviewBy) : '—'}
          </p>
          <p>
            {t('security.oldestOpenAge')}: {p.oldestOpenFindingAgeDays ?? '—'}
          </p>
          <p>
            {t('security.tasksOpen')}: {p.tasks.open} · {t('security.tasksDone')}: {p.tasks.done} ·{' '}
            {t('security.tasksCancelled')}: {p.tasks.cancelled} · {t('security.tasksOverdue')}:{' '}
            {p.tasks.overdue}
          </p>
          {p.tasksTruncated && <p>{t('security.tasksTruncated')}</p>}
          <h3>{t('security.findingCounts')}</h3>
          {Object.entries(p.findingByStatus).map(([status, count]) => (
            <p key={status}>
              {t(`security.status.${status}` as MessageKey)}: {count}
            </p>
          ))}
          {Object.entries(p.findingByConfidence).map(([confidence, count]) => (
            <p key={confidence}>
              {t(`security.confidence.${confidence}` as MessageKey)}: {count}
            </p>
          ))}
          <h3>{t('security.linkedChangeCounts')}</h3>
          {Object.entries(p.linkedChangesByStatus).map(([status, count]) => (
            <p key={status}>
              {t(`changes.status.${status}` as MessageKey)}: {count}
            </p>
          ))}
          {p.hiddenLinkedChanges > 0 && (
            <p>
              {t('security.restrictedChange')}: {p.hiddenLinkedChanges}
            </p>
          )}
        </>
      )}
      <TaskSection
        kind="advisories"
        id={id}
        version={version}
        onCreated={() => {
          progress.reload();
          onChanged();
        }}
      />
      <h3>{t('security.linkedChanges')}</h3>
      {can('security.manage') && (
        <Button type="submit" onClick={() => setAdding(true)}>
          {t('security.addChange')}
        </Button>
      )}
      {changes.error && <ApiErrorAlert error={changes.error} onRetry={changes.reload} />}
      {error && !adding && <ApiErrorAlert error={error} />}
      <Table>
        <thead>
          <tr>
            <th>{t('security.reference')}</th>
            <th>{t('security.status')}</th>
            <th>{t('security.actions')}</th>
          </tr>
        </thead>
        <tbody>
          {(changes.data?.items ?? []).map((change) => (
            <tr key={change.id}>
              <td>
                {change.hidden ? (
                  t('security.restrictedChange')
                ) : (
                  <Link to={`/changes/${encodeURIComponent(change.changeId ?? '')}`}>
                    {change.reference}
                  </Link>
                )}
              </td>
              <td>{change.hidden ? '—' : t(`changes.status.${change.status}` as MessageKey)}</td>
              <td>
                {can('security.manage') && !change.hidden && (
                  <Button
                    type="submit"
                    onClick={() => {
                      setError(undefined);
                      setRemoving({ id: change.changeId ?? '', reference: change.reference ?? '' });
                    }}
                  >
                    {t('security.remove')}
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
      {adding && (
        <Dialog title={t('security.addChange')} onClose={() => setAdding(false)}>
          <form className="form-stack" onSubmit={(event) => void link(event)}>
            <label>
              {t('security.searchChange')}
              <input
                type="search"
                value={query}
                onChange={(event) => {
                  setQuery(event.target.value);
                  setSelected('');
                }}
              />
            </label>
            {matches.error && <ApiErrorAlert error={matches.error} onRetry={matches.reload} />}
            <label>
              {t('security.linkedChanges')}
              <select
                required
                value={selected}
                onChange={(event) => setSelected(event.target.value)}
              >
                <option value="">{t('security.selectChange')}</option>
                {(matches.data?.items ?? []).map((change) => (
                  <option key={change.id} value={change.id}>
                    {change.reference} · {change.title}
                  </option>
                ))}
              </select>
            </label>
            {error && <ApiErrorAlert error={error} />}
            <div className="actions">
              <Button type="submit" disabled={busy || !selected}>
                {t('security.addChange')}
              </Button>
              <Button type="button" onClick={() => setAdding(false)}>
                {t('action.cancel')}
              </Button>
            </div>
          </form>
        </Dialog>
      )}
      {removing && (
        <Dialog title={t('security.removeChange')} onClose={() => setRemoving(null)}>
          <p>{t('security.removeChangeConfirm', { reference: removing.reference })}</p>
          {error && <ApiErrorAlert error={error} />}
          <div className="actions">
            <Button type="submit" onClick={() => void remove(removing.id)}>
              {t('security.removeChange')}
            </Button>
            <Button type="submit" onClick={() => setRemoving(null)}>
              {t('action.cancel')}
            </Button>
          </div>
        </Dialog>
      )}
    </section>
  );
}

export function SecurityOverviewScreen() {
  const { t } = useI18n();
  const overview = useAsync((signal) => securityApi.overview(signal), []);
  const p = overview.data;
  return (
    <>
      <PageHeader title={t('security.overview')} />
      {overview.error && <ApiErrorAlert error={overview.error} onRetry={overview.reload} />}
      {p && (
        <>
          <h2>{t('security.applicableBySeverity')}</h2>
          {Object.entries(p.applicableBySeverity).map(([severity, count]) => (
            <p key={severity}>
              <Link to={overviewLinks.severity(severity)}>
                {t(`security.severity.${severity}` as MessageKey)}: {count}
              </Link>
            </p>
          ))}
          <h2>{t('security.untriagedBySeverity')}</h2>
          {Object.entries(p.untriagedBySeverity ?? {}).map(([severity, count]) => (
            <p key={severity}>
              <Link to={overviewLinks.untriaged(severity)}>
                {t(`security.severity.${severity}` as MessageKey)}: {count}
              </Link>
            </p>
          ))}
          <h2>{t('security.openByConfidence')}</h2>
          {Object.entries(p.openFindingsByConfidence).map(([confidence, count]) => (
            <p key={confidence}>
              <Link to={overviewLinks.confidence(confidence)}>
                {t(`security.confidence.${confidence}` as MessageKey)}: {count}
              </Link>
            </p>
          ))}
          <p>
            <Link to={overviewLinks.overdueTasks}>
              {t('security.tasksOverdue')}: {p.overdueTasks}
            </Link>
          </p>
          <p>
            <Link to={overviewLinks.riskDue}>
              {t('security.riskDue30')}: {p.riskAcceptancesDueWithin30Days}
            </Link>
          </p>
          <p>{t('security.derivedByTuraco')}</p>
        </>
      )}
    </>
  );
}
