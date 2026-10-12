import { useAsync } from '../platform/api/useAsync';
import { formatDate } from '../platform/format/format';
import { useI18n } from '../platform/i18n/I18nProvider';
import { Link } from '../platform/router/Router';
import { useSession } from '../platform/session/SessionProvider';
import { ApiErrorAlert } from '../platform/ui/ApiErrorAlert';
import { PageHeader } from '../platform/ui/PageHeader';
import { Card, MetricCard, Skeleton } from '../platform/ui/Workspace';
import { taskOverviewApi, type TaskOverviewOwner } from '../modules/tasks/overviewApi';
import { ticketOverviewApi } from '../modules/tickets/overviewApi';
import { relativeDue } from '../modules/sla/slaModel';
import { ageInDays, taskLinks, ticketLinks } from './managerOverviewModel';
import './manager-overview.css';

/**
 * Manager overview (read-only): where tickets and tasks wait, by queue and by person. Each section has its own
 * endpoint and authorization; the server decides what the caller may see, this screen only presents it.
 */
export function ManagerOverviewScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const showTasks = can('tasks.view') || can('tasks.manage');
  return (
    <>
      <PageHeader
        eyebrow={t('managerOverview.eyebrow')}
        title={t('managerOverview.title')}
        intro={t('managerOverview.intro')}
      />
      <TicketSection />
      {showTasks ? <TaskSection /> : null}
    </>
  );
}

function TicketSection() {
  const { t, locale } = useI18n();
  const overview = useAsync((signal) => ticketOverviewApi.get(signal), []);
  const data = overview.data;
  return (
    <section aria-labelledby="mo-tickets">
      <div className="dashboard-section-heading">
        <h2 id="mo-tickets">{t('managerOverview.tickets')}</h2>
        <Link to="/service-desk" className="section-link">
          {t('overview.action.ticketQueue')} <span aria-hidden="true">→</span>
        </Link>
      </div>
      {overview.error ? (
        <ApiErrorAlert error={overview.error} onRetry={overview.reload} />
      ) : !data ? (
        <Skeleton lines={3} />
      ) : (
        <>
          <div
            className="workspace-metrics dashboard-metrics"
            style={{ '--metric-count': data.slaEnabled ? 5 : 3 } as never}
          >
            <MetricCard
              label={t('managerOverview.tickets.open')}
              value={data.totals.open}
              to={ticketLinks.open()}
              caption={t('managerOverview.tickets.openCaption')}
            />
            <MetricCard
              label={t('managerOverview.tickets.unassigned')}
              value={data.totals.unassigned}
              to={ticketLinks.unassigned()}
              tone="warning"
              caption={t('managerOverview.tickets.unassignedCaption')}
              zeroCaption={t('managerOverview.tickets.unassignedZero')}
            />
            <MetricCard
              label={t('managerOverview.tickets.waiting')}
              value={data.totals.waiting}
              to={ticketLinks.waiting()}
              caption={t('managerOverview.tickets.waitingCaption')}
            />
            {data.slaEnabled ? (
              <>
                <MetricCard
                  label={t('managerOverview.tickets.atRisk')}
                  value={data.totals.atRisk ?? 0}
                  to={ticketLinks.atRisk()}
                  tone="warning"
                  caption={t('managerOverview.tickets.atRiskCaption')}
                  zeroCaption={t('managerOverview.tickets.atRiskZero')}
                />
                <MetricCard
                  label={t('managerOverview.tickets.breached')}
                  value={data.totals.breached ?? 0}
                  to={ticketLinks.breached()}
                  tone="danger"
                  caption={t('managerOverview.tickets.breachedCaption')}
                  zeroCaption={t('managerOverview.tickets.breachedZero')}
                />
              </>
            ) : null}
          </div>
          <Card title={t('managerOverview.byQueue')}>
            <h3>{t('managerOverview.byQueue')}</h3>
            {data.queues.length === 0 ? (
              <p className="subtitle">{t('managerOverview.noOpenTickets')}</p>
            ) : (
              <table className="manager-table">
                <thead>
                  <tr>
                    <th scope="col">{t('managerOverview.col.queue')}</th>
                    <th scope="col">{t('managerOverview.col.open')}</th>
                    <th scope="col">{t('managerOverview.col.unassigned')}</th>
                    <th scope="col">{t('managerOverview.col.waiting')}</th>
                    {data.slaEnabled ? (
                      <>
                        <th scope="col">{t('managerOverview.col.atRisk')}</th>
                        <th scope="col">{t('managerOverview.col.breached')}</th>
                      </>
                    ) : null}
                    <th scope="col">{t('managerOverview.col.oldest')}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.queues.map((row) => (
                    <tr key={row.queue.id}>
                      <th scope="row">
                        {row.queue.name} <small>({row.queue.prefix})</small>
                      </th>
                      <td>
                        <Link to={ticketLinks.queueOpen(row.queue.id)}>{row.open}</Link>
                      </td>
                      <td>
                        <Link to={ticketLinks.queueUnassigned(row.queue.id)}>{row.unassigned}</Link>
                      </td>
                      <td>
                        <Link to={ticketLinks.queueWaiting(row.queue.id)}>{row.waiting}</Link>
                      </td>
                      {data.slaEnabled ? (
                        <>
                          <td>
                            <Link to={ticketLinks.atRisk(row.queue.id)}>{row.atRisk ?? 0}</Link>
                          </td>
                          <td>
                            <Link to={ticketLinks.breached(row.queue.id)}>{row.breached ?? 0}</Link>
                          </td>
                        </>
                      ) : null}
                      <td>{formatDate(locale, row.oldestOpenAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Card>
          <Card title={t('managerOverview.byAssignee')}>
            <h3>{t('managerOverview.byAssignee')}</h3>
            {data.assignees.length === 0 ? (
              <p className="subtitle">{t('managerOverview.noAssignees')}</p>
            ) : (
              <ul className="queue-teams">
                {data.assignees.map((person) => (
                  <li key={person.userId}>
                    <Link to={ticketLinks.assigneeOpen(person.userId)}>
                      {person.name || t('managerOverview.unknownPerson')}
                    </Link>
                    <strong>
                      {person.open}
                      {person.waiting > 0
                        ? ` (${t('managerOverview.ofWhichWaiting', { count: person.waiting })})`
                        : ''}
                    </strong>
                  </li>
                ))}
              </ul>
            )}
            {data.assigneesTruncated ? (
              <p className="subtitle">{t('managerOverview.truncated')}</p>
            ) : null}
          </Card>
          <div className="manager-columns">
            <Card title={t('managerOverview.oldestUnassigned')}>
              <h3>{t('managerOverview.oldestUnassigned')}</h3>
              <TicketRows items={data.oldestUnassigned} date="createdAt" now={data.generatedAt} />
            </Card>
            <Card title={t('managerOverview.oldestWaiting')}>
              <h3>{t('managerOverview.oldestWaiting')}</h3>
              <TicketRows items={data.oldestWaiting} date="updatedAt" now={data.generatedAt} />
            </Card>
          </div>
          {data.slaEnabled ? (
            <Card title={t('managerOverview.nextToBreach')}>
              <h3>{t('managerOverview.nextToBreach')}</h3>
              <NextToBreach items={data.nextToBreach ?? []} now={data.generatedAt} />
            </Card>
          ) : null}
        </>
      )}
    </section>
  );
}

function TicketRows({
  items,
  date,
  now,
}: {
  items: Array<{
    id: string;
    reference: string;
    title: string;
    createdAt: string;
    updatedAt: string;
    queue: { name: string };
  }>;
  date: 'createdAt' | 'updatedAt';
  now: string;
}) {
  const { t } = useI18n();
  if (items.length === 0) return <p className="subtitle">{t('managerOverview.nothing')}</p>;
  return (
    <ul className="manager-items">
      {items.map((item) => (
        <li key={item.id}>
          <Link to={`/support/${item.id}`}>
            <strong>{item.reference}</strong> {item.title}
          </Link>
          <span>
            {item.queue.name} ·{' '}
            {t(
              date === 'createdAt' ? 'managerOverview.ageDays' : 'managerOverview.changedDaysAgo',
              {
                count: ageInDays(item[date], new Date(now)),
              },
            )}
          </span>
        </li>
      ))}
    </ul>
  );
}

function NextToBreach({
  items,
  now,
}: {
  items: Array<{
    id: string;
    reference: string;
    title: string;
    queue: { name: string };
    slaDueAt?: string;
  }>;
  now: string;
}) {
  const { t } = useI18n();
  if (items.length === 0) return <p className="subtitle">{t('managerOverview.nothing')}</p>;
  return (
    <ul className="manager-items">
      {items.map((item) => {
        const due = relativeDue(item.slaDueAt, new Date(now));
        return (
          <li key={item.id}>
            <Link to={`/support/${item.id}`}>
              <strong>{item.reference}</strong> {item.title}
            </Link>
            <span>
              {item.queue.name}
              {due ? ` · ${t(`sla.relative.${due.unit}`, { count: due.count })}` : ''}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

function TaskSection() {
  const { t, locale } = useI18n();
  const overview = useAsync((signal) => taskOverviewApi.get(signal), []);
  const data = overview.data;
  return (
    <section aria-labelledby="mo-tasks">
      <div className="dashboard-section-heading">
        <h2 id="mo-tasks">{t('managerOverview.tasks')}</h2>
        <Link to="/tasks" className="section-link">
          {t('managerOverview.openTasks')} <span aria-hidden="true">→</span>
        </Link>
      </div>
      {overview.error ? (
        <ApiErrorAlert error={overview.error} onRetry={overview.reload} />
      ) : !data ? (
        <Skeleton lines={3} />
      ) : (
        <>
          <div
            className="workspace-metrics dashboard-metrics"
            style={{ '--metric-count': 4 } as never}
          >
            <MetricCard
              label={t('managerOverview.tasks.open')}
              value={data.totals.open}
              to={taskLinks.open()}
              caption={t('managerOverview.tasks.openCaption')}
            />
            <MetricCard
              label={t('managerOverview.tasks.overdue')}
              value={data.totals.overdue}
              to={taskLinks.overdue(data.generatedAt)}
              tone="danger"
              caption={t('managerOverview.tasks.overdueCaption')}
              zeroCaption={t('managerOverview.tasks.overdueZero')}
            />
            <MetricCard
              label={t('managerOverview.tasks.blocked')}
              value={data.totals.blocked}
              to={taskLinks.blocked()}
              tone="warning"
              caption={t('managerOverview.tasks.blockedCaption')}
              zeroCaption={t('managerOverview.tasks.blockedZero')}
            />
            <MetricCard
              label={t('managerOverview.tasks.unowned')}
              value={data.totals.unowned}
              to={taskLinks.unowned()}
              tone="warning"
              caption={t('managerOverview.tasks.unownedCaption')}
              zeroCaption={t('managerOverview.tasks.unownedZero')}
            />
          </div>
          <div className="manager-columns">
            <OwnerCard
              title={t('managerOverview.tasks.byUser')}
              owners={data.users}
              truncated={data.usersTruncated}
              link={taskLinks.user}
            />
            <OwnerCard
              title={t('managerOverview.tasks.byTeam')}
              owners={data.teams}
              truncated={data.teamsTruncated}
              link={taskLinks.team}
            />
          </div>
          <div className="manager-columns">
            <Card title={t('managerOverview.tasks.oldestOverdue')}>
              <h3>{t('managerOverview.tasks.oldestOverdue')}</h3>
              {data.oldestOverdue.length === 0 ? (
                <p className="subtitle">{t('managerOverview.nothing')}</p>
              ) : (
                <ul className="manager-items">
                  {data.oldestOverdue.map((task) => (
                    <li key={task.id}>
                      <Link to={`/tasks/${task.id}`}>{task.title}</Link>
                      <span>
                        {t('managerOverview.dueSince', { date: formatDate(locale, task.dueAt) })}
                        {' · '}
                        {task.assignedUserName ??
                          task.assignedTeamName ??
                          t('managerOverview.noOwner')}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </Card>
            <Card title={t('managerOverview.tasks.oldestUnowned')}>
              <h3>{t('managerOverview.tasks.oldestUnowned')}</h3>
              {data.oldestUnowned.length === 0 ? (
                <p className="subtitle">{t('managerOverview.nothing')}</p>
              ) : (
                <ul className="manager-items">
                  {data.oldestUnowned.map((task) => (
                    <li key={task.id}>
                      <Link to={`/tasks/${task.id}`}>{task.title}</Link>
                      <span>
                        {t('managerOverview.ageDays', {
                          count: ageInDays(task.createdAt, new Date(data.generatedAt)),
                        })}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </Card>
          </div>
        </>
      )}
    </section>
  );
}

function OwnerCard({
  title,
  owners,
  truncated,
  link,
}: {
  title: string;
  owners: TaskOverviewOwner[];
  truncated: boolean;
  link: (id: string) => string;
}) {
  const { t } = useI18n();
  return (
    <Card title={title}>
      <h3>{title}</h3>
      {owners.length === 0 ? (
        <p className="subtitle">{t('managerOverview.nothing')}</p>
      ) : (
        <ul className="queue-teams">
          {owners.map((owner) => (
            <li key={owner.id}>
              <Link to={link(owner.id)}>{owner.name || t('managerOverview.unknownPerson')}</Link>
              <strong>
                {owner.open}
                {owner.overdue > 0
                  ? ` (${t('managerOverview.ofWhichOverdue', { count: owner.overdue })})`
                  : ''}
              </strong>
            </li>
          ))}
        </ul>
      )}
      {truncated ? <p className="subtitle">{t('managerOverview.truncated')}</p> : null}
    </Card>
  );
}
