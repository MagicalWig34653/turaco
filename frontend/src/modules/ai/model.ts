import type { CanFn } from '../../platform/session/permissions';
import type { MessageKey } from '../../platform/i18n/i18n';
import type {
  AuthorizeParams,
  McpClientFields,
  Proposal,
  ProviderFields,
  ResourceRef,
  ScopeRequest,
  SettingsFields,
  Status,
} from './types';
export function aiCan(status: Status | undefined, fallback: CanFn): CanFn {
  return (permission) => {
    if (!permission.startsWith('ai.')) return fallback(permission);
    if (!status || (permission === 'ai.use' && !status.enabled)) return false;
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
  'ai.proposal_hash_mismatch': 'ai.error.proposal_hash_mismatch',
  'ai.proposal_not_pending': 'ai.error.proposal_not_pending',
  'ai.proposal_in_progress': 'ai.error.proposal_in_progress',
  'ai.execution_unknown': 'ai.error.execution_unknown',
  'ai.mcp_invalid_client': 'ai.error.mcp_invalid_client',
  'ai.mcp_invalid_request': 'ai.error.mcp_invalid_request',
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
    mcpEnabled: s.mcpEnabled,
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
  proposed: 'ai.outcome.proposed',
  proposal_limit: 'ai.outcome.proposal_limit',
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

/**
 * Reads an OAuth authorization request (RFC 6749 section 4.1.1) from the consent page's query string. Only the known
 * parameters are taken; the server validates every value and decides whether a redirect is allowed at all.
 */
export function authorizeParams(search: string): AuthorizeParams {
  const q = new URLSearchParams(search);
  const get = (key: string) => q.get(key) ?? '';
  return {
    responseType: get('response_type'),
    clientId: get('client_id'),
    redirectUri: get('redirect_uri'),
    codeChallenge: get('code_challenge'),
    codeChallengeMethod: get('code_challenge_method'),
    resource: get('resource'),
    scope: get('scope'),
    state: get('state'),
  };
}
export function authorizeBody(params: AuthorizeParams, decision?: 'approve' | 'deny') {
  return { ...params, ...(decision ? { decision } : {}) };
}
/** Redirect URIs are entered one per line; blank lines are dropped, the rest is sent unchanged (exact match). */
export function parseRedirectUris(text: string): string[] {
  return text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line !== '');
}
export function mcpClientBody(c: McpClientFields, expectedVersion?: number) {
  return {
    displayName: c.displayName,
    redirectUris: c.redirectUris,
    allowedDataClasses: c.allowedDataClasses,
    enabled: c.enabled,
    ...(expectedVersion === undefined ? {} : { expectedVersion }),
  };
}

/** The confirmation body: only the hash of the parameters the User was shown (the server holds the parameters). */
export function confirmBody(p: Proposal) {
  return { parametersHash: p.parametersHash };
}
const paramLabels: Record<string, MessageKey> = {
  ticketId: 'ai.param.ticketId',
  body: 'ai.param.body',
};
/**
 * The exact parameters of a proposal for the confirm dialog: every key, sorted, with its complete value as text.
 * Nothing is truncated, summarized or interpreted (no Markdown or links).
 */
export function proposalParams(
  p: Proposal,
): { key: string; label: MessageKey | null; value: string }[] {
  if (!p.parameters) return [];
  return Object.keys(p.parameters)
    .sort()
    .map((key) => {
      const v = p.parameters?.[key];
      return {
        key,
        label: paramLabels[key] ?? null,
        value: typeof v === 'string' ? v : JSON.stringify(v),
      };
    });
}
/** Whole seconds until the confirmation deadline (0 when it passed). */
export function remainingSeconds(expiresAt: string, now: number): number {
  const ms = Date.parse(expiresAt) - now;
  return Number.isFinite(ms) && ms > 0 ? Math.floor(ms / 1000) : 0;
}
/**
 * Confirm is offered while the proposal awaits confirmation and, for a confirmed proposal whose execution outcome
 * is not known, as a retry (the server allows it after the lease and never applies it twice).
 */
export function canConfirm(p: Proposal, now: number): boolean {
  if (p.parameters === null) return false;
  if (p.state === 'confirmed') return true;
  return p.state === 'proposed' && remainingSeconds(p.expiresAt, now) > 0;
}
const proposalOutcomes: Record<string, MessageKey> = {
  ok: 'ai.proposalOutcome.ok',
  'ai.proposal_stale': 'ai.proposalOutcome.stale',
  permission_denied: 'ai.proposalOutcome.permission_denied',
  forbidden: 'ai.proposalOutcome.permission_denied',
  not_found: 'ai.proposalOutcome.not_found',
  rejected: 'ai.proposalOutcome.rejected',
  parameters_tampered: 'ai.proposalOutcome.parameters_tampered',
  tool_unavailable: 'ai.proposalOutcome.tool_unavailable',
  'ai.execution_unknown': 'ai.proposalOutcome.execution_unknown',
  dismissed: 'ai.proposalOutcome.dismissed',
  expired: 'ai.proposalOutcome.expired',
};
export function proposalOutcomeKey(code: string | null): MessageKey | null {
  if (!code) return null;
  return proposalOutcomes[code] ?? 'ai.proposalOutcome.unknown';
}
