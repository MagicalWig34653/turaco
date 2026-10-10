import { api } from '../api/client';
import { registerErrorMessages } from '../api/errorMessages';
import type { Attachment, AttachmentAudience, AttachmentConfig } from './model';

registerErrorMessages({
  'attachments.invalid_request': 'attachments.error.invalid',
  'attachments.not_found': 'attachments.error.notFound',
  'attachments.limit': 'attachments.error.limit',
  'attachments.too_large': 'attachments.error.tooLarge',
  'attachments.unsupported_type': 'attachments.error.unsupportedType',
  'attachments.scan_pending': 'attachments.error.scanPending',
  'attachments.quarantined': 'attachments.error.quarantined',
  'attachments.scan_failed': 'attachments.error.scanFailed',
  'attachments.not_configured': 'attachments.error.notConfigured',
});

type Signal = AbortSignal | undefined;

export const attachmentsApi = {
  config: (signal?: Signal) => api.get<AttachmentConfig>('/attachments/config', { signal }),
  list: (ownerType: string, ownerId: string, signal?: Signal) =>
    api.get<{ items: Attachment[] }>('/attachments', { signal, query: { ownerType, ownerId } }),
  upload: (ownerType: string, ownerId: string, file: File, audience?: AttachmentAudience) => {
    const form = new FormData();
    form.append('file', file, file.name);
    return api.post<Attachment>('/attachments', form, { query: { ownerType, ownerId, audience } });
  },
  remove: (id: string) => api.delete<void>(`/attachments/${encodeURIComponent(id)}`),
  contentUrl: (id: string) => `/api/v1/attachments/${encodeURIComponent(id)}/content`,
};
