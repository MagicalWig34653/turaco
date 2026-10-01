export type RouteMatch<R extends { pattern: string }> = {
  route: R;
  params: Record<string, string>;
};

/** Leading slash, no trailing slash, no duplicate slashes. */
export function normalizePath(pathname: string): string {
  const collapsed = `/${pathname}`.replace(/\/{2,}/g, '/');
  return collapsed.length > 1 ? collapsed.replace(/\/$/, '') : collapsed;
}

function split(path: string): string[] {
  return normalizePath(path).split('/').filter(Boolean);
}

/**
 * Matches the first route whose pattern fits. ":name" segments capture one path segment.
 * Order the routes so that static patterns ("/admin/roles/new") precede parameterised ones.
 */
export function matchRoute<R extends { pattern: string }>(
  routes: readonly R[],
  pathname: string,
): RouteMatch<R> | null {
  const actual = split(pathname);
  for (const route of routes) {
    const expected = split(route.pattern);
    if (expected.length !== actual.length) continue;
    const params: Record<string, string> = {};
    let ok = true;
    for (let i = 0; i < expected.length; i += 1) {
      const want = expected[i] as string;
      const got = actual[i] as string;
      if (want.startsWith(':')) {
        try {
          params[want.slice(1)] = decodeURIComponent(got);
        } catch {
          ok = false;
          break;
        }
      } else if (want !== got) {
        ok = false;
        break;
      }
    }
    if (ok) return { route, params };
  }
  return null;
}

/** Fills ":name" segments (URL-encoded) of a pattern. */
export function buildPath(pattern: string, params: Record<string, string> = {}): string {
  return pattern.replace(/:(\w+)/g, (_, name: string) => encodeURIComponent(params[name] ?? ''));
}
