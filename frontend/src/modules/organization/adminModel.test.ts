import { describe, expect, it } from 'vitest';
import {
  buildTree,
  checkPassword,
  composeDisplayName,
  departedReasons,
  deactivateReasons,
  findNode,
  flattenTree,
  isFieldLocked,
  lifecycleActions,
  looksLikeEmail,
  moveTargets,
  ownerOf,
  parseTokenFragment,
  pathLabel,
  profileChanges,
  reactivateReasons,
  reasonsFor,
  statusTone,
  subtreeHeight,
  subtreeIds,
} from './adminModel';
import type { FieldOwner, OrgNode } from './adminTypes';

const node = (
  id: string,
  name: string,
  parentId: string | null = null,
  active = true,
): OrgNode => ({
  id,
  name,
  parentId,
  active,
  version: 1,
});

describe('status reasons', () => {
  it('offers the closed lists of the server per operation', () => {
    expect(reasonsFor('deactivate')).toBe(deactivateReasons);
    expect(reasonsFor('reactivate')).toBe(reactivateReasons);
    expect(reasonsFor('markDeparted')).toBe(departedReasons);
    expect(deactivateReasons).toContain('security_concern');
    expect(reactivateReasons).toEqual(['returned', 'mistake', 'contract_renewed']);
  });
});

describe('lifecycleActions', () => {
  it('offers nothing without the manage permission or for emergency accounts', () => {
    expect(lifecycleActions({ source: 'local', status: 'active' }, false)).toEqual([]);
    expect(lifecycleActions({ source: 'emergency', status: 'active' }, true)).toEqual([]);
  });
  it('offers sign-in actions only to active local accounts', () => {
    expect(lifecycleActions({ source: 'local', status: 'active' }, true)).toEqual([
      'sendInvitation',
      'resetPassword',
      'deactivate',
      'markDeparted',
    ]);
    expect(lifecycleActions({ source: 'directory', status: 'active' }, true)).toEqual([
      'deactivate',
      'markDeparted',
    ]);
  });
  it('offers reactivation for inactive and departed people', () => {
    expect(lifecycleActions({ source: 'directory', status: 'inactive' }, true)).toEqual([
      'reactivate',
      'markDeparted',
    ]);
    expect(lifecycleActions({ source: 'local', status: 'departed' }, true)).toEqual(['reactivate']);
  });
  it('maps statuses to tones', () => {
    expect(statusTone('active')).toBe('success');
    expect(statusTone('inactive')).toBe('warning');
    expect(statusTone('weird')).toBe('unknown');
  });
});

describe('field ownership', () => {
  const fields: FieldOwner[] = [
    { key: 'displayName', owner: 'directory', source: 'ad', observedAt: '2026-10-09T10:00:00Z' },
    { key: 'department', owner: 'platform', source: 'turaco' },
  ];
  it('finds owners and treats unknown attributes as platform-owned', () => {
    expect(ownerOf(fields, 'displayName').source).toBe('ad');
    expect(isFieldLocked(fields, 'displayName')).toBe(true);
    expect(isFieldLocked(fields, 'department')).toBe(false);
    expect(isFieldLocked(fields, 'manager')).toBe(false);
  });
});

describe('profileChanges', () => {
  const person = {
    displayName: 'Ada Lovelace',
    givenName: 'Ada',
    familyName: 'Lovelace',
    primaryEmail: 'ada@example.org',
    employeeNumber: null,
    version: 4,
  };
  const form = {
    displayName: 'Ada Lovelace',
    givenName: 'Ada',
    familyName: 'Lovelace',
    primaryEmail: 'ada@example.org',
    employeeNumber: '',
  };
  it('returns null when nothing changed', () => {
    expect(profileChanges(person, form)).toBeNull();
  });
  it('sends only changed attributes with the version', () => {
    expect(
      profileChanges(person, {
        ...form,
        primaryEmail: ' ada@new.example.org ',
        employeeNumber: '42',
      }),
    ).toEqual({
      expectedVersion: 4,
      primaryEmail: 'ada@new.example.org',
      employeeNumber: '42',
    });
  });
  it('clears optional attributes with null and skips locked ones', () => {
    expect(profileChanges(person, { ...form, givenName: '' })).toEqual({
      expectedVersion: 4,
      givenName: null,
    });
    expect(
      profileChanges(person, { ...form, displayName: 'Other' }, new Set(['displayName'])),
    ).toBeNull();
  });
});

describe('small helpers', () => {
  it('checks e-mail shape and composes names', () => {
    expect(looksLikeEmail('a@b.example')).toBe(true);
    expect(looksLikeEmail('a@b')).toBe(false);
    expect(looksLikeEmail('a b@c.example')).toBe(false);
    expect(composeDisplayName(' Ada ', 'Lovelace')).toBe('Ada Lovelace');
    expect(composeDisplayName('', 'Lovelace')).toBe('Lovelace');
  });
});

describe('set password', () => {
  it('reads the token and purpose from the URL fragment', () => {
    expect(parseTokenFragment('#token=abc_DEF-1&purpose=invitation')).toEqual({
      token: 'abc_DEF-1',
      purpose: 'invitation',
    });
    expect(parseTokenFragment('token=x&purpose=reset')).toEqual({ token: 'x', purpose: 'reset' });
    expect(parseTokenFragment('#token=x&purpose=nonsense')).toEqual({
      token: 'x',
      purpose: 'unknown',
    });
  });
  it('rejects a link without token', () => {
    expect(parseTokenFragment('')).toBeNull();
    expect(parseTokenFragment('#purpose=reset')).toBeNull();
    expect(parseTokenFragment('#token=%20')).toBeNull();
  });
  it('checks length and confirmation only', () => {
    expect(checkPassword('short', 'short')).toEqual({
      longEnough: false,
      matches: true,
      ok: false,
    });
    expect(checkPassword('a long passphrase here', 'a long passphrase here').ok).toBe(true);
    expect(checkPassword('a long passphrase here', 'different').matches).toBe(false);
    expect(checkPassword('', '').ok).toBe(false);
  });
});

describe('trees', () => {
  const items = [
    node('a', 'Alpha'),
    node('b', 'Beta', 'a'),
    node('c', 'Charlie', 'b'),
    node('d', 'Delta', 'a'),
    node('e', 'Echo', null),
    node('z', 'Zulu (archived)', 'a', false),
  ];
  it('builds a sorted forest and hides archived entries by default', () => {
    const forest = buildTree(items, false);
    expect(forest.map((n) => n.item.name)).toEqual(['Alpha', 'Echo']);
    expect(forest[0]?.children.map((n) => n.item.name)).toEqual(['Beta', 'Delta']);
    expect(forest[0]?.descendants).toBe(3);
    expect(buildTree(items, true)[0]?.descendants).toBe(4);
  });
  it('makes orphans roots and survives cycles', () => {
    const forest = buildTree(
      [node('x', 'X', 'missing'), node('p', 'P', 'q'), node('q', 'Q', 'p')],
      false,
    );
    expect(forest.map((n) => n.item.id)).toEqual(['x']);
  });
  it('flattens by expanded set', () => {
    const forest = buildTree(items, false);
    expect(flattenTree(forest, new Set()).map((n) => n.item.id)).toEqual(['a', 'e']);
    expect(flattenTree(forest, new Set(['a'])).map((n) => n.item.id)).toEqual(['a', 'b', 'd', 'e']);
    expect(flattenTree(forest, new Set(['a', 'b'])).map((n) => n.item.id)).toEqual([
      'a',
      'b',
      'c',
      'd',
      'e',
    ]);
  });
  it('finds nodes, subtrees and heights', () => {
    const forest = buildTree(items, false);
    const alpha = findNode(forest, 'a');
    expect(alpha && [...subtreeIds(alpha)].sort()).toEqual(['a', 'b', 'c', 'd']);
    expect(alpha && subtreeHeight(alpha)).toBe(3);
    expect(findNode(forest, 'nope')).toBeUndefined();
  });
  it('offers valid move targets only', () => {
    const forest = buildTree(items, false);
    const beta = findNode(forest, 'b');
    expect(beta).toBeDefined();
    const ids = moveTargets(forest, beta!, { allowRoot: true }).map(
      (t) => t.node?.item.id ?? 'root',
    );
    // not itself, not below itself; the root and other branches are fine.
    expect(ids).toEqual(['root', 'a', 'd', 'e']);
    const limited = moveTargets(forest, findNode(forest, 'a')!, {
      maxDepth: 4,
      allowRoot: false,
    }).map((t) => t.node?.item.id);
    expect(limited).toEqual(['e']);
    const depthLimited = moveTargets(forest, beta!, { maxDepth: 2, allowRoot: false }).map(
      (t) => t.node?.item.id,
    );
    // Beta has height 2, so below a depth-1 node it would reach depth 3.
    expect(depthLimited).toEqual([]);
  });
  it('writes breadcrumb paths', () => {
    expect(pathLabel(items, 'c')).toBe('Alpha / Beta / Charlie');
    expect(pathLabel(items, null)).toBe('');
  });
});
