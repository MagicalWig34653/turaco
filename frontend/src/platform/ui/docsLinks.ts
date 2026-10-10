/** Where the project documentation is published; links open in a new tab. */
const DOCS_BASE = 'https://github.com/MagicalWig34653/turaco/blob/main/docs';

export const docsLinks = {
  /** How Services, affected resources and changes fit together. */
  infrastructureChange: `${DOCS_BASE}/workflows/infrastructure-change.md`,
} as const;

/**
 * Public URL of a repository documentation path such as `docs/integrations/advisory-feeds.md`.
 * Only relative `docs/` paths without traversal are linked; anything else yields undefined.
 */
export function docsPathUrl(path: string | undefined): string | undefined {
  if (!path || !/^docs\/[A-Za-z0-9._/-]+$/.test(path) || path.includes('..')) return undefined;
  return `${DOCS_BASE.replace(/\/docs$/, '')}/${path}`;
}
