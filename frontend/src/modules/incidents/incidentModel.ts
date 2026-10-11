/** Whether a promised update time is still ahead or has passed without an update. */
export function nextUpdateState(
  due: string | null,
  now: Date = new Date(),
): 'none' | 'upcoming' | 'overdue' {
  if (!due) return 'none';
  const at = Date.parse(due);
  if (Number.isNaN(at)) return 'none';
  return at < now.getTime() ? 'overdue' : 'upcoming';
}

export type Freshness =
  | { kind: 'overdue'; since: string }
  | { kind: 'planned'; due: string }
  | { kind: 'none'; ageHours: number };

/**
 * How current the public status of an active incident is: an overdue promised update, a planned one, or none at
 * all with the age of the last change. The banner and list show it so a stale text is not mistaken for news.
 */
export function incidentFreshness(
  incident: { nextUpdateDue: string | null; updatedAt: string },
  now: Date = new Date(),
): Freshness {
  if (incident.nextUpdateDue) {
    const due = new Date(incident.nextUpdateDue);
    return due.getTime() < now.getTime()
      ? { kind: 'overdue', since: incident.nextUpdateDue }
      : { kind: 'planned', due: incident.nextUpdateDue };
  }
  const ageHours = Math.max(
    0,
    Math.floor((now.getTime() - new Date(incident.updatedAt).getTime()) / 3_600_000),
  );
  return { kind: 'none', ageHours };
}
