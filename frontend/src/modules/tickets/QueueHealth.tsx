import { api } from '../../platform/api/client';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { emptyState, serialize, type Node } from '../../platform/ui/query/filterModel';
import { Card, MetricCard, Skeleton } from '../../platform/ui/Workspace';
import { organizationApi } from '../organization/api';
import type { Team } from '../organization/types';

const openStatuses = ['new', 'open', 'in_progress', 'waiting'];
/** Tickets have no due date; a ticket nobody touched for this many days counts as stale. */
const STALE_DAYS = 3;
const MAX_TEAMS = 8;

const condition = (field: string, op: string, value?: unknown): Node => ({
  type: 'condition',
  field,
  op,
  ...(value === undefined ? {} : { value }),
});
const openOnly = condition('status', 'in', openStatuses);
const and = (...children: Node[]): Node => ({ type: 'group', logic: 'and', children });

/** The queue list URL that shows exactly the tickets behind a number. */
const queueLink = (root: Node) =>
  `/service-desk?query=${encodeURIComponent(serialize({ ...emptyState(), filter: { v: 1, root } }))}`;

async function count(root: Node, signal: AbortSignal): Promise<number> {
  const page = await api.post<{ count?: number }>(
    '/tickets/query',
    { filter: { v: 1, root }, count: true, limit: 1 },
    { signal },
  );
  return page.count ?? 0;
}

type Health = {
  unassigned: number;
  stale: number;
  urgent: number;
  teams: Array<{ team: Team; open: number }>;
};

async function load(signal: AbortSignal, withTeams: boolean): Promise<Health> {
  const [unassigned, stale, urgent] = await Promise.all([
    count(and(openOnly, condition('assignee', 'is_empty')), signal),
    count(and(openOnly, condition('updated_at', 'older_than_n_days', STALE_DAYS)), signal),
    count(and(openOnly, condition('priority', 'equals', 'urgent')), signal),
  ]);
  let teams: Health['teams'] = [];
  if (withTeams) {
    const found = (await organizationApi.searchTeams('', signal)).items
      .filter((team) => team.active)
      .slice(0, MAX_TEAMS);
    teams = await Promise.all(
      found.map(async (team) => ({
        team,
        open: await count(and(openOnly, condition('queue', 'equals', team.id)), signal),
      })),
    );
  }
  return { unassigned, stale, urgent, teams };
}

/** Compact queue health for IT leads: where tickets wait. Every number links to its filtered list. */
export function QueueHealth({ canListTeams }: { canListTeams: boolean }) {
  const { t } = useI18n();
  const health = useAsync((signal) => load(signal, canListTeams), [canListTeams]);
  return (
    <section aria-labelledby="overview-queue">
      <div className="dashboard-section-heading">
        <h2 id="overview-queue">{t('overview.queueHealth')}</h2>
        <Link to="/service-desk" className="section-link">
          {t('overview.action.ticketQueue')} <span aria-hidden="true">→</span>
        </Link>
      </div>
      {health.error ? (
        <ApiErrorAlert error={health.error} onRetry={health.reload} />
      ) : !health.data ? (
        <Skeleton lines={2} />
      ) : (
        <>
          <div
            className="workspace-metrics dashboard-metrics"
            style={{ '--metric-count': 3 } as never}
          >
            <MetricCard
              label={t('overview.queue.unassigned')}
              value={health.data.unassigned}
              to={queueLink(and(openOnly, condition('assignee', 'is_empty')))}
              tone="warning"
              caption={t('overview.queue.unassignedCaption')}
              zeroCaption={t('overview.queue.unassignedZero')}
            />
            <MetricCard
              label={t('overview.queue.stale')}
              value={health.data.stale}
              to={queueLink(
                and(openOnly, condition('updated_at', 'older_than_n_days', STALE_DAYS)),
              )}
              tone="warning"
              caption={t('overview.queue.staleCaption', { days: STALE_DAYS })}
              zeroCaption={t('overview.queue.staleZero')}
            />
            <MetricCard
              label={t('overview.queue.urgent')}
              value={health.data.urgent}
              to={queueLink(and(openOnly, condition('priority', 'equals', 'urgent')))}
              tone="danger"
              caption={t('overview.queue.urgentCaption')}
              zeroCaption={t('overview.queue.urgentZero')}
            />
          </div>
          {health.data.teams.length > 0 ? (
            <Card title={t('overview.queue.perTeam')}>
              <h3>{t('overview.queue.perTeam')}</h3>
              <ul className="queue-teams">
                {health.data.teams.map(({ team, open }) => (
                  <li key={team.id}>
                    <Link to={queueLink(and(openOnly, condition('queue', 'equals', team.id)))}>
                      {team.name}
                    </Link>
                    <strong>{open}</strong>
                  </li>
                ))}
              </ul>
            </Card>
          ) : null}
        </>
      )}
    </section>
  );
}
