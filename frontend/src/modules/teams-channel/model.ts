import type { ChannelRoute, ChannelRoutes } from './types';

/** Routes of one category, in the server order. */
export function routesFor(data: Pick<ChannelRoutes, 'items'>, category: string): ChannelRoute[] {
  return data.items.filter((route) => route.category === category);
}

/** Destination keys that the category is not routed to yet. */
export function freeDestinations(data: ChannelRoutes, category: string): string[] {
  const used = new Set(routesFor(data, category).map((route) => route.destinationKey));
  return data.destinations.filter((key) => !used.has(key));
}
