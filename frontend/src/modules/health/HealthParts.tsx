import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { docsPathUrl } from '../../platform/ui/docsLinks';
import { TableDate } from '../../platform/ui/TableDate';
import { errorTextKey, messageOr, resolveAppRoute, statusKey, statusTone } from './model';
import type { HealthResult, HealthStatus, NextStep } from './types';

/** Text plus symbol (never color alone); every non-OK status reads differently from OK. */
export function HealthStatusBadge({ status }: { status: HealthStatus }) {
  const { t } = useI18n();
  return <Badge tone={statusTone(status)}>{t(statusKey(status))}</Badge>;
}

/** What to do next: a link, the exact configuration key names (never values) or a guide. */
export function NextStepView({ step }: { step: NextStep | undefined }) {
  const { t } = useI18n();
  if (!step) return null;
  const route = resolveAppRoute(step.route);
  const docs = docsPathUrl(step.docsPath);
  const keys = step.configKeys ?? [];
  if (!route && !docs && keys.length === 0) return null;
  return (
    <div className="health-next">
      <strong>{t('health.nextStep')}</strong>
      {keys.length > 0 ? (
        <>
          <p className="health-meta">{t('health.nextStep.config')}</p>
          <ul className="health-keys" aria-label={t('health.nextStep.config')}>
            {keys.map((key) => (
              <li key={key}>
                <code>{key}</code>
              </li>
            ))}
          </ul>
        </>
      ) : null}
      <p className="health-meta">
        {route ? <Link to={route}>{t('health.nextStep.open')}</Link> : null}
        {route && docs ? ' · ' : null}
        {docs ? (
          <a href={docs} target="_blank" rel="noreferrer noopener">
            {t('health.nextStep.guide')}
          </a>
        ) : null}
      </p>
    </div>
  );
}

export function checkTitle(t: ReturnType<typeof useI18n>['t'], key: string): string {
  return t(messageOr(`health.check.${key}`, 'health.check.unknown'), { key });
}

const countLabels = (counts: Record<string, number> | undefined) =>
  Object.entries(counts ?? {}).filter(([, value]) => Number.isFinite(value));

/** One check: status, mode, freshness (observed, last success, last attempt), error code, counts, next step. */
export function CheckCard({ result }: { result: HealthResult }) {
  const { t } = useI18n();
  const counts = countLabels(result.counts);
  const code = result.errorCode;
  return (
    <li className="health-card" data-status={result.status}>
      <div className="health-card-head">
        <h3>{checkTitle(t, result.key)}</h3>
        <HealthStatusBadge status={result.status} />
      </div>
      <p className="health-meta">
        {t(messageOr(`health.check.${result.key}.about`, 'health.check.unknown.about'))}
      </p>
      <dl className="health-meta-list">
        {result.mode ? (
          <>
            <dt>{t('health.mode')}</dt>
            <dd>{t(messageOr(`health.mode.${result.mode}`, 'health.status.unknown'))}</dd>
          </>
        ) : null}
        <dt>{t('health.checkedAt')}</dt>
        <dd>
          <TableDate value={result.observedAt} />
        </dd>
        <dt>{t('health.lastSuccess')}</dt>
        <dd>
          {result.lastSuccessAt ? <TableDate value={result.lastSuccessAt} /> : t('health.never')}
        </dd>
        {result.lastAttemptAt ? (
          <>
            <dt>{t('health.lastAttempt')}</dt>
            <dd>
              <TableDate value={result.lastAttemptAt} />
            </dd>
          </>
        ) : null}
        {code ? (
          <>
            <dt>{t('health.errorCode')}</dt>
            <dd>
              <code>{code}</code>
              {' · '}
              {t(errorTextKey(code))}
            </dd>
          </>
        ) : null}
        {counts.map(([name, value]) => (
          <div key={name} style={{ display: 'contents' }}>
            <dt>{t(messageOr(`health.count.${name}`, 'health.count.unknown'), { name })}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
      <NextStepView step={result.nextStep} />
    </li>
  );
}
