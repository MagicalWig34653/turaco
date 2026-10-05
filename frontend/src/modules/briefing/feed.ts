import type { MessageKey, MessageParams } from '../../platform/i18n/i18n';
import type { FeedEntry, Severity } from './types';

const titles: Record<string, MessageKey> = {
  'briefing.feed.manual_item': 'briefing.feed.manual_item',
  'briefing.feed.security_advisory': 'briefing.feed.security_advisory',
  'briefing.feed.risk_review_due': 'briefing.feed.risk_review_due',
  'briefing.feed.maintenance': 'briefing.feed.maintenance',
  'briefing.feed.milestone_due': 'briefing.feed.milestone_due',
  'briefing.feed.major_incident': 'briefing.feed.major_incident',
  'briefing.feed.ticket_backlog': 'briefing.feed.ticket_backlog',
  'briefing.feed.autotask': 'briefing.feed.autotask',
  'briefing.feed.endpoint_sync': 'briefing.feed.endpoint_sync',
  'briefing.feed.endpoint_errors': 'briefing.feed.endpoint_errors',
  'briefing.feed.directory_sync': 'briefing.feed.directory_sync',
  'briefing.feed.pending_approvals': 'briefing.feed.pending_approvals',
};

export function resolveFeedTitle(entry: FeedEntry): { key: MessageKey; params: MessageParams } {
  const params: MessageParams = {};
  for (const [key, value] of Object.entries(entry.params ?? {})) {
    if (typeof value === 'string' || typeof value === 'number') params[key] = value;
  }
  return { key: titles[entry.titleKey] ?? 'briefing.feed.unknown', params };
}

const severityOrder: Severity[] = ['critical', 'warning', 'info'];

function entryTime(entry: FeedEntry): number | undefined {
  const value = entry.dueAt ?? entry.occurredAt;
  if (!value) return undefined;
  const time = Date.parse(value);
  return Number.isNaN(time) ? undefined : time;
}

export function groupFeedEntries(
  entries: readonly FeedEntry[],
): { severity: Severity; entries: FeedEntry[] }[] {
  return severityOrder
    .map((severity) => ({
      severity,
      entries: entries
        .filter((entry) => entry.severity === severity)
        .sort((a, b) => {
          const left = entryTime(a);
          const right = entryTime(b);
          if (left === undefined) return right === undefined ? 0 : 1;
          if (right === undefined) return -1;
          return left - right;
        }),
    }))
    .filter((group) => group.entries.length > 0);
}

export const feedSources: Record<string, MessageKey> = {
  briefing: 'briefing.source.briefing',
  security: 'briefing.source.security',
  security_advisory: 'briefing.source.security',
  risk_review_due: 'briefing.source.security',
  planning: 'briefing.source.planning',
  maintenance: 'briefing.source.planning',
  milestone_due: 'briefing.source.planning',
  servicedesk: 'briefing.source.servicedesk',
  autotask: 'briefing.source.autotask',
  endpoints: 'briefing.source.endpoints',
  endpoint_errors: 'briefing.source.endpoints',
  organization: 'briefing.source.organization',
  directory: 'briefing.source.organization',
  approvals: 'briefing.source.approvals',
  pending_approvals: 'briefing.source.approvals',
  ticket_backlog: 'briefing.source.servicedesk',
};

export function sourceKey(source: string): MessageKey {
  return feedSources[source] ?? 'briefing.source.unknown';
}
