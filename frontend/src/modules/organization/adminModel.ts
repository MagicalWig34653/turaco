import type { FieldOwner, OrgNode, PersonRow, ProfileUpdate } from './adminTypes';

/** Shape of every `POST /{resource}/query` answer. */
export type QueryPageResult<T> = {
  items: T[];
  nextCursor?: string;
  count?: number;
  countCapped?: boolean;
  warnings?: { code: string; path: string }[];
};

/** Closed reason lists of the status operations (the server rejects any other code). */
export const deactivateReasons = [
  'left_organization',
  'extended_leave',
  'security_concern',
  'duplicate_account',
  'no_longer_needed',
] as const;
export const reactivateReasons = ['returned', 'mistake', 'contract_renewed'] as const;
export const departedReasons = ['left_organization', 'contract_ended', 'retired'] as const;

export type StatusOperation = 'deactivate' | 'reactivate' | 'markDeparted';

export type ReasonCode =
  | (typeof deactivateReasons)[number]
  | (typeof reactivateReasons)[number]
  | (typeof departedReasons)[number];

export const reasonsFor = (operation: StatusOperation): readonly ReasonCode[] =>
  operation === 'deactivate'
    ? deactivateReasons
    : operation === 'reactivate'
      ? reactivateReasons
      : departedReasons;

export const isEmergencyAccount = (person: Pick<PersonRow, 'source'>) =>
  person.source === 'emergency';
export const isLocalAccount = (person: Pick<PersonRow, 'source'>) => person.source === 'local';
export const isDirectoryAccount = (person: Pick<PersonRow, 'source'>) =>
  person.source === 'directory';

/** Ownership of one attribute, as reported by `GET /users/{id}`; unknown attributes count as platform-owned. */
export function ownerOf(fields: readonly FieldOwner[], key: string): FieldOwner {
  return fields.find((field) => field.key === key) ?? { key, owner: 'platform', source: 'turaco' };
}

/** A directory-owned attribute of a directory-linked person cannot be edited in Turaco. */
export function isFieldLocked(fields: readonly FieldOwner[], key: string): boolean {
  return ownerOf(fields, key).owner === 'directory';
}

export type LifecycleAction =
  'deactivate' | 'reactivate' | 'markDeparted' | 'sendInvitation' | 'resetPassword';

/**
 * Lifecycle actions worth offering for a person. This is presentation only: emergency accounts have a
 * command-line lifecycle, local sign-in needs a local account, and the server authorizes every call.
 */
export function lifecycleActions(
  person: Pick<PersonRow, 'source' | 'status'>,
  canManage: boolean,
): LifecycleAction[] {
  if (!canManage || isEmergencyAccount(person)) return [];
  const actions: LifecycleAction[] = [];
  if (isLocalAccount(person) && person.status === 'active')
    actions.push('sendInvitation', 'resetPassword');
  if (person.status === 'active') actions.push('deactivate', 'markDeparted');
  else if (person.status === 'inactive') actions.push('reactivate', 'markDeparted');
  else if (person.status === 'departed') actions.push('reactivate');
  return actions;
}

export type StatusTone = 'success' | 'warning' | 'neutral' | 'unknown';
export function statusTone(status: string): StatusTone {
  if (status === 'active') return 'success';
  if (status === 'inactive') return 'warning';
  if (status === 'departed') return 'neutral';
  return 'unknown';
}

/** Only the attributes that changed, so a stale edit never overwrites an unrelated field. */
export function profileChanges(
  current: Pick<
    PersonRow,
    'displayName' | 'givenName' | 'familyName' | 'primaryEmail' | 'employeeNumber' | 'version'
  >,
  form: {
    displayName: string;
    givenName: string;
    familyName: string;
    primaryEmail: string;
    employeeNumber: string;
  },
  locked: ReadonlySet<string> = new Set(),
): ProfileUpdate | null {
  const body: ProfileUpdate = { expectedVersion: current.version };
  let changed = false;
  const text = (value: string | null | undefined) => (value ?? '').trim();
  const optional = (value: string) => (value.trim() === '' ? null : value.trim());
  if (!locked.has('displayName') && form.displayName.trim() !== text(current.displayName)) {
    body.displayName = form.displayName.trim();
    changed = true;
  }
  if (!locked.has('givenName') && form.givenName.trim() !== text(current.givenName)) {
    body.givenName = optional(form.givenName);
    changed = true;
  }
  if (!locked.has('familyName') && form.familyName.trim() !== text(current.familyName)) {
    body.familyName = optional(form.familyName);
    changed = true;
  }
  if (!locked.has('primaryEmail') && form.primaryEmail.trim() !== text(current.primaryEmail)) {
    body.primaryEmail = optional(form.primaryEmail);
    changed = true;
  }
  if (
    !locked.has('employeeNumber') &&
    form.employeeNumber.trim() !== text(current.employeeNumber)
  ) {
    body.employeeNumber = optional(form.employeeNumber);
    changed = true;
  }
  return changed ? body : null;
}

/** Light client check; the server parses addresses strictly. */
export function looksLikeEmail(value: string): boolean {
  return /^[^\s@<>"]+@[^\s@<>"]+\.[^\s@<>"]+$/.test(value.trim());
}

/** Default display name for the create wizard: "Given Family". */
export function composeDisplayName(givenName: string, familyName: string): string {
  return [givenName.trim(), familyName.trim()].filter(Boolean).join(' ');
}

// ---- Set password page -------------------------------------------------------------------------

export type TokenFragment = { token: string; purpose: 'invitation' | 'reset' | 'unknown' };

/** Reads `#token=...&purpose=...` of an invitation or reset link; null when there is no token. */
export function parseTokenFragment(hash: string): TokenFragment | null {
  const params = new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash);
  const token = params.get('token')?.trim() ?? '';
  if (token === '') return null;
  const purpose = params.get('purpose');
  return { token, purpose: purpose === 'invitation' || purpose === 'reset' ? purpose : 'unknown' };
}

export const MIN_LOCAL_PASSWORD_LENGTH = 12;

export type PasswordCheck = { longEnough: boolean; matches: boolean; ok: boolean };

/** Client-side hints only; the server applies the real policy (common passwords, name and email checks). */
export function checkPassword(password: string, confirmation: string): PasswordCheck {
  const longEnough = [...password].length >= MIN_LOCAL_PASSWORD_LENGTH;
  const matches = password !== '' && password === confirmation;
  return { longEnough, matches, ok: longEnough && matches };
}

// ---- Trees (Locations and Departments) ---------------------------------------------------------

export type TreeNode<T extends OrgNode> = {
  item: T;
  depth: number;
  children: TreeNode<T>[];
  /** Number of descendants that are shown (active, or all when inactive ones are included). */
  descendants: number;
};

const byName = (a: OrgNode, b: OrgNode) =>
  a.name.localeCompare(b.name, undefined, { sensitivity: 'base' });

/** Builds a forest from a flat list; items whose parent is missing become roots. */
export function buildTree<T extends OrgNode>(
  items: readonly T[],
  includeInactive: boolean,
): TreeNode<T>[] {
  const visible = items.filter((item) => includeInactive || item.active);
  const ids = new Set(visible.map((item) => item.id));
  const children = new Map<string, T[]>();
  const roots: T[] = [];
  for (const item of visible) {
    if (item.parentId && ids.has(item.parentId)) {
      const list = children.get(item.parentId);
      if (list) list.push(item);
      else children.set(item.parentId, [item]);
    } else roots.push(item);
  }
  const build = (item: T, depth: number, seen: Set<string>): TreeNode<T> => {
    const nextSeen = new Set(seen).add(item.id);
    const kids = (children.get(item.id) ?? [])
      .filter((child) => !nextSeen.has(child.id))
      .sort(byName)
      .map((child) => build(child, depth + 1, nextSeen));
    return {
      item,
      depth,
      children: kids,
      descendants: kids.reduce((sum, kid) => sum + 1 + kid.descendants, 0),
    };
  };
  return roots.sort(byName).map((root) => build(root, 1, new Set()));
}

/** Depth-first list of the nodes that are visible given the expanded set. */
export function flattenTree<T extends OrgNode>(
  forest: readonly TreeNode<T>[],
  expanded: ReadonlySet<string>,
): TreeNode<T>[] {
  const out: TreeNode<T>[] = [];
  const visit = (node: TreeNode<T>) => {
    out.push(node);
    if (expanded.has(node.item.id)) node.children.forEach(visit);
  };
  forest.forEach(visit);
  return out;
}

export function findNode<T extends OrgNode>(
  forest: readonly TreeNode<T>[],
  id: string,
): TreeNode<T> | undefined {
  for (const node of forest) {
    if (node.item.id === id) return node;
    const found = findNode(node.children, id);
    if (found) return found;
  }
  return undefined;
}

/** Ids of the node itself and everything below it. */
export function subtreeIds<T extends OrgNode>(node: TreeNode<T>): Set<string> {
  const ids = new Set<string>([node.item.id]);
  for (const child of node.children) for (const id of subtreeIds(child)) ids.add(id);
  return ids;
}

/** Number of levels of a subtree (a leaf has height 1). */
export function subtreeHeight<T extends OrgNode>(node: TreeNode<T>): number {
  return 1 + node.children.reduce((max, child) => Math.max(max, subtreeHeight(child)), 0);
}

export type MoveTarget<T extends OrgNode> = {
  node: TreeNode<T> | null;
  label: string;
  depth: number;
};

/**
 * Parents a node may move below: not itself, not one of its descendants and, when a depth limit
 * applies, not so deep that the moved subtree would exceed it. `null` (the root) is offered when allowed.
 */
export function moveTargets<T extends OrgNode>(
  forest: readonly TreeNode<T>[],
  moving: TreeNode<T>,
  options: { maxDepth?: number; allowRoot: boolean },
): MoveTarget<T>[] {
  const blocked = subtreeIds(moving);
  const height = subtreeHeight(moving);
  const out: MoveTarget<T>[] = [];
  if (options.allowRoot) out.push({ node: null, label: '', depth: 0 });
  const visit = (node: TreeNode<T>, trail: string[]) => {
    if (blocked.has(node.item.id)) return;
    const fits = options.maxDepth === undefined || node.depth + height <= options.maxDepth;
    if (fits && node.item.active)
      out.push({ node, label: [...trail, node.item.name].join(' / '), depth: node.depth });
    node.children.forEach((child) => visit(child, [...trail, node.item.name]));
  };
  forest.forEach((root) => visit(root, []));
  return out;
}

/** "Site / Area / Room" for a node id, from the flat list. */
export function pathLabel(items: readonly OrgNode[], id: string | null | undefined): string {
  if (!id) return '';
  const byId = new Map(items.map((item) => [item.id, item]));
  const names: string[] = [];
  const seen = new Set<string>();
  let current = byId.get(id);
  while (current && !seen.has(current.id)) {
    seen.add(current.id);
    names.unshift(current.name);
    current = current.parentId ? byId.get(current.parentId) : undefined;
  }
  return names.join(' / ');
}

export const LOCATION_MAX_DEPTH = 4;

/** Initials for avatars. */
export function initials(name: string): string {
  return (
    name
      .match(/\p{L}+/gu)
      ?.slice(0, 2)
      .map((part) => part[0]?.toUpperCase())
      .join('') ?? ''
  );
}
