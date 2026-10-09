import { useModules } from '../platform/modules/ModulesProvider';
import { pathEnabled } from '../platform/modules/model';
import { BriefingCoverage } from '../modules/my-work/BriefingCoverage';
import type { ReactNode } from 'react';
import { appRoutes, canViewRoute, type RouteId } from './routes';
import { useAsync, usePagedList } from '../platform/api/useAsync';
import { useI18n } from '../platform/i18n/I18nProvider';
import type { MessageKey } from '../platform/i18n/i18n';
import { Link } from '../platform/router/Router';
import { useSession } from '../platform/session/SessionProvider';
import { dayPart, greetingName } from '../platform/session/identity';
import { Button } from '../platform/ui/Button';
import { NavIcon } from '../platform/ui/NavIcon';
import { PageHeader } from '../platform/ui/PageHeader';
import { TableDate } from '../platform/ui/TableDate';
import { Card, MetricCard, Skeleton } from '../platform/ui/Workspace';
import { briefingApi } from '../modules/briefing/api';
import { resolveFeedTitle, sourceKey } from '../modules/briefing/feed';
import type { FeedEntry } from '../modules/briefing/types';
import { tasksApi } from '../modules/tasks/api';
import {
  buildAttention,
  overviewMetrics,
  type AttentionItem,
} from '../modules/my-work/overviewModel';
import { summarizeFeed } from '../modules/my-work/workModel';
import { QueueHealth } from '../modules/tickets/QueueHealth';

const quickActions: ReadonlyArray<{ route: RouteId; icon: RouteId; label: MessageKey }> = [
  { route: 'taskNew', icon: 'tasks', label: 'overview.action.createTask' },
  { route: 'ticketNew', icon: 'myTickets', label: 'overview.action.reportProblem' },
  { route: 'ticketQueue', icon: 'ticketQueue', label: 'overview.action.ticketQueue' },
  { route: 'changeNew', icon: 'changes', label: 'overview.action.changes' },
  { route: 'catalog', icon: 'catalog', label: 'overview.action.catalog' },
];

/** The localized page greeting; it never shows an identifier and falls back to no name. */
export function useGreeting(): string {
  const { t } = useI18n();
  const { session } = useSession();
  const name = greetingName(session);
  const part = dayPart(new Date());
  return name ? t(`overview.greeting.${part}`, { name }) : t(`overview.greetingPlain.${part}`);
}

function FeedTitle({ entry }: { entry: FeedEntry }) {
  const { t } = useI18n();
  const title = resolveFeedTitle(entry);
  return <>{t(title.key, title.params)}</>;
}

function AttentionCard({ item }: { item: AttentionItem }) {
  const { t } = useI18n();
  let to: string;
  let icon: RouteId;
  let title: ReactNode;
  let meta: ReactNode;
  let action: string;
  let time: string | null | undefined;
  if (item.kind === 'task') {
    to = `/tasks/${encodeURIComponent(item.task.id)}`;
    icon = 'tasks';
    title = item.task.title;
    meta = (
      <>
        {t(`tasks.priority.${item.task.priority}`)}
        {item.overdue ? ` · ${t('overview.overdueTask')}` : ''}
        {item.task.assignedTeamName ? ` · ${item.task.assignedTeamName}` : ''}
      </>
    );
    action = t('dashboard.openTask');
    time = item.task.dueAt ?? item.task.createdAt;
  } else if (item.kind === 'approvals') {
    to = '/approvals';
    icon = 'approvals';
    title = t('overview.approvalsTitle', { count: item.count });
    meta = t(sourceKey(item.entry.source));
    action = t('overview.reviewApprovals');
    time = item.entry.occurredAt;
  } else {
    to = item.entry.linkPath;
    icon = item.entry.kind === 'major_incident' ? 'incidents' : 'briefing';
    title = <FeedTitle entry={item.entry} />;
    meta = t(sourceKey(item.entry.source));
    action = t('dashboard.viewDetails');
    time = item.entry.dueAt ?? item.entry.occurredAt;
  }
  return (
    <Link to={to} className={`dashboard-action-card attention-${item.tone}`}>
      <div className="dashboard-action-top">
        <span className="dashboard-icon">
          <NavIcon id={icon} />
        </span>
        {time ? <TableDate value={time} /> : null}
      </div>
      <h3>{title}</h3>
      <p>{meta}</p>
      <div className="dashboard-action-footer">
        {action}
        <span aria-hidden="true">→</span>
      </div>
    </Link>
  );
}

/** High-level workspace dashboard. My Work remains the place to work the queue. */
export function OverviewScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const greeting = useGreeting();
  const { enabled } = useModules();
  const list = usePagedList((cursor, signal) => tasksApi.myWork(cursor, signal), []);
  const briefingRoute = appRoutes.find((route) => route.id === 'briefing');
  // The feed answers 403 without a briefing-related permission, so do not ask at all.
  const briefingEnabled =
    enabled('briefing') && (!briefingRoute || canViewRoute(can, briefingRoute, enabled));
  const queueRoute = appRoutes.find((route) => route.id === 'ticketQueue');
  const showQueueHealth = !!queueRoute && canViewRoute(can, queueRoute, enabled);
  const feed = useAsync(
    (signal) =>
      briefingEnabled
        ? briefingApi.feed(signal)
        : Promise.resolve({ entries: [], unavailable: [], truncated: {} }),
    [briefingEnabled],
  );
  const now = new Date();
  const entries = briefingEnabled
    ? (feed.data?.entries ?? []).filter((entry) => pathEnabled(entry.linkPath, enabled))
    : [];
  const feedIncomplete =
    !!feed.data?.unavailable.length || Object.values(feed.data?.truncated ?? {}).some(Boolean);
  const metrics = overviewMetrics(list.items, entries, now);
  const attention = buildAttention(list.items, entries, now);
  const summary = summarizeFeed(entries);
  const highlight = summary.highlight ?? entries.find((entry) => entry.severity !== 'info');
  const otherSignals = entries.filter(
    (entry) => entry !== highlight && entry.kind !== 'pending_approvals',
  ).length;
  const actions = quickActions.filter(({ route }) => {
    const found = appRoutes.find((candidate) => candidate.id === route);
    return found ? canViewRoute(can, found, enabled) : false;
  });
  const loading = (list.loading && !list.items.length) || (feed.loading && !feed.data);
  const today = new Intl.DateTimeFormat(locale, {
    weekday: 'long',
    day: 'numeric',
    month: 'long',
  }).format(now);
  return (
    <div className="work-dashboard overview-dashboard">
      <PageHeader
        eyebrow={t('overview.eyebrow', { date: today })}
        title={greeting}
        intro={t('overview.intro')}
        actions={
          can('tasks.manage') ? (
            <Link to="/tasks/new" className="btn btn-primary">
              <span aria-hidden="true">+</span> {t('tasks.create.action')}
            </Link>
          ) : null
        }
      />
      {loading ? (
        <Skeleton lines={3} />
      ) : !list.error && !feed.error ? (
        <div
          className="workspace-metrics dashboard-metrics"
          aria-label={t('overview.metrics')}
          style={{ '--metric-count': metrics.approvals === undefined ? 3 : 4 } as never}
        >
          <MetricCard
            label={t('overview.metric.open')}
            value={metrics.open}
            to="/my-work"
            caption={t('overview.metric.openCaption')}
            icon={<NavIcon id="myWork" />}
          />
          <MetricCard
            label={t('overview.metric.overdue')}
            value={metrics.overdue}
            to="/my-work?focus=overdue"
            tone="danger"
            caption={t('overview.metric.overdueCaption')}
            zeroCaption={t('overview.metric.overdueZero')}
            icon={<NavIcon id="maintenanceCalendar" />}
          />
          {metrics.approvals !== undefined ? (
            <MetricCard
              label={t('overview.metric.approvals')}
              value={metrics.approvals}
              to="/approvals"
              tone="warning"
              caption={t('overview.metric.approvalsCaption')}
              zeroCaption={t('overview.metric.approvalsZero')}
              icon={<NavIcon id="approvals" />}
            />
          ) : null}
          {briefingEnabled && (
            <MetricCard
              label={t('overview.metric.alerts')}
              value={metrics.alerts}
              to="/briefing"
              tone="warning"
              caption={t('overview.metric.alertsCaption')}
              {...(!feedIncomplete ? { zeroCaption: t('overview.metric.alertsZero') } : {})}
              icon={<NavIcon id="briefing" />}
            />
          )}
        </div>
      ) : null}
      <p className="dashboard-scope">{t('overview.scope')}</p>
      {list.error || feed.error ? (
        <p role="alert" className="workspace-source-notice">
          {t('error.generic')}{' '}
          <Button
            onClick={() => {
              list.reload();
              feed.reload();
            }}
          >
            {t('action.retry')}
          </Button>
        </p>
      ) : null}
      <section aria-labelledby="overview-needs">
        <div className="dashboard-section-heading">
          <h2 id="overview-needs">
            {t('overview.needsYou')}
            {attention.length ? <span className="section-count">{attention.length}</span> : null}
          </h2>
          <Link to="/my-work" className="section-link">
            {t('overview.viewAllWork')} <span aria-hidden="true">→</span>
          </Link>
        </div>
        <p className="dashboard-focus-intro">{t('overview.focusHint')}</p>
        {loading ? null : attention.length ? (
          <div className="dashboard-action-grid">
            {attention.map((item) => (
              <AttentionCard
                key={
                  item.kind === 'task'
                    ? item.task.id
                    : `${item.kind}-${item.entry.source}-${item.entry.linkPath}-${item.entry.titleKey}`
                }
                item={item}
              />
            ))}
          </div>
        ) : !list.error && !feed.error && !feedIncomplete ? (
          <div className="overview-clear">
            <span className="dashboard-icon" aria-hidden="true">
              ✓
            </span>
            <p>{t('overview.nothingNeeded')}</p>
          </div>
        ) : null}
      </section>
      {showQueueHealth ? <QueueHealth canListTeams={can('organization.view')} /> : null}
      <div className="overview-columns">
        <Card className="dashboard-timeline" title={t('overview.recent')}>
          <div className="dashboard-section-heading">
            <h2>{t('overview.recent')}</h2>
            <span>{t('overview.recentScope')}</span>
          </div>
          {feed.loading && !feed.data ? <Skeleton lines={3} /> : null}
          {summary.recent.length ? (
            <ul>
              {summary.recent.map((entry, index) => (
                <li key={`${entry.source}-${index}`}>
                  <span
                    className={`dashboard-timeline-dot severity-${entry.severity}`}
                    aria-hidden="true"
                  />
                  <div>
                    <Link to={entry.linkPath}>
                      <FeedTitle entry={entry} />
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
        {briefingEnabled && (
          <div className="overview-side">
            <Card
              className={`overview-highlight${highlight ? ` severity-${highlight.severity}` : ''}`}
              title={t('overview.highlightEyebrow')}
            >
              <span className="dashboard-eyebrow">{t('overview.highlightEyebrow')}</span>
              {highlight ? (
                <>
                  <h2>
                    <FeedTitle entry={highlight} />
                  </h2>
                  <p>
                    {t(sourceKey(highlight.source))}
                    {highlight.occurredAt || highlight.dueAt ? (
                      <>
                        {' · '}
                        <TableDate value={highlight.dueAt ?? highlight.occurredAt} />
                      </>
                    ) : null}
                  </p>
                  <Link to={highlight.linkPath} className="overview-highlight-link">
                    {t('dashboard.viewDetails')} <span aria-hidden="true">→</span>
                  </Link>
                </>
              ) : !feed.loading && !feed.error && !feedIncomplete ? (
                <p>{t('overview.noHighlight')}</p>
              ) : (
                <p>{t('overview.coverageHint')}</p>
              )}
              {otherSignals > 0 ? (
                <Link to="/briefing" className="overview-highlight-more">
                  {t('overview.highlightMore', { count: otherSignals })}
                </Link>
              ) : null}
            </Card>
          </div>
        )}
      </div>
      {briefingEnabled && feed.data ? <BriefingCoverage data={{ ...feed.data, entries }} /> : null}
      {actions.length ? (
        <section className="overview-actions" aria-labelledby="overview-actions">
          <div className="dashboard-section-heading">
            <h2 id="overview-actions">{t('overview.quickActions')}</h2>
          </div>
          <ul>
            {actions.map((action) => {
              const route = appRoutes.find((candidate) => candidate.id === action.route);
              return route ? (
                <li key={action.route}>
                  <Link to={route.pattern}>
                    <span className="overview-action-icon" aria-hidden="true">
                      <NavIcon id={action.icon} />
                    </span>
                    <span>{t(action.label)}</span>
                    <span aria-hidden="true">→</span>
                  </Link>
                </li>
              ) : null;
            })}
          </ul>
        </section>
      ) : null}
    </div>
  );
}
