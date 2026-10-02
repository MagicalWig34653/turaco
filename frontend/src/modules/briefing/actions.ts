import type { CanFn } from '../../platform/session/permissions';
import type { BriefingItem, Severity } from './types';

export type BriefingAction = 'edit' | 'publish' | 'withdraw' | 'delete';

/** Mirrors the Briefing Item state machine; the backend enforces it. */
export function availableActions(item: Pick<BriefingItem, 'status'>, can: CanFn): BriefingAction[] {
  if (!can('briefing.manage')) return [];
  switch (item.status) {
    case 'draft':
      return ['edit', 'publish', 'delete'];
    case 'published':
      return ['withdraw'];
    case 'withdrawn':
      return [];
  }
}

export function severityTone(severity: Severity): 'info' | 'warning' | 'danger' {
  switch (severity) {
    case 'info':
      return 'info';
    case 'warning':
      return 'warning';
    case 'critical':
      return 'danger';
  }
}

/** True once a published item's expiry has passed; viewers no longer see it. */
export function isExpired(item: Pick<BriefingItem, 'validUntil'>, now: Date): boolean {
  if (!item.validUntil) return false;
  const until = new Date(item.validUntil);
  return !Number.isNaN(until.getTime()) && until.getTime() <= now.getTime();
}
