import { describe, expect, it } from 'vitest';
import type { ImportBatch } from './adminTypes';
import {
  accessActions,
  bulkDraftReady,
  canApply,
  checkImportFile,
  emptyBulkDraft,
  extendInstant,
  extendMax,
  extendMin,
  importKinds,
  issueKey,
  matchKeysFor,
  selectablePerson,
  toBulkRequest,
} from './importModel';

const can =
  (...granted: string[]) =>
  (permission: string) =>
    granted.includes(permission);

const batch = (overrides: Partial<ImportBatch> = {}): ImportBatch => ({
  id: 'b1',
  kind: 'users',
  status: 'previewed',
  previewHash: 'h',
  rowCount: 3,
  counts: { create: 1, update: 1, reject: 1 },
  unknownColumns: [],
  expiresAt: '2026-10-10T13:00:00Z',
  ...overrides,
});

describe('importKinds', () => {
  it('needs organization.import and the manage permission of the kind', () => {
    expect(importKinds(can('organization.users.manage'))).toEqual([]);
    expect(importKinds(can('organization.import'))).toEqual([]);
    expect(importKinds(can('organization.import', 'organization.users.manage'))).toEqual(['users']);
    expect(
      importKinds(
        can(
          'organization.import',
          'organization.locations.manage',
          'organization.departments.manage',
        ),
      ),
    ).toEqual(['locations', 'departments']);
  });

  it('matches users by e-mail or employee number and the others by code', () => {
    expect(matchKeysFor('users')).toEqual(['primary_email', 'employee_number']);
    expect(matchKeysFor('locations')).toEqual(['code']);
    expect(matchKeysFor('departments')).toEqual(['code']);
  });
});

describe('checkImportFile', () => {
  it('rejects what the server would reject, before uploading', () => {
    expect(checkImportFile(null)).toBe('missing');
    expect(checkImportFile({ name: 'a.csv', size: 0 })).toBe('empty');
    expect(checkImportFile({ name: 'a.csv', size: 2 * 1024 * 1024 + 1 })).toBe('tooLarge');
    expect(checkImportFile({ name: 'a.xlsx', size: 10 })).toBe('notCsv');
    expect(checkImportFile({ name: 'People.CSV', size: 10 })).toBe('ok');
  });
});

describe('canApply', () => {
  const now = new Date('2026-10-10T12:00:00Z');
  it('applies a stored, unexpired preview that writes something', () => {
    expect(canApply(batch(), now)).toBe(true);
  });
  it('refuses expired, applied and empty previews', () => {
    expect(canApply(batch({ expiresAt: '2026-10-10T11:59:00Z' }), now)).toBe(false);
    expect(canApply(batch({ status: 'applied' }), now)).toBe(false);
    expect(canApply(batch({ counts: { reject: 3, unchanged: 0 } }), now)).toBe(false);
  });
});

describe('issueKey', () => {
  it('maps known codes and falls back for unknown ones', () => {
    expect(issueKey('directory_owned')).toBe('people.import.issue.directory_owned');
    expect(issueKey('something_new')).toBe('people.import.issue.unknown');
  });
});

describe('bulk requests', () => {
  it('sends only the parameter of the chosen operation and clears with null', () => {
    expect(toBulkRequest({ ...emptyBulkDraft, operation: 'set_department' }, ['u1'])).toEqual({
      operation: 'set_department',
      userIds: ['u1'],
      departmentId: null,
    });
    expect(
      toBulkRequest({ ...emptyBulkDraft, operation: 'set_primary_location', locationId: 'l1' }, [
        'u1',
      ]),
    ).toEqual({ operation: 'set_primary_location', userIds: ['u1'], locationId: 'l1' });
    expect(
      toBulkRequest(
        { ...emptyBulkDraft, operation: 'set_manager', manager: { id: 'm1', label: 'M' } },
        ['u1', 'u2'],
      ),
    ).toEqual({ operation: 'set_manager', userIds: ['u1', 'u2'], managerUserId: 'm1' });
    expect(
      toBulkRequest({ ...emptyBulkDraft, operation: 'deactivate', reason: 'left_organization' }, [
        'u1',
      ]),
    ).toEqual({ operation: 'deactivate', userIds: ['u1'], reason: 'left_organization' });
  });

  it('needs a reason for deactivation and a sane selection size', () => {
    expect(bulkDraftReady({ ...emptyBulkDraft, operation: 'deactivate' }, 2)).toBe(false);
    expect(bulkDraftReady({ ...emptyBulkDraft, operation: 'deactivate', reason: 'x' }, 2)).toBe(
      true,
    );
    expect(bulkDraftReady(emptyBulkDraft, 0)).toBe(false);
    expect(bulkDraftReady(emptyBulkDraft, 501)).toBe(false);
    expect(bulkDraftReady(emptyBulkDraft, 500)).toBe(true);
  });

  it('keeps emergency accounts out of the selection', () => {
    expect(selectablePerson({ source: 'emergency' })).toBe(false);
    expect(selectablePerson({ source: 'directory' })).toBe(true);
  });
});

describe('accessActions', () => {
  const local = { source: 'local', accountKind: 'employee', status: 'active' } as const;
  const external = { source: 'local', accountKind: 'external', status: 'active' } as const;
  it('offers directory linking to administrators for local employee accounts only', () => {
    expect(accessActions(local, can('platform.admin'))).toEqual(['linkDirectory']);
    expect(accessActions(local, can('organization.users.manage'))).toEqual([]);
    expect(accessActions({ ...local, source: 'directory' }, can('platform.admin'))).toEqual([]);
    expect(accessActions(external, can('platform.admin'))).toEqual([]);
  });
  it('offers the extension for external accounts that are not departed', () => {
    const manage = can('organization.external_parties.manage');
    expect(accessActions(external, manage)).toEqual(['extendAccess']);
    expect(accessActions({ ...external, status: 'departed' }, manage)).toEqual([]);
    expect(accessActions(local, manage)).toEqual([]);
    expect(accessActions(external, can('organization.users.manage'))).toEqual([]);
  });
});

describe('access extension dates', () => {
  const now = new Date(2026, 9, 10, 12, 0, 0);
  const current = new Date(2026, 9, 20, 9, 0, 0).toISOString();

  it('starts the day after the current end and ends 365 days from now', () => {
    expect(extendMin(current, now)).toBe('2026-10-21');
    expect(extendMin(undefined, now)).toBe('2026-10-11');
    expect(extendMin(new Date(2026, 8, 1).toISOString(), now)).toBe('2026-10-11');
    expect(extendMax(now)).toBe('2027-10-10');
  });

  it('builds the end of the chosen day and refuses unusable choices', () => {
    const ok = extendInstant('2026-11-01', current, now);
    expect(ok).toBeDefined();
    expect(new Date(ok ?? '').getHours()).toBe(23);
    expect(extendInstant('2026-10-20', current, now)).toBeUndefined();
    expect(extendInstant('2026-10-09', undefined, now)).toBeUndefined();
    expect(extendInstant('2027-10-11', current, now)).toBeUndefined();
    expect(extendInstant('tomorrow', current, now)).toBeUndefined();
    expect(extendInstant('2027-10-10', current, now)).toBeDefined();
  });
});
