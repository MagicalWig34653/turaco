import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { MetricCard } from '../../platform/ui/Workspace';
import { healthApi } from './api';
import { settledCount } from './model';

/**
 * Overview tile "Setup n/10" for users with platform.health.view. One request, no polling; it only
 * renders while items are open, and a failing request simply shows nothing (the checklist screen
 * reports errors).
 */
export function SetupTile() {
  const { t } = useI18n();
  const setup = useAsync((signal) => healthApi.setup(signal), []);
  const list = setup.data;
  if (!list || list.open <= 0) return null;
  return (
    <section aria-labelledby="overview-setup">
      <div className="dashboard-section-heading">
        <h2 id="overview-setup">{t('setup.title')}</h2>
      </div>
      <div className="workspace-metrics dashboard-metrics" style={{ '--metric-count': 1 } as never}>
        <MetricCard
          label={t('setup.tile')}
          value={settledCount(list)}
          denominator={list.total}
          to="/admin/setup"
          tone="info"
          caption={t('setup.tile.caption', { count: list.open })}
        />
      </div>
    </section>
  );
}
