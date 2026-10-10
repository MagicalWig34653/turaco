import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type { AdminSetting, SettingsList, SettingValue } from './types';

registerErrorMessages({
  'settings.version_conflict': 'settings.error.versionConflict',
  'settings.invalid_value': 'settings.error.invalidValue',
  'settings.version_required': 'settings.error.invalidValue',
  'settings.invalid_request': 'settings.error.invalidValue',
  'settings.not_found': 'settings.error.notFound',
});

export const settingsApi = {
  list: (signal?: AbortSignal) => api.get<SettingsList>('/admin/settings', { signal }),
  write: (key: string, value: SettingValue, expectedVersion: number) =>
    api.put<AdminSetting>(`/admin/settings/${encodeURIComponent(key)}`, { value, expectedVersion }),
};
