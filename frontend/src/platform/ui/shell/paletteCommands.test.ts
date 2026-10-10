import { describe, expect, it } from 'vitest';
import { appRoutes } from '../../../app/routes';
import {
  arrangeResults,
  filterCommands,
  minObjectQuery,
  moveCommandSelection,
  navigationCommands,
} from './paletteCommands';

describe('command palette navigation', () => {
  const commands = navigationCommands(
    appRoutes,
    (permission) => permission === 'tickets.view',
    (route) => route.id,
  );

  it('uses visible nav routes only and respects permissions', () => {
    expect(commands.some((command) => command.id === 'ticketQueue')).toBe(true);
    expect(commands.some((command) => command.id === 'securityAdvisories')).toBe(false);
    expect(commands.some((command) => command.id === 'ticketDetail')).toBe(false);
  });

  it('finds fuzzy navigation commands and handles empty results', () => {
    expect(filterCommands(commands, 'tktq').map((command) => command.id)).toContain('ticketQueue');
    expect(filterCommands(commands, 'zzzzzz')).toEqual([]);
  });

  it('wraps keyboard navigation and supports Home and End', () => {
    expect(moveCommandSelection(0, 3, 'ArrowUp')).toBe(2);
    expect(moveCommandSelection(2, 3, 'ArrowDown')).toBe(0);
    expect(moveCommandSelection(2, 3, 'Home')).toBe(0);
    expect(moveCommandSelection(0, 3, 'End')).toBe(2);
    expect(moveCommandSelection(0, 0, 'ArrowDown')).toBe(-1);
  });
});

describe('record results in the palette', () => {
  const page = { id: 'p', label: 'Users', path: '/users' };
  const hit = {
    id: 'tickets-1',
    label: 'TKT-1 · Etikettendrucker',
    path: '/support/1',
    group: 'tickets',
  };
  const exact = { ...hit, id: 'tickets-2', top: true };
  it('lists pages first, records after them, and exact reference hits before everything', () => {
    expect(arrangeResults([page], [hit, exact]).map((item) => item.id)).toEqual([
      'tickets-2',
      'p',
      'tickets-1',
    ]);
  });
  it('starts the record search at two characters', () => {
    expect(minObjectQuery).toBe(2);
  });
  it('wraps keyboard selection over the grouped list and survives an empty one', () => {
    expect(moveCommandSelection(0, 0, 'ArrowDown')).toBe(-1);
    expect(moveCommandSelection(-1, 3, 'ArrowDown')).toBe(0);
  });
});
