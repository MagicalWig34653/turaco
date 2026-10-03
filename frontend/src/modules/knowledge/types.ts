// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type ArticleStatus = 'draft' | 'published' | 'retired';
export type Audience = 'internal' | 'employee';

export type Article = {
  id: string;
  reference: string;
  title: string;
  summary: string;
  body?: string;
  audience: Audience;
  status: ArticleStatus;
  publishedAt: string | null;
  version: number;
  updatedAt: string;
};
