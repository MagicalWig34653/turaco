import { describe, expect, it } from 'vitest';
import { arrangeResults, type PaletteCommand } from '../../platform/ui/shell/paletteCommands';
import { looksLikeReference, normalizeReference, ticketCommands } from './paletteSearch';

const labels = { alias: (old: string, current: string) => `${old} -> ${current}` };

describe('ticket references in the palette', () => {
  it('recognizes references as people type them', () => {
    for (const text of ['TKT-000012', 'hr-3', ' IT-0001 ', 'A1-5'])
      expect(looksLikeReference(text)).toBe(true);
    for (const text of ['printer', 'tkt', 'tkt-', '12-3', 'TOOLONGPREFIX-1', 'a b-1'])
      expect(looksLikeReference(text)).toBe(false);
    expect(normalizeReference(' tkt-12 ')).toBe('TKT-12');
  });
});

describe('ticket palette entries', () => {
  const rows = [
    { id: 'a', reference: 'HR-0003', title: 'Contract question' },
    { id: 'b', reference: 'IT-0007', title: 'Printer' },
  ];
  it('leads with the reference hit and does not repeat the same ticket', () => {
    const items = ticketCommands(
      { query: 'hr-0003', hit: { ticketId: 'a', reference: 'HR-0003', alias: false } },
      rows,
      labels,
    );
    expect(items.map((entry) => entry.id)).toEqual(['ticket-a', 'ticket-b']);
    expect(items[0]).toMatchObject({ label: 'HR-0003', path: '/support/a', top: true });
    expect(items[1]).toMatchObject({ label: 'IT-0007 · Printer', path: '/support/b' });
  });
  it('labels an earlier number with the current one', () => {
    const [entry] = ticketCommands(
      { query: 'tkt-000012', hit: { ticketId: 'a', reference: 'HR-0003', alias: true } },
      [],
      labels,
    );
    expect(entry).toMatchObject({ label: 'TKT-000012 -> HR-0003', path: '/support/a' });
  });
  it('escapes the id in the path and handles no results', () => {
    expect(
      ticketCommands(
        { query: 'x', hit: undefined },
        [{ id: 'a/b', reference: 'X-1', title: 't' }],
        labels,
      )[0]?.path,
    ).toBe('/support/a%2Fb');
    expect(ticketCommands({ query: 'x', hit: undefined }, [], labels)).toEqual([]);
  });
  it('places an exact hit before navigation and titles after it', () => {
    const nav: PaletteCommand[] = [{ id: 'n1', label: 'Tickets', path: '/support' }];
    const objects: PaletteCommand[] = [
      { id: 'title', label: 'x', path: '/support/1', group: 'tickets' },
      { id: 'hit', label: 'y', path: '/support/2', group: 'tickets', top: true },
    ];
    expect(arrangeResults(nav, objects).map((entry) => entry.id)).toEqual(['hit', 'n1', 'title']);
  });
});
