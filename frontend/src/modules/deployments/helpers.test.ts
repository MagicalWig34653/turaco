import { describe, expect, it } from 'vitest';
import {
  availableKinds,
  breakdown,
  buildDefinition,
  definitionToForm,
  isAllDevices,
  moveItem,
  parseList,
  planActions,
  planSteps,
  ringFormValues,
  ringInput,
  ringOrderProblem,
  sameDefinition,
  soakParts,
  validateDefinition,
  validateRingForm,
  windowState,
} from './helpers';
import type { DeploymentRing, PlanIssue, TargetDefinition } from './types';

const A = '11111111-1111-4111-8111-111111111111';
const B = '22222222-2222-4222-8222-222222222222';

const empty: TargetDefinition = { filters: {}, includeDeviceIds: [], excludeDeviceIds: [] };

describe('definition builder', () => {
  it('normalizes like the API and leaves empty clauses out', () => {
    const def = buildDefinition({
      rows: [
        { kind: 'platform', values: ['windows', 'macos', 'windows'] },
        { kind: 'osVersionPrefix', text: '  10.0.2 ' },
        { kind: 'manufacturer', values: [' Lenovo', 'Dell', ''] },
        { kind: 'model', values: [] },
        {
          kind: 'groups',
          groups: [
            { externalId: 'g2', includeNested: false },
            { externalId: 'g1', includeNested: false },
            { externalId: 'g2', includeNested: true },
            { externalId: ' ', includeNested: true },
          ],
        },
        { kind: 'assetLocationIds', values: [B.toUpperCase()] },
      ],
      includeDeviceIds: [B, A.toUpperCase(), A],
      excludeDeviceIds: [],
    });
    expect(def).toEqual({
      filters: {
        platform: ['macos', 'windows'],
        osVersionPrefix: '10.0.2',
        manufacturer: ['Dell', 'Lenovo'],
        groups: [
          { externalId: 'g1', includeNested: false },
          { externalId: 'g2', includeNested: true },
        ],
        assetLocationIds: [B],
      },
      includeDeviceIds: [A, B],
      excludeDeviceIds: [],
    });
    expect(Object.keys(def.filters)).not.toContain('model');
  });

  it('round-trips a stored definition through the form', () => {
    const stored: TargetDefinition = {
      filters: { compliance: ['compliant'], ownership: ['corporate'], osVersionPrefix: '14' },
      includeDeviceIds: [],
      excludeDeviceIds: [A],
    };
    const form = definitionToForm(stored);
    expect(form.rows.map((row) => row.kind)).toEqual([
      'osVersionPrefix',
      'ownership',
      'compliance',
    ]);
    expect(buildDefinition(form)).toEqual(stored);
    expect(sameDefinition(stored, buildDefinition(form))).toBe(true);
    expect(availableKinds(form.rows)).not.toContain('ownership');
  });

  it('parses comma, semicolon and line separated lists', () => {
    expect(parseList('a, b;c\n\n d ')).toEqual(['a', 'b', 'c', 'd']);
  });

  it('detects the all-devices selection', () => {
    expect(isAllDevices(empty)).toBe(true);
    expect(isAllDevices({ ...empty, excludeDeviceIds: [A] })).toBe(true);
    expect(isAllDevices({ ...empty, includeDeviceIds: [A] })).toBe(false);
    expect(isAllDevices({ ...empty, filters: { platform: ['ios'] } })).toBe(false);
  });
});

describe('definition validation mirror', () => {
  it('accepts a valid definition', () => {
    expect(validateDefinition({ ...empty, filters: { platform: ['windows'] } })).toEqual([]);
  });

  it('reports limits, bad ids, unsafe text and include/exclude overlap', () => {
    const many = Array.from({ length: 21 }, (_, i) => `m${i}`);
    const codes = validateDefinition({
      filters: {
        model: many,
        manufacturer: ['x'.repeat(101)],
        osVersionPrefix: 'a​b',
        groups: [{ externalId: 'has space', includeNested: false }],
        assetLocationIds: ['nope'],
      },
      includeDeviceIds: [A],
      excludeDeviceIds: [A, 'bad'],
    }).map((error) => `${error.field}:${error.key}`);
    expect(codes).toEqual([
      'manufacturer:deployments.def.error.value',
      'model:deployments.def.error.tooMany',
      'osVersionPrefix:deployments.def.error.prefix',
      'groups:deployments.def.error.group',
      'assetLocationIds:deployments.def.error.uuid',
      'excludeDeviceIds:deployments.def.error.uuid',
      'excludeDeviceIds:deployments.def.error.overlap',
    ]);
  });
});

describe('evaluation breakdown', () => {
  it('sorts by count then key', () => {
    expect(breakdown({ ios: 2, windows: 9, android: 2 })).toEqual([
      ['windows', 9],
      ['android', 2],
      ['ios', 2],
    ]);
    expect(breakdown(undefined)).toEqual([]);
  });
});

const ring = (id: string, position: number, noWindowRequired = false): DeploymentRing => ({
  id,
  position,
  name: id,
  targetSetId: A,
  targetSetReference: 'TS-1',
  targetSetName: 'Pilot',
  approvalRequired: false,
  successThresholdPercent: 95,
  minFreshEvidencePercent: null,
  soakMinutes: 1440,
  changeId: noWindowRequired ? null : B,
  noWindowRequired,
  maxTargets: 5000,
});

describe('ring ordering', () => {
  it('moves an item and ignores out-of-range moves', () => {
    expect(moveItem(['a', 'b', 'c'], 0, 2)).toEqual(['b', 'c', 'a']);
    expect(moveItem(['a', 'b', 'c'], 2, 1)).toEqual(['a', 'c', 'b']);
    expect(moveItem(['a', 'b'], 0, 5)).toEqual(['a', 'b']);
  });

  it('keeps a window-less ring at the pilot position', () => {
    const rings = [ring('p', 1, true), ring('r2', 2)];
    expect(ringOrderProblem(['p', 'r2'], rings)).toBeUndefined();
    expect(ringOrderProblem(['r2', 'p'], rings)).toBe('deployments.ring.error.pilotOnly');
  });
});

describe('ring form', () => {
  it('validates gates and converts the soak unit', () => {
    const values = { ...ringFormValues(), name: 'Pilot', targetSetId: A, noWindowRequired: true };
    expect(validateRingForm(values, true)).toEqual({});
    expect(validateRingForm(values, false).noWindowRequired).toBe(
      'deployments.ring.error.pilotOnly',
    );
    expect(
      validateRingForm({ ...values, noWindowRequired: false, changeId: '' }, false).changeId,
    ).toBe('deployments.ring.error.window');
    expect(validateRingForm({ ...values, soakValue: '31', soakUnit: 'days' }, true).soakValue).toBe(
      'deployments.ring.error.soak',
    );
    expect(validateRingForm({ ...values, successThresholdPercent: '0' }, true)).toHaveProperty(
      'successThresholdPercent',
    );
    expect(
      ringInput({ ...values, soakValue: '2', soakUnit: 'hours', maxTargets: '' }),
    ).toMatchObject({
      soakMinutes: 120,
      maxTargets: null,
      changeId: null,
      minFreshEvidencePercent: null,
    });
    expect(soakParts(2880)).toEqual({ value: 2, unit: 'days' });
    expect(soakParts(90)).toEqual({ value: 90, unit: 'minutes' });
  });
});

describe('planning stepper', () => {
  it('shows the approval path only for high-impact plans', () => {
    expect(planSteps('draft', false).map((step) => step.id)).toEqual(['draft', 'scheduled']);
    expect(planSteps('pending_approval', true)).toEqual([
      { id: 'draft', state: 'done' },
      { id: 'submitted', state: 'current' },
      { id: 'approved', state: 'upcoming' },
      { id: 'scheduled', state: 'upcoming' },
    ]);
    expect(planSteps('cancelled', true).at(-1)).toEqual({ id: 'cancelled', state: 'failed' });
  });
});

describe('plan actions', () => {
  const blocking: PlanIssue = {
    code: 'window_required',
    blocking: true,
    ringId: null,
    count: null,
    deploymentId: null,
  };
  const approvalRequired: PlanIssue = { ...blocking, code: 'approval_required' };
  const can = (granted: string[]) => (permission: string) => granted.includes(permission);
  const plan = (status: string, highImpact: boolean, issues: PlanIssue[] = []) => ({
    status: status as 'draft',
    highImpact,
    validation: {
      valid: true,
      highImpact,
      evaluated: false,
      validatedAt: '',
      issues,
      rings: [],
    },
  });

  it('offers nothing without deployments.manage', () => {
    expect(planActions(plan('draft', false), can([]))).toEqual([]);
  });

  it('schedules ordinary drafts directly', () => {
    expect(planActions(plan('draft', false), can(['deployments.manage']))).toEqual([
      { id: 'schedule' },
      { id: 'cancel' },
    ]);
  });

  it('explains why high-impact plans cannot proceed', () => {
    const manager = can(['deployments.manage']);
    expect(planActions(plan('draft', true, [approvalRequired]), manager)).toEqual([
      { id: 'submit', disabledReason: 'deployments.reason.needsHighImpact' },
      { id: 'schedule', disabledReason: 'deployments.reason.needsHighImpact' },
      { id: 'cancel' },
    ]);
    const full = can(['deployments.manage', 'deployments.high_impact']);
    expect(planActions(plan('draft', true, [approvalRequired]), full)[0]).toEqual({ id: 'submit' });
    expect(planActions(plan('draft', true, [blocking]), full)[0]).toEqual({
      id: 'submit',
      disabledReason: 'deployments.reason.blocking',
    });
    expect(planActions(plan('pending_approval', true), full)).toEqual([
      { id: 'schedule', disabledReason: 'deployments.reason.approvalPending' },
      { id: 'cancel' },
    ]);
    expect(planActions(plan('approved', true), full, 'endpoints.plan_changed')[0]).toEqual({
      id: 'schedule',
      disabledReason: 'deployments.reason.planChanged',
    });
    expect(planActions(plan('scheduled', true), full)).toEqual([{ id: 'cancel' }]);
    expect(
      planActions(plan('draft', true, [approvalRequired]), full, 'endpoints.plan_changed')[0],
    ).toEqual({
      id: 'submit',
      disabledReason: 'deployments.reason.revalidate',
    });
    expect(planActions(plan('draft', false), full, 'endpoints.plan_changed')[0]).toEqual({
      id: 'schedule',
      disabledReason: 'deployments.reason.revalidate',
    });
  });
});

describe('change windows', () => {
  const change = {
    id: B,
    reference: 'CHG-1',
    status: 'scheduled',
    windowStart: '2026-10-10T08:00:00Z',
    windowEnd: '2026-10-10T10:00:00Z',
  };
  it('classifies the window against now', () => {
    expect(windowState(change, new Date('2026-10-09T00:00:00Z'))).toBe('upcoming');
    expect(windowState(change, new Date('2026-10-10T09:00:00Z'))).toBe('open');
    expect(windowState(change, new Date('2026-10-10T10:00:00Z'))).toBe('ended');
    expect(windowState(undefined, new Date())).toBe('unknown');
  });
});
