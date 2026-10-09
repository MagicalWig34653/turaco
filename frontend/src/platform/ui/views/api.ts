import { api } from '../../api/client';
import { registerErrorMessages } from '../../api/errorMessages';
import type { Node, Sort } from '../query/filterModel';

// Types mirror api/openapi/openapi.yaml (Saved Views, Shares, Pins, Pin Rules, sidebar).

export type ViewResource = 'tickets' | 'devices' | 'tasks';
export type ViewAccess = 'owner' | 'edit' | 'use' | 'admin';
export type ShareSubjectType = 'user' | 'team' | 'role' | 'everyone';
export type ShareLevel = 'use' | 'edit';

export type ViewDefinition = {
  filter?: { v: 1; root?: Node; search?: string; sort?: Sort[] };
  columns?: string[];
};
export type ViewShare = {
  subjectType: ShareSubjectType;
  subjectId?: string;
  level: ShareLevel;
  grantedBy?: string;
  grantedAt?: string;
};
export type SavedView = {
  id: string;
  resource: ViewResource;
  name: string;
  description: string;
  ownerId: string;
  ownerName?: string;
  definition: ViewDefinition;
  visibility: 'private' | 'shared';
  version: number;
  access: ViewAccess;
  moduleEnabled: boolean;
  pinned: boolean;
  lastEditedBy?: string;
  archivedAt?: string;
  createdAt: string;
  updatedAt: string;
  shares?: ViewShare[];
  /** Built-in System View (client-side marker): read-only, cannot be shared, pinned or edited. */
  system?: boolean;
  /** i18n key of a System View's name. */
  nameKey?: string;
};
export type ViewPin = {
  viewId: string;
  name: string;
  resource: string;
  groupKey: string;
  position: number;
  hidden: boolean;
  /** `system` entries are built-in System Views; their `viewId` is the key (`system:tickets:...`). */
  source: 'user' | 'rule' | 'system';
  /** i18n key of a System View's name (then `name` is empty). */
  nameKey?: string;
  /** The Queue id of a Queue entry. */
  ref?: string;
  /** Capped count; absent when not requested or unavailable. */
  count?: number;
  countCapped?: boolean;
  /** `unavailable` is never zero: the count could not be computed. */
  countStatus?: 'ok' | 'unavailable';
};
export type ViewCount = {
  id: string;
  count?: number;
  capped?: boolean;
  status: 'ok' | 'unavailable';
};
export type SystemViewInfo = {
  id: string;
  handle?: string;
  name: string;
  nameKey?: string;
  ref?: string;
  resource: string;
  groupKey: string;
  position: number;
  system: true;
};
export type SidebarResponse = {
  groups: { key: string; items: ViewPin[] }[];
  collapsedGroups: string[];
};
export type PinPayload = { viewId: string; groupKey: string; position?: number; hidden?: boolean };
export type ViewPinRule = {
  id: string;
  viewId: string;
  subjectType: 'team' | 'role';
  subjectId: string;
  groupKey: string;
  position: number;
  createdBy: string;
  createdAt: string;
};
export type ViewWarning = { code: string; path: string };
export type ViewRunPage<T = unknown> = {
  items: T[];
  nextCursor?: string;
  count?: number;
  countCapped?: boolean;
  warnings: ViewWarning[];
  view: { id: string; name: string; resource: string; version: number };
};

registerErrorMessages({
  'views.invalid_request': 'error.invalidRequest',
  'views.not_found': 'views.error.notFound',
  'views.subject_not_found': 'error.subjectNotFound',
  'views.module_disabled': 'modules.disabledInfo',
  'views.conflict': 'views.error.conflict',
  'views.archived': 'views.error.archived',
  'views.name_taken': 'views.error.nameTaken',
  'views.limit_reached': 'views.error.limitReached',
  'views.not_permitted': 'error.forbidden',
});

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

export const viewsApi = {
  list: (
    query: {
      resource?: ViewResource;
      scope?: 'all' | 'mine' | 'shared' | 'admin';
      archived?: boolean;
    },
    signal?: Signal,
  ) =>
    api.get<{ items: SavedView[]; nextCursor?: string }>('/views', {
      signal,
      query: { ...query, limit: 100 },
    }),
  get: (id: string, signal?: Signal) => api.get<SavedView>(`/views/${enc(id)}`, { signal }),
  create: (body: {
    resource: ViewResource;
    name: string;
    description?: string;
    definition: ViewDefinition;
  }) => api.post<SavedView>('/views', body),
  update: (
    id: string,
    body: {
      expectedVersion: number;
      name?: string;
      description?: string;
      definition?: ViewDefinition;
    },
  ) => api.patch<SavedView>(`/views/${enc(id)}`, body),
  archive: (id: string, expectedVersion: number) =>
    api.post<SavedView>(`/views/${enc(id)}/archive`, { expectedVersion }),
  restore: (id: string, expectedVersion: number) =>
    api.post<SavedView>(`/views/${enc(id)}/restore`, { expectedVersion }),
  takeOver: (id: string, expectedVersion: number) =>
    api.post<SavedView>(`/views/${enc(id)}/take-over`, { expectedVersion }),
  duplicate: (id: string) => api.post<SavedView>(`/views/${enc(id)}/duplicate`),
  setShares: (
    id: string,
    expectedVersion: number,
    shares: { subjectType: ShareSubjectType; subjectId?: string; level: ShareLevel }[],
  ) => api.put<SavedView>(`/views/${enc(id)}/shares`, { expectedVersion, shares }),
  run: <T>(
    id: string,
    query: { cursor?: string | undefined; limit?: number; count?: boolean },
    signal?: Signal,
  ) => api.get<ViewRunPage<T>>(`/views/${enc(id)}/results`, { signal, query }),
  pins: (signal?: Signal) => api.get<{ items: ViewPin[] }>('/me/pins', { signal }),
  replacePins: (pins: PinPayload[]) => api.put<{ items: ViewPin[] }>('/me/pins', { pins }),
  /** The built-in System Views the caller has (definitions are not returned). */
  system: (resource: ViewResource, signal?: Signal) =>
    api.get<{ items: SystemViewInfo[] }>('/views', {
      signal,
      query: { scope: 'system', resource },
    }),
  /** Capped counts of up to 30 View ids, System View keys or `queue:<id>` handles. */
  counts: (ids: readonly string[], signal?: Signal) =>
    api.get<{ items: ViewCount[] }>('/views/counts', {
      signal,
      query: { ids: ids.slice(0, 30).join(',') },
    }),
  sidebar: (signal?: Signal) => api.get<SidebarResponse>('/me/sidebar', { signal }),
  setSidebarState: (collapsedGroups: string[]) =>
    api.put<void>('/me/sidebar-state', { collapsedGroups }),
  pinRules: (id: string, signal?: Signal) =>
    api.get<{ items: ViewPinRule[] }>(`/views/${enc(id)}/pin-rules`, { signal }),
  createPinRule: (
    id: string,
    body: { subjectType: 'team' | 'role'; subjectId: string; groupKey: string; position?: number },
  ) => api.post<ViewPinRule>(`/views/${enc(id)}/pin-rules`, body),
  deletePinRule: (id: string, ruleId: string) =>
    api.delete<void>(`/views/${enc(id)}/pin-rules/${enc(ruleId)}`),
};

const SIDEBAR_EVENT = 'turaco:sidebar-changed';
/** Pins changed somewhere (pin button, Manage pins): the sidebar reloads its pinned section. */
export function notifySidebarChanged(): void {
  window.dispatchEvent(new Event(SIDEBAR_EVENT));
}
export function onSidebarChanged(listener: () => void): () => void {
  window.addEventListener(SIDEBAR_EVENT, listener);
  return () => window.removeEventListener(SIDEBAR_EVENT, listener);
}
