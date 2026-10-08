import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { Card, StatusBadge } from '../../platform/ui/Workspace';
import { TableDate } from '../../platform/ui/TableDate';
import { resolveFeedTitle, sourceKey } from '../briefing/feed';
import type { FeedResult } from '../briefing/types';

/** Preserve every returned signal and make incomplete sources explicit. */
export function BriefingCoverage({ data }: { data: FeedResult }) {
  const { t } = useI18n();
  const truncated = Object.keys(data.truncated).filter((source) => data.truncated[source]);
  return (
    <Card className="briefing-coverage" title={t('overview.coverage')}>
      <div className="dashboard-section-heading">
        <h2>{t('overview.coverage')}</h2>
        <Link to="/briefing" className="section-link">
          {t('dashboard.viewDetails')} →
        </Link>
      </div>
      <p className="dashboard-scope">{t('overview.coverageHint')}</p>
      {data.unavailable.length ? (
        <div className="workspace-source-notice" role="status">
          {t('briefing.feed.unavailable')}
          <ul>
            {data.unavailable.map(({ source, reason }) => (
              <li key={source}>
                {t(sourceKey(source))}:{' '}
                {t(
                  reason === 'source_timeout'
                    ? 'briefing.feed.sourceTimeout'
                    : reason === 'source_error'
                      ? 'briefing.feed.sourceError'
                      : 'briefing.feed.unknownError',
                )}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {truncated.length ? (
        <p className="workspace-source-notice">
          {t('briefing.feed.moreAvailable')}{' '}
          {truncated.map((source) => t(sourceKey(source))).join(', ')}
        </p>
      ) : null}
      {data.entries.length ? (
        <ul className="briefing-coverage-list">
          {data.entries.map((entry, index) => {
            const title = resolveFeedTitle(entry);
            return (
              <li key={`${entry.source}-${index}`}>
                <div>
                  <Link to={entry.linkPath}>{t(title.key, title.params)}</Link>
                  <small>
                    {t(sourceKey(entry.source))}
                    {entry.dueAt || entry.occurredAt ? (
                      <>
                        {' '}
                        · <TableDate value={entry.dueAt ?? entry.occurredAt} />
                      </>
                    ) : null}
                  </small>
                </div>
                <StatusBadge
                  tone={
                    entry.severity === 'critical'
                      ? 'danger'
                      : entry.severity === 'warning'
                        ? 'warning'
                        : 'info'
                  }
                >
                  {t(`briefing.severity.${entry.severity}`)}
                </StatusBadge>
              </li>
            );
          })}
        </ul>
      ) : !data.unavailable.length ? (
        <p className="dashboard-scope">{t('briefing.feed.empty')}</p>
      ) : null}
    </Card>
  );
}
