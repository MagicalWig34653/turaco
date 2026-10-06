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
    const group = shellNavigation(() => true).find(({ label }) => label === 'shell.serviceDesk');
    expect(group?.items.map(({ id }) => id)).toEqual(
      expect.arrayContaining(['ticketQueue', 'problems', 'runbooks']),
    );
    expect(group?.items.find(({ id }) => id === 'ticketQueue')?.pattern).toBe('/service-desk');
  });
});
