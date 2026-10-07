export const sessionStatuses = [
  'requested',
  'pending_approval',
  'authorized',
  'launched',
  'closed',
  'rejected',
  'cancelled',
  'expired',
  'failed',
] as const;
export type SessionStatus = (typeof sessionStatuses)[number];

export const refusalCodes = [
  'provider_disabled',
  'device_unknown',
  'device_retired',
  'stale_device',
  'no_peer_mapping',
  'ticket_unavailable',
  'mapping_changed',
  'no_recipient',
  'approval_required',
  'holder_mismatch',
  'session_open',
  'approver_required',
] as const;
export type RefusalCode = (typeof refusalCodes)[number];

export const mismatchReasons = ['holder_changed', 'shared_device', 'on_behalf'] as const;
export type MismatchReason = (typeof mismatchReasons)[number];
export const closeReasons = [
  'completed',
  'technician_aborted',
  'connection_failed',
  'other',
] as const;
export const cancelReasons = [
  'no_longer_needed',
  'wrong_device',
  'ticket_resolved',
  'other',
] as const;
export const mapReasons = ['initial_mapping', 'correction', 'provider_change', 'other'] as const;
export const unmapReasons = ['no_longer_valid', 'wrong_device', 'decommissioned', 'other'] as const;

export type Consent = 'unknown' | 'granted' | 'declined' | 'not_required';

export type ProviderCapability = {
  provider: string;
  capabilities: { attended: boolean; observeSessions: boolean; closeSessions: boolean };
  mapped: boolean;
  peerId?: string;
  available: boolean;
  reasons: string[];
};

export type Capabilities = {
  deviceId: string;
  enabled: boolean;
  deviceKnown: boolean;
  observedAt: string | null;
  lastCheckinAt?: string | null;
  stale: boolean;
  providers: ProviderCapability[];
};

export type RemoteSession = {
  id: string;
  reference: string;
  deviceId: string;
  ticketId: string;
  provider: string;
  mode: string;
  status: string;
  statusReason: string | null;
  initiatedBy: string;
  approvalId: string | null;
  consent: string;
  consentRecordedBy: string | null;
  consentRecordedAt: string | null;
  mismatchReason: string | null;
  launchedAt: string | null;
  closedAt: string | null;
  expiresAt: string;
  note: string | null;
  observedConnectedAt: string | null;
  observedEndedAt: string | null;
  observedSource: string | null;
  observedAt: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type Transition = {
  id: string;
  fromStatus: string | null;
  toStatus: string;
  operation: string;
  reason: string | null;
  actorUserId: string | null;
  actorSystem: string | null;
  createdAt: string;
};

export type SessionDetail = { session: RemoteSession; transitions: Transition[] };

export type PeerMapping = {
  id: string;
  deviceId: string;
  provider: string;
  peerId: string;
  source: string;
  reason: string | null;
  mappedBy: string | null;
  mappedAt: string;
  closedAt: string | null;
  closeReason: string | null;
};

export type SessionFilters = { status: string; deviceId: string; ticketId: string; mine: boolean };

export type NewSession = {
  deviceId: string;
  ticketId: string;
  provider: string;
  reason?: string;
  note?: string;
  approverUserId?: string;
  approverTeamId?: string;
};

export type ObservationsSummary = {
  unattributedRecords: number;
  byReason: { unattributed: number; duplicate: number; after_close: number };
  since: string;
};
