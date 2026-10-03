import { api } from '../../platform/api/client';
import { registerErrorMessages, registerErrorResolver } from '../../platform/api/errorMessages';
import type { Page } from '../../platform/api/types';
import type { Article, ArticleStatus, Audience } from './types';

type Signal = AbortSignal | undefined;
const enc = encodeURIComponent;

registerErrorMessages({
  'knowledge.version_conflict': 'error.versionConflict',
  'knowledge.invalid_transition': 'error.knowledgeInvalidTransition',
  'knowledge.not_found': 'error.notFound',
});
registerErrorResolver((error) =>
  error.code.startsWith('knowledge.invalid_') ? 'error.invalidRequest' : undefined,
);

export type ArticleInput = { title: string; summary: string; body: string; audience: Audience };

export const knowledgeApi = {
  list: (
    q: string,
    status: ArticleStatus | '',
    cursor?: string,
    signal?: Signal,
    limit = 50,
    any = false,
  ) =>
    api.get<Page<Article>>('/knowledge-articles', {
      signal,
      query: { q, status, limit, cursor, ...(any ? { match: 'any' } : {}) },
    }),
  get: (id: string, signal?: Signal) =>
    api.get<Article>(`/knowledge-articles/${enc(id)}`, { signal }),
  create: (body: ArticleInput) => api.post<Article>('/knowledge-articles', body),
  update: (id: string, expectedVersion: number, body: ArticleInput) =>
    api.patch<Article>(`/knowledge-articles/${enc(id)}`, { expectedVersion, ...body }),
  publish: (id: string, expectedVersion: number) =>
    api.post<Article>(`/knowledge-articles/${enc(id)}/publish`, { expectedVersion }),
  retire: (id: string, expectedVersion: number) =>
    api.post<Article>(`/knowledge-articles/${enc(id)}/retire`, { expectedVersion }),
};
