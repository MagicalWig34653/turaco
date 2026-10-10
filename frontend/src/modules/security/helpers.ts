import type { Finding } from './types';
export function advisoryActions(status: string): string[] {
  const actions: Record<string, string[]> = {
    new: ['start-analysis', 'applicable', 'not-applicable', 'archive'],
    analyzing: ['applicable', 'not-applicable'],
    applicable: ['not-applicable', 'plan-remediation', 'start-remediation', 'resolve'],
    not_applicable: ['archive'],
    remediation_planned: ['start-remediation', 'resolve'],
    remediating: ['resolve'],
    resolved: ['archive'],
  };
  return actions[status] ?? [];
}
export function findingActions(status: string, manage: boolean, acceptRisk: boolean): string[] {
  const actions: Record<string, string[]> = {
    open: ['investigate', 'accept', 'false-positive', 'plan-remediation', 'start-remediation'],
    investigating: ['accept', 'false-positive', 'plan-remediation', 'start-remediation'],
    accepted: ['false-positive', 'plan-remediation', 'start-remediation'],
    remediation_planned: ['false-positive', 'start-remediation'],
    remediating: ['false-positive'],
  };
  const result = manage ? [...(actions[status] ?? [])] : [];
  if (
    acceptRisk &&
    ['open', 'investigating', 'accepted', 'remediation_planned', 'remediating'].includes(status)
  )
    result.push('accept-risk');
  if ((manage || acceptRisk) && ['false_positive', 'risk_accepted'].includes(status))
    result.push('reopen');
  return result;
}
export function devicePath(finding: Finding, canView: boolean): string | null {
  return canView && !finding.deviceHidden
    ? `/devices/${encodeURIComponent(finding.deviceId)}`
    : null;
}
export function safeSourceUrl(url: string | null): string | null {
  try {
    if (!url) return null;
    const parsed = new URL(url);
    return parsed.protocol === 'https:' ? parsed.href : null;
  } catch {
    return null;
  }
}
export function reviewStartDate(today: Date): string {
  const date = new Date(today.getFullYear(), today.getMonth(), today.getDate() + 1);
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
}
export function maxReviewDate(today: Date): string {
  const date = new Date(today.getFullYear(), today.getMonth() + 12, today.getDate());
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
}
export function validReviewDate(value: string, today: Date): boolean {
  const date = new Date(`${value}T00:00:00`);
  const start = new Date(today.getFullYear(), today.getMonth(), today.getDate());
  return (
    /^\d{4}-\d{2}-\d{2}$/.test(value) &&
    !Number.isNaN(date.getTime()) &&
    date > start &&
    value <= maxReviewDate(today)
  );
}
export const overviewLinks = {
  severity: (severity: string) =>
    `/security/advisories?applicable=true&severity=${encodeURIComponent(severity)}`,
  untriaged: (severity: string) =>
    `/security/advisories?status=new&severity=${encodeURIComponent(severity)}`,
  confidence: (confidence: string) =>
    `/security/findings?open=true&confidence=${encodeURIComponent(confidence)}`,
  overdueTasks: '/tasks?overdue=true',
  riskDue: '/security/findings?riskDue=true',
};
export function isOpenFinding(status: string): boolean {
  return [
    'open',
    'investigating',
    'accepted',
    'remediation_planned',
    'remediating',
    'risk_accepted',
  ].includes(status);
}
export function isApplicableAdvisory(status: string): boolean {
  return ['applicable', 'remediation_planned', 'remediating'].includes(status);
}
export function riskReviewDueWithin30Days(finding: Finding, today: Date): boolean {
  if (finding.status !== 'risk_accepted' || !finding.riskReviewBy) return false;
  const end = new Date(today.getFullYear(), today.getMonth(), today.getDate() + 30);
  const start = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, '0')}-${String(today.getDate()).padStart(2, '0')}`;
  return (
    finding.riskReviewBy >= start &&
    finding.riskReviewBy <=
      `${end.getFullYear()}-${String(end.getMonth() + 1).padStart(2, '0')}-${String(end.getDate()).padStart(2, '0')}`
  );
}
