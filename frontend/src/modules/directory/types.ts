// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type SyncOutcome = 'running' | 'succeeded' | 'failed' | 'sweep_withheld';
export type SyncConflictKind = 'email_in_use' | 'manager_unresolved' | 'invalid_attributes';
export type SyncConflict = { kind: SyncConflictKind; externalId: string; username: string };

export type DirectorySyncRun = {
  id: string;
  providerKey: string;
  jobId: string | null;
  trigger: 'scheduled' | 'manual';
  startedAt: string;
  observedAt: string | null;
  finishedAt: string | null;
  outcome: SyncOutcome;
  counts: Record<string, number>;
  conflicts: SyncConflict[];
  conflictCount: number;
  error: string | null;
};
export type DirectorySyncRequest = { jobId: string; created: boolean };
