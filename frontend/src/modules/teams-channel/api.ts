import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { ChannelRoute, ChannelRoutes } from './types';

registerErrorMessages({
  'notifications.category_not_broadcastable': 'teamsChannel.error.notBroadcastable',
  'notifications.unknown_destination': 'teamsChannel.error.unknownDestination',
  'notifications.route_exists': 'teamsChannel.error.routeExists',
  'notifications.route_not_found': 'error.notFound',
  'notifications.invalid_request': 'error.invalidRequest',
});

const base = '/integrations/teams/channel-routes';

export const teamsChannelApi = {
  list: (signal?: AbortSignal) => api.get<ChannelRoutes>(base, { signal }),
  create: (category: string, destinationKey: string) =>
    api.post<ChannelRoute>(base, { category, destinationKey }),
  remove: (id: string) => api.delete<void>(`${base}/${encodeURIComponent(id)}`),
};
