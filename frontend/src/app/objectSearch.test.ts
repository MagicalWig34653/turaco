import { describe, expect, it } from 'vitest';
import { matchRows, searchSources } from './objectSearch';

const all = () => true;
const none = () => false;

describe('searchSources', () => {
  it('offers only what the modules and permissions allow', () => {
    expect(searchSources(none, all)).toMatchObject({
      tickets: true,
      incidents: true,
      knowledge: true,
      problems: false,
      devices: false,
      people: false,
    });
    expect(searchSources(all, all)).toEqual({
      tickets: true,
      problems: true,
      incidents: true,
      knowledge: true,
      devices: true,
      people: true,
    });
  });
  it('drops sources of switched-off modules', () => {
    const only = (key: string) => key === 'knowledge';
    expect(searchSources(all, only)).toMatchObject({
      tickets: false,
      problems: false,
      devices: false,
      people: false,
      knowledge: true,
    });
  });
  it('needs directory and endpoint management access for people', () => {
    const can = (p: string) => p === 'organization.view';
    expect(searchSources(can, all).people).toBe(false);
  });
});

describe('matchRows', () => {
  const rows = [
    { reference: 'PRB-000004', title: 'ORBIS hängt' },
    { reference: 'PRB-000005', title: 'Drucker' },
  ];
  it('matches reference or title case-insensitively and limits the result', () => {
    expect(matchRows(rows, 'orbis')).toHaveLength(1);
    expect(matchRows(rows, 'prb-00000')).toHaveLength(2);
    expect(matchRows(rows, 'prb-00000', 1)).toHaveLength(1);
    expect(matchRows(rows, '  ')).toEqual([]);
  });
});
