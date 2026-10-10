import { describe, expect, it } from 'vitest';
import { searchCommands } from './objectSearch';

describe('searchCommands', () => {
  it('maps hits to grouped palette entries with detail routes', () => {
    const items = searchCommands({
      items: [
        { type: 'user', id: 'u1', title: 'Anna Etikett', subtitle: 'anna@example.org' },
        { type: 'ticket', id: 't/1', reference: 'TKT-1', title: 'Etikettendrucker' },
        { type: 'knowledge', id: 'k1', reference: 'KB-7', title: 'Etiketten drucken' },
      ],
      unavailable: [],
    });
    expect(items.map((item) => item.group)).toEqual(['tickets', 'knowledge', 'people']);
    expect(items[0]).toMatchObject({
      label: 'TKT-1 · Etikettendrucker',
      path: '/support/t%2F1',
    });
    expect(items[2]).toMatchObject({
      label: 'Anna Etikett · anna@example.org',
      path: '/endpoints/users/u1',
    });
  });
  it('marks an exact reference hit as leading and ignores unknown types', () => {
    const items = searchCommands({
      items: [
        { type: 'ticket', id: 't1', reference: 'TKT-1', title: 'x', exact: true },
        { type: 'future_thing', id: 'f1', title: 'y' },
      ],
      unavailable: [],
    });
    expect(items).toHaveLength(1);
    expect(items[0]?.top).toBe(true);
  });
  it('gives every source its own group and route', () => {
    const types = [
      'ticket',
      'problem',
      'major_incident',
      'change',
      'request',
      'knowledge',
      'asset',
      'device',
      'user',
    ];
    const items = searchCommands({
      items: types.map((type) => ({ type, id: type, title: type })),
      unavailable: [],
    });
    expect(new Set(items.map((item) => item.group)).size).toBe(types.length);
    expect(new Set(items.map((item) => item.path)).size).toBe(types.length);
  });
});
