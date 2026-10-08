import type { CanFn } from '../../platform/session/permissions';
import type { MessageKey } from '../../platform/i18n/i18n';
import type { ProviderFields, ResourceRef, ScopeRequest, SettingsFields, Status } from './types';
export function aiCan(status: Status | undefined, fallback: CanFn): CanFn {
  return (permission) => {
    if (!permission.startsWith('ai.')) return fallback(permission);
    if (!status?.enabled) return false;
    const p = status.permissions;
    return (
      (
        {
          'ai.use': p.use,
          'ai.admin': p.settingsView || p.settingsManage || p.usageView,
          'ai.settings.view': p.settingsView || p.settingsManage,
          'ai.settings.manage': p.settingsManage,
          'ai.usage.view': p.usageView,
        } as Record<string, boolean>
      )[permission] === true
    );
  };
}
export const errorMessages: Record<string, MessageKey> = {
  'ai.invalid_request': 'ai.error.invalid_request',
  'ai.disabled': 'ai.error.disabled',
  'ai.not_permitted': 'ai.error.not_permitted',
  'ai.not_found': 'ai.error.not_found',
  'ai.version_conflict': 'ai.error.version_conflict',
  'ai.rate_limited': 'ai.error.rate_limited',
  'ai.budget_exceeded': 'ai.error.budget_exceeded',
  'ai.provider_unavailable': 'ai.error.provider_unavailable',
  'ai.provider_changed': 'ai.error.provider_changed',
  'ai.turn_in_progress': 'ai.error.turn_in_progress',
  'ai.conversation_too_large': 'ai.error.conversation_too_large',
  'ai.proposal_expired': 'ai.error.proposal_expired',
  'ai.proposal_stale': 'ai.error.proposal_stale',
};
export function messageBody(text: string, conversationId?: string, context?: ResourceRef) {
  return {
    text: text.trim(),
    ...(conversationId ? { conversationId } : {}),
    ...(context ? { context: [context] } : {}),
  };
}
export function consentBody(conversationId: string, scope: ScopeRequest) {
  if (!['ticket', 'device'].includes(scope.resourceType)) return null;
  return { conversationId, resourceType: scope.resourceType, resourceId: scope.resourceId };
}
export function settingsBody(s: SettingsFields, expectedVersion: number) {
  return {
    enabled: s.enabled,
    retainConversations: s.retainConversations,
    retentionDays: s.retentionDays,
    userRequestsPerHour: s.userRequestsPerHour,
    userRequestsPerDay: s.userRequestsPerDay,
    userTokensPerDay: s.userTokensPerDay,
    installationTokensPerDay: s.installationTokensPerDay,
    maxOutputTokens: s.maxOutputTokens,
    maxToolIterations: s.maxToolIterations,
    expectedVersion,
  };
}
export function providerBody(p: ProviderFields, expectedVersion?: number) {
  return {
    kind: p.kind,
    displayName: p.displayName,
    endpointUrl: p.endpointUrl,
    model: p.model,
    local: p.local,
    allowedDataClasses: p.allowedDataClasses,
    dpaRecordedOn: p.dpaRecordedOn || null,
    noTrainingConfirmed: p.noTrainingConfirmed,
    region: p.region,
    secretRef: p.secretRef || null,
    enabled: p.enabled,
    priceInPerMTok: p.priceInPerMTok,
    priceOutPerMTok: p.priceOutPerMTok,
    ...(expectedVersion === undefined ? {} : { expectedVersion }),
  };
}

const outcomes: Record<string, MessageKey> = {
  ok: 'ai.outcome.ok',
  not_found: 'ai.outcome.not_found',
  forbidden: 'ai.outcome.forbidden',
  invalid_arguments: 'ai.outcome.invalid_arguments',
  unknown_tool: 'ai.outcome.unknown_tool',
  permission_denied: 'ai.outcome.permission_denied',
  scope_expansion: 'ai.outcome.scope_expansion',
  too_many_calls: 'ai.outcome.too_many_calls',
  tool_error: 'ai.outcome.tool_error',
  egress_violation: 'ai.outcome.egress_violation',
  result_too_large: 'ai.outcome.result_too_large',
  unknown: 'ai.outcome.unknown',
};
export function toolOutcomeKey(outcome: string): MessageKey {
  return outcomes[outcome] ?? 'ai.outcome.unknown';
}
