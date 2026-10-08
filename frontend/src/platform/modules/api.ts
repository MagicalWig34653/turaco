import { api } from '../api/client';
import { switchAction, switchRequest, type Module, type ReasonCode } from './model';
export const modulesApi = {
  status: (signal?: AbortSignal) =>
    api.get<{ items: { key: string; enabled: boolean }[] }>('/modules/status', { signal }),
  list: (signal?: AbortSignal) => api.get<{ items: Module[] }>('/admin/modules', { signal }),
  toggle: (module: Module, reason: ReasonCode) =>
    api.post<Module>(
      `/admin/modules/${encodeURIComponent(module.key)}/${switchAction(module)}`,
      switchRequest(module, reason),
    ),
};
