import type { MessageKey } from '../../platform/i18n/i18n';
import type { ApprovalPreview, ApprovalStep } from './types';

/** Pure presentation logic for the approval route shown before a request is sent. */

export type PreviewState = 'none' | 'route' | 'blocked';

export function previewState(
  preview: ApprovalPreview | null | undefined,
): PreviewState | 'unknown' {
  if (!preview) return 'unknown';
  if (!preview.approvalRequired) return 'none';
  return preview.canSubmit && preview.steps.every((step) => step.resolved) ? 'route' : 'blocked';
}

/** Message and parameters for one step ("Your manager: Anna Weber"). */
export function stepText(step: ApprovalStep): { key: MessageKey; params: Record<string, string> } {
  const name = step.approverName ?? '';
  if (!step.resolved) return { key: 'catalog.approval.step.unresolved', params: {} };
  const kind = step.kind === 'manager' ? 'manager' : step.kind === 'team' ? 'team' : 'user';
  if (step.fallback) return { key: 'catalog.approval.step.fallback', params: { name } };
  return { key: `catalog.approval.step.${kind}` as MessageKey, params: { name } };
}
