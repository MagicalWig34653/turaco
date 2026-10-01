import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type { DirectorySyncRequest, DirectorySyncRun } from './types';

type Signal = AbortSignal | undefined;

registerErrorMessages({
  'organization.directory_sync_not_configured': 'error.directorySyncNotConfigured',
});

export const directoryApi = {
  syncRuns: (cursor?: string, signal?: Signal) =>
    api.get<Page<DirectorySyncRun>>('/directory-sync-runs', {
      signal,
      query: { limit: 25, cursor },
    }),
  syncRun: (id: string, signal?: Signal) =>
    api.get<DirectorySyncRun>(`/directory-sync-runs/${encodeURIComponent(id)}`, { signal }),
  requestSync: () => api.post<DirectorySyncRequest>('/directory-sync-runs'),
};
