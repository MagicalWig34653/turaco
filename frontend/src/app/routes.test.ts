import { describe, expect, it } from 'vitest';
import { matchRoute } from '../platform/router/routing';
import { createCan } from '../platform/session/permissions';
import { appRoutes, canViewRoute, isNavActive, visibleNavItems } from './routes';

const ids = (permissions: string[], group: 'main' | 'admin') =>
  visibleNavItems(createCan({ permissions }), group).map((route) => route.id);

describe('visibleNavItems', () => {
  it('always shows the main entries and hides admin entries without permission', () => {
    expect(ids([], 'main')).toEqual(['home', 'me']);
    expect(ids([], 'admin')).toEqual([]);
  });

  it('filters admin entries by permission', () => {
    expect(ids(['platform.roles.view'], 'admin')).toEqual(['roles', 'roleAssignments']);
    expect(ids(['platform.audit.view'], 'admin')).toEqual(['audit']);
  });

  it('shows My Work and Tasks with any one task permission', () => {
    for (const permission of ['tasks.view', 'tasks.work', 'tasks.manage']) {
      expect(ids([permission], 'main')).toEqual(['home', 'me', 'myWork', 'tasks']);
    }
    expect(ids(['organization.view'], 'main')).toEqual(['home', 'me']);
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
