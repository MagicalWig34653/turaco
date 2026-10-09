import { describe, expect, it } from 'vitest';
import { appRoutes, canViewRoute } from './routes';
import { shellNavigation } from './shellNavigation';

describe('shell navigation presentation', () => {
  it.each(
    [[], ['tasks.work'], ['tickets.manage'], ['endpoints.view', 'services.view']].map(
      (permissions) => ({ permissions }),
    ),
  )('keeps the exact authorized navigation population for %j', ({ permissions }) => {
    const can = (permission: string) => permissions.includes(permission);
    const result = shellNavigation(can).flatMap((group) => group.items);
    expect(result.map((route) => route.id).sort()).toEqual(
      appRoutes
        .filter((route) => route.nav && canViewRoute(can, route))
        .map((route) => route.id)
        .sort(),
    );
    expect(new Set(result.map((route) => route.id)).size).toBe(result.length);
  });
  it('puts ticket operations together without changing their routes', () => {
    const group = shellNavigation(() => true).find(({ key }) => key === 'service_desk');
    expect(group?.items.map(({ id }) => id)).toEqual(
      expect.arrayContaining(['ticketQueue', 'problems', 'incidents']),
    );
    expect(group?.items.find(({ id }) => id === 'ticketQueue')?.pattern).toBe('/service-desk');
    expect(group?.items.map(({ id }) => id)).not.toContain('runbooks');
  });
  it('places every navigable route in exactly one section', () => {
    const sections = shellNavigation(() => true);
    const placed = sections.flatMap((section) => section.items.map((route) => route.id));
    expect(placed.length).toBe(new Set(placed).size);
    expect(placed.length).toBe(appRoutes.filter((route) => route.nav).length);
    expect(sections.map(({ key }) => key)).toEqual([
      'work',
      'service_desk',
      'assets',
      'infrastructure',
      'security',
      'knowledge',
      'people',
      'admin',
    ]);
  });
  it('omits sections without any visible destination', () => {
    expect(shellNavigation(() => false).every(({ items }) => items.length > 0)).toBe(true);
    expect(shellNavigation((p) => p === 'security.view').map(({ key }) => key)).not.toContain(
      'admin',
    );
  });
});
