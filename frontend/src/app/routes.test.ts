import { describe, expect, it } from 'vitest';
import { matchRoute } from '../platform/router/routing';
import { createCan } from '../platform/session/permissions';
import { appRoutes, isNavActive, visibleNavItems } from './routes';

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

  it('marks parent entries active on detail paths', () => {
    expect(isNavActive('/admin/roles', '/admin/roles/42')).toBe(true);
    expect(isNavActive('/admin/roles', '/admin/role-assignments')).toBe(false);
    expect(isNavActive('/', '/me')).toBe(false);
  });
});
