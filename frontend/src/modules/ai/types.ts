export type ResourceRef = { type: 'ticket' | 'device'; id: string };
export const dataClasses = [
  'public_reference',
  'business_record',
  'personal_contact',
  'device_context',
] as const;
export type DataClass = (typeof dataClasses)[number];
export type UsageCounters = {
  day: string;
  requestsToday: number;
  requestsThisHour: number;
  requestsTodayRemaining: number;
  requestsHourRemaining: number;
  tokensTodayRemaining: number;
  installationTokensRemaining: number;
};
export type Status = {
  enabled: boolean;
  permissions: { use: boolean; settingsView: boolean; settingsManage: boolean; usageView: boolean };
  provider: null | {
    displayName: string;
    local: boolean;
    model: string;
    allowedDataClasses: DataClass[];
  };
  conversationTtlMinutes: number;
  usage: UsageCounters | null;
};
export type ScopeRequest = { resourceType: string; resourceId: string; tool: string };
export type ProposalState =
  'proposed' | 'confirmed' | 'executed' | 'failed' | 'dismissed' | 'expired';
/** An AI Proposal (F12 A-C). parameters are the exact values the confirmation is bound to (null once purged). */
export type Proposal = {
  id: string;
  tool: string;
  effect: 'internal' | '';
  parameters: Record<string, unknown> | null;
  parametersHash: string;
  target: { type: string; id: string };
  targetVersion: number;
  targetLabel: string;
  state: ProposalState;
  createdAt: string;
  expiresAt: string;
  confirmedAt: string | null;
  endedAt: string | null;
  attempt: number;
  outcomeCode: string | null;
  result: { type: string; id: string } | null;
};
export type Turn = {
  conversationId: string;
  answer: string;
  stopReason: 'answer' | 'empty' | 'iteration_limit' | 'malformed_tool_calls';
  toolsUsed: { tool: string; target: ResourceRef | null; itemCount: number; outcome: string }[];
  scopeRequests: ScopeRequest[];
  proposals: Proposal[];
  tokens: { in: number; out: number };
  usage: UsageCounters;
};
export type SettingsFields = {
  enabled: boolean;
  retainConversations: boolean;
  retentionDays: number;
  userRequestsPerHour: number;
  userRequestsPerDay: number;
  userTokensPerDay: number;
  installationTokensPerDay: number;
  maxOutputTokens: number;
  maxToolIterations: number;
  mcpEnabled: boolean;
};
export type Settings = SettingsFields & { version: number };
export type McpClientFields = {
  displayName: string;
  redirectUris: string[];
  allowedDataClasses: DataClass[];
  enabled: boolean;
};
export type McpClient = McpClientFields & {
  id: string;
  clientId: string;
  activeGrants: number;
  version: number;
};
export type McpClientList = {
  items: McpClient[];
  mounted: boolean;
  issuer: string | null;
  resource: string | null;
};
export type McpGrant = {
  id: string;
  clientId: string;
  clientName: string;
  createdAt: string;
  expiresAt: string;
  lastUsedAt: string | null;
};
export type AuthorizeParams = {
  responseType: string;
  clientId: string;
  redirectUri: string;
  codeChallenge: string;
  codeChallengeMethod: string;
  resource: string;
  scope: string;
  state: string;
};
export type AuthorizePreview = {
  clientName: string;
  clientId: string;
  redirectUri: string;
  tools: string[];
  dataClasses: DataClass[];
  grantHours: number;
  scope: string;
};
export type ProviderFields = {
  kind: 'fake' | 'openai_compatible';
  displayName: string;
  endpointUrl: string;
  model: string;
  local: boolean;
  allowedDataClasses: DataClass[];
  dpaRecordedOn: string | null;
  noTrainingConfirmed: boolean;
  region: string;
  secretRef: string | null;
  enabled: boolean;
  priceInPerMTok: number;
  priceOutPerMTok: number;
};
export type Provider = ProviderFields & { id: string; version: number };
export type UsageRow = {
  day: string;
  users: number;
  requests: number;
  tokensIn: number;
  tokensOut: number;
  estimatedCostMicro: number;
};
