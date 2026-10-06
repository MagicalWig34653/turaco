import { messages, type MessageKey } from '../../platform/i18n/i18n';
import type {
  ChangeWindow,
  DeploymentDetail,
  DeploymentRing,
  DeploymentStatus,
  PlanIssue,
  TargetDefinition,
  TargetGroup,
} from './types';

export type Tone = 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'unknown';

/** Limits mirrored from the backend definition rules (application/targetset_definition.go). */
export const definitionLimits = {
  filterValues: 20,
  groups: 20,
  explicitDevices: 500,
  valueLength: 100,
  osPrefixLength: 50,
  groupIdLength: 200,
} as const;

/** Ring limits mirrored from application/deployment_types.go. */
export const ringLimits = { rings: 10, maxTargets: 5000, soakMinutes: 30 * 24 * 60 } as const;

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const isUuid = (value: string) => uuidPattern.test(value.trim());

// ---- Target Set definition: structured filter rows ----

export const filterKinds = [
  'platform',
  'osVersionPrefix',
  'ownership',
  'compliance',
  'manufacturer',
  'model',
  'groups',
  'assetLocationIds',
] as const;
export type FilterKind = (typeof filterKinds)[number];

/** One editable filter row. List kinds keep values; the prefix keeps text; groups keep group rows. */
export type FilterRow =
  | { kind: 'osVersionPrefix'; text: string }
  | { kind: 'groups'; groups: TargetGroup[] }
  | {
      kind: Exclude<FilterKind, 'osVersionPrefix' | 'groups'>;
      values: string[];
    };

export type DefinitionForm = {
  rows: FilterRow[];
  includeDeviceIds: string[];
  excludeDeviceIds: string[];
};

export function emptyRow(kind: FilterKind): FilterRow {
  if (kind === 'osVersionPrefix') return { kind, text: '' };
  if (kind === 'groups') return { kind, groups: [{ externalId: '', includeNested: false }] };
  return { kind, values: [] };
}

/** Kinds that can still be added (each kind appears at most once). */
export function availableKinds(rows: readonly FilterRow[]): FilterKind[] {
  const used = new Set(rows.map((row) => row.kind));
  return filterKinds.filter((kind) => !used.has(kind));
}

/** Splits free text on commas, semicolons and line breaks; trims and drops empty entries. */
export function parseList(text: string): string[] {
  return text
    .split(/[,;\n]/)
    .map((value) => value.trim())
    .filter(Boolean);
}

const uniqueSorted = (values: readonly string[]) =>
  [...new Set(values)].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));

const cleanIds = (values: readonly string[]) =>
  uniqueSorted(values.map((value) => value.trim().toLowerCase()).filter(Boolean));

/**
 * Builds the strict API definition from form rows: trims, de-duplicates, sorts, lower-cases ids and
 * leaves empty clauses out, like the backend normalization. Only known fields are emitted.
 */
export function buildDefinition(form: DefinitionForm): TargetDefinition {
  const filters: TargetDefinition['filters'] = {};
  for (const row of form.rows) {
    if (row.kind === 'osVersionPrefix') {
      const prefix = row.text.trim();
      if (prefix) filters.osVersionPrefix = prefix;
    } else if (row.kind === 'groups') {
      const byId = new Map<string, boolean>();
      for (const group of row.groups) {
        const id = group.externalId.trim();
        if (id) byId.set(id, (byId.get(id) ?? false) || group.includeNested);
      }
      const groups = uniqueSorted([...byId.keys()]).map((externalId) => ({
        externalId,
        includeNested: byId.get(externalId) ?? false,
      }));
      if (groups.length) filters.groups = groups;
    } else if (row.kind === 'assetLocationIds') {
      const ids = cleanIds(row.values);
      if (ids.length) filters.assetLocationIds = ids;
    } else {
      const values = uniqueSorted(row.values.map((value) => value.trim()).filter(Boolean));
      if (values.length) filters[row.kind] = values;
    }
  }
  return {
    filters,
    includeDeviceIds: cleanIds(form.includeDeviceIds),
    excludeDeviceIds: cleanIds(form.excludeDeviceIds),
  };
}

/** Turns a stored definition back into editable rows (in the canonical kind order). */
export function definitionToForm(definition: TargetDefinition | undefined): DefinitionForm {
  const f = definition?.filters ?? {};
  const rows: FilterRow[] = [];
  for (const kind of filterKinds) {
    if (kind === 'osVersionPrefix') {
      if (f.osVersionPrefix) rows.push({ kind, text: f.osVersionPrefix });
    } else if (kind === 'groups') {
      if (f.groups?.length) rows.push({ kind, groups: f.groups.map((group) => ({ ...group })) });
    } else {
      const values = f[kind];
      if (values?.length) rows.push({ kind, values: [...values] });
    }
  }
  return {
    rows,
    includeDeviceIds: [...(definition?.includeDeviceIds ?? [])],
    excludeDeviceIds: [...(definition?.excludeDeviceIds ?? [])],
  };
}

/** Mirrors the backend rule: no filter and no explicit include selects every Device. */
export function isAllDevices(definition: TargetDefinition): boolean {
  const f = definition.filters;
  const hasFilter =
    !!f.osVersionPrefix ||
    [f.platform, f.ownership, f.compliance, f.manufacturer, f.model, f.groups, f.assetLocationIds]
      .filter((list) => list !== undefined)
      .some((list) => list.length > 0);
  return !hasFilter && definition.includeDeviceIds.length === 0;
}

export type DefinitionError = {
  field: FilterKind | 'includeDeviceIds' | 'excludeDeviceIds';
  key: MessageKey;
  params?: Record<string, string | number>;
};

const unsafeText = /[\p{Cc}\p{Cf}]/u;

/** Client-side mirror of the backend definition validation; the API stays authoritative. */
export function validateDefinition(definition: TargetDefinition): DefinitionError[] {
  const errors: DefinitionError[] = [];
  const f = definition.filters;
  const { filterValues, groups, explicitDevices, valueLength, osPrefixLength, groupIdLength } =
    definitionLimits;
  for (const kind of ['platform', 'ownership', 'compliance', 'manufacturer', 'model'] as const) {
    const values = f[kind] ?? [];
    if (values.length > filterValues)
      errors.push({
        field: kind,
        key: 'deployments.def.error.tooMany',
        params: { max: filterValues },
      });
    if (
      (kind === 'manufacturer' || kind === 'model') &&
      values.some((value) => value.length > valueLength || unsafeText.test(value))
    )
      errors.push({
        field: kind,
        key: 'deployments.def.error.value',
        params: { max: valueLength },
      });
  }
  const prefix = f.osVersionPrefix ?? '';
  if (prefix.length > osPrefixLength || unsafeText.test(prefix))
    errors.push({
      field: 'osVersionPrefix',
      key: 'deployments.def.error.prefix',
      params: { max: osPrefixLength },
    });
  const groupList = f.groups ?? [];
  if (groupList.length > groups)
    errors.push({ field: 'groups', key: 'deployments.def.error.tooMany', params: { max: groups } });
  if (
    groupList.some(
      (group) =>
        group.externalId.length > groupIdLength ||
        /[\s]/.test(group.externalId) ||
        unsafeText.test(group.externalId),
    )
  )
    errors.push({
      field: 'groups',
      key: 'deployments.def.error.group',
      params: { max: groupIdLength },
    });
  const locations = f.assetLocationIds ?? [];
  if (locations.length > filterValues)
    errors.push({
      field: 'assetLocationIds',
      key: 'deployments.def.error.tooMany',
      params: { max: filterValues },
    });
  if (locations.some((id) => !isUuid(id)))
    errors.push({ field: 'assetLocationIds', key: 'deployments.def.error.uuid' });
  for (const field of ['includeDeviceIds', 'excludeDeviceIds'] as const) {
    const ids = definition[field];
    if (ids.length > explicitDevices)
      errors.push({
        field,
        key: 'deployments.def.error.tooMany',
        params: { max: explicitDevices },
      });
    if (ids.some((id) => !isUuid(id))) errors.push({ field, key: 'deployments.def.error.uuid' });
  }
  const excluded = new Set(definition.excludeDeviceIds);
  if (definition.includeDeviceIds.some((id) => excluded.has(id)))
    errors.push({ field: 'excludeDeviceIds', key: 'deployments.def.error.overlap' });
  return errors;
}

/** Stable comparison of two definitions after normalization (dirty check for the preview). */
export function sameDefinition(a: TargetDefinition, b: TargetDefinition): boolean {
  const canonical = (d: TargetDefinition) =>
    JSON.stringify(buildDefinition(definitionToForm(d)), (_key, value: unknown) =>
      value && typeof value === 'object' && !Array.isArray(value)
        ? Object.fromEntries(
            Object.entries(value as Record<string, unknown>).sort(([x], [y]) =>
              x < y ? -1 : x > y ? 1 : 0,
            ),
          )
        : value,
    );
  return canonical(a) === canonical(b);
}

/** Breakdown entries, largest first, then by key. */
export function breakdown(counts: Record<string, number> | undefined): [string, number][] {
  return Object.entries(counts ?? {}).sort(([ka, a], [kb, b]) => b - a || (ka < kb ? -1 : 1));
}

// ---- Deployments ----

export function deploymentTone(status: DeploymentStatus | string): Tone {
  switch (status) {
    case 'draft':
      return 'neutral';
    case 'pending_approval':
      return 'warning';
    case 'approved':
      return 'info';
    case 'scheduled':
      return 'success';
    case 'cancelled':
      return 'neutral';
    default:
      return 'unknown';
  }
}

export function approvalTone(status: string): Tone {
  return status === 'approved'
    ? 'success'
    : status === 'rejected'
      ? 'danger'
      : status === 'pending'
        ? 'warning'
        : 'neutral';
}

export type StepState = 'done' | 'current' | 'upcoming' | 'failed';
export type PlanStep = { id: DeploymentStatus | 'submitted'; state: StepState };

/**
 * Planning stepper: draft → submitted → approved → scheduled for high-impact plans, draft → scheduled
 * otherwise. Only the actual current state is highlighted; a cancelled plan ends in a cancelled step.
 */
export function planSteps(status: DeploymentStatus, highImpact: boolean): PlanStep[] {
  const order: PlanStep['id'][] = highImpact
    ? ['draft', 'submitted', 'approved', 'scheduled']
    : ['draft', 'scheduled'];
  if (status === 'cancelled')
    return [
      ...order.map((id) => ({ id, state: 'upcoming' as const })),
      { id: 'cancelled', state: 'failed' },
    ];
  const current = status === 'pending_approval' ? 'submitted' : status;
  const index = order.indexOf(current);
  return order.map((id, i) => ({
    id,
    state:
      i < index ? 'done' : i === index ? (id === 'scheduled' ? 'done' : 'current') : 'upcoming',
  }));
}

export const blockingIssues = (issues: readonly PlanIssue[], ...except: string[]) =>
  issues.filter((issue) => issue.blocking && !except.includes(issue.code));
export const warningIssues = (issues: readonly PlanIssue[]) =>
  issues.filter((issue) => !issue.blocking);
export const ringIssues = (issues: readonly PlanIssue[], ringId: string) =>
  issues.filter((issue) => issue.ringId === ringId);

export type PlanAction = { id: 'submit' | 'schedule' | 'cancel'; disabledReason?: MessageKey };

/**
 * The planning operations shown for a plan, with the reason when one is not possible now. The API
 * still decides; this mirrors its rules so the screen can explain them.
 */
export function planActions(
  plan: Pick<DeploymentDetail, 'status' | 'highImpact' | 'validation'>,
  can: (permission: string) => boolean,
  lastErrorCode?: string,
): PlanAction[] {
  if (!can('deployments.manage')) return [];
  const actions: PlanAction[] = [];
  const highImpactAllowed = can('deployments.high_impact');
  const blocking = blockingIssues(plan.validation.issues, 'approval_required');
  const reason = (...checks: [boolean, MessageKey][]) => checks.find(([failed]) => failed)?.[1];
  if (plan.status === 'draft' && plan.highImpact) {
    const disabledReason = reason(
      [!highImpactAllowed, 'deployments.reason.needsHighImpact'],
      [lastErrorCode === 'endpoints.plan_changed', 'deployments.reason.revalidate'],
      [blocking.length > 0, 'deployments.reason.blocking'],
    );
    actions.push({ id: 'submit', ...(disabledReason ? { disabledReason } : {}) });
  }
  if (plan.status !== 'cancelled' && plan.status !== 'scheduled') {
    const disabledReason = reason(
      [plan.highImpact && !highImpactAllowed, 'deployments.reason.needsHighImpact'],
      [plan.status === 'pending_approval', 'deployments.reason.approvalPending'],
      [plan.highImpact && plan.status === 'draft', 'deployments.reason.approvalFirst'],
      [
        lastErrorCode === 'endpoints.plan_changed',
        plan.status === 'approved'
          ? 'deployments.reason.planChanged'
          : 'deployments.reason.revalidate',
      ],
      [blocking.length > 0, 'deployments.reason.blocking'],
    );
    actions.push({ id: 'schedule', ...(disabledReason ? { disabledReason } : {}) });
  }
  if (plan.status !== 'cancelled') actions.push({ id: 'cancel' });
  return actions;
}

// ---- Rings ----

export const sortRings = (rings: readonly DeploymentRing[]) =>
  [...rings].sort((a, b) => a.position - b.position);

/** Moves the id at `from` to `to` and returns the new order (unchanged when out of range). */
export function moveItem<T>(items: readonly T[], from: number, to: number): T[] {
  if (from === to || from < 0 || to < 0 || from >= items.length || to >= items.length)
    return [...items];
  const next = [...items];
  const [moved] = next.splice(from, 1);
  next.splice(to, 0, moved as T);
  return next;
}

/** Only the pilot (position 1) may run without a maintenance window; mirrors the reorder rule. */
export function ringOrderProblem(
  order: readonly string[],
  rings: readonly DeploymentRing[],
): MessageKey | undefined {
  const byId = new Map(rings.map((ring) => [ring.id, ring]));
  return order.some((id, index) => index > 0 && byId.get(id)?.noWindowRequired)
    ? 'deployments.ring.error.pilotOnly'
    : undefined;
}

/** Soak duration in the largest whole unit. */
export function soakParts(minutes: number): { value: number; unit: 'days' | 'hours' | 'minutes' } {
  if (minutes > 0 && minutes % 1440 === 0) return { value: minutes / 1440, unit: 'days' };
  if (minutes > 0 && minutes % 60 === 0) return { value: minutes / 60, unit: 'hours' };
  return { value: minutes, unit: 'minutes' };
}

export function soakMinutes(value: number, unit: 'days' | 'hours' | 'minutes'): number {
  return Math.round(value * (unit === 'days' ? 1440 : unit === 'hours' ? 60 : 1));
}

export type WindowState = 'open' | 'upcoming' | 'ended' | 'unknown';

/** Whether a Change window is open now, still ahead or over. */
export function windowState(change: ChangeWindow | undefined, now: Date): WindowState {
  if (!change?.windowStart || !change.windowEnd) return 'unknown';
  const start = Date.parse(change.windowStart);
  const end = Date.parse(change.windowEnd);
  if (Number.isNaN(start) || Number.isNaN(end)) return 'unknown';
  if (now.getTime() >= end) return 'ended';
  return now.getTime() >= start ? 'open' : 'upcoming';
}

export type RingFormValues = {
  name: string;
  targetSetId: string;
  approvalRequired: boolean;
  successThresholdPercent: string;
  minFreshEvidencePercent: string;
  soakValue: string;
  soakUnit: 'days' | 'hours' | 'minutes';
  changeId: string;
  noWindowRequired: boolean;
  maxTargets: string;
};

export type RingFormErrors = Partial<Record<keyof RingFormValues, MessageKey>>;

const intIn = (text: string, min: number, max: number) => {
  if (!/^\d+$/.test(text.trim())) return false;
  const n = Number(text);
  return n >= min && n <= max;
};

/** Mirrors the backend ring checks; `pilot` is true for position 1. */
export function validateRingForm(values: RingFormValues, pilot: boolean): RingFormErrors {
  const errors: RingFormErrors = {};
  const name = values.name.trim();
  if (!name || name.length > 100 || unsafeText.test(name))
    errors.name = 'deployments.ring.error.name';
  if (!values.targetSetId) errors.targetSetId = 'deployments.ring.error.targetSet';
  if (!intIn(values.successThresholdPercent, 1, 100))
    errors.successThresholdPercent = 'deployments.ring.error.percent';
  if (values.minFreshEvidencePercent.trim() && !intIn(values.minFreshEvidencePercent, 1, 100))
    errors.minFreshEvidencePercent = 'deployments.ring.error.percent';
  const soak = Number(values.soakValue);
  if (
    !/^\d+$/.test(values.soakValue.trim()) ||
    soakMinutes(soak, values.soakUnit) > ringLimits.soakMinutes
  )
    errors.soakValue = 'deployments.ring.error.soak';
  if (values.maxTargets.trim() && !intIn(values.maxTargets, 1, ringLimits.maxTargets))
    errors.maxTargets = 'deployments.ring.error.maxTargets';
  if (values.noWindowRequired && !pilot)
    errors.noWindowRequired = 'deployments.ring.error.pilotOnly';
  if (!values.noWindowRequired && !values.changeId)
    errors.changeId = 'deployments.ring.error.window';
  return errors;
}

export function ringFormValues(ring?: DeploymentRing): RingFormValues {
  const soak = soakParts(ring?.soakMinutes ?? 1440);
  return {
    name: ring?.name ?? '',
    targetSetId: ring?.targetSetId ?? '',
    approvalRequired: ring?.approvalRequired ?? false,
    successThresholdPercent: String(ring?.successThresholdPercent ?? 95),
    minFreshEvidencePercent:
      ring?.minFreshEvidencePercent != null ? String(ring.minFreshEvidencePercent) : '',
    soakValue: String(soak.value),
    soakUnit: soak.unit,
    changeId: ring?.changeId ?? '',
    noWindowRequired: ring?.noWindowRequired ?? false,
    maxTargets: ring ? String(ring.maxTargets) : '',
  };
}

export function ringInput(values: RingFormValues) {
  return {
    name: values.name.trim(),
    targetSetId: values.targetSetId,
    approvalRequired: values.approvalRequired,
    successThresholdPercent: Number(values.successThresholdPercent),
    minFreshEvidencePercent: values.minFreshEvidencePercent.trim()
      ? Number(values.minFreshEvidencePercent)
      : null,
    soakMinutes: soakMinutes(Number(values.soakValue), values.soakUnit),
    changeId: values.noWindowRequired ? null : values.changeId || null,
    noWindowRequired: values.noWindowRequired,
    maxTargets: values.maxTargets.trim() ? Number(values.maxTargets) : null,
  };
}

/** The plan is high impact by its own fields (an all-Devices ring is decided by the API). */
export const intentIsHighImpact = (intent: string, supersede: boolean) =>
  intent === 'uninstall' || supersede;

/** Whether a message key exists; the API may add codes before the UI knows them. */
export const hasMessage = (key: string): key is MessageKey => key in messages.en;

/** Key for a code under a prefix, or the fallback key for codes this UI does not know yet. */
export function codeKey(
  prefix: string,
  code: string | null | undefined,
  fallback: MessageKey,
): MessageKey {
  const key = `${prefix}.${code ?? ''}`;
  return code && hasMessage(key) ? key : fallback;
}
