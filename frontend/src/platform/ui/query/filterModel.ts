/** The public query AST. Kept independent of React so URL state is validated before use. */
export type Condition = { type: 'condition'; field: string; op: string; value?: unknown };
export type Group = { type: 'group'; logic: 'and' | 'or'; children: Node[] };
export type Node = Condition | Group;
export type Sort = { field: string; dir: 'asc' | 'desc'; nulls?: 'first' | 'last' };
export type Filter = { v: 1; root?: Node };
export type QueryState = { filter: Filter; sort: Sort[]; search: string };
export type Field = {
  key: string;
  labelKey: string;
  type: 'text' | 'number' | 'boolean' | 'date' | 'datetime' | 'enum' | 'reference' | 'tags';
  operators: string[];
  filterable: boolean;
  sortable: boolean;
  searchable: boolean;
  nullable: boolean;
  slow: boolean;
  enumValues?: string[];
  reference?: string;
};
export type Catalog = {
  resource: string;
  fields: Field[];
  limits: Record<string, number>;
  defaultSort: Sort[];
};

export const unaryOperators = new Set([
  'is_empty',
  'is_not_empty',
  'is_true',
  'is_false',
  'is_me',
  'is_my_teams',
  'today',
  'this_week',
  'this_month',
]);
export const relativeOperators = new Set([
  'last_n_days',
  'next_n_days',
  'older_than_n_days',
  'within_n_days_from_now',
]);
export const listOperators = new Set(['in', 'not_in', 'has_any', 'has_all', 'has_none']);
export const emptyState = (): QueryState => ({ filter: { v: 1 }, sort: [], search: '' });
const record = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);
const keys = (value: Record<string, unknown>, allowed: string[]) =>
  Object.keys(value).every((key) => allowed.includes(key));
const text = (value: unknown): value is string =>
  typeof value === 'string' &&
  !value.includes('\u0000') &&
  !/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value);
const length = (value: string) => Array.from(value).length;
const bytes = (value: string) => new TextEncoder().encode(value).length;

function validNode(node: unknown, depth = 1): node is Node {
  if (!record(node) || depth > 32) return false;
  if (node.type === 'condition') {
    return (
      keys(node, ['type', 'field', 'op', 'value']) &&
      text(node.field) &&
      !!node.field &&
      text(node.op) &&
      !!node.op
    );
  }
  return (
    node.type === 'group' &&
    keys(node, ['type', 'logic', 'children']) &&
    (node.logic === 'and' || node.logic === 'or') &&
    Array.isArray(node.children) &&
    node.children.length > 0 &&
    node.children.length <= 100 &&
    node.children.every((child) => validNode(child, depth + 1))
  );
}
function validState(state: unknown): state is QueryState {
  if (!record(state) || !keys(state, ['filter', 'sort', 'search']) || !record(state.filter))
    return false;
  const filter = state.filter;
  return (
    keys(filter, ['v', 'root']) &&
    filter.v === 1 &&
    (filter.root === undefined || validNode(filter.root)) &&
    text(state.search) &&
    Array.isArray(state.sort) &&
    state.sort.every(
      (sort) =>
        record(sort) &&
        keys(sort, ['field', 'dir', 'nulls']) &&
        text(sort.field) &&
        !!sort.field &&
        (sort.dir === 'asc' || sort.dir === 'desc') &&
        (sort.nulls === undefined || sort.nulls === 'first' || sort.nulls === 'last'),
    )
  );
}
export function serialize(state: QueryState): string {
  if (!validState(state)) throw new Error('query.invalid_filter');
  const raw = JSON.stringify(state);
  if (bytes(raw) > 16384) throw new Error('query.too_complex');
  return raw;
}
export function deserialize(raw: string): QueryState {
  if (bytes(raw) > 16384) throw new Error('query.too_complex');
  const value: unknown = JSON.parse(raw);
  if (!validState(value)) throw new Error('query.invalid_filter');
  return value;
}
function dateValue(value: unknown, datetime: boolean): boolean {
  if (!text(value)) return false;
  const day = value.slice(0, 10);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(day)) return false;
  const parsed = new Date(`${day}T00:00:00Z`);
  if (!Number.isFinite(parsed.valueOf()) || parsed.toISOString().slice(0, 10) !== day) return false;
  if (value === day) return true;
  return (
    datetime &&
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) &&
    Number.isFinite(Date.parse(value))
  );
}

/** Mirrors public limits and typed values; backend retains authorization and cost authority. */
export function validate(state: QueryState, catalog: Catalog): string[] {
  const errors = new Set<string>();
  const error = (key: string) => {
    errors.add(`query.validation.${key}`);
  };
  if (!validState(state)) return ['query.validation.invalid'];
  const limit = (key: string, fallback: number) => catalog.limits[key] ?? fallback;
  if (bytes(JSON.stringify(state)) > 16384) error('maxDefinitionBytes');
  if (length(state.search.trim()) > limit('maxSearchLength', 100)) error('maxSearchLength');
  if (state.sort.length > limit('maxSortKeys', 3)) error('maxSortKeys');
  const seen = new Set<string>();
  for (const sort of state.sort) {
    if (!catalog.fields.some((field) => field.key === sort.field && field.sortable)) error('field');
    if (seen.has(sort.field)) error('duplicateSort');
    seen.add(sort.field);
  }
  let conditions = 0;
  const scalar = (value: unknown, field: Field): boolean => {
    if (typeof value === 'string' && length(value) > limit('maxStringLength', 200))
      error('maxStringLength');
    switch (field.type) {
      case 'number':
        return (
          typeof value === 'number' &&
          Number.isFinite(value) &&
          /^-?[0-9]{1,15}(\.[0-9]{1,6})?$/.test(String(value))
        );
      case 'text':
        return text(value);
      case 'enum':
        return text(value) && !!field.enumValues?.includes(value);
      case 'tags':
        return (
          text(value) &&
          (field.enumValues
            ? field.enumValues.includes(value)
            : /^[a-z0-9][a-z0-9_.-]{0,39}$/.test(value))
        );
      case 'reference':
        return (
          text(value) &&
          /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value)
        );
      case 'date':
        return dateValue(value, false);
      case 'datetime':
        return dateValue(value, true);
      case 'boolean':
        return typeof value === 'boolean';
    }
  };
  const visit = (node: Node, depth: number) => {
    if (node.type === 'group') {
      if (depth > limit('maxDepth', 4)) error('maxDepth');
      if (node.children.length > limit('maxConditions', 25)) error('maxConditions');
      node.children.forEach((child) => visit(child, depth + 1));
      return;
    }
    conditions++;
    const field = catalog.fields.find((field) => field.key === node.field && field.filterable);
    if (!field) {
      error('field');
      return;
    }
    if (!field.operators.includes(node.op)) {
      error('operator');
      return;
    }
    const value = node.value;
    if (unaryOperators.has(node.op)) {
      if (value !== undefined && value !== null) error('value');
    } else if (relativeOperators.has(node.op)) {
      if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 3650)
        error('value');
    } else if (listOperators.has(node.op) || node.op === 'between') {
      if (
        !Array.isArray(value) ||
        value.length === 0 ||
        (node.op === 'between' && value.length !== 2)
      ) {
        error('value');
        return;
      }
      if (value.length > limit('maxInValues', 100)) error('maxInValues');
      if (!value.every((item) => scalar(item, field))) error('value');
    } else if (!scalar(value, field)) error('value');
  };
  if (state.filter.root) visit(state.filter.root, 1);
  if (conditions > limit('maxConditions', 25)) error('maxConditions');
  return [...errors];
}

/** Only exact complements. Boolean/range inversions would otherwise lose NULL rows. */
export function negate(condition: Condition, field: Field): Condition | undefined {
  const pairs: Record<string, string> = {
    equals: 'not_equals',
    not_equals: 'equals',
    contains: 'not_contains',
    not_contains: 'contains',
    in: 'not_in',
    not_in: 'in',
    is_empty: 'is_not_empty',
    is_not_empty: 'is_empty',
    has_any: 'has_none',
    has_none: 'has_any',
  };
  if (!field.nullable)
    Object.assign(pairs, {
      is_true: 'is_false',
      is_false: 'is_true',
      greater: 'less_or_equal',
      less_or_equal: 'greater',
      less: 'greater_or_equal',
      greater_or_equal: 'less',
    });
  const op = pairs[condition.op];
  return op &&
    field.key === condition.field &&
    field.operators.includes(condition.op) &&
    field.operators.includes(op)
    ? { ...condition, op }
    : undefined;
}

export const emptyGroup = (): Group => ({ type: 'group', logic: 'and', children: [] });
export const isMultiValue = (op: string) => listOperators.has(op) || op === 'between';

/** A valid starting condition for a field, using its first allowed operator. */
export function defaultCondition(field: Field): Condition {
  const op = field.operators[0] ?? 'equals';
  return { type: 'condition', field: field.key, op, ...defaultValue(field, op) };
}

/** The initial value an operator needs; unary operators carry no value. */
export function defaultValue(field: Field, op: string): { value?: unknown } {
  if (unaryOperators.has(op)) return {};
  if (relativeOperators.has(op)) return { value: 7 };
  if (isMultiValue(op)) return { value: [] };
  if (field.type === 'number') return { value: 0 };
  if (field.type === 'boolean') return { value: true };
  if (field.enumValues?.length) return { value: field.enumValues[0] };
  return { value: '' };
}

/** Replaces (or, without replacement, removes) the node at a child-index path. */
export function replaceAt(node: Node, path: readonly number[], replacement?: Node): Node {
  const [index, ...rest] = path;
  if (index === undefined) return replacement ?? emptyGroup();
  if (node.type !== 'group') return node;
  const children = node.children.flatMap((child, i): Node[] => {
    if (i !== index) return [child];
    if (rest.length === 0) return replacement ? [replacement] : [];
    return [replaceAt(child, rest, replacement)];
  });
  return { ...node, children };
}

/** Flat list of conditions with their paths, in display order. */
export function listConditions(
  node: Node | undefined,
  path: number[] = [],
): Array<{ node: Condition; path: number[] }> {
  if (!node) return [];
  if (node.type === 'condition') return [{ node, path }];
  return node.children.flatMap((child, i) => listConditions(child, [...path, i]));
}

/** Removes empty groups and unwraps single-child groups below the root so the server never sees them. */
export function normalize(node: Node | undefined): Node | undefined {
  if (!node) return node;
  if (node.type === 'condition') {
    if (!isMultiValue(node.op) || node.op === 'between' || !Array.isArray(node.value)) return node;
    return {
      ...node,
      value: node.value.filter(
        (item) => item !== '' && !(typeof item === 'number' && Number.isNaN(item)),
      ),
    };
  }
  const children = node.children.flatMap((child): Node[] => {
    const next = normalize(child);
    return next ? [next] : [];
  });
  return children.length === 0 ? undefined : { ...node, children };
}

/** Number of group levels in a tree; a bare condition has depth 0. */
export function depthOf(node: Node | undefined): number {
  if (!node || node.type === 'condition') return 0;
  return 1 + Math.max(0, ...node.children.map(depthOf));
}

/** Wraps a lone condition so group-based editing always has a root group. */
export function asGroup(node: Node | undefined): Group {
  if (!node) return emptyGroup();
  return node.type === 'group' ? node : { type: 'group', logic: 'and', children: [node] };
}

/** Inserts a copy of the node at `path` directly after it (a lone root condition becomes a group). */
export function duplicateAt(root: Node, path: readonly number[]): Node {
  if (path.length === 0)
    return { type: 'group', logic: 'and', children: [root, structuredClone(root)] };
  const parentPath = path.slice(0, -1);
  const index = path[path.length - 1] as number;
  const parent = parentPath.reduce<Node | undefined>(
    (node, i) => (node?.type === 'group' ? node.children[i] : undefined),
    root,
  );
  if (!parent || parent.type !== 'group' || !parent.children[index]) return root;
  const children = [...parent.children];
  children.splice(index + 1, 0, structuredClone(parent.children[index]));
  return replaceAt(root, parentPath, { ...parent, children });
}

/** Keys of `catalog.limits` that the matching `query.validation.*` message names. */
export const limitOfError: Record<string, string> = {
  maxDepth: 'maxDepth',
  maxConditions: 'maxConditions',
  maxSortKeys: 'maxSortKeys',
  maxInValues: 'maxInValues',
  maxStringLength: 'maxStringLength',
  maxSearchLength: 'maxSearchLength',
};
