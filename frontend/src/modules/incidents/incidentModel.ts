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
