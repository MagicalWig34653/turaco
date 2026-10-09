import { describe, expect, it } from 'vitest';
import {
  asGroup,
  defaultCondition,
  duplicateAt,
  listConditions,
  normalize,
  replaceAt,
  deserialize,
  emptyState,
  negate,
  serialize,
  validate,
  type Catalog,
  type Condition,
  type Field,
  type Node,
  type QueryState,
} from './filterModel';

const field = (
  key: string,
  type: Field['type'],
  operators: string[],
  extra: Partial<Field> = {},
): Field => ({
  key,
  type,
  operators,
  labelKey: `test.${key}`,
  filterable: true,
  sortable: true,
  searchable: type === 'text',
  nullable: true,
  slow: false,
  ...extra,
});
const catalog: Catalog = {
  resource: 'test',
  defaultSort: [{ field: 'title', dir: 'asc' }],
  limits: {
    maxDepth: 4,
    maxConditions: 25,
    maxSortKeys: 3,
    maxInValues: 100,
    maxStringLength: 200,
    maxSearchLength: 100,
  },
  fields: [
    field('title', 'text', [
      'equals',
      'not_equals',
      'contains',
      'not_contains',
      'in',
      'not_in',
      'is_empty',
      'is_not_empty',
    ]),
    field('number', 'number', [
      'equals',
      'not_equals',
      'greater',
      'less_or_equal',
      'between',
      'in',
    ]),
    field('date', 'date', ['equals', 'before', 'after', 'between', 'today', 'last_n_days']),
    field('datetime', 'datetime', ['equals', 'between']),
    field('status', 'enum', ['equals', 'in'], { enumValues: ['open', 'closed'] }),
    field('assignee', 'reference', ['equals', 'in', 'is_me', 'is_empty']),
    field('flag', 'boolean', ['is_true', 'is_false', 'is_empty']),
    field('tags', 'tags', ['has_any', 'has_all', 'has_none'], {
      enumValues: ['hot', 'cold'],
      sortable: false,
    }),
  ],
};
const condition = (field = 'title', op = 'equals', value: unknown = 'hello'): Condition => ({
  type: 'condition',
  field,
  op,
  value,
});
const state = (root?: Node): QueryState => ({
  ...emptyState(),
  filter: { v: 1, ...(root ? { root } : {}) },
});
const valid = (node: Node) => validate(state(node), catalog);

describe('query filter model', () => {
  it('round trips nested AST, Unicode, search and ordered sort without date resolution', () => {
    const input: QueryState = {
      filter: {
        v: 1,
        root: {
          type: 'group',
          logic: 'and',
          children: [
            condition('title', 'contains', 'Über %_🐦'),
            {
              type: 'group',
              logic: 'or',
              children: [
                condition('date', 'last_n_days', 7),
                condition('status', 'in', ['open', 'closed']),
              ],
            },
          ],
        },
      },
      sort: [
        { field: 'title', dir: 'desc', nulls: 'first' },
        { field: 'date', dir: 'asc' },
      ],
      search: '東京',
    };
    expect(deserialize(serialize(input))).toEqual(input);
    expect(validate(input, catalog)).toEqual([]);
    expect(deserialize(serialize(emptyState()))).toEqual(emptyState());
  });

  it.each([
    null,
    [],
    {},
    { filter: { v: 2 }, sort: [], search: '' },
    { filter: { v: 1 }, sort: [], search: '', extra: true },
    { filter: { v: 1, root: null }, sort: [], search: '' },
    { filter: { v: 1 }, sort: [{ field: 'title', dir: 'up' }], search: '' },
    { filter: { v: 1 }, sort: [{ field: 'title', dir: 'asc', nulls: 'middle' }], search: '' },
    state({ type: 'group', logic: 'and', children: [] }),
    state({ ...condition(), logic: 'and' } as Node),
    { filter: { v: 1 }, sort: [], search: '\u0000' },
  ])('rejects malformed URL state %j', (input) => {
    expect(() => deserialize(JSON.stringify(input))).toThrow();
  });

  it('rejects malformed JSON, deeply nested and oversized URL state', () => {
    expect(() => deserialize('{')).toThrow();
    let node: Node = condition();
    for (let depth = 0; depth < 40; depth++)
      node = { type: 'group', logic: 'and', children: [node] };
    expect(() => deserialize(JSON.stringify(state(node)))).toThrow();
    expect(() => deserialize(' '.repeat(16385))).toThrow('query.too_complex');
    expect(() => serialize({ ...emptyState(), search: '🐦'.repeat(5000) })).toThrow(
      'query.too_complex',
    );
  });

  it('rejects unavailable fields, unsupported operators and unsortable fields', () => {
    expect(valid(condition('private'))).toContain('query.validation.field');
    expect(valid(condition('status', 'contains'))).toContain('query.validation.operator');
    expect(validate({ ...emptyState(), sort: [{ field: 'tags', dir: 'asc' }] }, catalog)).toContain(
      'query.validation.field',
    );
    expect(
      validate(state(condition()), {
        ...catalog,
        fields: [{ ...catalog.fields[0]!, filterable: false }],
      }),
    ).toContain('query.validation.field');
  });

  it.each([
    ['number', 'equals', '2'],
    ['number', 'equals', 1.0000001],
    ['number', 'equals', Infinity],
    ['number', 'between', [1]],
    ['number', 'between', [1, '2']],
    ['number', 'in', []],
    ['status', 'equals', 'secret'],
    ['status', 'in', ['open', 'secret']],
    ['assignee', 'equals', 'name'],
    ['assignee', 'in', ['bad-uuid']],
    ['date', 'equals', '2025-02-29'],
    ['date', 'equals', '2026-13-01'],
    ['date', 'equals', '2026-01-01T12:00:00Z'],
    ['datetime', 'equals', '2026-01-01T12:00'],
    ['date', 'last_n_days', 0],
    ['date', 'last_n_days', 3651],
    ['date', 'last_n_days', 1.5],
    ['flag', 'is_true', true],
    ['date', 'today', 7],
    ['tags', 'has_any', ['unknown']],
    ['title', 'equals', { sql: 'x' }],
  ])('rejects invalid %s %s value %j', (field, op, value) => {
    expect(valid(condition(field, op, value))).toContain('query.validation.value');
  });

  it.each([
    ['number', 'equals', 0],
    ['number', 'between', [-1.25, 2]],
    ['number', 'in', [1, 2]],
    ['status', 'in', ['open']],
    ['assignee', 'equals', '8A2CA521-11BA-45BE-A727-A48A71935623'],
    ['date', 'equals', '2024-02-29'],
    ['datetime', 'equals', '2026-01-01T12:30:00+02:00'],
    ['date', 'last_n_days', 3650],
    ['flag', 'is_true', null],
    ['date', 'today', null],
    ['tags', 'has_any', ['hot']],
    ['title', 'equals', '%_\\'],
  ])('accepts valid %s %s value %j', (field, op, value) => {
    expect(valid(condition(field, op, value))).toEqual([]);
  });

  it('applies catalog condition and depth limits, counting root group as depth one', () => {
    const tight = { ...catalog, limits: { ...catalog.limits, maxConditions: 1, maxDepth: 1 } };
    expect(
      validate(state({ type: 'group', logic: 'and', children: [condition()] }), tight),
    ).toEqual([]);
    expect(
      validate(state({ type: 'group', logic: 'and', children: [condition(), condition()] }), tight),
    ).toContain('query.validation.maxConditions');
    expect(
      validate(
        state({
          type: 'group',
          logic: 'and',
          children: [{ type: 'group', logic: 'or', children: [condition()] }],
        }),
        tight,
      ),
    ).toContain('query.validation.maxDepth');
  });

  it('applies Unicode-aware search/string, list and sort limits', () => {
    const tight = {
      ...catalog,
      limits: {
        ...catalog.limits,
        maxInValues: 1,
        maxStringLength: 2,
        maxSearchLength: 2,
        maxSortKeys: 1,
      },
    };
    expect(validate(state(condition('title', 'equals', '🐦🐦')), tight)).toEqual([]);
    expect(validate(state(condition('title', 'equals', '🐦🐦🐦')), tight)).toContain(
      'query.validation.maxStringLength',
    );
    expect(validate({ ...emptyState(), search: 'abc' }, tight)).toContain(
      'query.validation.maxSearchLength',
    );
    expect(validate(state(condition('title', 'in', ['a', 'b'])), tight)).toContain(
      'query.validation.maxInValues',
    );
    expect(
      validate(
        {
          ...emptyState(),
          sort: [
            { field: 'title', dir: 'asc' },
            { field: 'title', dir: 'desc' },
          ],
        },
        tight,
      ),
    ).toEqual(
      expect.arrayContaining(['query.validation.maxSortKeys', 'query.validation.duplicateSort']),
    );
  });

  it('negates only exact complements allowed by the field and preserves values', () => {
    const title = catalog.fields[0]!;
    const original = condition('title', 'contains', 'a');
    expect(negate(original, title)).toEqual({ ...original, op: 'not_contains' });
    expect(negate(negate(original, title)!, title)).toEqual(original);
    expect(negate(condition('title', 'is_empty', null), title)?.op).toBe('is_not_empty');
    expect(
      negate(condition('title', 'contains', 'a'), { ...title, operators: ['contains'] }),
    ).toBeUndefined();
    const number = catalog.fields[1]!;
    expect(negate(condition('number', 'greater', 1), number)).toBeUndefined();
    expect(negate(condition('number', 'greater', 1), { ...number, nullable: false })?.op).toBe(
      'less_or_equal',
    );
    expect(negate(condition('date', 'before', '2026-01-01'), catalog.fields[2]!)).toBeUndefined();
    const flag = catalog.fields[6]!;
    expect(negate(condition('flag', 'is_true', null), flag)).toBeUndefined();
    expect(negate(condition('flag', 'is_true', null), { ...flag, nullable: false })?.op).toBe(
      'is_false',
    );
  });
});

describe('query tree editing', () => {
  const a = condition('title', 'equals', 'a');
  const b = condition('status', 'equals', 'open');
  const tree: Node = {
    type: 'group',
    logic: 'and',
    children: [a, { type: 'group', logic: 'or', children: [b] }],
  };

  it('builds valid default conditions for every field and operator', () => {
    for (const f of catalog.fields) {
      const made = defaultCondition(f);
      expect(made.op).toBe(f.operators[0]);
    }
    expect(valid(defaultCondition(catalog.fields[4] as Field))).toEqual([]);
  });

  it('lists conditions with paths and replaces or removes by path', () => {
    expect(listConditions(tree).map((entry) => entry.path)).toEqual([[0], [1, 0]]);
    const removed = replaceAt(tree, [1, 0]);
    expect(listConditions(removed)).toHaveLength(1);
    const replaced = replaceAt(tree, [0], condition('title', 'contains', 'z'));
    expect(listConditions(replaced)[0]?.node.op).toBe('contains');
  });

  it('drops empty groups and blank list entries before the server sees them', () => {
    const empty: Node = {
      type: 'group',
      logic: 'and',
      children: [a, { type: 'group', logic: 'or', children: [] }],
    };
    expect(normalize(empty)).toEqual({ type: 'group', logic: 'and', children: [a] });
    expect(normalize({ type: 'group', logic: 'and', children: [] })).toBeUndefined();
    const list = condition('title', 'in', ['x', '', 'y']);
    expect(normalize(list)).toEqual(condition('title', 'in', ['x', 'y']));
  });

  it('duplicates a condition next to itself and wraps a lone root condition', () => {
    const copy = duplicateAt(tree, [0]);
    expect(listConditions(copy)).toHaveLength(3);
    expect(listConditions(copy)[1]?.node).toEqual(a);
    expect(duplicateAt(a, []).type).toBe('group');
    expect(asGroup(a).children).toEqual([a]);
  });
});
