import { describe, expect, it } from 'vitest';
import { matchRoute } from '../platform/router/routing';
import { createCan } from '../platform/session/permissions';
import { appRoutes, canViewRoute, isNavActive, visibleNavItems } from './routes';

const ids = (
  permissions: string[],
  group: 'main' | 'logistics' | 'endpoints' | 'infrastructure' | 'services' | 'changes' | 'admin',
) => visibleNavItems(createCan({ permissions }), group).map((route) => route.id);

describe('visibleNavItems', () => {
  it('always shows the main entries and hides admin entries without permission', () => {
    expect(ids([], 'main')).toEqual([
      'home',
      'me',
      'notifications',
      'catalog',
      'requests',
      'approvals',
      'myChanges',
      'myAssets',
      'myTickets',
      'knowledge',
      'incidents',
    ]);
    expect(ids([], 'admin')).toEqual([]);
  });

  it('filters admin entries by permission', () => {
    expect(ids(['platform.roles.view'], 'admin')).toEqual(['roles', 'roleAssignments']);
    expect(ids(['platform.audit.view'], 'admin')).toEqual(['audit']);
  });

  it('shows My Work and Tasks with any one task permission', () => {
    for (const permission of ['tasks.view', 'tasks.work', 'tasks.manage']) {
      expect(ids([permission], 'main')).toEqual([
        'home',
        'me',
        'myWork',
        'notifications',
        'catalog',
        'requests',
        'approvals',
        'myChanges',
        'myAssets',
        'myTickets',
        'knowledge',
        'incidents',
        'tasks',
      ]);
    }
    expect(ids(['organization.view'], 'main')).toEqual([
      'home',
      'me',
      'notifications',
      'catalog',
      'requests',
      'approvals',
      'myChanges',
      'myAssets',
      'myTickets',
      'knowledge',
      'incidents',
    ]);
  });

  it('shows the briefing with either briefing permission and creation only to managers', () => {
    expect(ids(['briefing.view'], 'main')).toEqual([
      'home',
      'me',
      'notifications',
      'catalog',
      'requests',
      'approvals',
      'briefing',
      'myChanges',
      'myAssets',
      'myTickets',
      'knowledge',
      'incidents',
    ]);
    expect(ids(['briefing.manage'], 'main')).toEqual([
      'home',
      'me',
      'notifications',
      'catalog',
      'requests',
      'approvals',
      'briefing',
      'myChanges',
      'myAssets',
      'myTickets',
      'knowledge',
      'incidents',
    ]);
    expect(matchRoute(appRoutes, '/briefing/new')?.route.id).toBe('briefingNew');
    expect(matchRoute(appRoutes, '/briefing/5')?.route.id).toBe('briefingDetail');
    const route = (id: string) => appRoutes.find((candidate) => candidate.id === id)!;
    expect(canViewRoute(createCan({ permissions: ['briefing.view'] }), route('briefingNew'))).toBe(
      false,
    );
    expect(
      canViewRoute(createCan({ permissions: ['briefing.manage'] }), route('briefingNew')),
    ).toBe(true);
  });

  it('shows recurring tasks only with tasks.recurrence.manage', () => {
    expect(ids(['tasks.manage'], 'admin')).toEqual([]);
    expect(ids(['tasks.recurrence.manage'], 'admin')).toEqual(['recurrence']);
    expect(matchRoute(appRoutes, '/admin/recurring-tasks/new')?.route.id).toBe('recurrenceNew');
    expect(matchRoute(appRoutes, '/admin/recurring-tasks/7')?.route.id).toBe('recurrenceDetail');
  });

  it('requires both directory permissions for directory sync', () => {
    expect(ids(['organization.directory.view'], 'admin')).toEqual([]);
    expect(ids(['organization.directory.view', 'organization.view'], 'admin')).toEqual([
      'directorySync',
    ]);
  });
});

describe('route table', () => {
  it('resolves detail routes and keeps "new" ahead of ":id"', () => {
    expect(matchRoute(appRoutes, '/admin/roles/new')?.route.id).toBe('roleNew');
    expect(matchRoute(appRoutes, '/admin/roles/42')?.route.id).toBe('roleDetail');
    expect(matchRoute(appRoutes, '/admin/directory-sync/9')?.params).toEqual({ id: '9' });
    expect(matchRoute(appRoutes, '/unknown')).toBeNull();
  });

  it('keeps /tasks/new ahead of /tasks/:id and gates creation on tasks.manage', () => {
    expect(matchRoute(appRoutes, '/tasks/new')?.route.id).toBe('taskNew');
    expect(matchRoute(appRoutes, '/tasks/42')?.route.id).toBe('taskDetail');
    const route = (id: string) => appRoutes.find((candidate) => candidate.id === id)!;
    expect(canViewRoute(createCan({ permissions: ['tasks.work'] }), route('taskNew'))).toBe(false);
    expect(canViewRoute(createCan({ permissions: ['tasks.manage'] }), route('taskNew'))).toBe(true);
    expect(canViewRoute(createCan({ permissions: ['tasks.work'] }), route('taskDetail'))).toBe(
      true,
    );
    expect(canViewRoute(createCan({ permissions: [] }), route('taskDetail'))).toBe(false);
  });

  it('marks parent entries active on detail paths', () => {
    expect(isNavActive('/admin/roles', '/admin/roles/42')).toBe(true);
    expect(isNavActive('/admin/roles', '/admin/role-assignments')).toBe(false);
    expect(isNavActive('/', '/me')).toBe(false);
  });
});

describe('catalog, requests and approvals', () => {
  it('are available to every signed-in user, admin entries need their permissions', () => {
    expect(ids([], 'main')).toEqual(expect.arrayContaining(['catalog', 'requests', 'approvals']));
    expect(ids(['products.view'], 'admin')).toEqual(['products']);
    expect(ids(['catalog.manage'], 'admin')).toEqual(['catalogAdmin']);
    expect(ids(['requests.manage'], 'admin')).toEqual(['allRequests']);
  });

  it('matches detail routes', () => {
    expect(matchRoute(appRoutes, '/catalog/7')?.route.id).toBe('catalogRequest');
    expect(matchRoute(appRoutes, '/requests/7')?.route.id).toBe('requestDetail');
    expect(matchRoute(appRoutes, '/approvals/7')?.route.id).toBe('approvalDetail');
    expect(matchRoute(appRoutes, '/admin/requests')?.route.id).toBe('allRequests');
  });
});

describe('equipment and procurement', () => {
  it('shows My equipment to everyone and the logistics group by permission', () => {
    expect(ids([], 'main')).toContain('myAssets');
    expect(ids([], 'logistics')).toEqual([]);
    expect(ids(['assets.view'], 'logistics')).toEqual(['assets']);
    expect(ids(['inventory.view'], 'logistics')).toEqual([
      'stock',
      'warehouses',
      'reservations',
      'ledger',
      'receipts',
    ]);
    expect(ids(['procurement.manage'], 'logistics')).toEqual([
      'orders',
      'procurementRequests',
      'suppliers',
    ]);
  });

  it('keeps creation screens behind the manage permissions', () => {
    const route = (id: string) => appRoutes.find((candidate) => candidate.id === id)!;
    expect(canViewRoute(createCan({ permissions: ['assets.view'] }), route('assetNew'))).toBe(
      false,
    );
    expect(canViewRoute(createCan({ permissions: ['assets.manage'] }), route('assetNew'))).toBe(
      true,
    );
    expect(canViewRoute(createCan({ permissions: ['inventory.view'] }), route('receiptNew'))).toBe(
      false,
    );
    expect(
      canViewRoute(createCan({ permissions: ['inventory.manage'] }), route('receiptNew')),
    ).toBe(true);
  });

  it('matches static routes before parameterised ones', () => {
    expect(matchRoute(appRoutes, '/assets/new')?.route.id).toBe('assetNew');
    expect(matchRoute(appRoutes, '/assets/7')?.route.id).toBe('assetDetail');
    expect(matchRoute(appRoutes, '/inventory/receipts/new')?.route.id).toBe('receiptNew');
    expect(matchRoute(appRoutes, '/procurement/orders/7')?.route.id).toBe('orderDetail');
  });
});

describe('support', () => {
  it('offers My tickets to everyone and the queue to ticket staff', () => {
    expect(ids([], 'main')).toContain('myTickets');
    expect(ids([], 'logistics')).toEqual([]);
    expect(ids(['tickets.view'], 'logistics')).toEqual(['ticketQueue', 'problems']);
    expect(ids(['tickets.manage'], 'logistics')).toEqual(['ticketQueue', 'problems']);
  });

  it('matches the static ticket route before the parameterised one', () => {
    expect(matchRoute(appRoutes, '/support/new')?.route.id).toBe('ticketNew');
    expect(matchRoute(appRoutes, '/support/7')?.route.id).toBe('ticketDetail');
  });
});

describe('knowledge', () => {
  it('lists articles for everyone and keeps authoring behind knowledge.manage', () => {
    expect(ids([], 'main')).toContain('knowledge');
    const route = (id: string) => appRoutes.find((candidate) => candidate.id === id)!;
    expect(canViewRoute(createCan({ permissions: ['knowledge.view'] }), route('articleNew'))).toBe(
      false,
    );
    expect(
      canViewRoute(createCan({ permissions: ['knowledge.manage'] }), route('articleEdit')),
    ).toBe(true);
    expect(matchRoute(appRoutes, '/knowledge/new')?.route.id).toBe('articleNew');
    expect(matchRoute(appRoutes, '/knowledge/7/edit')?.route.id).toBe('articleEdit');
    expect(matchRoute(appRoutes, '/knowledge/7')?.route.id).toBe('articleDetail');
  });
});

describe('incidents', () => {
  it('shows known issues to everyone and matches the detail route', () => {
    expect(ids([], 'main')).toContain('incidents');
    expect(matchRoute(appRoutes, '/incidents/7')?.route.id).toBe('incidentDetail');
  });
});

describe('problems', () => {
  it('shows problems to ticket staff and matches the detail route', () => {
    expect(ids([], 'logistics')).toEqual([]);
    expect(ids(['tickets.view'], 'logistics')).toContain('problems');
    expect(ids(['problems.manage'], 'logistics')).toEqual(['problems']);
    expect(matchRoute(appRoutes, '/problems/7')?.route.id).toBe('problemDetail');
  });
});

describe('runbooks', () => {
  it('shows runbooks to readers and executors, authoring only to knowledge.manage', () => {
    expect(ids([], 'logistics')).not.toContain('runbooks');
    expect(ids(['knowledge.view'], 'logistics')).toContain('runbooks');
    expect(ids(['runbooks.execute'], 'logistics')).toContain('runbooks');
    const route = (id: string) => appRoutes.find((candidate) => candidate.id === id)!;
    expect(
      canViewRoute(createCan({ permissions: ['runbooks.execute'] }), route('runbookNew')),
    ).toBe(false);
    expect(matchRoute(appRoutes, '/runbooks/new')?.route.id).toBe('runbookNew');
    expect(matchRoute(appRoutes, '/runbooks/7/edit')?.route.id).toBe('runbookEdit');
    expect(matchRoute(appRoutes, '/runbooks/7')?.route.id).toBe('runbookDetail');
  });
});

describe('endpoints', () => {
  it('gates the nav and all routes by endpoint permission', () => {
    expect(ids([], 'endpoints')).toEqual([]);
    for (const permission of ['endpoints.view', 'endpoints.manage']) {
      expect(ids([permission], 'endpoints')).toEqual(
        permission === 'endpoints.manage'
          ? ['devices', 'endpointFindings', 'managementArtifacts', 'managementFilters']
          : ['devices', 'endpointFindings'],
      );
      for (const id of ['devices', 'deviceDetail', 'endpointFindings']) {
        const route = appRoutes.find((candidate) => candidate.id === id)!;
        expect(canViewRoute(createCan({ permissions: [permission] }), route)).toBe(true);
      }
    }
    expect(matchRoute(appRoutes, '/devices/7')?.route.id).toBe('deviceDetail');
    expect(matchRoute(appRoutes, '/endpoint-findings')?.route.id).toBe('endpointFindings');
    expect(ids(['endpoint.management.view'], 'endpoints')).toEqual([
      'managementArtifacts',
      'managementFilters',
    ]);
    for (const id of ['managementArtifacts', 'managementArtifactDetail', 'managementFilters']) {
      const route = appRoutes.find((candidate) => candidate.id === id)!;
      expect(canViewRoute(createCan({ permissions: ['endpoint.management.view'] }), route)).toBe(
        true,
      );
      expect(canViewRoute(createCan({ permissions: ['endpoints.view'] }), route)).toBe(false);
    }
    expect(matchRoute(appRoutes, '/management-artifacts/7')?.route.id).toBe(
      'managementArtifactDetail',
    );
    expect(matchRoute(appRoutes, '/endpoints/devices/7/diff')?.route.id).toBe(
      'deviceManagementDiff',
    );
    expect(matchRoute(appRoutes, '/endpoints/groups/7/diff')?.route.id).toBe('groupManagementDiff');
    const deviceDiff = appRoutes.find((route) => route.id === 'deviceManagementDiff')!;
    expect(
      canViewRoute(
        createCan({ permissions: ['endpoints.view', 'endpoint.management.view'] }),
        deviceDiff,
      ),
    ).toBe(true);
    expect(canViewRoute(createCan({ permissions: ['endpoints.manage'] }), deviceDiff)).toBe(true);
    expect(canViewRoute(createCan({ permissions: ['endpoint.management.view'] }), deviceDiff)).toBe(
      false,
    );
    const groupDiff = appRoutes.find((route) => route.id === 'groupManagementDiff')!;
    expect(
      canViewRoute(
        createCan({ permissions: ['organization.directory.view', 'endpoint.management.view'] }),
        groupDiff,
      ),
    ).toBe(true);
    expect(canViewRoute(createCan({ permissions: ['endpoint.management.view'] }), groupDiff)).toBe(
      false,
    );
  });
});

describe('infrastructure routes', () => {
  it('shows navigation with view or manage permission', () => {
    expect(ids([], 'infrastructure')).toEqual([]);
    expect(ids(['infrastructure.view'], 'infrastructure')).toEqual([
      'infrastructureTree',
      'infrastructureVMs',
    ]);
    expect(ids(['infrastructure.manage'], 'infrastructure')).toEqual([
      'infrastructureTree',
      'infrastructureVMs',
    ]);
  });
});

describe('services routes', () => {
  it('gates navigation and matches detail and impact routes', () => {
    expect(ids([], 'services')).toEqual([]);
    expect(ids(['services.view'], 'services')).toEqual(['services']);
    expect(ids(['services.manage'], 'services')).toEqual(['services']);
    expect(matchRoute(appRoutes, '/services/abc')?.route.id).toBe('serviceDetail');
    expect(matchRoute(appRoutes, '/impact')?.route.id).toBe('impact');
  });
});

describe('changes routes', () => {
  it('gates navigation while own changes remain reachable', () => {
    expect(ids([], 'changes')).toEqual([]);
    expect(ids(['changes.view'], 'changes')).toEqual(['changes']);
    expect(ids(['changes.manage'], 'changes')).toEqual(['changes']);
    expect(ids(['changes.execute'], 'changes')).toEqual(['changes']);
    expect(matchRoute(appRoutes, '/changes/mine')?.route.id).toBe('myChanges');
    expect(matchRoute(appRoutes, '/changes/new')?.route.id).toBe('changeNew');
    expect(matchRoute(appRoutes, '/changes/abc')?.route.id).toBe('changeDetail');
  });
});
