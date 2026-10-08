import { useModules } from '../../platform/modules/ModulesProvider';
import { pathEnabled } from '../../platform/modules/model';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Card, EmptyState, MetricCard, Skeleton, useCountUp } from '../../platform/ui/Workspace';
import { briefingApi } from './api';
import { severityTone } from './actions';
import { groupFeedEntries, resolveFeedTitle, sourceKey } from './feed';

function FeedCount({ value }: { value: number }) {
  const display = useCountUp(value);
  return <strong aria-label={String(value)}>{display}</strong>;
}

export function BriefingFeedScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const feed = useAsync((signal) => briefingApi.feed(signal), []);
  const { enabled } = useModules();
  const data = feed.data
    ? {
        ...feed.data,
        entries: feed.data.entries.filter((entry) => pathEnabled(entry.linkPath, enabled)),
      }
    : undefined;
  const groups = groupFeedEntries(data?.entries ?? []);
  const truncated = Object.keys(data?.truncated ?? {}).filter((source) => data?.truncated[source]);

  return (
    <>
      <PageHeader
        title={t('nav.briefing')}
        intro={t('briefing.intro')}
        actions={
          can('briefing.view') || can('briefing.manage') ? (
            <Link to="/briefing/items" className="btn btn-primary">
              {t('briefing.items.title')}
            </Link>
          ) : null
        }
      />
      {feed.loading && !data ? <Skeleton lines={6} /> : null}
      {feed.error ? <ApiErrorAlert error={feed.error} onRetry={feed.reload} /> : null}
      {data ? (
        <div className="workspace-metrics" aria-label={t('briefing.feed.summary')}>
          {(['critical', 'warning', 'info'] as const).map((severity) => {
            const entries = data.entries.filter((entry) => entry.severity === severity);
            return (
              <MetricCard
                key={severity}
                label={t(`briefing.severity.${severity}`)}
                value={entries.length}
                to={entries[0]?.linkPath ?? '/briefing'}
                tone={
                  severity === 'critical' ? 'danger' : severity === 'warning' ? 'warning' : 'info'
                }
              />
            );
          })}
        </div>
      ) : null}
      {data?.unavailable.length ? (
        <Alert kind="warning">
          {t('briefing.feed.unavailable')}
          <ul>
            {data.unavailable.map(({ source, reason }) => (
              <li key={source}>
                {t(sourceKey(source))}:{' '}
                {t(
                  reason === 'source_error'
                    ? 'briefing.feed.sourceError'
                    : reason === 'source_timeout'
                      ? 'briefing.feed.sourceTimeout'
                      : 'briefing.feed.unknownError',
                )}
              </li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {truncated.length ? (
        <Alert kind="info">
          {t('briefing.feed.moreAvailable')}
          <ul>
            {truncated.map((source) => (
              <li key={source}>{t(sourceKey(source))}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {data && groups.length === 0 ? <EmptyState title={t('briefing.feed.empty')} /> : null}
      {groups.map((group) => (
        <section
          key={group.severity}
          className="briefing-group"
          aria-label={t(`briefing.severity.${group.severity}`)}
        >
          <div className="workspace-section-head">
            <h2>{t(`briefing.severity.${group.severity}`)}</h2>
            <span>{group.entries.length}</span>
          </div>
          <div className="briefing-grid">
            {group.entries.map((entry, index) => {
              const title = resolveFeedTitle(entry);
              return (
                <Card
                  className={`briefing-card severity-${entry.severity}`}
                  key={`${entry.kind}-${entry.reference?.id ?? entry.linkPath}-${index}`}
                >
                  <Badge tone={severityTone(entry.severity)}>
                    {t(`briefing.severity.${entry.severity}`)}
                  </Badge>
                  <h3>
                    <Link to={entry.linkPath}>{t(title.key, title.params)}</Link>
                  </h3>
                  <p>
                    {t('briefing.feed.kind')}: {t(`briefing.kind.${entry.kind}`)}
                  </p>
                  {entry.count !== undefined ? (
                    <p>
                      <Link to={entry.linkPath}>
                        <FeedCount value={entry.count} />{' '}
                        <span className="visually-hidden">
                          {t('briefing.feed.count', { count: entry.count })}
                        </span>
                      </Link>
                    </p>
                  ) : null}
                  {entry.dueAt ? (
                    <p>
                      {t('briefing.feed.dueAt')}:{' '}
                      <time dateTime={entry.dueAt}>{formatDateTime(locale, entry.dueAt)}</time>
                    </p>
                  ) : null}
                  {entry.occurredAt ? (
                    <p>
                      {t('briefing.feed.occurredAt')}:{' '}
                      <time dateTime={entry.occurredAt}>
                        {formatDateTime(locale, entry.occurredAt)}
                      </time>
                    </p>
                  ) : null}
                  <p>
                    {t('briefing.feed.source')}: {t(sourceKey(entry.source))}
                  </p>
                </Card>
              );
            })}
          </div>
        </section>
      ))}
    </>
  );
}
