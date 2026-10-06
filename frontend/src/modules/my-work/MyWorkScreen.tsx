import { SegmentedFilter } from '../../platform/ui/FilterBar';
import { useMemo } from 'react';
import { useAsync, usePagedList } from '../../platform/api/useAsync';
import { TableDate } from '../../platform/ui/TableDate';
import { NavIcon } from '../../platform/ui/NavIcon';
import { summarizeFeed } from './workModel';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate, useLocation } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { copyContextText, useContextMenu, type MenuItem } from '../../platform/ui/ContextMenu';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Card, EmptyState, MetricCard, Skeleton } from '../../platform/ui/Workspace';
import { briefingApi } from '../briefing/api';
import { resolveFeedTitle, sourceKey } from '../briefing/feed';
import { organizationApi } from '../organization/api';
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
export function MyWorkScreen({ overview = false }: { overview?: boolean }) {
  const { t } = useI18n();
  const { can, session } = useSession();
  const list = usePagedList((cursor, signal) => tasksApi.myWork(cursor, signal), []);
  const feed = useAsync((signal) => briefingApi.feed(signal), []);
  const user = useAsync(
    (signal) =>
      can('organization.view') && session
        ? organizationApi.user(session.userId, signal)
        : Promise.resolve(undefined),
    [can, session?.userId],
  );
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
  const summary = summarizeFeed(feed.data?.entries ?? []);
  const needsYou = ordered
    .filter((task) => task.status !== 'completed' && task.status !== 'cancelled')
    .slice(0, 3);
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
  return (
    <div className="work-dashboard">
      <PageHeader
        eyebrow={t(overview ? 'dashboard.overviewEyebrow' : 'dashboard.workEyebrow')}
        title={
          overview
            ? t('dashboard.overviewTitle')
            : user.data?.displayName
              ? t('myWork.greeting', {
                  name: user.data.displayName.split(' ')[0] ?? user.data.displayName,
                })
              : t('nav.myWork')
        }
        intro={t(overview ? 'dashboard.overviewIntro' : 'myWork.intro')}
        actions={
          can('tasks.manage') ? (
            <Link to="/tasks/new" className="btn btn-primary">
              {t('tasks.create.action')}
            </Link>
          ) : null
        }
      />
      {list.loading && !list.items.length ? (
        <Skeleton lines={3} />
      ) : list.error && !list.items.length ? null : (
        <div className="workspace-metrics dashboard-metrics" aria-label={t('myWork.metrics')}>
          <MetricCard
            label={t('myWork.loaded')}
            value={ordered.length}
            to="/my-work"
            caption={t('dashboard.metricOpen')}
            icon={<NavIcon id="myWork" />}
          />
          <MetricCard
            label={t('myWork.highPriority')}
            value={high.length}
            to="/my-work?focus=high"
            denominator={ordered.length}
            caption={t('dashboard.metricHigh')}
            icon={<NavIcon id="incidents" />}
            tone="warning"
          />
          <MetricCard
            label={t('myWork.urgent')}
            value={critical.length}
            to="/my-work?focus=urgent"
            denominator={ordered.length}
            caption={t('dashboard.metricUrgent')}
            icon={<NavIcon id="incidents" />}
            tone="danger"
          />
          <MetricCard
            label={t('myWork.overdue')}
            value={overdue.length}
            to="/my-work?focus=overdue"
            denominator={ordered.length}
            caption={t('dashboard.metricOverdue')}
            icon={<NavIcon id="maintenanceCalendar" />}
            tone="danger"
          />
        </div>
      )}
      <p className="dashboard-scope">{t('dashboard.loadedScope')}</p>
      {needsYou.length > 0 ? (
        <section className="dashboard-needs" aria-labelledby="needs-title">
          <div className="dashboard-section-heading">
            <h2 id="needs-title">{t('dashboard.needsYou')}</h2>
            <span>{t('dashboard.topItems', { count: needsYou.length })}</span>
          </div>
          <div className="dashboard-action-grid">
            {needsYou.map((task) => (
              <Link
                to={`/tasks/${encodeURIComponent(task.id)}`}
                className={`dashboard-action-card priority-${task.priority}`}
                key={task.id}
              >
                <div className="dashboard-action-top">
                  <span className="dashboard-icon">
                    <NavIcon id="tasks" />
                  </span>
                  <TableDate value={task.createdAt} />
                </div>
                <h3>{task.title}</h3>
                <p>
                  {t(`tasks.priority.${task.priority}`)} ·{' '}
                  {task.assignedTeamName ?? t('nav.myWork')}
                </p>
                <div className="dashboard-action-footer">
                  {t('dashboard.openTask')}
                  <span aria-hidden="true">→</span>
                </div>
              </Link>
            ))}
          </div>
        </section>
      ) : null}
      <div className="dashboard-columns">
        <div className="dashboard-primary">
          <Card title={t('myWork.queue')}>
            <div className="workspace-section-head">
              <h2>{t('myWork.queue')}</h2>
              <Link to="/tasks">{t('nav.tasks')} ↗</Link>
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
              <p role="alert">
                {t('error.generic')} <Button onClick={list.reload}>{t('action.retry')}</Button>
              </p>
            ) : null}
            {!list.loading && !list.error && visible.length === 0 ? (
              <EmptyState title={t('myWork.empty')} />
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
                    <span className="dashboard-task-type">
                      <NavIcon id="tasks" />
                      <span>{t('dashboard.task')}</span>
                    </span>
                    <div className="work-row-body">
                      <Link to={`/tasks/${encodeURIComponent(task.id)}`}>{task.title}</Link>
                      <small>
                        {t(`tasks.priority.${task.priority}`)} ·{' '}
                        {task.dueAt ? <TableDate value={task.dueAt} /> : t('myWork.noDueDate')}
                      </small>
                    </div>
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
          </Card>
        </div>
        <aside className="dashboard-context" aria-label={t('myWork.briefing')}>
          {summary.approvals !== undefined ? (
            <Link to="/approvals" className="dashboard-approval">
              <span className="dashboard-icon">
                <NavIcon id="approvals" />
              </span>
              <span>
                <strong>{summary.approvals}</strong>
                <span>{t('dashboard.pendingApprovals')}</span>
              </span>
              <span aria-hidden="true">→</span>
            </Link>
          ) : null}
          {summary.highlight ? (
            <Card className={`dashboard-info severity-${summary.highlight.severity}`}>
              <span className="dashboard-eyebrow">{t(sourceKey(summary.highlight.source))}</span>
              <h2>
                {t(
                  resolveFeedTitle(summary.highlight).key,
                  resolveFeedTitle(summary.highlight).params,
                )}
              </h2>
              <p>{t('dashboard.infoContext')}</p>
              {summary.highlight.dueAt ? <TableDate value={summary.highlight.dueAt} /> : null}
              <Link to={summary.highlight.linkPath}>
                {t('dashboard.viewDetails')} <span aria-hidden="true">→</span>
              </Link>
            </Card>
          ) : null}
          <Card title={t('myWork.briefing')}>
            <div className="workspace-section-head">
              <h2>{t('myWork.briefing')}</h2>
              <Link to="/briefing">{t('nav.briefing')} ↗</Link>
            </div>
            {feed.loading && !feed.data ? <Skeleton lines={4} /> : null}
            {feed.error ? (
              <p role="alert">
                {t('error.generic')} <Button onClick={feed.reload}>{t('action.retry')}</Button>
              </p>
            ) : null}
            {feed.data?.unavailable.length ? (
              <p className="workspace-source-notice" role="status">
                {t('briefing.feed.unavailable')}
              </p>
            ) : null}
            {feed.data && Object.values(feed.data.truncated).some(Boolean) ? (
              <p className="workspace-source-notice">{t('dashboard.feedLimited')}</p>
            ) : null}
            {feed.data && !feed.data.entries.length ? (
              <EmptyState title={t('briefing.feed.empty')} />
            ) : null}
            {feed.data?.entries.slice(0, 5).map((entry, index) => {
              const title = resolveFeedTitle(entry);
              return (
                <div
                  className={`briefing-mini severity-${entry.severity}`}
                  key={`${entry.source}-${index}`}
                >
                  <Badge
                    tone={
                      entry.severity === 'critical'
                        ? 'danger'
                        : entry.severity === 'warning'
                          ? 'warning'
                          : 'info'
                    }
                  >
                    {t(`briefing.severity.${entry.severity}`)}
                  </Badge>
                  <Link to={entry.linkPath}>{t(title.key, title.params)}</Link>
                  <small>
                    {t(sourceKey(entry.source))}
                    {entry.occurredAt ? (
                      <>
                        {' '}
                        · <TableDate value={entry.occurredAt} />
                      </>
                    ) : null}
                  </small>
                </div>
              );
            })}
          </Card>
        </aside>
      </div>
      {overview ? (
        <Card className="dashboard-timeline" title={t('dashboard.recent')}>
          <div className="dashboard-section-heading">
            <h2>{t('dashboard.recent')}</h2>
            <span>{t('dashboard.fromBriefing')}</span>
          </div>
          {feed.loading && !feed.data ? <Skeleton lines={3} /> : null}
          {summary.recent.length ? (
            <ul>
              {summary.recent.map((entry, index) => (
                <li key={`${entry.source}-${index}`}>
                  <span className="dashboard-timeline-dot" aria-hidden="true" />
                  <div>
                    <Link to={entry.linkPath}>
                      {t(resolveFeedTitle(entry).key, resolveFeedTitle(entry).params)}
                    </Link>
                    <small>{t(sourceKey(entry.source))}</small>
                  </div>
                  <TableDate value={entry.occurredAt} />
                </li>
              ))}
            </ul>
          ) : !feed.loading && !feed.error ? (
            <p className="dashboard-scope">{t('dashboard.noRecent')}</p>
          ) : null}
        </Card>
      ) : null}
      {menu.menu}
    </div>
  );
}
