// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

/** Resolved display name; `gone` marks an id the owning module no longer knows. */
export type AuditLabel = { text?: string; gone?: boolean };

export type AuditVia = 'ai' | 'mcp';
export type AuditActorKind = 'user' | 'system';

export type AuditEvent = {
  id: string;
  occurredAt: string;
  actorId?: string;
  action: string;
  targetType: string;
  targetId: string;
  correlationId: string;
  before?: unknown;
  after?: unknown;
  metadata: Record<string, unknown>;
  actor?: AuditLabel;
  systemActor?: string;
  target?: AuditLabel;
  via?: AuditVia;
};

export type AuditEventList = {
  items: AuditEvent[];
  nextCursor?: string;
  /** Target types whose names could not be resolved; their rows show ids. */
  unresolvedTypes?: string[];
};

export type AuditRetention = {
  retentionDays: number;
  minimumDays: number;
  policy: 'keep' | 'purge';
  oldestEventAt?: string;
};

export type AuditFilter = {
  actionPrefix?: string;
  module?: string;
  targetType?: string;
  targetId?: string;
  actorId?: string;
  actorKind?: AuditActorKind;
  systemActor?: string;
  via?: AuditVia;
  correlationId?: string;
  from?: string;
  to?: string;
};
