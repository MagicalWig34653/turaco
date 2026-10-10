// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type BriefingStatus = 'draft' | 'published' | 'withdrawn';
export type Severity = 'info' | 'warning' | 'critical';
/** `it` stays in the IT briefing; `all` is also announced to every signed-in user. */
export type Audience = 'it' | 'all';
export const audiences: readonly Audience[] = ['it', 'all'];

export type FeedKind =
  | 'manual_item'
  | 'security_advisory'
  | 'risk_review_due'
  | 'maintenance'
  | 'milestone_due'
  | 'major_incident'
  | 'ticket_backlog'
  | 'integration_health'
  | 'pending_approvals';

export type FeedEntry = {
  kind: FeedKind;
  severity: Severity;
  titleKey: string;
  params: Record<string, unknown>;
  count?: number;
  reference?: { type: string; id: string };
  linkPath: string;
  occurredAt?: string;
  dueAt?: string;
  source: string;
};

export type FeedResult = {
  entries: FeedEntry[];
  truncated: Record<string, boolean>;
  unavailable: { source: string; reason: string }[];
};

export const severities: readonly Severity[] = ['info', 'warning', 'critical'];
export const briefingStatuses: readonly BriefingStatus[] = ['draft', 'published', 'withdrawn'];

export type BriefingItem = {
  id: string;
  title: string;
  /** Plain text; never render it as HTML. */
  body: string;
  severity: Severity;
  status: BriefingStatus;
  audience: Audience;
  validUntil: string | null;
  authorUserId: string | null;
  publishedAt: string | null;
  publishedByUserId: string | null;
  withdrawnAt: string | null;
  withdrawnByUserId: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type BriefingCreateBody = {
  title: string;
  body?: string;
  severity?: Severity;
  audience?: Audience;
  validUntil?: string | null;
};

export type BriefingUpdateBody = {
  expectedVersion: number;
  title?: string;
  body?: string;
  severity?: Severity;
  audience?: Audience;
  validUntil?: string;
  clearValidUntil?: boolean;
};

/** Employee-facing part of the briefing: no author, status or internal fields. */
export type Announcements = {
  items: Array<{
    id: string;
    title: string;
    /** Plain text; never render it as HTML. */
    body: string;
    severity: Severity;
    publishedAt: string | null;
    validUntil: string | null;
  }>;
  incidents: Array<{
    id: string;
    reference: string;
    title: string;
    summary: string;
    status: string;
    nextUpdateDue: string | null;
    updatedAt: string;
  }>;
  incidentsUnavailable: boolean;
};
