import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { HealthReport, SetupChecklist, SetupItem, SetupWrite, SystemInfo } from './types';

type Signal = AbortSignal | undefined;

registerErrorMessages({
  'health.version_conflict': 'health.error.versionConflict',
  'health.not_confirmable': 'health.error.notConfirmable',
  'health.not_found': 'health.error.notFound',
  'health.invalid_request': 'health.error.invalidRequest',
});

export const healthApi = {
  health: (signal?: Signal) => api.get<HealthReport>('/admin/health', { signal }),
  integrations: (signal?: Signal) => api.get<HealthReport>('/admin/integrations', { signal }),
  system: (signal?: Signal) => api.get<SystemInfo>('/admin/system', { signal }),
  setup: (signal?: Signal) => api.get<SetupChecklist>('/admin/setup', { signal }),
  writeSetupItem: (key: string, body: SetupWrite) =>
    api.put<SetupItem>(`/admin/setup/items/${encodeURIComponent(key)}`, body),
};
