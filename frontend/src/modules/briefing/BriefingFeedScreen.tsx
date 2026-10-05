import { useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { PageHeader } from '../../platform/ui/PageHeader';
import { briefingApi } from './api';
import { severityTone } from './actions';
import { groupFeedEntries, resolveFeedTitle, sourceKey } from './feed';

export function BriefingFeedScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const feed = useAsync((signal) => briefingApi.feed(signal), []);
  const data = feed.data;
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
      {feed.loading && !data ? <p role="status">{t('state.loading')}</p> : null}
      {feed.error ? (
        <Alert kind="error">
          {t('error.generic')} <button onClick={feed.reload}>{t('action.retry')}</button>
        </Alert>
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
      {data && groups.length === 0 ? <p>{t('briefing.feed.empty')}</p> : null}
      {groups.map((group) => (
        <section key={group.severity} aria-label={t(`briefing.severity.${group.severity}`)}>
          <h2>{t(`briefing.severity.${group.severity}`)}</h2>
          <div className="grid">
            {group.entries.map((entry, index) => {
              const title = resolveFeedTitle(entry);
              return (
                <article
                  className="card"
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
                        {t('briefing.feed.count', { count: entry.count })}
                      </Link>
                    </p>
                  ) : null}
                  {entry.dueAt ? (
                    <p>
                      {t('briefing.feed.dueAt')}: {formatDateTime(locale, entry.dueAt)}
                    </p>
                  ) : null}
                  {entry.occurredAt ? (
                    <p>
                      {t('briefing.feed.occurredAt')}: {formatDateTime(locale, entry.occurredAt)}
                    </p>
                  ) : null}
                  <p>
                    {t('briefing.feed.source')}: {t(sourceKey(entry.source))}
                  </p>
                </article>
              );
            })}
          </div>
        </section>
      ))}
    </>
  );
}
