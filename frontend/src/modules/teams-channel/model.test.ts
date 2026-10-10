import { describe, expect, it } from 'vitest';
import { freeDestinations, routesFor } from './model';
import type { ChannelRoutes } from './types';

const data: ChannelRoutes = {
  mode: 'real',
  destinations: ['infra', 'ops'],
  categories: ['change.scheduled', 'majorincident.update'],
  items: [
    {
      id: '1',
      category: 'majorincident.update',
      destinationKey: 'infra',
      createdBy: 'u',
      createdAt: '',
    },
  ],
};

describe('teams channel model', () => {
  it('lists the routes of one category', () => {
    expect(routesFor(data, 'majorincident.update')).toHaveLength(1);
    expect(routesFor(data, 'change.scheduled')).toHaveLength(0);
  });

  it('offers only destinations that are not routed yet', () => {
    expect(freeDestinations(data, 'majorincident.update')).toEqual(['ops']);
    expect(freeDestinations(data, 'change.scheduled')).toEqual(['infra', 'ops']);
  });
});
