import type { MessageKey } from '../../platform/i18n/i18n';

/** Pure logic of the report-a-problem form: impact signal, duplicate hint and shared devices. */

export const impactChoices = ['patient_care', 'blocked', 'impaired', 'request'] as const;
export type ImpactChoice = (typeof impactChoices)[number];

export const impactLabelKey: Record<ImpactChoice, MessageKey> = {
  patient_care: 'report.impact.patient_care',
  blocked: 'report.impact.blocked',
  impaired: 'report.impact.impaired',
  request: 'report.impact.request',
};

export const impactHintKey: Record<ImpactChoice, MessageKey> = {
  patient_care: 'report.impact.patient_care.hint',
  blocked: 'report.impact.blocked.hint',
  impaired: 'report.impact.impaired.hint',
  request: 'report.impact.request.hint',
};

/** The impact is a signal for triage; the employee never sets the priority. */
export function impactFields(choice: ImpactChoice | ''): {
  impact?: ImpactChoice;
  patientImpact?: true;
} {
  if (!choice) return {};
  return choice === 'patient_care' ? { impact: choice, patientImpact: true } : { impact: choice };
}

/** Description with the impact written into it, for a server that does not know the impact fields. */
export function describeImpactInText(description: string, label: string): string {
  const line = `[${label}]`;
  return description.trim() ? `${line}\n${description.trim()}` : line;
}

/** Description with a free-text device or place added. */
export function withDeviceNote(description: string, label: string, note: string): string {
  const text = note.trim();
  if (!text) return description.trim();
  const line = `${label}: ${text}`;
  return description.trim() ? `${description.trim()}\n\n${line}` : line;
}

const normalizeTitle = (value: string) =>
  value.normalize('NFKC').toLocaleLowerCase().replace(/\s+/g, ' ').trim();

type OpenTicket = { id: string; reference: string; title: string; status: string };

const finished = new Set(['resolved', 'closed', 'cancelled']);

/** Open tickets of the same person whose subject is identical (ignoring case and spacing). */
export function duplicateCandidates<T extends OpenTicket>(title: string, mine: readonly T[]): T[] {
  const wanted = normalizeTitle(title);
  if ([...wanted].length < 4) return [];
  return mine.filter(
    (ticket) => !finished.has(ticket.status) && normalizeTitle(ticket.title) === wanted,
  );
}

/** Devices not already offered, in a stable order. */
export function mergeDevices<T extends { id: string }>(
  own: readonly T[],
  shared: readonly T[],
): { own: T[]; shared: T[] } {
  const seen = new Set(own.map((device) => device.id));
  return { own: [...own], shared: shared.filter((device) => !seen.has(device.id)) };
}
