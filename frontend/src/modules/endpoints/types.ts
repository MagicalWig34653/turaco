export const platforms = ['windows', 'macos', 'ios', 'android', 'linux', 'other'] as const;
export const complianceStates = [
  'compliant',
  'noncompliant',
  'in_grace_period',
  'unknown',
] as const;
export const findingKinds = [
  'no_asset_match',
  'serial_conflict',
  'duplicate_device',
  'unmatched_software',
] as const;
export const reasonCodes = [
  'serial_confirmed',
  'correction',
  'duplicate',
  'wrong_asset',
  'other',
] as const;
export type ReasonCode = (typeof reasonCodes)[number];

export type Device = {
  id: string;
  provider: string;
  externalId: string;
  name: string;
  serialNumber: string | null;
  assetId: string | null;
  assetLinkSource: string | null;
  autoLinkBlocked: boolean;
  osPlatform: string;
  osVersion: string | null;
  manufacturer: string | null;
  model: string | null;
  ownership: string;
  complianceState: string;
  lastCheckinAt: string | null;
  source: string;
  observedAt: string;
  lastSyncedAt: string;
  deletedObservedAt: string | null;
  version: number;
};
export type Installation = {
  id: string;
  softwareProductId: string | null;
  productName: string | null;
  rawName: string;
  rawVersion: string;
  rawPublisher: string | null;
  observedAt: string;
};
export type Finding = {
  id: string;
  kind: string;
  deviceId: string;
  deviceName: string;
  status: string;
  detail: Record<string, unknown>;
  raisedAt: string;
  resolvedAt: string | null;
};
export type DeviceDetail = Device & { software: Installation[]; findings: Finding[] };
export type DeviceFilters = {
  platform: string;
  compliance: string;
  q: string;
  linked: string;
  includeDeleted: boolean;
};
export type FindingFilters = { kind: string; status: string; deviceId: string };
export type SyncCounts = {
  devicesCreated: number;
  devicesUpdated: number;
  devicesUnchanged: number;
  devicesTombstoned: number;
  devicesRejected: number;
  devicesLinked: number;
  softwareObserved: number;
  softwareSkipped: number;
  softwareErrors: number;
  findingsRaised: number;
  findingsResolved: number;
};
