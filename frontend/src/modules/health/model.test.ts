import { describe, expect, it } from 'vitest';
import { de } from '../../platform/i18n/messages.de';
import { en } from '../../platform/i18n/messages.en';
import {
  errorTextKey,
  expectedVersion,
  healthStatuses,
  needsAttention,
  orderedItems,
  resolveAppRoute,
  setupActions,
  setupTone,
  settledCount,
  statusCounts,
  statusKey,
  statusTone,
} from './model';
import type { SetupItem } from './types';

const item = (patch: Partial<SetupItem>): SetupItem => ({
  key: 'modules',
  order: 1,
  route: '/admin/modules',
  state: 'todo',
  ...patch,
});

describe('status vocabulary', () => {
  it('keeps every non-OK status visibly distinct from OK', () => {
    expect(statusTone('ok')).toBe('success');
    for (const status of healthStatuses.filter((s) => s !== 'ok')) {
      expect(statusTone(status), status).not.toBe('success');
    }
    expect(statusTone('failing')).toBe('danger');
    expect(statusTone('not_configured')).toBe('warning');
    expect(statusTone('fake')).toBe('warning');
  });
  it('has a label in both languages for each status', () => {
    for (const status of healthStatuses) {
      expect(en[statusKey(status)]).toBeTruthy();
      expect(de[statusKey(status)]).toBeTruthy();
    }
    expect(statusKey('surprise')).toBe('health.status.unknown');
  });
  it('counts attention statuses and summarizes in order', () => {
    expect(needsAttention('stale')).toBe(true);
    expect(needsAttention('disabled')).toBe(false);
    expect(statusCounts([{ status: 'failing' }, { status: 'ok' }, { status: 'ok' }])).toEqual([
      { status: 'ok', count: 2 },
      { status: 'failing', count: 1 },
    ]);
  });
  it('explains error codes with a shared message for unreadable facts', () => {
    expect(errorTextKey('jobs_unreadable')).toBe('health.error.unreadable');
    expect(errorTextKey('client_not_built')).toBe('health.error.client_not_built');
    expect(errorTextKey('never_heard_of_it')).toBe('health.error.generic');
  });
});

describe('route resolution', () => {
  it('links known screens, maps the contract aliases and refuses the rest', () => {
    expect(resolveAppRoute('/admin/roles')).toBe('/admin/roles');
    expect(resolveAppRoute('/admin/people')).toBe('/admin/users');
    expect(resolveAppRoute('/admin/directory')).toBe('/admin/directory-sync');
    expect(resolveAppRoute('/nope')).toBeUndefined();
    expect(resolveAppRoute('//evil.example')).toBeUndefined();
    expect(resolveAppRoute('https://evil.example')).toBeUndefined();
    expect(resolveAppRoute(undefined)).toBeUndefined();
  });
});

describe('setup checklist', () => {
  it('orders items and counts settled steps', () => {
    expect(
      orderedItems({ items: [item({ key: 'b', order: 2 }), item({ key: 'a', order: 1 })] }).map(
        (i) => i.key,
      ),
    ).toEqual(['a', 'b']);
    expect(settledCount({ open: 3, total: 10 })).toBe(7);
    expect(settledCount({ open: 12, total: 10 })).toBe(0);
  });
  it('offers actions by state, item and permission', () => {
    expect(setupActions(item({}), false)).toEqual({ skip: false, confirm: false, clear: false });
    expect(setupActions(item({}), true)).toEqual({ skip: true, confirm: true, clear: false });
    expect(setupActions(item({ key: 'teams' }), true)).toEqual({
      skip: true,
      confirm: false,
      clear: false,
    });
    expect(setupActions(item({ state: 'skipped', version: 2 }), true)).toEqual({
      skip: false,
      confirm: false,
      clear: true,
    });
    expect(setupActions(item({ state: 'done' }), true)).toEqual({
      skip: false,
      confirm: false,
      clear: false,
    });
  });
  it('sends the version of an existing mark only', () => {
    expect(expectedVersion(item({}))).toBeUndefined();
    expect(expectedVersion(item({ state: 'skipped', version: 3 }))).toBe(3);
  });
  it('marks attention as a warning even when the step is done', () => {
    expect(setupTone({ state: 'done', attention: true })).toBe('warning');
    expect(setupTone({ state: 'done' })).toBe('success');
    expect(setupTone({ state: 'skipped' })).toBe('neutral');
  });
});
