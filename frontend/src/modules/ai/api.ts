import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import { errorMessages, messageBody, providerBody, settingsBody } from './model';
import type {
  Provider,
  ProviderFields,
  ResourceRef,
  Settings,
  Status,
  Turn,
  UsageRow,
} from './types';
registerErrorMessages(errorMessages);
registerErrorResolver((error) => (error.code.startsWith('ai.') ? 'ai.error.unknown' : undefined));
export const aiApi = {
  status: (signal?: AbortSignal) => api.get<Status>('/ai/status', { signal }),
  message: (text: string, id?: string, context?: ResourceRef) =>
    api.post<Turn>('/ai/conversations/messages', messageBody(text, id, context)),
  scope: (body: { conversationId: string; resourceType: string; resourceId: string }) =>
    api.post<void>('/ai/conversations/scope', body),
  end: (conversationId: string) => api.post<void>('/ai/conversations/end', { conversationId }),
  transcript: (conversationId: string) =>
    api.post<{ items: { role: 'user' | 'assistant'; content: string }[] }>(
      '/ai/conversations/transcript',
      { conversationId },
    ),
  settings: (signal?: AbortSignal) => api.get<Settings>('/ai/settings', { signal }),
  saveSettings: (s: Settings) => api.put<Settings>('/ai/settings', settingsBody(s, s.version)),
  providers: (signal?: AbortSignal) => api.get<{ items: Provider[] }>('/ai/providers', { signal }),
  saveProvider: (p: ProviderFields, original?: Provider) =>
    original
      ? api.put<Provider>(
          `/ai/providers/${encodeURIComponent(original.id)}`,
          providerBody(p, original.version),
        )
      : api.post<Provider>('/ai/providers', providerBody(p)),
  test: (id: string) =>
    api.post<{ ok: boolean; code: string; durationMs: number }>(
      `/ai/providers/${encodeURIComponent(id)}/test`,
    ),
  usage: (signal?: AbortSignal) =>
    api.get<{ items: UsageRow[] }>('/ai/usage', { signal, query: { days: 30 } }),
};
