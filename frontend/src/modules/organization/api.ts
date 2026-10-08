import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type { DirectoryGroup, Location, Team, User } from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({ 'organization.not_found': 'error.notFound' });
registerErrorResolver((error) =>
  error.code.startsWith('organization.invalid_') ? 'error.invalidRequest' : undefined,
);

/** Users and Directory Groups (used by the shell, profile and subject pickers). */
export const organizationApi = {
  location: (id: string, signal?: Signal) => api.get<Location>(`/locations/${enc(id)}`, { signal }),
  user: (id: string, signal?: Signal) => api.get<User>(`/users/${enc(id)}`, { signal }),
  searchUsers: (q: string, signal?: Signal) =>
    api.get<Page<User>>('/users', { signal, query: { q, limit: 10 } }),
  searchTeams: (q: string, signal?: Signal) =>
    api.get<Page<Team>>('/teams', { signal, query: { q, limit: 10 } }),
  searchDirectoryGroups: (q: string, signal?: Signal) =>
    api.get<Page<DirectoryGroup>>('/directory-groups', { signal, query: { q, limit: 10 } }),
  searchLocations: (q: string, signal?: Signal) =>
    api.get<Page<Location>>('/locations', { signal, query: { q, limit: 10 } }),
};
