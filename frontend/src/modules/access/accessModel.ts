import type {
  EffectivePermissions,
  EffectiveRole,
  Permission,
  PermissionGroupKind,
  PermissionRisk,
  Role,
  SodRule,
  TemplateUse,
} from './types';

export const GROUP_ORDER: readonly PermissionGroupKind[] = [
  'view',
  'manage',
  'execute',
  'approve',
  'admin',
  'other',
];
export const RISK_ORDER: readonly PermissionRisk[] = ['normal', 'elevated', 'high'];

/** Module of a permission: the registry key from the server, else the first name segment. */
export function moduleOf(permission: Pick<Permission, 'name' | 'module'>): string {
  return permission.module || permission.name.split('.')[0] || permission.name;
}

export type PermissionBucket = { group: PermissionGroupKind; items: Permission[] };
export type ModuleBucket = { module: string; groups: PermissionBucket[]; items: Permission[] };

/** Module, then view/manage/execute/approve/admin/other; names sorted inside each group. */
export function groupByModule(permissions: readonly Permission[]): ModuleBucket[] {
  const modules = new Map<string, Permission[]>();
  for (const permission of permissions) {
    const key = moduleOf(permission);
    const list = modules.get(key);
    if (list) list.push(permission);
    else modules.set(key, [permission]);
  }
  return [...modules.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([module, items]) => {
      const sorted = [...items].sort((a, b) => a.name.localeCompare(b.name));
      const groups = GROUP_ORDER.map((group) => ({
        group,
        items: sorted.filter((item) => (item.group ?? 'other') === group),
      })).filter((bucket) => bucket.items.length > 0);
      return { module, groups, items: sorted };
    });
}

export type PickerFilter = { search: string; risk: PermissionRisk | ''; onlySelected: boolean };

export function filterPermissions(
  permissions: readonly Permission[],
  filter: PickerFilter,
  selected: ReadonlySet<string>,
): Permission[] {
  const needle = filter.search.trim().toLocaleLowerCase();
  return permissions.filter(
    (permission) =>
      (!filter.risk || permission.risk === filter.risk) &&
      (!filter.onlySelected || selected.has(permission.name)) &&
      (!needle ||
        permission.name.toLocaleLowerCase().includes(needle) ||
        permission.description.toLocaleLowerCase().includes(needle)),
  );
}

export type RiskCounts = Record<PermissionRisk, number>;

export function riskCounts(
  names: Iterable<string>,
  byName: ReadonlyMap<string, Permission>,
): RiskCounts {
  const counts: RiskCounts = { normal: 0, elevated: 0, high: 0 };
  for (const name of names) {
    const permission = byName.get(name);
    if (permission) counts[permission.risk] += 1;
  }
  return counts;
}

export type MissingNeed = { permission: string; needs: string[] };

/** Selected permissions whose companion permissions are not selected. */
export function missingNeeds(
  selected: ReadonlySet<string>,
  byName: ReadonlyMap<string, Permission>,
): MissingNeed[] {
  const out: MissingNeed[] = [];
  for (const name of [...selected].sort()) {
    const missing = (byName.get(name)?.needs ?? []).filter((need) => !selected.has(need));
    if (missing.length > 0) out.push({ permission: name, needs: missing });
  }
  return out;
}

export type Actor = { isAdministrator: boolean; has: (permission: string) => boolean };
export type Grantability = 'ok' | 'beyondOwn' | 'administratorOnly';

/**
 * What the signed-in person may add to a role. This mirrors the server rules (grant ceiling and high-risk
 * permissions for administrators only) to explain disabled choices; the server decides.
 */
export function grantability(
  permission: Pick<Permission, 'name' | 'risk'>,
  actor: Actor,
): Grantability {
  if (actor.isAdministrator) return 'ok';
  if (permission.risk === 'high') return 'administratorOnly';
  return actor.has(permission.name) ? 'ok' : 'beyondOwn';
}

export type PermissionDiff = { added: string[]; removed: string[] };

export function diffPermissions(before: Iterable<string>, after: Iterable<string>): PermissionDiff {
  const previous = new Set(before);
  const next = new Set(after);
  return {
    added: [...next].filter((name) => !previous.has(name)).sort(),
    removed: [...previous].filter((name) => !next.has(name)).sort(),
  };
}

/** Rules for which the selection holds at least one permission from each side. */
export function sodConflicts(selected: ReadonlySet<string>, rules: readonly SodRule[]): SodRule[] {
  return rules.filter(
    (rule) =>
      rule.left.some((name) => selected.has(name)) && rule.right.some((name) => selected.has(name)),
  );
}

export function sameSet(a: ReadonlySet<string>, b: readonly string[]): boolean {
  return a.size === b.length && b.every((value) => a.has(value));
}

// ---- Assignments and expiry --------------------------------------------------------------------

export const MAX_HIGH_RISK_EXPIRY_DAYS = 366;
const DAY = 86_400_000;

export type ExpiryState = 'none' | 'active' | 'soon' | 'expired';

export function expiryState(
  expiresAt: string | undefined | null,
  now: number,
  soonDays = 14,
): ExpiryState {
  if (!expiresAt) return 'none';
  const time = Date.parse(expiresAt);
  if (Number.isNaN(time)) return 'none';
  if (time <= now) return 'expired';
  return time - now <= soonDays * DAY ? 'soon' : 'active';
}

export type ExpiryProblem = 'past' | 'tooFar' | 'administratorNoExpiry';

/** `date` is a calendar day (YYYY-MM-DD); the assignment ends at the end of that day in local time. */
export function validateExpiry(
  date: string,
  role: Pick<Role, 'builtIn' | 'permissions' | 'key'> & { builtInAdmin?: boolean },
  byName: ReadonlyMap<string, Permission>,
  now: number,
): ExpiryProblem | null {
  if (date === '') return null;
  if (role.builtInAdmin || role.key === 'platform-administrator') return 'administratorNoExpiry';
  const end = endOfDay(date);
  if (end === undefined || end <= now) return 'past';
  const highRisk = role.permissions.some((name) => byName.get(name)?.risk === 'high');
  if (highRisk && end - now > MAX_HIGH_RISK_EXPIRY_DAYS * DAY) return 'tooFar';
  return null;
}

/** RFC 3339 instant for the end of a local calendar day. */
export function endOfDayIso(date: string): string | undefined {
  const end = endOfDay(date);
  return end === undefined ? undefined : new Date(end).toISOString();
}

function endOfDay(date: string): number | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  if (!match) return undefined;
  const [, year, month, day] = match;
  const value = new Date(Number(year), Number(month) - 1, Number(day), 23, 59, 59, 0).getTime();
  return Number.isNaN(value) ? undefined : value;
}

// ---- Templates and keys ------------------------------------------------------------------------

export function templateDrift(use: Pick<TemplateUse, 'missing' | 'extra'>): {
  behind: number;
  extra: number;
} {
  return { behind: use.missing.length, extra: use.extra.length };
}

/** A role key proposal from a name: lowercase letters, digits and dashes (2 to 63 characters). */
export function proposeRoleKey(name: string): string {
  const folded = name
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .replace(/ß/g, 'ss')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
    .replace(/-+$/g, '');
  return folded.length >= 2 ? folded : '';
}

// ---- Effective permissions ---------------------------------------------------------------------

export type GrantReason = Pick<
  EffectiveRole,
  'roleKey' | 'roleName' | 'source' | 'expiresAt' | 'groupId'
>;

/** The roles (and their path: direct or Directory Group) that grant one permission. */
export function grantReasons(effective: EffectivePermissions, permission: string): GrantReason[] {
  const entry = effective.permissions.find((candidate) => candidate.name === permission);
  if (!entry) return [];
  const byAssignment = new Map(effective.roles.map((role) => [role.assignmentId, role]));
  const out: GrantReason[] = [];
  for (const id of entry.grantedBy) {
    const role = byAssignment.get(id);
    if (role) out.push(role);
  }
  return out;
}

export type EffectiveFilter = { search: string; risk: PermissionRisk | '' };

export function filterEffective(effective: EffectivePermissions, filter: EffectiveFilter) {
  const needle = filter.search.trim().toLocaleLowerCase();
  return effective.permissions.filter(
    (permission) =>
      (!filter.risk || permission.risk === filter.risk) &&
      (!needle ||
        permission.name.toLocaleLowerCase().includes(needle) ||
        permission.module.includes(needle)),
  );
}
