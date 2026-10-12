/** Pure helpers of the routing rules screen (docs/product/f5-service-desk-design.md, Queue routing rules). */

export const routingImpacts = ['patient_care', 'blocked', 'impaired', 'request'] as const;

export type RoutingRule = {
  id: string;
  queueId: string;
  position: number;
  name: string;
  keywords: string[];
  impact?: string;
  patientImpact?: boolean;
  enabled: boolean;
  version: number;
};

/** Splits a comma or newline separated keyword entry into trimmed, distinct, non-empty words (at most ten). */
export function parseKeywords(input: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const part of input.split(/[,\n]/)) {
    const word = part.trim();
    const key = word.toLowerCase();
    if (word === '' || seen.has(key)) continue;
    seen.add(key);
    out.push(word);
  }
  return out.slice(0, 10);
}

/** A rule needs at least one condition. */
export function hasCondition(input: {
  keywords: string[];
  impact: string;
  patientImpact: boolean | undefined;
}): boolean {
  return input.keywords.length > 0 || input.impact !== '' || input.patientImpact !== undefined;
}

/** Returns the ids in a new order after moving one rule by `delta` places; unchanged at the edges. */
export function moveRule(ids: readonly string[], id: string, delta: -1 | 1): string[] {
  const index = ids.indexOf(id);
  const target = index + delta;
  if (index < 0 || target < 0 || target >= ids.length) return [...ids];
  const next = [...ids];
  next.splice(index, 1);
  next.splice(target, 0, id);
  return next;
}
