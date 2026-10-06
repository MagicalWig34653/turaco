import { useEffect } from 'react';
import type { ReactNode } from 'react';
import { AuditScreen } from '../modules/audit/AuditScreen';
import { LoginScreen } from '../modules/auth/LoginScreen';
import { MeScreen } from '../modules/access/MeScreen';
import { RoleAssignmentsScreen } from '../modules/access/RoleAssignmentsScreen';
import { RoleCreateScreen } from '../modules/access/RoleCreateScreen';
import { RoleDetailScreen } from '../modules/access/RoleDetailScreen';
import { RolesScreen } from '../modules/access/RolesScreen';
import { NotificationsScreen } from '../modules/notifications/NotificationsScreen';
import {
  DefinitionCreateScreen,
  DefinitionDetailScreen,
} from '../modules/recurrence/DefinitionScreens';
import { RecurrenceScreen } from '../modules/recurrence/RecurrenceScreen';
import { BriefingScreen } from '../modules/briefing/BriefingScreen';
import { BriefingFeedScreen } from '../modules/briefing/BriefingFeedScreen';
import {
  BriefingCreateScreen,
  BriefingDetailScreen,
} from '../modules/briefing/BriefingItemScreens';
import { ApprovalDetailScreen } from '../modules/approvals/ApprovalDetailScreen';
import { ApprovalsScreen } from '../modules/approvals/ApprovalsScreen';
import { CatalogAdminScreen } from '../modules/catalog/CatalogAdminScreen';
import { CatalogScreen } from '../modules/catalog/CatalogScreen';
import { RequestFormScreen } from '../modules/catalog/RequestFormScreen';
import { ProductsScreen } from '../modules/products/ProductsScreen';
import { RequestDetailScreen } from '../modules/requests/RequestDetailScreen';
import { RequestsScreen } from '../modules/requests/RequestsScreen';
import {
  SiteTreeScreen,
  BuildingScreen,
  RoomScreen,
  RackScreen,
} from '../modules/infrastructure/TopologyScreens';
import {
  ServicesListScreen,
  ServiceDetailScreen,
  ImpactScreen,
} from '../modules/services/ServicesScreens';
import {
  ChangesListScreen,
  ChangeCreateScreen,
  ChangeDetailScreen,
} from '../modules/changes/ChangesScreens';
import {
  InitiativesListScreen,
  InitiativeCreateScreen,
  InitiativeDetailScreen,
  MaintenanceCalendarScreen,
} from '../modules/planning/PlanningScreens';
import {
  AdvisoriesScreen,
  AdvisoryCreateScreen,
  AdvisoryDetailScreen,
  SecurityFindingsScreen,
  SecurityFindingDetailScreen,
} from '../modules/security/SecurityScreens';
import { SecurityOverviewScreen } from '../modules/security/Remediation';
import { VMListScreen, VMDetailScreen } from '../modules/infrastructure/VMScreens';
import { AssetCreateScreen } from '../modules/assets/AssetCreateScreen';
import { AssetDetailScreen } from '../modules/assets/AssetDetailScreen';
import { AssetsScreen, MyAssetsScreen } from '../modules/assets/AssetsScreen';
import { DevicesScreen } from '../modules/endpoints/DevicesScreen';
import { DeviceDetailScreen } from '../modules/endpoints/DeviceDetailScreen';
import { DeviceDiffScreen, GroupDiffScreen } from '../modules/endpoints/HistoryDiffScreens';
import { FindingsScreen } from '../modules/endpoints/FindingsScreen';
import {
  ManagementArtifactsScreen,
  ManagementArtifactDetailScreen,
  ManagementFiltersScreen,
  DirectoryGroupManagementScreen,
  UserManagementScreen,
} from '../modules/endpoints/ManagementScreens';
import { LedgerScreen } from '../modules/inventory/LedgerScreen';
import { ReceiptCreateScreen } from '../modules/inventory/ReceiptCreateScreen';
import { ReceiptsScreen } from '../modules/inventory/ReceiptsScreen';
import { ReservationsScreen } from '../modules/inventory/ReservationsScreen';
import { StockScreen } from '../modules/inventory/StockScreen';
import { WarehousesScreen } from '../modules/inventory/WarehousesScreen';
import { NeedsScreen } from '../modules/procurement/NeedsScreen';
import { OrderDetailScreen } from '../modules/procurement/OrderDetailScreen';
import { OrdersScreen } from '../modules/procurement/OrdersScreen';
import { SuppliersScreen } from '../modules/procurement/SuppliersScreen';
import { TicketCreateScreen } from '../modules/tickets/TicketCreateScreen';
import { TicketDetailScreen } from '../modules/tickets/TicketDetailScreen';
import { TicketsScreen } from '../modules/tickets/TicketsScreen';
import { ArticleDetailScreen } from '../modules/knowledge/ArticleDetailScreen';
import { ArticleEditScreen } from '../modules/knowledge/ArticleEditScreen';
import { ArticlesScreen } from '../modules/knowledge/ArticlesScreen';
import { IncidentDetailScreen } from '../modules/incidents/IncidentDetailScreen';
import { IncidentsScreen } from '../modules/incidents/IncidentsScreen';
import { ProblemDetailScreen } from '../modules/problems/ProblemDetailScreen';
import { ProblemsScreen } from '../modules/problems/ProblemsScreen';
import { RunbookDetailScreen } from '../modules/runbooks/RunbookDetailScreen';
import { RunbookEditScreen } from '../modules/runbooks/RunbookEditScreen';
import { RunbooksScreen } from '../modules/runbooks/RunbooksScreen';
import { MyWorkScreen } from '../modules/my-work/MyWorkScreen';
import { TaskCreateScreen } from '../modules/tasks/TaskCreateScreen';
import { TaskDetailScreen } from '../modules/tasks/TaskDetailScreen';
import { TasksScreen } from '../modules/tasks/TasksScreen';
import { DirectorySyncRunScreen } from '../modules/directory/DirectorySyncRunScreen';
import { DirectorySyncScreen } from '../modules/directory/DirectorySyncScreen';
import { I18nProvider, useI18n } from '../platform/i18n/I18nProvider';
import { matchRoute } from '../platform/router/routing';
import { navigate, useLocation } from '../platform/router/Router';
import { SessionProvider, useSession } from '../platform/session/SessionProvider';
import { ThemeProvider } from '../platform/theme/ThemeProvider';
import { Home } from './Home';
import { appRoutes, canViewRoute, type RouteId } from './routes';
import { Shell } from './Shell';
import { ForbiddenView, NotFoundView } from './StatusViews';

function renderScreen(id: RouteId, params: Record<string, string>): ReactNode {
  switch (id) {
    case 'home':
      return <Home />;
    case 'me':
      return <MeScreen />;
    case 'myWork':
      return <MyWorkScreen />;
    case 'briefing':
      return <BriefingFeedScreen />;
    case 'briefingItems':
      return <BriefingScreen />;
    case 'briefingNew':
      return <BriefingCreateScreen />;
    case 'briefingDetail':
      return <BriefingDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'notifications':
      return <NotificationsScreen />;
    case 'catalog':
      return <CatalogScreen />;
    case 'catalogRequest':
      return <RequestFormScreen key={params.id} id={params.id ?? ''} />;
    case 'requests':
      return <RequestsScreen scope="mine" />;
    case 'requestDetail':
      return <RequestDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'approvals':
      return <ApprovalsScreen />;
    case 'approvalDetail':
      return <ApprovalDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'products':
      return <ProductsScreen />;
    case 'catalogAdmin':
      return <CatalogAdminScreen />;
    case 'allRequests':
      return <RequestsScreen scope="all" />;
    case 'myAssets':
      return <MyAssetsScreen />;
    case 'devices':
      return <DevicesScreen />;
    case 'deviceManagementDiff':
      return <DeviceDiffScreen id={params.id ?? ''} />;
    case 'groupManagementDiff':
      return <GroupDiffScreen id={params.id ?? ''} />;
    case 'deviceDetail':
      return <DeviceDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'endpointFindings':
      return <FindingsScreen />;
    case 'managementArtifacts':
      return <ManagementArtifactsScreen />;
    case 'managementArtifactDetail':
      return <ManagementArtifactDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'endpointGroupManagement':
      return <DirectoryGroupManagementScreen key={params.id} id={params.id ?? ''} />;
    case 'endpointUserManagement':
      return <UserManagementScreen key={params.id} id={params.id ?? ''} />;
    case 'managementFilters':
      return <ManagementFiltersScreen />;
    case 'initiatives':
      return <InitiativesListScreen />;
    case 'myInitiatives':
      return <InitiativesListScreen mine />;
    case 'initiativeNew':
      return <InitiativeCreateScreen />;
    case 'initiativeDetail':
      return <InitiativeDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'maintenanceCalendar':
      return <MaintenanceCalendarScreen />;
    case 'changes':
      return <ChangesListScreen />;
    case 'myChanges':
      return <ChangesListScreen mine />;
    case 'changeNew':
      return <ChangeCreateScreen />;
    case 'changeDetail':
      return <ChangeDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'securityOverview':
      return <SecurityOverviewScreen />;
    case 'securityAdvisories':
      return <AdvisoriesScreen />;
    case 'securityAdvisoryNew':
      return <AdvisoryCreateScreen />;
    case 'securityAdvisoryDetail':
      return <AdvisoryDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'securityFindings':
      return <SecurityFindingsScreen />;
    case 'securityFindingDetail':
      return <SecurityFindingDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'services':
      return <ServicesListScreen />;
    case 'serviceDetail':
      return <ServiceDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'impact':
      return <ImpactScreen />;
    case 'infrastructureTree':
      return <SiteTreeScreen />;
    case 'infrastructureBuilding':
      return <BuildingScreen key={params.id} id={params.id ?? ''} />;
    case 'infrastructureRoom':
      return <RoomScreen key={params.id} id={params.id ?? ''} />;
    case 'infrastructureRack':
      return <RackScreen key={params.id} id={params.id ?? ''} />;
    case 'infrastructureVMs':
      return <VMListScreen />;
    case 'infrastructureVM':
      return <VMDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'assets':
      return <AssetsScreen />;
    case 'assetNew':
      return <AssetCreateScreen />;
    case 'assetDetail':
      return <AssetDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'stock':
      return <StockScreen />;
    case 'warehouses':
      return <WarehousesScreen />;
    case 'reservations':
      return <ReservationsScreen />;
    case 'ledger':
      return <LedgerScreen />;
    case 'receipts':
      return <ReceiptsScreen />;
    case 'receiptNew':
      return <ReceiptCreateScreen />;
    case 'orders':
      return <OrdersScreen />;
    case 'orderDetail':
      return <OrderDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'procurementRequests':
      return <NeedsScreen />;
    case 'suppliers':
      return <SuppliersScreen />;
    case 'myTickets':
      return <TicketsScreen scope="mine" />;
    case 'ticketNew':
      return <TicketCreateScreen />;
    case 'ticketDetail':
      return <TicketDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'ticketQueue':
      return <TicketsScreen scope="all" />;
    case 'knowledge':
      return <ArticlesScreen />;
    case 'articleNew':
      return <ArticleEditScreen />;
    case 'articleEdit':
      return <ArticleEditScreen key={params.id} id={params.id ?? ''} />;
    case 'articleDetail':
      return <ArticleDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'incidents':
      return <IncidentsScreen />;
    case 'incidentDetail':
      return <IncidentDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'problems':
      return <ProblemsScreen />;
    case 'problemDetail':
      return <ProblemDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'runbooks':
      return <RunbooksScreen />;
    case 'runbookNew':
      return <RunbookEditScreen />;
    case 'runbookEdit':
      return <RunbookEditScreen key={params.id} id={params.id ?? ''} />;
    case 'runbookDetail':
      return <RunbookDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'tasks':
      return <TasksScreen />;
    case 'taskNew':
      return <TaskCreateScreen />;
    case 'taskDetail':
      return <TaskDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'recurrence':
      return <RecurrenceScreen />;
    case 'recurrenceNew':
      return <DefinitionCreateScreen />;
    case 'recurrenceDetail':
      return <DefinitionDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'roles':
      return <RolesScreen />;
    case 'roleNew':
      return <RoleCreateScreen />;
    case 'roleDetail':
      return <RoleDetailScreen key={params.id} id={params.id ?? ''} />;
    case 'roleAssignments':
      return <RoleAssignmentsScreen />;
    case 'directorySync':
      return <DirectorySyncScreen />;
    case 'directorySyncRun':
      return <DirectorySyncRunScreen key={params.id} id={params.id ?? ''} />;
    case 'audit':
      return <AuditScreen />;
  }
}

function AuthenticatedApp() {
  const { t } = useI18n();
  const { can } = useSession();
  const { pathname } = useLocation();

  useEffect(() => {
    if (pathname === '/login') navigate('/', { replace: true });
  }, [pathname]);

  const match = matchRoute(appRoutes, pathname);
  if (!match) {
    return (
      <Shell title={t('notFound.title')}>
        <NotFoundView />
      </Shell>
    );
  }
  const allowed = canViewRoute(can, match.route);
  return (
    <Shell title={allowed ? t(match.route.titleKey) : t('forbidden.title')}>
      {allowed ? renderScreen(match.route.id, match.params) : <ForbiddenView />}
    </Shell>
  );
}

function Gate() {
  const { t } = useI18n();
  const { state } = useSession();
  useEffect(() => {
    if (state.status === 'anonymous') document.title = `${t('login.title')} – ${t('app.name')}`;
  }, [state.status, t]);
  if (state.status === 'loading') {
    return (
      <p className="boot" role="status">
        {t('state.loading')}
      </p>
    );
  }
  if (state.status === 'anonymous') return <LoginScreen />;
  return <AuthenticatedApp />;
}

export function App() {
  return (
    <ThemeProvider>
      <I18nProvider>
        <SessionProvider>
          <Gate />
        </SessionProvider>
      </I18nProvider>
    </ThemeProvider>
  );
}
