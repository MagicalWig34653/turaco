import type { MessageKey } from '../platform/i18n/i18n';
import { canAll, type CanFn } from '../platform/session/permissions';

export type RouteId =
  | 'home'
  | 'me'
  | 'myWork'
  | 'notifications'
  | 'briefing'
  | 'briefingNew'
  | 'briefingDetail'
  | 'catalog'
  | 'catalogRequest'
  | 'requests'
  | 'requestDetail'
  | 'approvals'
  | 'approvalDetail'
  | 'products'
  | 'catalogAdmin'
  | 'allRequests'
  | 'myAssets'
  | 'assets'
  | 'devices'
  | 'deviceDetail'
  | 'deviceManagementDiff'
  | 'groupManagementDiff'
  | 'endpointFindings'
  | 'managementArtifacts'
  | 'managementArtifactDetail'
  | 'managementFilters'
  | 'endpointGroupManagement'
  | 'endpointUserManagement'
  | 'assetNew'
  | 'assetDetail'
  | 'stock'
  | 'warehouses'
  | 'reservations'
  | 'ledger'
  | 'receipts'
  | 'receiptNew'
  | 'orders'
  | 'orderDetail'
  | 'procurementRequests'
  | 'suppliers'
  | 'myTickets'
  | 'ticketNew'
  | 'ticketDetail'
  | 'ticketQueue'
  | 'knowledge'
  | 'articleNew'
  | 'articleEdit'
  | 'articleDetail'
  | 'incidents'
  | 'incidentDetail'
  | 'problems'
  | 'problemDetail'
  | 'runbooks'
  | 'runbookNew'
  | 'runbookEdit'
  | 'runbookDetail'
  | 'tasks'
  | 'taskNew'
  | 'taskDetail'
  | 'recurrence'
  | 'recurrenceNew'
  | 'recurrenceDetail'
  | 'roles'
  | 'roleNew'
  | 'roleDetail'
  | 'roleAssignments'
  | 'directorySync'
  | 'directorySyncRun'
  | 'audit';

export type NavGroup = 'main' | 'logistics' | 'endpoints' | 'admin';

export type AppRoute = {
  id: RouteId;
  pattern: string;
  titleKey: MessageKey;
  /** Permissions required to see the screen (UI hiding only; the backend enforces). */
  requires?: readonly string[];
  /** Alternative to `requires`: the screen is visible with at least one of these permissions. */
  requiresAny?: readonly string[];
  nav?: NavGroup;
};

const taskViewPermissions = ['tasks.view', 'tasks.manage', 'tasks.work'] as const;

// Static patterns must precede parameterised ones ("/admin/roles/new" before "/admin/roles/:id").
export const appRoutes: readonly AppRoute[] = [
  { id: 'home', pattern: '/', titleKey: 'nav.home', nav: 'main' },
  { id: 'me', pattern: '/me', titleKey: 'nav.me', nav: 'main' },
  {
    id: 'myWork',
    pattern: '/my-work',
    titleKey: 'nav.myWork',
    requiresAny: taskViewPermissions,
    nav: 'main',
  },
  { id: 'notifications', pattern: '/notifications', titleKey: 'nav.notifications', nav: 'main' },
  { id: 'catalog', pattern: '/catalog', titleKey: 'nav.catalog', nav: 'main' },
  { id: 'catalogRequest', pattern: '/catalog/:id', titleKey: 'nav.catalog' },
  { id: 'requests', pattern: '/requests', titleKey: 'nav.myRequests', nav: 'main' },
  { id: 'requestDetail', pattern: '/requests/:id', titleKey: 'nav.myRequests' },
  { id: 'approvals', pattern: '/approvals', titleKey: 'nav.approvals', nav: 'main' },
  { id: 'approvalDetail', pattern: '/approvals/:id', titleKey: 'nav.approvals' },
  {
    id: 'briefing',
    pattern: '/briefing',
    titleKey: 'nav.briefing',
    requiresAny: ['briefing.view', 'briefing.manage'],
    nav: 'main',
  },
  {
    id: 'briefingNew',
    pattern: '/briefing/new',
    titleKey: 'briefing.create.title',
    requires: ['briefing.manage'],
  },
  {
    id: 'briefingDetail',
    pattern: '/briefing/:id',
    titleKey: 'briefing.detail.title',
    requiresAny: ['briefing.view', 'briefing.manage'],
  },
  {
    id: 'devices',
    pattern: '/devices',
    titleKey: 'nav.devices',
    requiresAny: ['endpoints.view', 'endpoints.manage'],
    nav: 'endpoints',
  },
  {
    id: 'deviceManagementDiff',
    pattern: '/endpoints/devices/:id/diff',
    titleKey: 'management.compareDevices',
    requiresAny: ['endpoint.management.view', 'endpoints.manage'],
  },
  {
    id: 'groupManagementDiff',
    pattern: '/endpoints/groups/:id/diff',
    titleKey: 'management.compareGroups',
    requires: ['organization.directory.view'],
    requiresAny: ['endpoint.management.view', 'endpoints.manage'],
  },
  {
    id: 'deviceDetail',
    pattern: '/devices/:id',
    titleKey: 'endpoints.detailTitle',
    requiresAny: ['endpoints.view', 'endpoints.manage'],
  },
  {
    id: 'endpointFindings',
    pattern: '/endpoint-findings',
    titleKey: 'nav.endpointFindings',
    requiresAny: ['endpoints.view', 'endpoints.manage'],
    nav: 'endpoints',
  },
  {
    id: 'managementArtifacts',
    pattern: '/management-artifacts',
    titleKey: 'nav.managementArtifacts',
    requiresAny: ['endpoint.management.view', 'endpoints.manage'],
    nav: 'endpoints',
  },
  {
    id: 'managementArtifactDetail',
    pattern: '/management-artifacts/:id',
    titleKey: 'nav.managementArtifacts',
    requiresAny: ['endpoint.management.view', 'endpoints.manage'],
  },
  {
    id: 'managementFilters',
    pattern: '/management-filters',
    titleKey: 'nav.managementFilters',
    requiresAny: ['endpoint.management.view', 'endpoints.manage'],
    nav: 'endpoints',
  },
  {
    id: 'endpointGroupManagement',
    pattern: '/endpoints/groups/:id',
    titleKey: 'management.groupPage',
    requires: ['organization.directory.view'],
    requiresAny: ['endpoint.management.view', 'endpoints.manage'],
  },
  {
    id: 'endpointUserManagement',
    pattern: '/endpoints/users/:id',
    titleKey: 'management.userPage',
    requires: ['organization.directory.view'],
    requiresAny: ['endpoint.management.view', 'endpoints.manage'],
  },
  { id: 'myAssets', pattern: '/my-assets', titleKey: 'nav.myAssets', nav: 'main' },
  {
    id: 'assets',
    pattern: '/assets',
    titleKey: 'nav.assets',
    requiresAny: ['assets.view', 'assets.manage'],
    nav: 'logistics',
  },
  {
    id: 'assetNew',
    pattern: '/assets/new',
    titleKey: 'assets.create.title',
    requires: ['assets.manage'],
  },
  // A holder reads the asset assigned to them: the server decides (404 otherwise).
  { id: 'assetDetail', pattern: '/assets/:id', titleKey: 'assets.detail.title' },
  {
    id: 'stock',
    pattern: '/inventory',
    titleKey: 'nav.stock',
    requiresAny: ['inventory.view', 'inventory.manage'],
    nav: 'logistics',
  },
  {
    id: 'warehouses',
    pattern: '/inventory/warehouses',
    titleKey: 'nav.warehouses',
    requiresAny: ['inventory.view', 'inventory.manage'],
    nav: 'logistics',
  },
  {
    id: 'reservations',
    pattern: '/inventory/reservations',
    titleKey: 'nav.reservations',
    requiresAny: ['inventory.view', 'inventory.manage'],
    nav: 'logistics',
  },
  {
    id: 'ledger',
    pattern: '/inventory/ledger',
    titleKey: 'nav.ledger',
    requiresAny: ['inventory.view', 'inventory.manage'],
    nav: 'logistics',
  },
  {
    id: 'receipts',
    pattern: '/inventory/receipts',
    titleKey: 'nav.receipts',
    requiresAny: ['inventory.view', 'inventory.manage'],
    nav: 'logistics',
  },
  {
    id: 'receiptNew',
    pattern: '/inventory/receipts/new',
    titleKey: 'inventory.receipt.new',
    requires: ['inventory.manage'],
  },
  {
    id: 'orders',
    pattern: '/procurement/orders',
    titleKey: 'nav.purchaseOrders',
    requiresAny: ['procurement.view', 'procurement.manage'],
    nav: 'logistics',
  },
  {
    id: 'orderDetail',
    pattern: '/procurement/orders/:id',
    titleKey: 'nav.purchaseOrders',
    requiresAny: ['procurement.view', 'procurement.manage'],
  },
  {
    id: 'procurementRequests',
    pattern: '/procurement/requests',
    titleKey: 'nav.procurementRequests',
    requiresAny: ['procurement.view', 'procurement.manage'],
    nav: 'logistics',
  },
  {
    id: 'suppliers',
    pattern: '/procurement/suppliers',
    titleKey: 'nav.suppliers',
    requiresAny: ['procurement.view', 'procurement.manage'],
    nav: 'logistics',
  },
  { id: 'myTickets', pattern: '/support', titleKey: 'nav.myTickets', nav: 'main' },
  { id: 'ticketNew', pattern: '/support/new', titleKey: 'tickets.create.title' },
  // The server decides who may read a ticket (reporter, affected user, tickets.view).
  { id: 'ticketDetail', pattern: '/support/:id', titleKey: 'tickets.detail.title' },
  {
    id: 'ticketQueue',
    pattern: '/service-desk',
    titleKey: 'nav.ticketQueue',
    requiresAny: ['tickets.view', 'tickets.manage'],
    nav: 'logistics',
  },
  { id: 'knowledge', pattern: '/knowledge', titleKey: 'nav.knowledge', nav: 'main' },
  {
    id: 'articleNew',
    pattern: '/knowledge/new',
    titleKey: 'knowledge.new',
    requires: ['knowledge.manage'],
  },
  {
    id: 'articleEdit',
    pattern: '/knowledge/:id/edit',
    titleKey: 'knowledge.edit',
    requires: ['knowledge.manage'],
  },
  { id: 'articleDetail', pattern: '/knowledge/:id', titleKey: 'knowledge.detail.title' },
  { id: 'incidents', pattern: '/incidents', titleKey: 'nav.incidents', nav: 'main' },
  { id: 'incidentDetail', pattern: '/incidents/:id', titleKey: 'incidents.detail.title' },
  {
    id: 'problems',
    pattern: '/problems',
    titleKey: 'nav.problems',
    requiresAny: ['tickets.view', 'tickets.manage', 'problems.manage'],
    nav: 'logistics',
  },
  {
    id: 'problemDetail',
    pattern: '/problems/:id',
    titleKey: 'problems.detail.title',
    requiresAny: ['tickets.view', 'tickets.manage', 'problems.manage'],
  },
  {
    id: 'runbooks',
    pattern: '/runbooks',
    titleKey: 'nav.runbooks',
    requiresAny: ['knowledge.view', 'knowledge.manage', 'runbooks.execute'],
    nav: 'logistics',
  },
  {
    id: 'runbookNew',
    pattern: '/runbooks/new',
    titleKey: 'runbooks.new',
    requires: ['knowledge.manage'],
  },
  {
    id: 'runbookEdit',
    pattern: '/runbooks/:id/edit',
    titleKey: 'runbooks.edit',
    requires: ['knowledge.manage'],
  },
  {
    id: 'runbookDetail',
    pattern: '/runbooks/:id',
    titleKey: 'runbooks.detail.title',
    requiresAny: ['knowledge.view', 'knowledge.manage', 'runbooks.execute'],
  },
  {
    id: 'tasks',
    pattern: '/tasks',
    titleKey: 'nav.tasks',
    requiresAny: taskViewPermissions,
    nav: 'main',
  },
  {
    id: 'taskNew',
    pattern: '/tasks/new',
    titleKey: 'tasks.create.title',
    requires: ['tasks.manage'],
  },
  {
    id: 'taskDetail',
    pattern: '/tasks/:id',
    titleKey: 'tasks.detail.title',
    requiresAny: taskViewPermissions,
  },
  {
    id: 'recurrence',
    pattern: '/admin/recurring-tasks',
    titleKey: 'nav.recurrence',
    requires: ['tasks.recurrence.manage'],
    nav: 'admin',
  },
  {
    id: 'recurrenceNew',
    pattern: '/admin/recurring-tasks/new',
    titleKey: 'recurrence.create.title',
    requires: ['tasks.recurrence.manage'],
  },
  {
    id: 'recurrenceDetail',
    pattern: '/admin/recurring-tasks/:id',
    titleKey: 'recurrence.detail.title',
    requires: ['tasks.recurrence.manage'],
  },
  {
    id: 'products',
    pattern: '/admin/products',
    titleKey: 'nav.products',
    requiresAny: ['products.view', 'products.manage'],
    nav: 'admin',
  },
  {
    id: 'catalogAdmin',
    pattern: '/admin/catalog',
    titleKey: 'nav.catalogAdmin',
    requires: ['catalog.manage'],
    nav: 'admin',
  },
  {
    id: 'allRequests',
    pattern: '/admin/requests',
    titleKey: 'nav.allRequests',
    requiresAny: ['requests.view', 'requests.manage'],
    nav: 'admin',
  },
  {
    id: 'roles',
    pattern: '/admin/roles',
    titleKey: 'nav.roles',
    requires: ['platform.roles.view'],
    nav: 'admin',
  },
  {
    id: 'roleNew',
    pattern: '/admin/roles/new',
    titleKey: 'roles.create.title',
    requires: ['platform.roles.manage', 'platform.roles.view'],
  },
  {
    id: 'roleDetail',
    pattern: '/admin/roles/:id',
    titleKey: 'roles.detail.title',
    requires: ['platform.roles.view'],
  },
  {
    id: 'roleAssignments',
    pattern: '/admin/role-assignments',
    titleKey: 'nav.roleAssignments',
    requires: ['platform.roles.view'],
    nav: 'admin',
  },
  {
    id: 'directorySync',
    pattern: '/admin/directory-sync',
    titleKey: 'nav.directorySync',
    requires: ['organization.directory.view', 'organization.view'],
    nav: 'admin',
  },
  {
    id: 'directorySyncRun',
    pattern: '/admin/directory-sync/:id',
    titleKey: 'sync.detail.title',
    requires: ['organization.directory.view', 'organization.view'],
  },
  {
    id: 'audit',
    pattern: '/admin/audit',
    titleKey: 'nav.audit',
    requires: ['platform.audit.view'],
    nav: 'admin',
  },
];

/** UI hiding only: all of `requires` and, when set, at least one of `requiresAny`. */
export function canViewRoute(can: CanFn, route: AppRoute): boolean {
  if (!canAll(can, route.requires)) return false;
  if (route.id === 'deviceManagementDiff' && !can('endpoints.view') && !can('endpoints.manage'))
    return false;
  return !route.requiresAny || route.requiresAny.some((permission) => can(permission));
}

/** Navigation entries the current user may see, in declaration order. */
export function visibleNavItems(can: CanFn, group: NavGroup): AppRoute[] {
  return appRoutes.filter((route) => route.nav === group && canViewRoute(can, route));
}

/** A nav entry is active for its own path and every sub-path (detail screens). */
export function isNavActive(pattern: string, pathname: string): boolean {
  if (pattern === '/') return pathname === '/';
  return pathname === pattern || pathname.startsWith(`${pattern}/`);
}
