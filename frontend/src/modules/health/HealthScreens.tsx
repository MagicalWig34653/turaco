import type { ReactNode } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TableDate } from '../../platform/ui/TableDate';
import { Skeleton } from '../../platform/ui/Workspace';
import { healthApi } from './api';
import { CheckCard, HealthStatusBadge } from './HealthParts';
import { messageOr, needsAttention, statusCounts } from './model';
import type { HealthReport, HealthStatus } from './types';
import './health.css';

function Summary({ report }: { report: HealthReport }) {
  const { t } = useI18n();
  const attention = report.items.filter((item) => needsAttention(item.status)).length;
  return (
    <div className="health-summary">
      {attention > 0 ? (
        <Alert kind="warning">{t('health.attention', { count: attention })}</Alert>
      ) : (
        <p role="status">{t('health.noAttention')}</p>
      )}
      {statusCounts(report.items).map(({ status, count }) => (
        <span key={status}>
          <HealthStatusBadge status={status} /> {count}
        </span>
      ))}
    </div>
  );
}

function ReportView({
  load,
  titleKey,
  introKey,
  groups,
}: {
  load: (signal: AbortSignal) => Promise<HealthReport>;
  titleKey: 'health.title' | 'integrations.title';
  introKey: 'health.intro' | 'integrations.intro';
  groups: ReadonlyArray<'system' | 'integration' | 'module'>;
}) {
  const { t } = useI18n();
  const report = useAsync(load, []);
  return (
    <div className="health-screen">
      <PageHeader
        title={t(titleKey)}
        intro={t(introKey)}
        actions={<Button onClick={report.reload}>{t('action.refresh')}</Button>}
      />
      <p className="health-meta">{t('health.cacheNote')}</p>
      {report.error ? <ApiErrorAlert error={report.error} onRetry={report.reload} /> : null}
      {report.loading && !report.data ? <Skeleton lines={4} /> : null}
      {report.data ? (
        <>
          <Summary report={report.data} />
          {groups.map((group) => {
            const items = report.data?.items.filter((item) => item.category === group) ?? [];
            if (!items.length) return null;
            return (
              <section key={group} aria-labelledby={`health-group-${group}`}>
                <h2 id={`health-group-${group}`}>{t(`health.group.${group}`)}</h2>
                <ul className="health-cards">
                  {items.map((item) => (
                    <CheckCard key={item.key} result={item} />
                  ))}
                </ul>
              </section>
            );
          })}
          {!report.data.items.length ? <p>{t('health.empty')}</p> : null}
        </>
      ) : null}
    </div>
  );
}

export function HealthScreen() {
  return (
    <ReportView
      load={(signal) => healthApi.health(signal)}
      titleKey="health.title"
      introKey="health.intro"
      groups={['system', 'integration', 'module']}
    />
  );
}

export function IntegrationsScreen() {
  return (
    <ReportView
      load={(signal) => healthApi.integrations(signal)}
      titleKey="integrations.title"
      introKey="integrations.intro"
      groups={['integration']}
    />
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </>
  );
}

export function SystemScreen() {
  const { t } = useI18n();
  const system = useAsync((signal) => healthApi.system(signal), []);
  const info = system.data;
  const dash = '–';
  const status = (value: HealthStatus | undefined) =>
    value ? <HealthStatusBadge status={value} /> : dash;
  return (
    <div className="health-screen">
      <PageHeader
        title={t('system.title')}
        intro={t('system.intro')}
        actions={<Button onClick={system.reload}>{t('action.refresh')}</Button>}
      />
      {system.error ? <ApiErrorAlert error={system.error} onRetry={system.reload} /> : null}
      {system.loading && !info ? <Skeleton lines={4} /> : null}
      {info ? (
        <>
          <section aria-labelledby="system-app">
            <h2 id="system-app">{t('system.application')}</h2>
            <dl className="health-meta-list">
              <Fact label={t('system.version')}>{info.version ?? dash}</Fact>
              <Fact label={t('system.commit')}>
                {info.commit ? <code>{info.commit}</code> : dash}
              </Fact>
              <Fact label={t('system.environment')}>{info.environment ?? dash}</Fact>
              <Fact label={t('system.startedAt')}>
                {info.startedAt ? <TableDate value={info.startedAt} /> : dash}
              </Fact>
            </dl>
          </section>
          <section aria-labelledby="system-db">
            <h2 id="system-db">{t('system.database')}</h2>
            <dl className="health-meta-list">
              <Fact label={t('system.status')}>{status(info.database?.status)}</Fact>
              <Fact label={t('system.databaseVersion')}>
                {info.database?.serverVersion ?? dash}
              </Fact>
              <Fact label={t('system.migrationsStatus')}>{status(info.migrations?.status)}</Fact>
              <Fact label={t('system.latestMigration')}>
                {info.migrations?.latestVersion !== undefined
                  ? `${info.migrations.latestVersion}${info.migrations.latestName ? ` · ${info.migrations.latestName}` : ''}`
                  : dash}
              </Fact>
              <Fact label={t('system.migrationsApplied')}>{info.migrations?.applied ?? dash}</Fact>
            </dl>
            <p className="health-meta">{t('system.migrationNote')}</p>
          </section>
          {info.modules ? (
            <section aria-labelledby="system-modules">
              <h2 id="system-modules">{t('system.modules')}</h2>
              <dl className="health-meta-list">
                {Object.entries(info.modules).map(([key, value]) => (
                  <Fact
                    key={key}
                    label={t(messageOr(`system.modules.${key}`, 'system.modules.unknown'), { key })}
                  >
                    {value}
                  </Fact>
                ))}
              </dl>
            </section>
          ) : null}
        </>
      ) : null}
    </div>
  );
}
