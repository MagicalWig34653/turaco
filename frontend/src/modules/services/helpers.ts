import type { ServiceNode, TargetType } from './types';
/** Hidden IDs are response-local placeholders and must never be shown. */
export function nodeLabel(node: ServiceNode): string {
  return node.name ?? node.reference ?? node.id;
}
export function impactUrl(type: TargetType, id: string): string {
  return `/impact?type=${encodeURIComponent(type)}&id=${encodeURIComponent(id)}`;
}
export function impactTarget(search: string): { type: TargetType; id: string } | null {
  const params = new URLSearchParams(search);
  const type = params.get('type');
  const id = params.get('id');
  if (!id || !['service', 'vm', 'asset', 'location'].includes(type ?? '')) return null;
  return { type: type as TargetType, id };
}
