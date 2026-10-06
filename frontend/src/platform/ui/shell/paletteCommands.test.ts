import { describe, expect, it } from 'vitest';
import { appRoutes } from '../../../app/routes';
import { filterCommands, moveCommandSelection, navigationCommands } from './paletteCommands';

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
