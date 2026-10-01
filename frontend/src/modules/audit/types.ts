// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

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
};

export type AuditFilter = {
  actionPrefix?: string;
  targetType?: string;
  targetId?: string;
  actorId?: string;
  correlationId?: string;
  from?: string;
  to?: string;
};
