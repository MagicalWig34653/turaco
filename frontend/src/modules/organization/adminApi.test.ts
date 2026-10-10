import { afterEach, describe, expect, it, vi } from 'vitest';
import { errorMessageKey } from '../../platform/api/errorMessages';
import { peopleAdminApi } from './adminApi';
import '../access/api';

describe('F14 error codes', () => {
  it('maps People and Teams conflicts to plain messages', () => {
    expect(errorMessageKey({ code: 'organization.version_conflict', status: 409 })).toBe(
      'people.error.versionConflict',
    );
    expect(errorMessageKey({ code: 'organization.field_directory_owned', status: 409 })).toBe(
      'people.error.directoryOwned',
    );
    expect(errorMessageKey({ code: 'access.team_membership_self', status: 409 })).toBe(
      'people.error.teamMembershipSelf',
    );
    expect(errorMessageKey({ code: 'organization.emergency_account', status: 409 })).toBe(
      'people.error.emergencyAccount',
    );
  });
  it('maps escalation guards of roles and assignments', () => {
    expect(errorMessageKey({ code: 'access.dominance_required', status: 403 })).toBe(
      'access.error.dominanceRequired',
    );
    expect(errorMessageKey({ code: 'access.grant_exceeds_holder', status: 403 })).toBe(
      'access.error.grantExceedsHolder',
    );
    expect(errorMessageKey({ code: 'access.self_assignment', status: 409 })).toBe(
      'access.error.selfAssignment',
    );
    expect(errorMessageKey({ code: 'access.local_account_high_risk', status: 409 })).toBe(
      'access.error.localAccountHighRisk',
    );
  });
  it('maps the sign-in link errors an administrator can meet', () => {
    expect(errorMessageKey({ code: 'auth.base_url_not_configured', status: 409 })).toBe(
      'auth.error.baseUrlNotConfigured',
    );
    expect(errorMessageKey({ code: 'auth.mail_not_configured', status: 409 })).toBe(
      'auth.error.mailNotConfigured',
    );
    expect(errorMessageKey({ code: 'auth.mail_failed', status: 502 })).toBe(
      'auth.error.mailFailed',
    );
  });
});

describe('F14 import, bulk, link and extend calls', () => {
  const calls: { url: string; init: RequestInit }[] = [];
  const respond = (status: number, body: unknown) => {
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init: RequestInit) => {
        calls.push({ url, init });
        return Promise.resolve(
          new Response(JSON.stringify(body), {
            status,
            headers: { 'Content-Type': 'application/json' },
          }),
        );
      }),
    );
  };
  afterEach(() => {
    vi.unstubAllGlobals();
    calls.length = 0;
  });

  it('uploads the CSV as multipart form data with kind, key and mode', async () => {
    respond(201, { id: 'b1', rows: [] });
    const file = new File(['display_name,primary_email\nA,a@example.test\n'], 'people.csv', {
      type: 'text/csv',
    });
    await peopleAdminApi.previewImport('users', 'primary_email', 'upsert', file);
    const call = calls[0];
    expect(call?.url).toBe('/api/v1/import-batches');
    const body = call?.init.body;
    expect(body).toBeInstanceOf(FormData);
    const form = body as FormData;
    expect([form.get('kind'), form.get('matchKey'), form.get('mode')]).toEqual([
      'users',
      'primary_email',
      'upsert',
    ]);
    expect((form.get('file') as File).name).toBe('people.csv');
    expect((call?.init.headers as Record<string, string>)['Content-Type']).toBeUndefined();
  });

  it('previews and applies bulk operations with the stored preview hash', async () => {
    respond(201, { id: 'b2' });
    await peopleAdminApi.bulkPreview({ operation: 'deactivate', userIds: ['u1'], reason: 'x' });
    expect(JSON.parse(String(calls[0]?.init.body))).toEqual({
      operation: 'deactivate',
      userIds: ['u1'],
      reason: 'x',
      dryRun: true,
    });
    await peopleAdminApi.bulkApply('b2', 'hash', 1);
    expect(JSON.parse(String(calls[1]?.init.body))).toEqual({
      dryRun: false,
      batchId: 'b2',
      previewHash: 'hash',
      expectedRejects: 1,
    });
  });

  it('sends expectedVersion with link and extend', async () => {
    respond(200, { id: 'u1' });
    await peopleAdminApi.linkDirectoryIdentity('u1', 3, { runId: 'r1', externalId: 'e1' });
    expect(calls[0]?.url).toBe('/api/v1/users/u1/link-directory-identity');
    expect(JSON.parse(String(calls[0]?.init.body))).toEqual({
      expectedVersion: 3,
      runId: 'r1',
      externalId: 'e1',
    });
    await peopleAdminApi.extendAccess('u1', 4, '2027-01-01T00:00:00Z', 'contract_renewed');
    expect(JSON.parse(String(calls[1]?.init.body))).toEqual({
      expectedVersion: 4,
      accessExpiresAt: '2027-01-01T00:00:00Z',
      reason: 'contract_renewed',
    });
  });

  it('links and unlinks Entra identities with the documented paths and bodies', async () => {
    respond(200, { configured: true, tenantId: 't', tenantIds: ['t'] });
    await peopleAdminApi.entraLinking();
    expect(calls[0]?.url).toBe('/api/v1/users/entra-linking');
    respond(201, { user: { id: 'u1' }, credentialDeleted: false, noticeSent: true });
    await peopleAdminApi.linkEntraIdentity('u1', 3, 'tenant-guid', 'object-guid');
    expect(calls[1]?.url).toBe('/api/v1/users/u1/external-identities/entra');
    expect(calls[1]?.init.method).toBe('POST');
    expect(JSON.parse(String(calls[1]?.init.body))).toEqual({
      expectedVersion: 3,
      tenantId: 'tenant-guid',
      objectId: 'object-guid',
    });
    await peopleAdminApi.unlinkExternalIdentity('u1', 'i1');
    expect(calls[2]?.url).toBe('/api/v1/users/u1/external-identities/i1');
    expect(calls[2]?.init.method).toBe('DELETE');
  });

  it('maps the Entra linking errors to plain messages', () => {
    for (const [code, key] of [
      ['organization.entra_not_configured', 'entra.error.notConfigured'],
      ['organization.entra_tenant_not_allowed', 'entra.error.tenantNotAllowed'],
      ['organization.entra_identity_in_use', 'entra.error.inUse'],
      ['organization.entra_tenant_already_linked', 'entra.error.tenantAlreadyLinked'],
    ] as const)
      expect(errorMessageKey({ code, status: 409 })).toBe(key);
  });

  it('offers only e-mail-in-use conflicts of successful runs for linking', async () => {
    respond(200, {
      items: [
        {
          id: 'r1',
          providerKey: 'ldap',
          outcome: 'succeeded',
          conflicts: [
            { kind: 'email_in_use', externalId: 'e1', username: 'jdoe' },
            { kind: 'invalid_attributes', externalId: 'e2', username: '' },
          ],
        },
        {
          id: 'r2',
          providerKey: 'ldap',
          outcome: 'failed',
          conflicts: [{ kind: 'email_in_use', externalId: 'e3', username: 'x' }],
        },
      ],
    });
    await expect(peopleAdminApi.syncConflicts()).resolves.toEqual([
      { runId: 'r1', providerKey: 'ldap', externalId: 'e1', username: 'jdoe' },
    ]);
  });

  it('maps the new conflict codes to plain messages', () => {
    expect(errorMessageKey({ code: 'organization.import_stale', status: 409 })).toBe(
      'people.import.error.stale',
    );
    expect(
      errorMessageKey({ code: 'organization.directory_link_refused_roles', status: 409 }),
    ).toBe('people.link.error.roles');
    expect(errorMessageKey({ code: 'access.admin_required', status: 403 })).toBe(
      'people.link.error.adminRequired',
    );
  });
});
