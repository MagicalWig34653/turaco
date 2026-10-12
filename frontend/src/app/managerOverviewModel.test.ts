import { describe, expect, it } from 'vitest';
import { ageInDays, taskLinks, ticketLinks } from './managerOverviewModel';

const decode = (link: string) => {
  const [path, search] = link.split('?');
  const raw = new URLSearchParams(search).get('query') ?? '';
  return { path, query: raw };
};

describe('manager overview links', () => {
  it('opens the ticket list with a query parameter', () => {
    const link = decode(ticketLinks.queueUnassigned('q-1'));
    expect(link.path).toBe('/service-desk');
    expect(link.query).toContain('q-1');
    expect(link.query).toContain('is_empty');
  });

  it('opens the ticket list for the SLA numbers by state, never by assignee', () => {
    const atRisk = decode(ticketLinks.atRisk('q-1'));
    expect(atRisk.path).toBe('/service-desk');
    expect(atRisk.query).toContain('sla_state');
    expect(atRisk.query).toContain('at_risk');
    expect(atRisk.query).toContain('q-1');
    expect(atRisk.query).not.toContain('assignee');
    expect(decode(ticketLinks.breached()).query).toContain('breached');
  });

  it('opens the task list for overdue and unowned tasks', () => {
    const overdue = decode(taskLinks.overdue('2026-10-11T10:00:00Z'));
    expect(overdue.path).toBe('/tasks');
    expect(overdue.query).toContain('due_at');
    expect(overdue.query).toContain('2026-10-11T10:00:00Z');
    const unowned = decode(taskLinks.unowned()).query;
    expect(unowned).toContain('assigned_user');
    expect(unowned).toContain('assigned_team');
  });
});

describe('ageInDays', () => {
  const now = new Date('2026-10-11T12:00:00Z');
  it('counts whole days and never goes negative', () => {
    expect(ageInDays('2026-10-08T11:00:00Z', now)).toBe(3);
    expect(ageInDays('2026-10-12T00:00:00Z', now)).toBe(0);
    expect(ageInDays(null, now)).toBe(0);
    expect(ageInDays('garbage', now)).toBe(0);
  });
});
