import { describe, expect, it } from 'vitest';
import { ApiError } from '../../platform/api/client';
import {
  approvalSteps,
  hashDiffers,
  isHttpsUrl,
  isProviderNotConfigured,
  isSha256,
  normalizePage,
  productOperations,
  publishBlocker,
  shortHash,
  validateRegister,
  versionActions,
} from './helpers';

const hash = 'a'.repeat(64);
const all = () => true;
const only =
  (...permissions: string[]) =>
  (permission: string) =>
    permissions.includes(permission);

describe('normalizePage', () => {
  it('drops an empty cursor so the list stops paging', () => {
    expect(normalizePage({ items: [1], nextCursor: '' })).toEqual({ items: [1] });
    expect(normalizePage({ items: [1], nextCursor: 'c' })).toEqual({ items: [1], nextCursor: 'c' });
  });
});

describe('productOperations', () => {
  it('follows the Software Approval Status lifecycle', () => {
    expect(productOperations('candidate')).toEqual(['approve', 'block']);
    expect(productOperations('approved')).toEqual(['deprecate', 'block']);
    expect(productOperations('deprecated')).toEqual(['retire', 'block']);
    expect(productOperations('retired')).toEqual(['block']);
    expect(productOperations('blocked')).toEqual(['unblock']);
  });
});

describe('approvalSteps', () => {
  it('marks the current step of the happy path', () => {
    expect(approvalSteps('pending')).toEqual([
      { id: 'registered', state: 'done' },
      { id: 'pending', state: 'current' },
      { id: 'approved', state: 'upcoming' },
    ]);
    expect(approvalSteps('approved').at(-1)).toEqual({ id: 'approved', state: 'done' });
  });
  it('replaces approved by rejected and appends revoked', () => {
    expect(approvalSteps('rejected').map((s) => s.id)).toEqual([
      'registered',
      'pending',
      'rejected',
    ]);
    expect(approvalSteps('rejected').at(-1)?.state).toBe('failed');
    expect(approvalSteps('revoked')).toEqual([
      { id: 'registered', state: 'done' },
      { id: 'pending', state: 'done' },
      { id: 'approved', state: 'done' },
      { id: 'revoked', state: 'failed' },
    ]);
  });
});

describe('versionActions', () => {
  const pending = { approvalStatus: 'pending' as const, registeredBy: 'u1', requestedBy: 'u2' };
  it('disables approval for the registrant and the requester', () => {
    for (const me of ['u1', 'u2']) {
      const approve = versionActions(pending, me, all).find((a) => a.id === 'approve');
      expect(approve?.disabledReason).toBe('software.disabled.separationOfDuties');
    }
    expect(versionActions(pending, 'u3', all).find((a) => a.id === 'approve')).toEqual({
      id: 'approve',
    });
  });
  it('hides actions without the permission', () => {
    expect(versionActions(pending, 'u3', only('software.view'))).toEqual([]);
    expect(
      versionActions({ ...pending, approvalStatus: 'registered' }, 'u1', only('software.package')),
    ).toEqual([{ id: 'request-approval' }]);
  });
  it('offers revoke and package for an approved version', () => {
    expect(versionActions({ ...pending, approvalStatus: 'approved' }, 'u1', all)).toEqual([
      { id: 'revoke' },
      { id: 'package' },
    ]);
  });
  it('explains why packaging waits for approval', () => {
    expect(versionActions(pending, 'u3', only('software.package'))).toEqual([
      { id: 'package', disabledReason: 'software.disabled.versionNotApproved' },
    ]);
  });
  it('offers nothing after a rejection or revocation', () => {
    expect(versionActions({ ...pending, approvalStatus: 'rejected' }, 'u3', all)).toEqual([]);
    expect(versionActions({ ...pending, approvalStatus: 'revoked' }, 'u3', all)).toEqual([]);
  });
});

describe('publishBlocker', () => {
  it('blocks a hash mismatch before anything else', () => {
    expect(publishBlocker({ status: 'packaged', hashMismatch: true })).toBe(
      'software.disabled.hashMismatch',
    );
  });
  it('requires a packaged package', () => {
    expect(publishBlocker({ status: 'packaged', hashMismatch: false })).toBeUndefined();
    expect(publishBlocker({ status: 'building', hashMismatch: false })).toBe(
      'software.disabled.packageNotReady',
    );
    expect(publishBlocker({ status: 'published', hashMismatch: false })).toBe(
      'software.disabled.alreadyPublished',
    );
  });
});

describe('format and validation helpers', () => {
  it('checks hashes and https URLs', () => {
    expect(isSha256(hash)).toBe(true);
    expect(isSha256(hash.slice(1))).toBe(false);
    expect(isSha256('g'.repeat(64))).toBe(false);
    expect(isHttpsUrl('https://example.org/a.msi')).toBe(true);
    expect(isHttpsUrl('http://example.org/a.msi')).toBe(false);
    expect(isHttpsUrl('not a url')).toBe(false);
  });
  it('shortens hashes and compares case-insensitively', () => {
    expect(shortHash('0123456789abcdef'.repeat(4))).toBe('01234567…89abcdef');
    expect(shortHash('abc')).toBe('abc');
    expect(hashDiffers(hash, hash.toUpperCase())).toBe(false);
    expect(hashDiffers(hash, 'b'.repeat(64))).toBe(true);
    expect(hashDiffers(hash, null)).toBe(false);
  });
  it('recognizes the provider-not-configured error', () => {
    const error = new ApiError({
      status: 409,
      code: 'endpoints.software_provider_not_configured',
      message: 'x',
    });
    expect(isProviderNotConfigured(error)).toBe(true);
    expect(isProviderNotConfigured(new Error('x'))).toBe(false);
  });
  it('validates the registration form', () => {
    const valid = {
      productId: 'p',
      version: '1.0',
      installerSha256: hash,
      installerUrl: 'https://example.org/a.msi',
      installCommand: 'msiexec /i a.msi /qn',
      detectionRule: 'file exists',
    };
    expect(validateRegister(valid)).toEqual({});
    expect(
      validateRegister({
        ...valid,
        productId: '',
        installerSha256: 'abc',
        installerUrl: 'http://x',
        installCommand: 'a\nb',
      }),
    ).toEqual({
      productId: 'software.validation.required',
      installerSha256: 'software.validation.sha256',
      installerUrl: 'software.validation.https',
      installCommand: 'software.validation.oneLine',
    });
  });
});
