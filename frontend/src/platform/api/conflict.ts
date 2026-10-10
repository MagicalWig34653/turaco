import type { ApiError } from './client';

/** What a 409 answer asks the caller to confirm or fix (`error.details`). */
export type Conflict = {
  /** Field names the server refused (for example directory-owned attributes). */
  fields: string[];
  /** Records that still refer to the target; repeat the request with `confirmImpact: true`. */
  counts: Record<string, number>;
  /** Separation-of-duties rule keys; resend with `acknowledgedRules` and a reason. */
  rules: string[];
};

export const IMPACT_CONFIRMATION_CODE = 'organization.impact_confirmation_required';
export const SOD_ACK_CODE = 'access.sod_acknowledgement_required';

function strings(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const out: string[] = [];
  for (const entry of value) {
    if (typeof entry === 'string') out.push(entry);
    else if (typeof entry === 'object' && entry !== null) {
      const name =
        (entry as { field?: unknown; key?: unknown }).field ?? (entry as { key?: unknown }).key;
      if (typeof name === 'string') out.push(name);
    }
  }
  return out;
}

/** Reads `details.fields`, `details.counts` and `details.rules` defensively; unknown shapes are empty. */
export function parseConflict(error: Pick<ApiError, 'details'>): Conflict {
  const details = error.details;
  const empty: Conflict = { fields: [], counts: {}, rules: [] };
  if (typeof details !== 'object' || details === null || Array.isArray(details)) return empty;
  const record = details as { fields?: unknown; counts?: unknown; rules?: unknown };
  const counts: Record<string, number> = {};
  if (
    typeof record.counts === 'object' &&
    record.counts !== null &&
    !Array.isArray(record.counts)
  ) {
    for (const [key, value] of Object.entries(record.counts)) {
      if (typeof value === 'number' && Number.isFinite(value) && value > 0) counts[key] = value;
    }
  }
  return { fields: strings(record.fields), counts, rules: strings(record.rules) };
}

/** True when the server wants an explicit impact confirmation before it acts. */
export function needsImpactConfirmation(
  error: Pick<ApiError, 'status' | 'code' | 'details'>,
): boolean {
  return (
    error.status === 409 &&
    (error.code === IMPACT_CONFIRMATION_CODE || Object.keys(parseConflict(error).counts).length > 0)
  );
}

/** True when the server wants separation-of-duties rules acknowledged with a reason. */
export function needsRuleAcknowledgement(
  error: Pick<ApiError, 'status' | 'code' | 'details'>,
): boolean {
  return (
    error.status === 409 && (error.code === SOD_ACK_CODE || parseConflict(error).rules.length > 0)
  );
}
