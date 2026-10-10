// Types mirror api/openapi/openapi.yaml (HealthResult, HealthReport, SetupItem, SetupChecklist).

export type HealthStatus =
  'ok' | 'stale' | 'fake' | 'not_configured' | 'disabled' | 'failing' | 'unknown';

export type NextStep = {
  kind: 'route' | 'config' | 'docs';
  route?: string;
  /** Names of configuration keys, never values. */
  configKeys?: string[];
  docsPath?: string;
};

export type HealthResult = {
  key: string;
  category: 'system' | 'integration' | 'module';
  status: HealthStatus;
  mode?: 'real' | 'fake' | 'not_configured';
  observedAt: string;
  lastSuccessAt?: string;
  lastAttemptAt?: string;
  errorCode?: string;
  counts?: Record<string, number>;
  detail?: Record<string, unknown>;
  nextStep?: NextStep;
};

export type HealthReport = {
  items: HealthResult[];
  summary: { attention?: number; total?: number };
};

export type SetupState = 'done' | 'todo' | 'skipped' | 'confirmed';

export type SetupItem = {
  key: string;
  order: number;
  route: string;
  state: SetupState;
  attention?: boolean;
  reason?: string;
  version?: number;
  updatedAt?: string;
};

export type SetupChecklist = { items: SetupItem[]; open: number; total: number };

export type SetupWrite = {
  state: 'skipped' | 'confirmed' | 'cleared';
  reason?: string;
  expectedVersion?: number;
};

export type SystemInfo = {
  version?: string;
  environment?: string;
  startedAt?: string;
  commit?: string;
  database?: { status?: HealthStatus; serverVersion?: string };
  migrations?: {
    status?: HealthStatus;
    latestVersion?: number | string;
    latestName?: string;
    applied?: number;
  };
  modules?: Record<string, number>;
};
