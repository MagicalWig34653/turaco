export type ApiErrorBody = {
  error: {
    code: string;
    message: string;
    requestId?: string;
    fields?: unknown;
    blockers?: unknown;
  };
};

/** Keeps only string-to-string entries of a validation `fields` object. */
export function parseFields(value: unknown): Record<string, string> | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined;
  const out: Record<string, string> = {};
  for (const [key, code] of Object.entries(value)) {
    if (typeof code === 'string') out[key] = code;
  }
  return out;
}

export const NETWORK_ERROR_CODE = 'platform.network_error';
export const INVALID_RESPONSE_CODE = 'platform.invalid_response';
export const UNKNOWN_ERROR_CODE = 'platform.unknown_error';

/** Typed error for every non-2xx response and for transport failures (status 0). */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string | undefined;
  readonly retryAfterSeconds: number | undefined;
  /** Per-field validation codes of a 422 response (field key to code); empty otherwise. */
  readonly fields: Readonly<Record<string, string>>;
  readonly blockers: readonly string[];

  constructor(init: {
    status: number;
    code: string;
    message: string;
    requestId?: string | undefined;
    retryAfterSeconds?: number | undefined;
    fields?: Record<string, string> | undefined;
    blockers?: string[] | undefined;
  }) {
    super(init.message);
    this.name = 'ApiError';
    this.status = init.status;
    this.code = init.code;
    this.requestId = init.requestId;
    this.retryAfterSeconds = init.retryAfterSeconds;
    this.fields = init.fields ?? {};
    this.blockers = init.blockers ?? [];
  }
}

export function isApiError(value: unknown): value is ApiError {
  return value instanceof ApiError;
}

export function isAbortError(value: unknown): boolean {
  return value instanceof Error && value.name === 'AbortError';
}

export type QueryValue = string | number | boolean | undefined | null;
export type Query = Record<string, QueryValue>;

/** Builds a query string ("?a=b") omitting empty, null, undefined and false values. */
export function buildQuery(query?: Query): string {
  if (!query) return '';
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === false || value === '') continue;
    params.set(key, String(value));
  }
  const text = params.toString();
  return text ? `?${text}` : '';
}

/** Parses a Retry-After header holding delta-seconds; HTTP dates are not used by the API. */
export function parseRetryAfter(header: string | null): number | undefined {
  if (header === null) return undefined;
  const trimmed = header.trim();
  if (!/^\d+$/.test(trimmed)) return undefined;
  return Number.parseInt(trimmed, 10);
}

export type RequestOptions = {
  query?: Query;
  body?: unknown;
  signal?: AbortSignal | undefined;
  /** Login-style endpoints answer 401 as a normal result; they must not end the session. */
  skipUnauthorizedHandler?: boolean;
};

type FetchFn = (input: string, init?: RequestInit) => Promise<Response>;

export class ApiClient {
  private readonly fetchImpl: FetchFn;
  private readonly baseUrl: string;
  private readonly unauthorizedListeners = new Set<() => void>();

  constructor(fetchImpl?: FetchFn, baseUrl = '/api/v1') {
    this.fetchImpl = fetchImpl ?? ((input, init) => globalThis.fetch(input, init));
    this.baseUrl = baseUrl;
  }

  /** Registers a callback for 401 responses of authenticated calls; returns an unsubscribe function. */
  onUnauthorized(listener: () => void): () => void {
    this.unauthorizedListeners.add(listener);
    return () => {
      this.unauthorizedListeners.delete(listener);
    };
  }

  async request<T>(method: string, path: string, options: RequestOptions = {}): Promise<T> {
    const headers: Record<string, string> = { Accept: 'application/json' };
    const init: RequestInit = { method, credentials: 'same-origin', headers };
    if (options.body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(options.body);
    }
    if (options.signal) init.signal = options.signal;

    let response: Response;
    try {
      response = await this.fetchImpl(`${this.baseUrl}${path}${buildQuery(options.query)}`, init);
    } catch (cause) {
      if (isAbortError(cause)) throw cause;
      throw new ApiError({ status: 0, code: NETWORK_ERROR_CODE, message: 'Network error' });
    }

    if (response.ok) {
      if (response.status === 204) return undefined as T;
      try {
        const text = await response.text();
        return (text === '' ? undefined : JSON.parse(text)) as T;
      } catch (cause) {
        if (isAbortError(cause)) throw cause;
        throw new ApiError({
          status: response.status,
          code: INVALID_RESPONSE_CODE,
          message: 'Invalid response',
        });
      }
    }

    const error = await toApiError(response);
    if (response.status === 401 && !options.skipUnauthorizedHandler) {
      for (const listener of [...this.unauthorizedListeners]) listener();
    }
    throw error;
  }

  get<T>(path: string, options?: RequestOptions): Promise<T> {
    return this.request<T>('GET', path, options);
  }
  post<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
    return this.request<T>('POST', path, { ...options, body });
  }
  put<T>(path: string, body: unknown, options?: RequestOptions): Promise<T> {
    return this.request<T>('PUT', path, { ...options, body });
  }
  patch<T>(path: string, body: unknown, options?: RequestOptions): Promise<T> {
    return this.request<T>('PATCH', path, { ...options, body });
  }
  delete<T>(path: string, options?: RequestOptions): Promise<T> {
    return this.request<T>('DELETE', path, options);
  }
}

/** Maps an error response (envelope {error:{code,message,requestId}}) to an ApiError. */
export async function toApiError(response: Response): Promise<ApiError> {
  const retryAfterSeconds = parseRetryAfter(response.headers.get('Retry-After'));
  let body: Partial<ApiErrorBody> | null = null;
  try {
    body = (await response.json()) as Partial<ApiErrorBody> | null;
  } catch {
    body = null;
  }
  const envelope = body && typeof body === 'object' ? body.error : undefined;
  const code = typeof envelope?.code === 'string' ? envelope.code : UNKNOWN_ERROR_CODE;
  const message =
    typeof envelope?.message === 'string'
      ? envelope.message
      : `Request failed with status ${response.status}`;
  const requestId = typeof envelope?.requestId === 'string' ? envelope.requestId : undefined;
  return new ApiError({
    status: response.status,
    code,
    message,
    requestId,
    retryAfterSeconds,
    fields: parseFields(envelope?.fields),
    blockers: Array.isArray(envelope?.blockers)
      ? envelope.blockers.filter((key): key is string => typeof key === 'string')
      : [],
  });
}

export const api = new ApiClient();
