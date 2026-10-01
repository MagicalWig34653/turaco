import { describe, expect, it } from 'vitest';
import { buildPath, matchRoute, normalizePath } from './routing';

const routes = [
  { pattern: '/', id: 'home' },
  { pattern: '/admin/roles', id: 'roles' },
  { pattern: '/admin/roles/new', id: 'new' },
  { pattern: '/admin/roles/:id', id: 'detail' },
];

describe('normalizePath', () => {
  it('handles slashes', () => {
    expect(normalizePath('')).toBe('/');
    expect(normalizePath('/admin//roles/')).toBe('/admin/roles');
  });
});

describe('matchRoute', () => {
  it('matches static routes and the root', () => {
    expect(matchRoute(routes, '/')?.route.id).toBe('home');
    expect(matchRoute(routes, '/admin/roles/')?.route.id).toBe('roles');
  });
  it('prefers static over parameterised routes by order', () => {
    expect(matchRoute(routes, '/admin/roles/new')?.route.id).toBe('new');
  });
  it('captures and decodes parameters', () => {
    const match = matchRoute(routes, '/admin/roles/a%20b');
    expect(match?.route.id).toBe('detail');
    expect(match?.params).toEqual({ id: 'a b' });
  });
  it('returns null for unknown paths and malformed encoding', () => {
    expect(matchRoute(routes, '/nope')).toBeNull();
    expect(matchRoute(routes, '/admin/roles/a/b')).toBeNull();
    expect(matchRoute(routes, '/admin/roles/%E0%A4%A')).toBeNull();
  });
});

describe('buildPath', () => {
  it('encodes parameters', () => {
    expect(buildPath('/admin/roles/:id', { id: 'a/b' })).toBe('/admin/roles/a%2Fb');
  });
});
