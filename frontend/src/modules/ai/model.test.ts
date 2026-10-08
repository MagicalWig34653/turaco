import { describe, expect, it } from 'vitest';
import {
  aiCan,
  consentBody,
  errorMessages,
  messageBody,
  providerBody,
  settingsBody,
} from './model';
import type { Provider, Settings, Status } from './types';
import { en } from '../../platform/i18n/messages.en';
import { de } from '../../platform/i18n/messages.de';
import { canViewRoute, appRoutes } from '../../app/routes';
import { navigationCommands } from '../../platform/ui/shell/paletteCommands';
import { errorMessageKey } from '../../platform/api/errorMessages';
import './api';
const status: Status = {
  enabled: true,
  permissions: { use: true, settingsView: false, settingsManage: false, usageView: false },
  provider: null,
  conversationTtlMinutes: 30,
  usage: null,
};
const provider: Provider = {
  id: 'provider-id',
  version: 7,
  kind: 'openai_compatible',
  displayName: 'Local',
  endpointUrl: 'http://localhost:11434',
  model: 'model',
  local: true,
  allowedDataClasses: ['public_reference'],
  dpaRecordedOn: null,
  noTrainingConfirmed: false,
  region: '',
  secretRef: 'ai-key',
  enabled: true,
  priceInPerMTok: 0,
  priceOutPerMTok: 0,
};
const settings: Settings = {
  version: 4,
  enabled: true,
  retainConversations: false,
  retentionDays: 7,
  userRequestsPerHour: 10,
  userRequestsPerDay: 100,
  userTokensPerDay: 10000,
  installationTokensPerDay: 100000,
  maxOutputTokens: 1024,
  maxToolIterations: 4,
};
describe('status gates', () => {
  it('fails closed before status, when disabled and for unknown permissions', () => {
    for (const value of [undefined, { ...status, enabled: false }]) {
      const can = aiCan(value, () => true);
      for (const key of [
        'ai.use',
        'ai.admin',
        'ai.settings.view',
        'ai.settings.manage',
        'ai.usage.view',
      ])
        expect(can(key)).toBe(false);
      expect(can('tickets.view')).toBe(true);
    }
    expect(aiCan(status, () => true)('ai.unknown')).toBe(false);
    expect(aiCan(status, () => true)('ai.admin')).toBe(false);
  });
  it('uses status permissions, not a broad session permission', () => {
    expect(aiCan(status, () => false)('ai.use')).toBe(true);
    expect(
      aiCan(
        { ...status, permissions: { ...status.permissions, use: false } },
        () => true,
      )('ai.use'),
    ).toBe(false);
  });
  it.each(['settingsView', 'settingsManage', 'usageView'] as const)(
    'permits the admin route for %s only with an enabled status',
    (flag) => {
      const can = aiCan(
        { ...status, permissions: { ...status.permissions, [flag]: true } },
        () => false,
      );
      const route = appRoutes.find((r) => r.id === 'aiAdmin')!;
      expect(canViewRoute(can, route)).toBe(true);
      expect(
        navigationCommands(appRoutes, can, (r) => r.titleKey).some((r) => r.id === 'aiAdmin'),
      ).toBe(true);
      expect(can('ai.settings.manage')).toBe(flag === 'settingsManage');
      expect(can('ai.usage.view')).toBe(flag === 'usageView');
    },
  );
});
describe('closed request models', () => {
  it('sends only new user text and explicit context, never a client transcript', () => {
    expect(messageBody(' hello ')).toEqual({ text: 'hello' });
    expect(messageBody('next', 'opaque', { type: 'ticket', id: 'record' })).toEqual({
      text: 'next',
      conversationId: 'opaque',
      context: [{ type: 'ticket', id: 'record' }],
    });
  });
  it('limits consent to the exact ticket or device and omits model tool instructions', () => {
    expect(
      consentBody('opaque', { resourceType: 'device', resourceId: 'device-id', tool: 'ignored' }),
    ).toEqual({ conversationId: 'opaque', resourceType: 'device', resourceId: 'device-id' });
    expect(
      consentBody('opaque', { resourceType: 'audit', resourceId: 'id', tool: 'ignore' }),
    ).toBeNull();
  });
  it('binds settings and provider edits to the loaded version without server-only fields', () => {
    const policy = settingsBody(
      { ...settings, updatedBy: 'private' } as Settings,
      settings.version,
    );
    expect(policy.expectedVersion).toBe(4);
    expect(policy).not.toHaveProperty('version');
    expect(policy).not.toHaveProperty('updatedBy');
    const body = providerBody(
      { ...provider, secret: 'must not send', createdAt: 'yesterday' } as Provider,
      provider.version,
    );
    expect(body.expectedVersion).toBe(7);
    for (const field of ['secret', 'id', 'version', 'createdAt'])
      expect(body).not.toHaveProperty(field);
    expect(body.secretRef).toBe('ai-key');
    expect(providerBody(provider)).not.toHaveProperty('expectedVersion');
  });
});
describe('AI errors', () => {
  it('localizes every implemented error and fails safely for future codes', () => {
    const codes = [
      'invalid_request',
      'disabled',
      'not_permitted',
      'not_found',
      'version_conflict',
      'rate_limited',
      'budget_exceeded',
      'provider_unavailable',
      'provider_changed',
      'turn_in_progress',
      'conversation_too_large',
      'proposal_expired',
      'proposal_stale',
    ];
    for (const code of codes) {
      const key = errorMessages[`ai.${code}`]!;
      expect(en[key]).toBeTruthy();
      expect(de[key]).toBeTruthy();
      expect(errorMessageKey({ status: 400, code: `ai.${code}` })).toBe(key);
    }
    expect(errorMessageKey({ status: 500, code: 'ai.future_error' })).toBe('ai.error.unknown');
  });
});
