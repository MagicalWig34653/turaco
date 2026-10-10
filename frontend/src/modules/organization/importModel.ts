import type {
  BulkOperation,
  BulkPreviewRequest,
  ExtendReason,
  ImportBatch,
  ImportKind,
  ImportMatchKey,
  ImportMode,
  PersonRow,
  RowAction,
} from './adminTypes';

/** Limits of the server (docs/product/f14-administration-design.md section 1.6); the server enforces them. */
export const IMPORT_MAX_BYTES = 2 * 1024 * 1024;
export const IMPORT_MAX_ROWS = 5000;
export const BULK_MAX_USERS = 500;

type CanFn = (permission: string) => boolean;

const managePermission: Record<ImportKind, string> = {
  users: 'organization.users.manage',
  locations: 'organization.locations.manage',
  departments: 'organization.departments.manage',
};

/** Kinds the viewer may import: `organization.import` plus the manage permission of the kind. */
export function importKinds(can: CanFn): ImportKind[] {
  if (!can('organization.import')) return [];
  return (Object.keys(managePermission) as ImportKind[]).filter((kind) =>
    can(managePermission[kind]),
  );
}

export const importModes: readonly ImportMode[] = ['create_only', 'update_only', 'upsert'];

export function matchKeysFor(kind: ImportKind): ImportMatchKey[] {
  return kind === 'users' ? ['primary_email', 'employee_number'] : ['code'];
}

/** The columns the server knows per kind (unknown columns are reported, never applied). */
export const importColumns: Record<ImportKind, readonly string[]> = {
  users: [
    'display_name',
    'given_name',
    'family_name',
    'primary_email',
    'employee_number',
    'department_code',
    'location_code',
    'manager_email',
  ],
  locations: ['code', 'name', 'kind', 'parent_code', 'description'],
  departments: ['code', 'name', 'parent_code'],
};

export type FileCheck = 'ok' | 'missing' | 'empty' | 'tooLarge' | 'notCsv';

export function checkImportFile(
  file: { name: string; size: number } | null | undefined,
): FileCheck {
  if (!file) return 'missing';
  if (file.size === 0) return 'empty';
  if (file.size > IMPORT_MAX_BYTES) return 'tooLarge';
  if (!/\.(csv|txt)$/i.test(file.name)) return 'notCsv';
  return 'ok';
}

export const rowActions: readonly RowAction[] = ['create', 'update', 'unchanged', 'reject'];

/** Issue codes the UI has a translation for; anything else is shown with the generic text. */
export const knownIssueCodes = [
  'missing_key',
  'invalid_value',
  'control_characters',
  'formula_prefix',
  'duplicate_key_in_file',
  'column_count',
  'cell_too_long',
  'display_name_required',
  'name_required',
  'already_exists',
  'not_found',
  'reference_not_found',
  'directory_owned',
  'emergency_account',
  'last_administrator',
  'self_operation',
  'dominance_required',
  'invalid_state',
  'target_inactive',
  'invalid_hierarchy',
  'conflict',
  'version_conflict',
  'parent_change_not_supported',
  'kind_change_not_supported',
  'ambiguous_key',
  'directory_user',
  'directory_identity_disabled',
] as const;

type IssueCode = (typeof knownIssueCodes)[number];

export function issueKey(code: string): `people.import.issue.${IssueCode | 'unknown'}` {
  return (knownIssueCodes as readonly string[]).includes(code)
    ? `people.import.issue.${code as IssueCode}`
    : 'people.import.issue.unknown';
}

export function count(batch: Pick<ImportBatch, 'counts'>, action: RowAction): number {
  return batch.counts[action] ?? 0;
}

export function writes(batch: Pick<ImportBatch, 'counts'>): number {
  return count(batch, 'create') + count(batch, 'update');
}

export function isExpired(batch: Pick<ImportBatch, 'expiresAt'>, now: Date): boolean {
  return Date.parse(batch.expiresAt) <= now.getTime();
}

/** A preview can be applied while it is stored, not expired and would write something. */
export function canApply(batch: ImportBatch, now: Date): boolean {
  return batch.status === 'previewed' && !isExpired(batch, now) && writes(batch) > 0;
}

// ---- bulk operations ----

export const bulkOperations: readonly BulkOperation[] = [
  'set_department',
  'set_primary_location',
  'set_manager',
  'deactivate',
];

/** The closed reason list of the deactivate operation (same as the single operation). */
export const bulkDeactivateReasons = [
  'left_organization',
  'extended_leave',
  'security_concern',
  'duplicate_account',
  'no_longer_needed',
] as const;

export type BulkDraft = {
  operation: BulkOperation;
  departmentId: string;
  locationId: string;
  manager: { id: string; label: string } | null;
  reason: string;
};

export const emptyBulkDraft: BulkDraft = {
  operation: 'set_department',
  departmentId: '',
  locationId: '',
  manager: null,
  reason: '',
};

/** Whether the draft can be sent. An empty department, location or manager means "clear". */
export function bulkDraftReady(draft: BulkDraft, selected: number): boolean {
  if (selected < 1 || selected > BULK_MAX_USERS) return false;
  return draft.operation !== 'deactivate' || draft.reason !== '';
}

export function toBulkRequest(draft: BulkDraft, userIds: readonly string[]): BulkPreviewRequest {
  const request: BulkPreviewRequest = { operation: draft.operation, userIds: [...userIds] };
  if (draft.operation === 'set_department') request.departmentId = draft.departmentId || null;
  if (draft.operation === 'set_primary_location') request.locationId = draft.locationId || null;
  if (draft.operation === 'set_manager') request.managerUserId = draft.manager?.id ?? null;
  if (draft.operation === 'deactivate') request.reason = draft.reason;
  return request;
}

/** The bulk bar offers operations only for rows the viewer may change; the server decides per row. */
export function selectablePerson(person: Pick<PersonRow, 'source'>): boolean {
  return person.source !== 'emergency';
}

// ---- directory linking and access extension ----

export type AccessAction = 'linkDirectory' | 'extendAccess';

/** Presentation only; the server authorizes both operations. */
export function accessActions(
  person: Pick<PersonRow, 'source' | 'accountKind' | 'status'>,
  can: CanFn,
): AccessAction[] {
  const actions: AccessAction[] = [];
  if (can('platform.admin') && person.source === 'local' && person.accountKind === 'employee')
    actions.push('linkDirectory');
  if (
    can('organization.external_parties.manage') &&
    person.accountKind === 'external' &&
    person.status !== 'departed'
  )
    actions.push('extendAccess');
  return actions;
}

export const extendReasons: readonly ExtendReason[] = [
  'contract_renewed',
  'project_extended',
  'sponsor_request',
  'correction',
];

export const MAX_ACCESS_DAYS = 365;

const DAY_MS = 24 * 60 * 60 * 1000;

/** Local calendar date (yyyy-mm-dd) of an instant. */
export function dateInputValue(at: Date): string {
  const month = String(at.getMonth() + 1).padStart(2, '0');
  const day = String(at.getDate()).padStart(2, '0');
  return `${at.getFullYear()}-${month}-${day}`;
}

/** Earliest selectable day: the day after the current end, and never in the past. */
export function extendMin(current: string | null | undefined, now: Date): string {
  const base = current ? Math.max(Date.parse(current), now.getTime()) : now.getTime();
  return dateInputValue(new Date(base + DAY_MS));
}

export function extendMax(now: Date): string {
  return dateInputValue(new Date(now.getTime() + MAX_ACCESS_DAYS * DAY_MS));
}

/**
 * The instant sent to the server: the end of the chosen local day, which must lie after the current end and
 * within MAX_ACCESS_DAYS of now. Returns undefined for an unusable choice.
 */
export function extendInstant(
  day: string,
  current: string | null | undefined,
  now: Date,
): string | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  if (!match) return undefined;
  const end = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]), 23, 59, 0, 0);
  if (Number.isNaN(end.getTime())) return undefined;
  if (day > extendMax(now)) return undefined;
  if (current && day <= dateInputValue(new Date(current))) return undefined;
  // The last selectable day ends a minute before the server's limit of 365 days from now.
  const limit = now.getTime() + MAX_ACCESS_DAYS * DAY_MS - 60_000;
  const instant = new Date(Math.min(end.getTime(), limit));
  if (instant.getTime() <= now.getTime()) return undefined;
  return instant.toISOString();
}
