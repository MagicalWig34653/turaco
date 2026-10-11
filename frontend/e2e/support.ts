import type { Page, Route } from '@playwright/test';

export type MockOptions = {
  methods?: { password: boolean; kerberos: boolean; emergency: boolean; entra?: boolean };
  /** Credentials accepted by POST /api/v1/auth/login. */
  validLogin?: { identifier: string; password: string };
  permissions?: string[];
};

export type MockState = { signedIn: boolean; requests: string[] };

const json = (route: Route, status: number, body: unknown) =>
  route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

/**
 * Mocks the whole API surface the shell touches. Unlisted /api routes answer with an empty page so a screen
 * renders its empty state; the tests therefore never depend on a running backend.
 */
export async function mockApi(page: Page, options: MockOptions = {}): Promise<MockState> {
  const state: MockState = { signedIn: false, requests: [] };
  const methods = options.methods ?? { password: true, kerberos: false, emergency: false };
  const valid = options.validLogin ?? { identifier: 'ada', password: 'correct horse battery' };

  await page.route(
    (url) => /^\/(api\/v1|health)\//.test(url.pathname),
    async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname.replace(/^\/api\/v1/, '');
      state.requests.push(`${request.method()} ${path}`);
      switch (`${request.method()} ${path}`) {
        case 'GET /auth/methods':
          return json(route, 200, methods);
        case 'GET /auth/session':
          if (!state.signedIn) {
            return json(route, 401, {
              error: { code: 'platform.unauthenticated', message: 'unauthenticated' },
            });
          }
          return json(route, 200, {
            userId: '00000000-0000-7000-8000-000000000001',
            authMethod: 'password',
            expiresAt: '2099-01-01T00:00:00Z',
            permissions: options.permissions ?? [],
            displayName: 'Ada Admin',
            locale: 'en',
          });
        case 'POST /auth/login': {
          const body = request.postDataJSON() as { identifier?: string; password?: string };
          if (body.identifier === valid.identifier && body.password === valid.password) {
            state.signedIn = true;
            return route.fulfill({ status: 204 });
          }
          return json(route, 401, {
            error: { code: 'platform.invalid_credentials', message: 'invalid' },
          });
        }
        case 'GET /ai/status':
          return json(route, 200, {
            enabled: false,
            permissions: {
              use: false,
              settingsView: false,
              settingsManage: false,
              usageView: false,
            },
            provider: null,
            conversationTtlMinutes: 0,
            usage: null,
          });
        case 'GET /presence/status':
          return json(route, 200, {
            enabled: false,
            permissions: {
              manageOwn: false,
              viewAvailability: false,
              viewEntries: false,
              manageEntries: false,
              manageTeams: false,
              admin: false,
            },
          });
        case 'POST /auth/logout':
          state.signedIn = false;
          return route.fulfill({ status: 204 });
        default:
          return json(route, 200, { items: [] });
      }
    },
  );
  return state;
}
