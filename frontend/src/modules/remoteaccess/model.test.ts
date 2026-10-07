import { describe, expect, it } from 'vitest';
import {
  activeSession,
  canLaunch,
  canRecordConsent,
  isProviderLaunchLink,
  providerAvailability,
  reasonKey,
  reasonKeys,
  sessionFacts,
  startableProviders,
  stepperState,
} from './model';
import type { Capabilities, RemoteSession } from './types';

const session = (patch: Partial<RemoteSession> = {}): RemoteSession => ({
  id: 's1',
  reference: 'RA-1',
  deviceId: 'd1',
  ticketId: 't1',
  provider: 'rustdesk',
  mode: 'attended',
  status: 'authorized',
  statusReason: null,
  initiatedBy: 'u1',
  approvalId: null,
  consent: 'unknown',
  consentRecordedBy: null,
  consentRecordedAt: null,
  mismatchReason: null,
  launchedAt: null,
  closedAt: null,
  expiresAt: '2026-10-07T10:00:00Z',
  note: null,
  observedConnectedAt: null,
  observedEndedAt: null,
  observedSource: null,
  observedAt: null,
  version: 1,
  createdAt: '2026-10-07T09:00:00Z',
  updatedAt: '2026-10-07T09:00:00Z',
  ...patch,
});

describe('reason mapping', () => {
  it('maps every known refusal code and falls back for unknown ones', () => {
    expect(reasonKey('no_peer_mapping')).toBe('remoteaccess.reason.no_peer_mapping');
    expect(reasonKey('holder_mismatch')).toBe('remoteaccess.reason.holder_mismatch');
    expect(reasonKey('something_new')).toBe('remoteaccess.reason.unknown');
    expect(reasonKeys(['stale_device', 'stale_device', 'x'])).toEqual([
      'remoteaccess.reason.stale_device',
      'remoteaccess.reason.unknown',
    ]);
  });
});

describe('availability explanation', () => {
  const caps = (patch: Partial<Capabilities> = {}): Capabilities => ({
    deviceId: 'd1',
    enabled: true,
    deviceKnown: true,
    observedAt: null,
    stale: false,
    providers: [],
    ...patch,
  });
  const provider = (available: boolean, reasons: string[] = []) => ({
    provider: 'rustdesk',
    capabilities: { attended: true, observeSessions: false, closeSessions: false },
    mapped: available,
    available,
    reasons,
  });
  it('is available without reasons', () => {
    expect(providerAvailability(caps(), provider(true))).toEqual({
      state: 'available',
      reasons: [],
    });
  });
  it('explains a disabled deployment, an unknown device and policy reasons', () => {
    expect(providerAvailability(caps({ enabled: false }), provider(false)).state).toBe('off');
    expect(providerAvailability(caps({ deviceKnown: false }), provider(false)).reasons).toEqual([
      'remoteaccess.reason.device_unknown',
    ]);
    expect(
      providerAvailability(caps(), provider(false, ['no_peer_mapping', 'stale_device'])).reasons,
    ).toEqual(['remoteaccess.reason.no_peer_mapping', 'remoteaccess.reason.stale_device']);
    expect(providerAvailability(caps(), provider(false)).reasons).toEqual([
      'remoteaccess.reason.unknown',
    ]);
  });
  it('lists only available providers as startable', () => {
    const c = caps({ providers: [provider(true), { ...provider(false), provider: 'anydesk' }] });
    expect(startableProviders(c).map((p) => p.provider)).toEqual(['rustdesk']);
    expect(startableProviders(undefined)).toEqual([]);
  });
});

describe('stepper state', () => {
  const states = (s: Partial<RemoteSession>) => stepperState(session(s)).steps.map((x) => x.state);
  it('marks the current step and the steps before it', () => {
    expect(states({ status: 'requested' })).toEqual([
      'current',
      'upcoming',
      'upcoming',
      'upcoming',
    ]);
    expect(states({ status: 'authorized' })).toEqual(['done', 'current', 'upcoming', 'upcoming']);
    expect(states({ status: 'launched' })).toEqual(['done', 'done', 'current', 'upcoming']);
    expect(states({ status: 'closed' })).toEqual(['done', 'done', 'done', 'done']);
  });
  it('flags waiting for approval and early endings without inventing progress', () => {
    expect(stepperState(session({ status: 'pending_approval' })).waiting).toBe(true);
    const ended = stepperState(session({ status: 'expired' }));
    expect(ended.ended).toBe('expired');
    expect(ended.steps.map((x) => x.state)).toEqual([
      'upcoming',
      'upcoming',
      'upcoming',
      'upcoming',
    ]);
    expect(
      stepperState(session({ status: 'failed', launchedAt: '2026-10-07T09:05:00Z' })).steps[1]
        ?.state,
    ).toBe('done');
  });
});

describe('controls', () => {
  it('allows consent and launch only for the initiator in the right state', () => {
    expect(canRecordConsent(session(), 'u1')).toBe(true);
    expect(canRecordConsent(session(), 'u2')).toBe(false);
    expect(canRecordConsent(session({ consent: 'granted' }), 'u1')).toBe(false);
    expect(canRecordConsent(session({ status: 'requested' }), 'u1')).toBe(false);
    expect(canLaunch(session(), 'u1')).toBe(true);
    expect(canLaunch(session({ status: 'launched' }), 'u1')).toBe(false);
  });
  it('finds the active session', () => {
    expect(activeSession([session({ status: 'closed' }), session({ id: 's2' })])?.id).toBe('s2');
    expect(activeSession([session({ status: 'failed' })])).toBeUndefined();
  });
});

describe('launch link guard', () => {
  it('accepts custom schemes and rejects script, data and web schemes', () => {
    expect(isProviderLaunchLink('rustdesk://connection/new/123')).toBe(true);
    expect(isProviderLaunchLink('anydesk:123456')).toBe(true);
    for (const bad of [
      'javascript:alert(1)',
      'DATA:text/html,x',
      'https://x',
      'file:///etc',
      '',
      'x y:z',
      'rustdesk://a\nb',
    ])
      expect(isProviderLaunchLink(bad)).toBe(false);
  });
});

describe('session facts', () => {
  it('separates provider-reported from Turaco facts and keeps unknown as null', () => {
    const facts = sessionFacts(session({ observedConnectedAt: '2026-10-07T09:06:00Z' }));
    expect(facts.filter((f) => f.origin === 'provider').map((f) => f.value)).toEqual([
      '2026-10-07T09:06:00Z',
      null,
    ]);
    expect(facts.filter((f) => f.origin === 'turaco')).toHaveLength(2);
  });
});
