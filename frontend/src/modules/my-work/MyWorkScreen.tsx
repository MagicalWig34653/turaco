import { UserAvailability } from '../presence/AvailabilityChip';
import { SegmentedFilter } from '../../platform/ui/FilterBar';
import { useMemo } from 'react';
import { TableDate } from '../../platform/ui/TableDate';
import { NavIcon } from '../../platform/ui/NavIcon';
import { focusSummary } from './workModel';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate, useLocation } from '../../platform/router/Router';
import { dayPart, greetingName } from '../../platform/session/identity';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { copyContextText, useContextMenu, type MenuItem } from '../../platform/ui/ContextMenu';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Card, EmptyState, MetricCard, Skeleton } from '../../platform/ui/Workspace';
import { SourceNotice } from './SourceNotice';
import { StatusBadge as TaskStatusBadge } from '../tasks/TaskTable';
import type { TaskStatus } from '../tasks/types';
import { TicketStatusBadge } from '../tickets/TicketsScreen';
import { ticketStatuses, type TicketStatus } from '../tickets/types';
import { workSources, type WorkItem } from './api';
import {
  countText,
  isItemOverdue,
  parseSourceFilter,
  priorityOf,
  sourceLabelKey,
  totalOf,
  type SourceFilter,
} from './feedModel';
import { useMyWorkFeed } from './useMyWorkFeed';

const priorityRank = { urgent: 0, high: 1, normal: 2, low: 3 } as const;
const taskStatuses: readonly string[] = [
  'open',
  'in_progress',
  'blocked',
  'completed',
  'cancelled',
];

/** Urgent first, then the earlier due date. The server already merges in its shared order. */
export function prioritizeWork<T extends Pick<WorkItem, 'priority' | 'dueAt'>>(
  items: readonly T[],
): T[] {
  return [...items].sort(
    (a, b) =>
      priorityRank[priorityOf(a)] - priorityRank[priorityOf(b)] ||
      (a.dueAt ?? '9999').localeCompare(b.dueAt ?? '9999'),
  );
}

const sourceCaption: Record<(typeof workSources)[number], MessageKey> = {
  tickets: 'myWork.metric.ticketsCaption',
  team_tickets: 'myWork.metric.teamTicketsCaption',
  tasks: 'myWork.metric.tasksCaption',
};

function ItemStatus({ item }: { item: WorkItem }) {
  const { t } = useI18n();
  if (item.kind === 'ticket' && ticketStatuses.includes(item.status as TicketStatus))
    return <TicketStatusBadge status={item.status as TicketStatus} />;
  if (item.kind === 'task' && taskStatuses.includes(item.status))
    return <TaskStatusBadge status={item.status as TaskStatus} />;
  return <Badge tone="neutral">{item.status || t('myWork.statusUnknown')}</Badge>;
}

/** Tickets and tasks assigned to you, and tickets of your teams waiting to be picked up, in one list. */
export function MyWorkScreen() {
  const { t } = useI18n();
  const { can, session } = useSession();
  const name = greetingName(session);
  const part = dayPart(new Date());
  const greeting = name
    ? t(`overview.greeting.${part}`, { name })
    : t(`overview.greetingPlain.${part}`);
  const { search } = useLocation();
  const params = new URLSearchParams(search);
  const focus = params.get('focus');
  const shown = focus === 'high' || focus === 'urgent' || focus === 'overdue' ? focus : 'all';
  const source: SourceFilter = parseSourceFilter(params.get('source'));
  const feed = useMyWorkFeed(source);
  const { list } = feed;
  const menu = useContextMenu();
  const now = new Date();
  const ordered = useMemo(() => feed.items, [feed.items]);
  const high = ordered.filter((item) => priorityOf(item) === 'high');
  const critical = ordered.filter((item) => priorityOf(item) === 'urgent');
  const overdue = ordered.filter((item) => isItemOverdue(item, now));
  const visible =
    shown === 'high'
      ? high
      : shown === 'urgent'
        ? critical
        : shown === 'overdue'
          ? overdue
          : ordered;
  const summary = focusSummary(ordered, now);
  const total = totalOf(feed.counts);
  const countsKnown = feed.counts.size > 0;
  const go = (nextSource: SourceFilter, nextFocus: string) => {
    const next = new URLSearchParams();
    if (nextSource !== 'all') next.set('source', nextSource);
    if (nextFocus !== 'all') next.set('focus', nextFocus);
    const text = next.toString();
    navigate(text ? `/my-work?${text}` : '/my-work');
  };
  const copy = async (value: string) => {
    if (!(await copyContextText(value))) window.prompt(t('contextMenu.copyFallback'), value);
  };
  const actions = (item: WorkItem): MenuItem[] => [
    {
      id: 'open',
      label: t('contextMenu.open'),
      onSelect: () => navigate(item.href),
    },
    ...(item.reference
      ? [
          {
            id: 'copy-reference',
            label: t('contextMenu.copyReference'),
            onSelect: () => void copy(item.reference ?? ''),
          },
        ]
      : []),
    {
      id: 'copy-link',
      label: t('contextMenu.copyLink'),
      onSelect: () => void copy(new URL(item.href, window.location.origin).href),
    },
  ];
  const priorities = ['urgent', 'high', 'normal', 'low'] as const;
  const loadingFirst = list.loading && !list.items.length;
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
      {session?.userId ? <UserAvailability userId={session.userId} /> : null}
      {feed.countsLoading ? (
        <Skeleton lines={2} />
      ) : (
        <div
          className="workspace-metrics dashboard-metrics work-metrics"
          aria-label={t('overview.metrics')}
          style={
            {
              '--metric-count': workSources.filter((key) => feed.counts.has(key)).length + 1,
            } as never
          }
        >
          {workSources
            .filter((key) => feed.counts.has(key))
            .map((key) => {
              const view = feed.counts.get(key);
              return (
                <MetricCard
                  key={key}
                  label={t(sourceLabelKey(key))}
                  value={view?.count ?? 0}
                  capped={view?.capped ?? false}
                  {...(view?.unavailable ? { unavailable: t('myWork.countUnavailable') } : {})}
                  to={`/my-work?source=${key}`}
                  caption={t(sourceCaption[key])}
                  icon={<NavIcon id={key === 'tasks' ? 'tasks' : 'myTickets'} />}
                />
              );
            })}
          {!list.error ? (
            <MetricCard
              label={t('overview.metric.overdue')}
              value={overdue.length}
              to="/my-work?focus=overdue"
              tone="danger"
              caption={t('myWork.metric.overdueCaption')}
              zeroCaption={t('overview.metric.overdueZero')}
              icon={<NavIcon id="maintenanceCalendar" />}
            />
          ) : null}
        </div>
      )}
      <p className="dashboard-scope">
        {feed.countsError || (countsKnown && total.partial)
          ? t('myWork.scopePartial')
          : t('myWork.scope')}
      </p>
      {feed.unavailable.length > 0 ? (
        <SourceNotice sources={feed.unavailable} onRetry={feed.reload} />
      ) : null}
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
                label={t('myWork.sourceFilter')}
                value={source}
                onChange={(key) => go(parseSourceFilter(key), shown)}
                options={[
                  {
                    value: 'all',
                    label: t('myWork.source.all'),
                    // A partial total is only a lower bound, so it is not shown as a number.
                    ...(countsKnown && !total.unknown && !total.partial
                      ? { count: `${total.value}${total.capped ? '+' : ''}` }
                      : {}),
                  },
                  ...workSources
                    .filter((key) => feed.counts.has(key))
                    .map((key) => ({
                      value: key,
                      label: t(sourceLabelKey(key)),
                      count: countText(feed.counts.get(key)),
                    })),
                ]}
              />
              <SegmentedFilter
                label={t('myWork.filter')}
                value={shown}
                onChange={(key) => go(source, key)}
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
            {loadingFirst ? <Skeleton lines={5} /> : null}
            {list.error ? (
              <p role="alert" className="workspace-source-notice">
                {t('error.generic')} <Button onClick={feed.reload}>{t('action.retry')}</Button>
              </p>
            ) : null}
            {!list.loading && !list.error && visible.length === 0 ? (
              <EmptyState
                title={
                  shown === 'all' && feed.unavailable.length === 0
                    ? t('myWork.empty')
                    : shown === 'all'
                      ? t('myWork.emptyPartial')
                      : t('myWork.emptyFilter')
                }
              />
            ) : null}
            {visible.length ? (
              <ul className="work-list">
                {visible.map((item) => {
                  const priority = priorityOf(item);
                  return (
                    <li
                      key={`${item.source}:${item.id}`}
                      className={`work-row priority-${priority}`}
                      tabIndex={0}
                      onContextMenu={(event) => {
                        if (
                          (event.target as Element).closest('a,button') ||
                          window.getSelection()?.toString()
                        )
                          return;
                        event.preventDefault();
                        menu.openAtPoint(
                          actions(item),
                          { x: event.clientX, y: event.clientY },
                          event.currentTarget,
                          t('contextMenu.actions'),
                        );
                      }}
                      onKeyDown={(event) => {
                        if (
                          (event.shiftKey && event.key === 'F10') ||
                          event.key === 'ContextMenu'
                        ) {
                          event.preventDefault();
                          menu.openAtElement(
                            actions(item),
                            event.currentTarget,
                            t('contextMenu.actions'),
                          );
                        }
                      }}
                    >
                      <span className="dashboard-icon" aria-hidden="true">
                        <NavIcon id={item.kind === 'ticket' ? 'myTickets' : 'tasks'} />
                      </span>
                      <div className="work-row-body">
                        <span className="work-row-title">
                          {item.reference ? (
                            <span className="incident-reference">{item.reference}</span>
                          ) : null}
                          <Link to={item.href}>{item.title}</Link>
                        </span>
                        <small>
                          <span className="work-source-chip">{t(sourceLabelKey(item.source))}</span>{' '}
                          ·{' '}
                          {item.dueAt ? (
                            <>
                              {t('myWork.due')} <TableDate value={item.dueAt} />
                            </>
                          ) : (
                            t('myWork.noDueDate')
                          )}
                          {item.waitingReason
                            ? ` · ${t(`tickets.waiting.${item.waitingReason}` as MessageKey)}`
                            : ''}
                        </small>
                      </div>
                      <span className={`priority-chip priority-chip-${priority}`}>
                        {t(`tasks.priority.${priority}`)}
                      </span>
                      <ItemStatus item={item} />
                      <Button
                        type="button"
                        className="table-actions-trigger"
                        aria-label={`${t('contextMenu.actions')}: ${item.title}`}
                        onClick={(event) =>
                          menu.openAtElement(
                            actions(item),
                            event.currentTarget,
                            t('contextMenu.actions'),
                          )
                        }
                      >
                        ⋯
                      </Button>
                    </li>
                  );
                })}
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
          {loadingFirst ? (
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
                    {summary.dueSoon.map((item) => (
                      <li key={`${item.source}:${item.id}`}>
                        <Link to={item.href}>{item.title}</Link>
                        <TableDate value={item.dueAt} />
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
