import { describe, expect, it } from 'vitest';
import { appRoutes, canViewRoute } from '../../app/routes';
import { shellNavigation } from '../../app/shellNavigation';
import { navigationCommands } from '../ui/shell/paletteCommands';
import { toApiError } from '../api/client';
import { en } from '../i18n/messages.en';
import { de } from '../i18n/messages.de';
import {
  categories,
  filterModules,
  moduleForPath,
  pathEnabled,
  reasonCodes,
  settingsPath,
  statusEnabled,
  switchAction,
  switchRequest,
  type Module,
} from './model';
const module: Module = {
  key: 'presence',
  nameKey: 'modules.presence.name',
  descriptionKey: 'modules.presence.description',
  category: 'workforce',
  core: false,
  enabled: false,
  switchOn: true,
  state: 'blocked',
  blockedReason: 'runtime_setting_off',
  requires: [],
  requiredBy: [],
  startupGates: ['PRESENCE_ENABLED'],
  version: 0,
};
describe('module visibility', () => {
  it('fails closed for unknown and disabled modules', () => {
    expect(statusEnabled(undefined)('assets')).toBe(false);
    const enabled = statusEnabled([
      { key: 'assets', enabled: false },
      { key: 'inventory', enabled: true },
    ]);
    expect(enabled('assets')).toBe(false);
    expect(enabled('inventory')).toBe(true);
    expect(enabled('new-module')).toBe(false);
    expect(pathEnabled('/assets/123?tab=history', enabled)).toBe(false);
    expect(pathEnabled('/admin/modules', enabled)).toBe(true);
  });
  it('maps detail routes and settings without prefix collisions', () => {
    expect(moduleForPath('/software/versions/new')).toBe('endpoints');
    expect(moduleForPath('/admin/catalog')).toBe('catalog');
    expect(moduleForPath('/maintenance-calendar')).toBe('planning');
    expect(moduleForPath('/assets-other')).toBeUndefined();
    expect(moduleForPath('/admin/presence')).toBeUndefined();
    expect(moduleForPath('/admin/ai')).toBeUndefined();
  });
  it('gates permissionless routes, navigation and palette together', () => {
    const enabled = () => false;
    const route = appRoutes.find((r) => r.id === 'myAssets')!;
    expect(canViewRoute(() => true, route, enabled)).toBe(false);
    expect(
      shellNavigation(() => true, enabled)
        .flatMap((g) => g.items)
        .some((r) => r.id === route.id),
    ).toBe(false);
    expect(
      navigationCommands(
        appRoutes,
        () => true,
        (r) => r.titleKey,
        enabled,
      ).some((r) => r.id === route.id),
    ).toBe(false);
    expect(
      canViewRoute(
        () => false,
        appRoutes.find((r) => r.id === 'modulesAdmin')!,
        enabled,
      ),
    ).toBe(false);
    expect(
      canViewRoute(
        (p) => p === 'modules.manage',
        appRoutes.find((r) => r.id === 'modulesAdmin')!,
        enabled,
      ),
    ).toBe(true);
  });
});
describe('switch model', () => {
  it('uses the actual switch position for blocked modules and preserves version zero', () => {
    expect(switchAction(module)).toBe('disable');
    expect(switchAction({ ...module, switchOn: false })).toBe('enable');
    expect(switchRequest(module, 'compliance_review')).toEqual({
      expectedVersion: 0,
      reasonCode: 'compliance_review',
    });
    expect(switchRequest({ ...module, version: 7 }, 'maintenance').expectedVersion).toBe(7);
  });
  it('combines localized search with category and effective state', () => {
    expect(
      filterModules([module], ' ANWESENHEIT ', 'workforce', 'blocked', () => 'Anwesenheit'),
    ).toEqual([module]);
    expect(filterModules([module], '', 'insight', '', () => '')).toEqual([]);
    expect(filterModules([module], '', '', 'enabled', () => '')).toEqual([]);
    expect(settingsPath('presence')).toBe('/admin/presence');
    expect(settingsPath('ai')).toBe('/admin/ai');
    expect(settingsPath('assets')).toBeUndefined();
  });
  it.each(['requires_disabled', 'required_by_enabled', 'blocked'])(
    'retains validated blockers for %s',
    async (code) => {
      const error = await toApiError(
        new Response(
          JSON.stringify({
            error: {
              code: `platform.modules.${code}`,
              message: 'Conflict',
              blockers: ['assets', 12, null, 'changes'],
            },
          }),
          { status: 409 },
        ),
      );
      expect(error.blockers).toEqual(['assets', 'changes']);
      expect(error.code).toBe(`platform.modules.${code}`);
    },
  );
  it('does not accept malformed blocker data', async () => {
    const error = await toApiError(
      new Response(JSON.stringify({ error: { blockers: 'assets' } }), { status: 409 }),
    );
    expect(error.blockers).toEqual([]);
  });
  it('translates the complete catalog and all finite key families in both languages', () => {
    const keys =
      'platform access organization audit tasks approvals notifications servicedesk knowledge catalog requests products assets procurement inventory endpoints security remoteaccess infrastructure services changes planning presence briefing ai'.split(
        ' ',
      );
    const blocked =
      'startup_gate_off dpia_not_recorded no_enabled_provider no_providers_configured dependency_disabled runtime_setting_off'.split(
        ' ',
      );
    const messages = [
      ...keys.flatMap((key) => [`modules.${key}.name`, `modules.${key}.description`]),
      ...categories.map((key) => `modules.category.${key}`),
      ...reasonCodes.map((key) => `modules.reason.${key}`),
      ...blocked.map((key) => `modules.blocked.${key}`),
    ];
    for (const catalog of [en, de])
      for (const key of messages)
        expect((catalog as Record<string, string>)[key], key).toBeTruthy();
  });
});
