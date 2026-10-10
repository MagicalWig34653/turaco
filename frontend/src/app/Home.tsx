import { useModules } from '../platform/modules/ModulesProvider';
import { useI18n } from '../platform/i18n/I18nProvider';
import { Link } from '../platform/router/Router';
import { useSession } from '../platform/session/SessionProvider';
import { PageHeader } from '../platform/ui/PageHeader';
import { NavIcon } from '../platform/ui/NavIcon';
import { Card } from '../platform/ui/Workspace';
import { AnnouncementsBanner } from '../modules/briefing/AnnouncementsBanner';
import { IncidentBanner } from '../modules/incidents/IncidentBanner';
import { OverviewScreen, useGreeting } from './OverviewScreen';

export function Home() {
  const { t } = useI18n();
  const { can } = useSession();
  const greeting = useGreeting();
  const { enabled } = useModules();
  if (can('tasks.view') || can('tasks.manage') || can('tasks.work')) return <OverviewScreen />;
  return (
    <div className="work-dashboard employee-home">
      <PageHeader
        eyebrow={t('dashboard.employeeEyebrow')}
        title={greeting}
        intro={t('dashboard.employeeIntro')}
      />
      {enabled('briefing') && <AnnouncementsBanner />}
      {enabled('servicedesk') && <IncidentBanner />}
      {enabled('servicedesk') && (
        <Card className="employee-support">
          <span className="dashboard-icon">
            <NavIcon id="myTickets" />
          </span>
          <div>
            <span className="dashboard-eyebrow">{t('dashboard.supportEyebrow')}</span>
            <h2>{t('dashboard.supportTitle')}</h2>
            <p>{t('dashboard.supportIntro')}</p>
          </div>
          <Link to="/support/new" className="btn btn-primary">
            {t('tickets.create.title')} <span aria-hidden="true">→</span>
          </Link>
        </Card>
      )}
      <div className="dashboard-section-heading">
        <h2>{t('dashboard.yourWorkspace')}</h2>
      </div>
      <div className="dashboard-action-grid">
        {enabled('requests') && (
          <Link to="/requests" className="dashboard-action-card">
            <span className="dashboard-icon">
              <NavIcon id="requests" />
            </span>
            <h3>{t('nav.myRequests')}</h3>
            <p>{t('dashboard.requestsIntro')}</p>
            <div className="dashboard-action-footer">
              {t('dashboard.viewRequests')}
              <span aria-hidden="true">→</span>
            </div>
          </Link>
        )}
        {enabled('assets') && (
          <Link to="/my-assets" className="dashboard-action-card">
            <span className="dashboard-icon">
              <NavIcon id="myAssets" />
            </span>
            <h3>{t('nav.myAssets')}</h3>
            <p>{t('dashboard.equipmentIntro')}</p>
            <div className="dashboard-action-footer">
              {t('dashboard.viewEquipment')}
              <span aria-hidden="true">→</span>
            </div>
          </Link>
        )}
        {enabled('servicedesk') && (
          <Link to="/support" className="dashboard-action-card">
            <span className="dashboard-icon">
              <NavIcon id="myTickets" />
            </span>
            <h3>{t('nav.myTickets')}</h3>
            <p>{t('dashboard.ticketsIntro')}</p>
            <div className="dashboard-action-footer">
              {t('dashboard.viewTickets')}
              <span aria-hidden="true">→</span>
            </div>
          </Link>
        )}
      </div>
      {enabled('catalog') && (
        <Card className="dashboard-info employee-catalog">
          <span className="dashboard-eyebrow">{t('nav.catalog')}</span>
          <h2>{t('dashboard.catalogTitle')}</h2>
          <p>{t('dashboard.catalogIntro')}</p>
          {enabled('catalog') && (
            <Link to="/catalog" className="btn">
              {t('dashboard.browseCatalog')} <span aria-hidden="true">→</span>
            </Link>
          )}
        </Card>
      )}
    </div>
  );
}
