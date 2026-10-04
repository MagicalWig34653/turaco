import type { Change, ResourceType } from './types';
/** The detail response is authoritative, including when it omits an empty list. */
export function allowedActions(change: Change): string[] {
  return change.allowedOperations ?? [];
}
export function resourceLabel(
  node: {
    type: ResourceType;
    id: string;
    name?: string | null;
    reference?: string | null;
    hidden?: boolean;
  },
  restricted: (type: ResourceType) => string,
): string {
  return node.hidden ? restricted(node.type) : node.name || node.reference || node.id;
}
