import { describe, expect, it } from 'vitest';
import type { Catalog, QueryState } from '../query/filterModel';
import type { SavedView, ViewPin } from './api';
import {
  abilities,
  addPin,
  definitionToState,
  filterViews,
  groupViews,
  isDirty,
  isEmptyQuery,
  movePin,
  pinPayload,
  removePin,
  removeShare,
  sameQuery,
  shareProblems,
  sharesChanged,
  stateToDefinition,
  unavailableConditions,
  upsertShare,
  warningConditionPaths,
  isSystemKey,
  systemDefinition,
  systemSavedView,
} from './model';

const state = (extra: Partial<QueryState> = {}): QueryState => ({
  filter: {
    v: 1,
    root: {
      type: 'group',
      logic: 'and',
      children: [{ type: 'condition', field: 'status', op: 'in', value: ['open'] }],
    },
  },
  sort: [{ field: 'updated_at', dir: 'desc' }],
  search: '',
  ...extra,
});
const view = (extra: Partial<SavedView> = {}): SavedView => ({
  id: 'v1',
  resource: 'tickets',
  name: 'Open',
  description: '',
  ownerId: 'u1',
  definition: stateToDefinition(state(), ['title', 'status']),
  visibility: 'private',
  version: 3,
  access: 'owner',
  moduleEnabled: true,
  pinned: false,
  createdAt: '',
  updatedAt: '',
  ...extra,
});
const catalog: Catalog = {
  resource: 'tickets',
  defaultSort: [],
  limits: {},
  fields: [
    {
      key: 'status',
      labelKey: 'status',
      type: 'enum',
      operators: ['in', 'not_in'],
      filterable: true,
      sortable: true,
      searchable: false,
      nullable: false,
      slow: false,
      enumValues: ['open', 'closed'],
    },
    {
      key: 'secret',
      labelKey: 'secret',
      type: 'text',
      operators: ['equals'],
      filterable: false,
      sortable: false,
      searchable: false,
      nullable: false,
      slow: false,
    },
  ],
};

describe('definition and state', () => {
  it('round-trips filter, sort, search and columns', () => {
    const original = state({ search: ' vpn ' });
    const definition = stateToDefinition(original, ['title', 'Bad-Key', 'status']);
    expect(definition.columns).toEqual(['title', 'status']);
    expect(definition.filter?.search).toBe('vpn');
    expect(sameQuery(definitionToState(definition), original)).toBe(true);
  });
  it('stores an empty query as a bare filter', () => {
    const definition = stateToDefinition(definitionToState(undefined));
    expect(definition).toEqual({ filter: { v: 1 } });
    expect(isEmptyQuery(definitionToState(definition))).toBe(true);
  });
});

describe('dirty state', () => {
  it('is clean for the saved state and ignores whitespace-only search changes', () => {
    expect(isDirty(view(), state(), ['title', 'status'], ['title', 'status', 'updated'])).toBe(
      false,
    );
    expect(isDirty(view(), state({ search: '  ' }), ['title', 'status'], ['title', 'status'])).toBe(
      false,
    );
  });
  it('detects filter, sort and column changes', () => {
    expect(isDirty(view(), state({ sort: [] }), undefined, [])).toBe(true);
    expect(isDirty(view(), state({ search: 'vpn' }), undefined, [])).toBe(true);
    expect(isDirty(view(), state(), ['status', 'title'], ['title', 'status'])).toBe(true);
  });
  it('does not count columns the view never stored or the list no longer offers', () => {
    const plain = view({ definition: stateToDefinition(state()) });
    expect(isDirty(plain, state(), ['a'], ['a', 'b'])).toBe(false);
    expect(isDirty(view(), state(), ['title'], ['title'])).toBe(false);
  });
});

describe('unavailable conditions', () => {
  it('flags unknown fields, unfilterable fields, missing operators and unknown enum values', () => {
    const root = {
      type: 'group' as const,
      logic: 'and' as const,
      children: [
        { type: 'condition' as const, field: 'status', op: 'in', value: ['open'] },
        { type: 'condition' as const, field: 'status', op: 'in', value: ['gone'] },
        { type: 'condition' as const, field: 'status', op: 'starts_with', value: 'x' },
        { type: 'condition' as const, field: 'secret', op: 'equals', value: 'x' },
        { type: 'condition' as const, field: 'missing', op: 'equals', value: 'x' },
      ],
    };
    expect(unavailableConditions(root, catalog).map((entry) => entry.path)).toEqual([
      [1],
      [2],
      [3],
      [4],
    ]);
    expect(unavailableConditions(root, undefined)).toEqual([]);
  });
  it('parses server warning paths', () => {
    expect(
      warningConditionPaths([
        { code: 'query.field_unavailable', path: 'root.children[1].children[0]' },
        { code: 'query.field_unavailable', path: 'root' },
        { code: 'query.field_unavailable', path: 'sort[0]' },
        { code: 'other', path: 'root.children[2]' },
      ]),
    ).toEqual([[1, 0], []]);
  });
});

describe('abilities', () => {
  const none = () => false;
  it('lets owners manage and editors edit but not share', () => {
    expect(abilities({ access: 'owner' }, none)).toMatchObject({
      canEdit: true,
      canManage: true,
      canShare: false,
    });
    expect(abilities({ access: 'owner' }, (p) => p === 'views.share')).toMatchObject({
      canShare: true,
    });
    expect(abilities({ access: 'edit' }, () => true)).toMatchObject({
      canEdit: true,
      canManage: false,
      canShare: false,
    });
  });
  it('marks use-level views read-only', () => {
    expect(abilities({ access: 'use' }, none)).toMatchObject({
      readOnly: true,
      canEdit: false,
      canRun: true,
    });
  });
  it('lets admins manage and take over but not run or pin', () => {
    expect(abilities({ access: 'admin' }, none)).toMatchObject({
      canRun: false,
      canManage: true,
      canTakeOver: true,
      canPin: false,
    });
  });
});

describe('view lists', () => {
  const list = [
    view({ id: 'a', name: 'Zeta' }),
    view({ id: 'b', name: 'Alpha', access: 'use', ownerName: 'Lena Bauer' }),
    view({ id: 'c', name: 'Org', shares: [{ subjectType: 'everyone', level: 'use' }] }),
  ];
  it('groups into mine, shared and global, sorted by name', () => {
    const groups = groupViews([...list, view({ id: 'd', name: 'Beta' })]);
    expect(groups.mine.map((v) => v.name)).toEqual(['Beta', 'Zeta']);
    expect(groups.shared.map((v) => v.name)).toEqual(['Alpha']);
    expect(groups.global.map((v) => v.name)).toEqual(['Org']);
  });
  it('searches name, description and owner case-insensitively', () => {
    expect(filterViews(list, 'alp').map((v) => v.id)).toEqual(['b']);
    expect(filterViews(list, 'LENA').map((v) => v.id)).toEqual(['b']);
    expect(filterViews(list, '  ').length).toBe(3);
  });
});

describe('shares', () => {
  it('upserts, removes and forces everyone to use', () => {
    let shares = upsertShare([], { subjectType: 'user', subjectId: 'u', level: 'use' });
    shares = upsertShare(shares, { subjectType: 'user', subjectId: 'u', level: 'edit' });
    shares = upsertShare(shares, { subjectType: 'everyone', level: 'edit' });
    expect(shares).toEqual([
      { subjectType: 'user', subjectId: 'u', level: 'edit' },
      { subjectType: 'everyone', level: 'use' },
    ]);
    expect(removeShare(shares, { subjectType: 'everyone' })).toHaveLength(1);
    expect(sharesChanged(shares, [...shares].reverse())).toBe(false);
    expect(
      sharesChanged(shares, removeShare(shares, { subjectType: 'user', subjectId: 'u' })),
    ).toBe(true);
  });
  it('requires views.share to add or raise and views.publish for everyone, but nothing to remove', () => {
    const saved = [{ subjectType: 'team' as const, subjectId: 't', level: 'use' as const }];
    const none = () => false;
    expect(shareProblems(saved, [], none)).toEqual([]);
    expect(shareProblems(saved, saved, none)).toEqual([]);
    expect(shareProblems(saved, [{ ...saved[0]!, level: 'edit' }], none)).toEqual(['needShare']);
    expect(
      shareProblems(
        saved,
        [...saved, { subjectType: 'everyone', level: 'use' }],
        (p) => p === 'views.share',
      ),
    ).toEqual(['needPublish']);
    expect(shareProblems([], [{ subjectType: 'everyone', level: 'edit' }], () => true)).toEqual([
      'everyoneEdit',
    ]);
    const many = Array.from({ length: 51 }, (_, i) => ({
      subjectType: 'user' as const,
      subjectId: `u${i}`,
      level: 'use' as const,
    }));
    expect(shareProblems(many, many, none)).toEqual(['limit']);
  });
});

describe('pins', () => {
  const pin = (viewId: string, position: number, extra: Partial<ViewPin> = {}): ViewPin => ({
    viewId,
    name: viewId,
    resource: 'tickets',
    groupKey: 'tickets',
    position,
    hidden: false,
    source: 'user',
    ...extra,
  });
  it('adds a pin at the end of its group and maps devices to endpoints', () => {
    const next = addPin([pin('a', 0), pin('b', 1)], { id: 'c', name: 'C', resource: 'devices' });
    expect(next.at(-1)).toMatchObject({
      viewId: 'c',
      groupKey: 'endpoints',
      position: 0,
      source: 'user',
    });
    expect(
      addPin([pin('a', 0)], { id: 'x', name: 'X', resource: 'tickets' }).at(-1)?.position,
    ).toBe(1);
  });
  it('removes user pins but only hides rule pins', () => {
    const items = [pin('a', 0), pin('b', 1, { source: 'rule' })];
    expect(removePin(items, 'a').map((p) => p.viewId)).toEqual(['b']);
    expect(removePin(items, 'b')[1]).toMatchObject({ viewId: 'b', hidden: true });
  });
  it('moves within a group and stops at the ends', () => {
    const items = [pin('a', 0), pin('b', 1), pin('c', 2)];
    expect(movePin(items, 'a', -1)).toBeNull();
    expect(movePin(items, 'c', 1)).toBeNull();
    const moved = movePin(items, 'b', -1);
    expect(moved?.items.map((p) => p.viewId)).toEqual(['b', 'a', 'c']);
    expect(moved?.items.map((p) => p.position)).toEqual([0, 1, 2]);
  });
  it('sends user pins and touched or hidden rule pins, numbered per group', () => {
    const items = [
      pin('a', 5),
      pin('r1', 0, { source: 'rule' }),
      pin('r2', 1, { source: 'rule', hidden: true }),
      pin('t', 0, { groupKey: 'tasks', resource: 'tasks' }),
    ];
    expect(pinPayload(items).map((p) => [p.viewId, p.groupKey, p.position, p.hidden])).toEqual([
      ['a', 'tickets', 0, false],
      ['r2', 'tickets', 1, true],
      ['t', 'tasks', 0, false],
    ]);
    expect(pinPayload(items, new Set(['r1'])).map((p) => p.viewId)).toEqual(['a', 'r1', 'r2', 't']);
  });
});

describe('System Views', () => {
  it('knows the key prefix', () => {
    expect(isSystemKey('system:tickets:my-open')).toBe(true);
    expect(isSystemKey('0a1b')).toBe(false);
    expect(isSystemKey(null)).toBe(false);
  });
  it('turns the built-in ticket keys into the same filters the server runs', () => {
    const open = {
      type: 'condition',
      field: 'status',
      op: 'in',
      value: ['new', 'open', 'in_progress', 'waiting'],
    };
    expect(systemDefinition('system:tickets:my-open')?.filter?.root).toEqual({
      type: 'group',
      logic: 'and',
      children: [open, { type: 'condition', field: 'assignee', op: 'is_me' }],
    });
    expect(systemDefinition('system:tickets:unassigned')?.filter?.root).toMatchObject({
      children: [open, { field: 'assignee', op: 'is_empty' }],
    });
    expect(systemDefinition('system:tickets:queue:abc')?.filter?.root).toMatchObject({
      children: [open, { field: 'queue', op: 'equals', value: 'abc' }],
    });
    expect(systemDefinition('system:tickets:queue:')).toBeUndefined();
    expect(systemDefinition('system:tickets:other')).toBeUndefined();
  });
  it('builds a read-only stand-in View without abilities beyond running', () => {
    const view = systemSavedView({
      id: 'system:tickets:queue:q1',
      name: 'HR',
      resource: 'tickets',
      groupKey: 'tickets',
      position: 2,
      system: true,
    });
    expect(view).toMatchObject({ system: true, name: 'HR', access: 'use' });
    expect(view && abilities(view, () => true)).toEqual({
      canRun: true,
      canEdit: false,
      canManage: false,
      canShare: false,
      canPin: false,
      canTakeOver: false,
      readOnly: true,
    });
    expect(
      systemSavedView({
        id: 'system:tickets:nope',
        name: '',
        resource: 'tickets',
        groupKey: 'tickets',
        position: 0,
        system: true,
      }),
    ).toBeUndefined();
  });
});
