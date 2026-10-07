import type { MessageKey } from '../../platform/i18n/i18n';
import {
  refusalCodes,
  type Capabilities,
  type ProviderCapability,
  type RefusalCode,
  type RemoteSession,
} from './types';

export const stepperSteps = ['requested', 'authorized', 'launched', 'closed'] as const;
export type StepperStep = (typeof stepperSteps)[number];
export type StepState = 'done' | 'current' | 'upcoming';

const activeStatuses = ['requested', 'pending_approval', 'authorized', 'launched'];
const endedEarly = ['rejected', 'cancelled', 'expired', 'failed'];

export function isActive(status: string): boolean {
  return activeStatuses.includes(status);
}

export function isRefusalCode(code: string): code is RefusalCode {
  return (refusalCodes as readonly string[]).includes(code);
}

/** Message key of a refusal code; unknown codes get a neutral message instead of a raw code. */
export function reasonKey(code: string): MessageKey {
  return isRefusalCode(code) ? `remoteaccess.reason.${code}` : 'remoteaccess.reason.unknown';
}

/** Reasons in a stable order with duplicates removed. */
export function reasonKeys(reasons: readonly string[]): MessageKey[] {
  return [...new Set(reasons)].map(reasonKey);
}

export type Availability = {
  state: 'available' | 'blocked' | 'off';
  /** Message keys that explain a blocked or off state. */
  reasons: MessageKey[];
};

/** Plain-language availability of one provider on one device. */
export function providerAvailability(
  caps: Pick<Capabilities, 'enabled' | 'deviceKnown'>,
  provider: Pick<ProviderCapability, 'available' | 'reasons'>,
): Availability {
  if (provider.available) return { state: 'available', reasons: [] };
  if (!caps.enabled) return { state: 'off', reasons: ['remoteaccess.reason.provider_disabled'] };
  if (!caps.deviceKnown)
    return { state: 'blocked', reasons: ['remoteaccess.reason.device_unknown'] };
  const keys = reasonKeys(provider.reasons);
  return {
    state: 'blocked',
    reasons: keys.length > 0 ? keys : ['remoteaccess.reason.unknown'],
  };
}

/** Providers a technician can pick: available ones only. */
export function startableProviders(caps: Capabilities | undefined): ProviderCapability[] {
  return caps ? caps.providers.filter((p) => p.available) : [];
}

/**
 * Stepper over requested -> authorized -> launched -> closed. A session that ended early
 * (rejected, cancelled, expired, failed) keeps the steps it actually reached; Turaco knows only
 * about launch, so the authorized step is marked reached when the session was launched.
 */
export function stepperState(session: Pick<RemoteSession, 'status' | 'launchedAt'>): {
  steps: Array<{ step: StepperStep; state: StepState }>;
  ended: string | null;
  waiting: boolean;
} {
  const { status } = session;
  let current: number;
  let ended: string | null = null;
  if (status === 'requested' || status === 'pending_approval') current = 0;
  else if (status === 'authorized') current = 1;
  else if (status === 'launched') current = 2;
  else if (status === 'closed') current = 3;
  else {
    ended = endedEarly.includes(status) ? status : null;
    current = session.launchedAt ? 2 : 0;
  }
  const steps = stepperSteps.map((step, index) => ({
    step,
    state: (status === 'closed' || index < current
      ? 'done'
      : index === current && !ended
        ? 'current'
        : 'upcoming') as StepState,
  }));
  return { steps, ended, waiting: status === 'pending_approval' };
}

/** The initiator can record consent while the session is authorized or launched and none is recorded. */
export function canRecordConsent(session: RemoteSession, userId: string | undefined): boolean {
  return (
    session.initiatedBy === userId &&
    session.consent === 'unknown' &&
    (session.status === 'authorized' || session.status === 'launched')
  );
}

export function canLaunch(session: RemoteSession, userId: string | undefined): boolean {
  return session.initiatedBy === userId && session.status === 'authorized';
}

export function canClose(session: RemoteSession, userId: string | undefined): boolean {
  return session.initiatedBy === userId && session.status === 'launched';
}

export function canCancel(session: RemoteSession, userId: string | undefined): boolean {
  return (
    session.initiatedBy === userId &&
    ['requested', 'pending_approval', 'authorized'].includes(session.status)
  );
}

/** The newest session that has not ended, if any (the list is newest first). */
export function activeSession(sessions: readonly RemoteSession[]): RemoteSession | undefined {
  return sessions.find((s) => isActive(s.status));
}

/** Link schemes the browser must never be sent to; everything else is the provider's custom scheme. */
const blockedSchemes = ['javascript', 'data', 'vbscript', 'file', 'blob', 'http', 'https', 'about'];

/** True when the value looks like a custom provider link and is safe to hand to the browser. */
export function isProviderLaunchLink(value: string): boolean {
  const match = /^([a-z][a-z0-9+.-]*):/i.exec(value);
  if (!match?.[1]) return false;
  if (blockedSchemes.includes(match[1].toLowerCase())) return false;
  return !/[\u0000-\u001f\s]/.test(value);
}

export type ObservedFact = {
  label: MessageKey;
  value: string | null;
  origin: 'provider' | 'turaco';
};

/** Facts of a session split by who vouches for them; null is shown as unknown, never inferred. */
export function sessionFacts(s: RemoteSession): ObservedFact[] {
  return [
    { label: 'remoteaccess.fact.launchedAt', value: s.launchedAt, origin: 'turaco' },
    { label: 'remoteaccess.fact.closedAt', value: s.closedAt, origin: 'turaco' },
    { label: 'remoteaccess.fact.connectedAt', value: s.observedConnectedAt, origin: 'provider' },
    { label: 'remoteaccess.fact.endedAt', value: s.observedEndedAt, origin: 'provider' },
  ];
}

export function statusTone(status: string): 'info' | 'success' | 'warning' | 'danger' | 'neutral' {
  switch (status) {
    case 'launched':
      return 'info';
    case 'closed':
      return 'neutral';
    case 'authorized':
      return 'success';
    case 'requested':
    case 'pending_approval':
      return 'warning';
    case 'rejected':
    case 'failed':
      return 'danger';
    default:
      return 'neutral';
  }
}
