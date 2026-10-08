import { SegmentedFilter } from '../../platform/ui/FilterBar';
import { useMemo } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { TableDate } from '../../platform/ui/TableDate';
import { NavIcon } from '../../platform/ui/NavIcon';
import { focusSummary } from './workModel';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate, useLocation } from '../../platform/router/Router';
import { dayPart, greetingName } from '../../platform/session/identity';
import { useSession } from '../../platform/session/SessionProvider';
import { Button } from '../../platform/ui/Button';
import { copyContextText, useContextMenu, type MenuItem } from '../../platform/ui/ContextMenu';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Card, EmptyState, MetricCard, Skeleton } from '../../platform/ui/Workspace';
import { isOverdue } from '../tasks/actions';
import { tasksApi } from '../tasks/api';
import { StatusBadge } from '../tasks/TaskTable';
import type { Task } from '../tasks/types';

const priorityRank: Record<Task['priority'], number> = { urgent: 0, high: 1, normal: 2, low: 3 };
export function prioritizeWork(items: readonly Task[]): Task[] {
  return [...items].sort(
    (a, b) =>
      priorityRank[a.priority] - priorityRank[b.priority] ||
      (a.dueAt ?? '9999').localeCompare(b.dueAt ?? '9999'),
  );
}

/** The shared Task queue remains the source of work; briefing entries remain linked read models. */
export function MyWorkScreen() {
  const { t } = useI18n();
  const { can, session } = useSession();
  const name = greetingName(session);
  const part = dayPart(new Date());
  const greeting = name
    ? t(`overview.greeting.${part}`, { name })
    : t(`overview.greetingPlain.${part}`);
  const list = usePagedList((cursor, signal) => tasksApi.myWork(cursor, signal), []);
  const { search } = useLocation();
  const focus = new URLSearchParams(search).get('focus');
  const shown = focus === 'high' || focus === 'urgent' || focus === 'overdue' ? focus : 'all';
  const menu = useContextMenu();
  const now = new Date();
  const ordered = useMemo(() => prioritizeWork(list.items), [list.items]);
  const high = ordered.filter((task) => task.priority === 'high');
  const critical = ordered.filter((task) => task.priority === 'urgent');
  const overdue = ordered.filter((task) => isOverdue(task, now));
  const visible =
    shown === 'high'
      ? high
      : shown === 'urgent'
        ? critical
        : shown === 'overdue'
          ? overdue
          : ordered;
  const summary = focusSummary(ordered, now);
  const copy = async (value: string) => {
    if (!(await copyContextText(value))) window.prompt(t('contextMenu.copyFallback'), value);
  };
  const actions = (task: Task): MenuItem[] => [
    {
      id: 'open',
      label: t('contextMenu.open'),
      onSelect: () => navigate(`/tasks/${encodeURIComponent(task.id)}`),
    },
    {
      id: 'copy-link',
      label: t('contextMenu.copyLink'),
      onSelect: () =>
        void copy(new URL(`/tasks/${encodeURIComponent(task.id)}`, window.location.origin).href),
    },
  ];
  const priorities = ['urgent', 'high', 'normal', 'low'] as const;
  return (
    <div className="work-dashboard work-bench">
      <PageHeader
        eyebrow={t('dashboard.workEyebrow')}
        title={t('nav.myWork')}
        intro={greeting}
        actions={
          can('tasks.manage') ? (
            <Link to="/tasks/new" className="btn btn-primary">
              <span aria-hidden="true">+</span> {t('tasks.create.action')}
            </Link>
          ) : null
        }
      />
      {list.loading && !list.items.length ? (
        <Skeleton lines={2} />
      ) : !list.error ? (
        <div
          className="workspace-metrics dashboard-metrics work-metrics"
          aria-label={t('overview.metrics')}
        >
          <MetricCard
            label={t('myWork.loaded')}
            value={ordered.length}
            to="/my-work"
            caption={t('overview.metric.openCaption')}
            icon={<NavIcon id="myWork" />}
          />
          <MetricCard
            label={t('overview.metric.overdue')}
            value={overdue.length}
            to="/my-work?focus=overdue"
            tone="danger"
            caption={t('overview.metric.overdueCaption')}
            zeroCaption={t('overview.metric.overdueZero')}
            icon={<NavIcon id="maintenanceCalendar" />}
          />
          <MetricCard
            label={t('tasks.priority.urgent')}
            value={critical.length}
            to="/my-work?focus=urgent"
            tone="warning"
            caption={t('myWork.polish.priorityCaption')}
            icon={<NavIcon id="tasks" />}
          />
        </div>
      ) : null}
      <p className="dashboard-scope">{t('myWork.polish.scope')}</p>
      <div className="dashboard-columns">
        <div className="dashboard-primary">
          <Card title={t('myWork.queue')}>
            <div className="workspace-section-head">
              <h2>{t('myWork.queue')}</h2>
              <span className="work-bench-count">
                {!list.loading && !list.error
                  ? t('table.showingLoaded', { count: ordered.length })
                  : null}
              </span>
            </div>
            <div className="workspace-toolbar">
              <SegmentedFilter
                label={t('myWork.filter')}
                value={shown}
                onChange={(key) => navigate(key === 'all' ? '/my-work' : `/my-work?focus=${key}`)}
                options={(['all', 'high', 'urgent', 'overdue'] as const).map((key) => ({
                  value: key,
                  label: t(`myWork.filter.${key}`),
                  count: {
                    all: ordered.length,
                    high: high.length,
                    urgent: critical.length,
                    overdue: overdue.length,
                  }[key],
                }))}
              />
            </div>
            {list.loading && !list.items.length ? <Skeleton lines={5} /> : null}
            {list.error ? (
              <p role="alert" className="workspace-source-notice">
                {t('error.generic')} <Button onClick={list.reload}>{t('action.retry')}</Button>
              </p>
            ) : null}
            {!list.loading && !list.error && visible.length === 0 ? (
              <EmptyState title={shown === 'all' ? t('myWork.empty') : t('myWork.emptyFilter')} />
            ) : null}
            {visible.length ? (
              <ul className="work-list">
                {visible.map((task) => (
                  <li
                    key={task.id}
                    className={`work-row priority-${task.priority}`}
                    tabIndex={0}
                    onContextMenu={(event) => {
                      if (
                        (event.target as Element).closest('a,button') ||
                        window.getSelection()?.toString()
                      )
                        return;
                      event.preventDefault();
                      menu.openAtPoint(
                        actions(task),
                        { x: event.clientX, y: event.clientY },
                        event.currentTarget,
                        t('contextMenu.actions'),
                      );
                    }}
                    onKeyDown={(event) => {
                      if ((event.shiftKey && event.key === 'F10') || event.key === 'ContextMenu') {
                        event.preventDefault();
                        menu.openAtElement(
                          actions(task),
                          event.currentTarget,
                          t('contextMenu.actions'),
                        );
                      }
                    }}
                  >
                    <span className="dashboard-icon" aria-hidden="true">
                      <NavIcon id="tasks" />
                    </span>
                    <div className="work-row-body">
                      <Link to={`/tasks/${encodeURIComponent(task.id)}`}>{task.title}</Link>
                      <small>
                        {task.assignedTeamName ?? t('dashboard.task')} ·{' '}
                        {task.dueAt ? (
                          <>
                            {t('myWork.due')} <TableDate value={task.dueAt} />
                          </>
                        ) : (
                          t('myWork.noDueDate')
                        )}
                      </small>
                    </div>
                    <span className={`priority-chip priority-chip-${task.priority}`}>
                      {t(`tasks.priority.${task.priority}`)}
                    </span>
                    <StatusBadge status={task.status} />
                    <Button
                      type="button"
                      className="table-actions-trigger"
                      aria-label={`${t('contextMenu.actions')}: ${task.title}`}
                      onClick={(event) =>
                        menu.openAtElement(
                          actions(task),
                          event.currentTarget,
                          t('contextMenu.actions'),
                        )
                      }
                    >
                      ⋯
                    </Button>
                  </li>
                ))}
              </ul>
            ) : null}
            {list.hasMore ? (
              <div className="load-more">
                <Button onClick={list.loadMore} busy={list.loadingMore}>
                  {t('action.loadMore')}
                </Button>
              </div>
            ) : null}
            {list.loadMoreError ? <p role="alert">{t('error.generic')}</p> : null}
            {!list.hasMore && visible.length ? (
              <p className="work-bench-end">
                {t('myWork.endOfList')}{' '}
                <Link to="/tasks" className="section-link">
                  {t('myWork.allTasks')} <span aria-hidden="true">→</span>
                </Link>
              </p>
            ) : null}
          </Card>
        </div>
        <aside className="dashboard-context" aria-label={t('myWork.focus')}>
          {list.loading && !list.items.length ? (
            <Skeleton lines={4} />
          ) : !list.error ? (
            <>
              <Card className="work-focus" title={t('myWork.focus')}>
                <h2>{t('myWork.focus')}</h2>
                <p className="work-focus-total">
                  <strong>{summary.total}</strong> {t('myWork.focusOpen')}
                </p>
                <ul className="work-focus-bars">
                  {priorities.map((priority) => (
                    <li key={priority}>
                      <span>{t(`tasks.priority.${priority}`)}</span>
                      <span className="work-focus-track" aria-hidden="true">
                        <span
                          className={`work-focus-fill priority-fill-${priority}`}
                          style={{
                            width: summary.total
                              ? `${(summary.byPriority[priority] / summary.total) * 100}%`
                              : '0%',
                          }}
                        />
                      </span>
                      <strong>{summary.byPriority[priority]}</strong>
                    </li>
                  ))}
                </ul>
              </Card>
              <Card className="work-due" title={t('myWork.dueSoon')}>
                <h2>{t('myWork.dueSoon')}</h2>
                {summary.dueSoon.length ? (
                  <ul>
                    {summary.dueSoon.map((task) => (
                      <li key={task.id}>
                        <Link to={`/tasks/${encodeURIComponent(task.id)}`}>{task.title}</Link>
                        <TableDate value={task.dueAt} />
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="dashboard-scope">{t('myWork.nothingDue')}</p>
                )}
              </Card>
            </>
          ) : null}
        </aside>
      </div>
      {menu.menu}
    </div>
  );
}
