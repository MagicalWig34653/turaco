export type ApiError = {
  error: {
    code: string;
    message: string;
    requestId?: string;
  };
};

export class ApiClient {
  async get<T>(path: string, signal?: AbortSignal): Promise<T> {
    const response = await fetch(path, {
      method: 'GET',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
      signal: signal ?? null,
    });
    if (!response.ok) {
      const body = (await response.json().catch(() => null)) as ApiError | null;
      throw new Error(body?.error.message ?? `Request failed with status ${response.status}`);
    }
    return (await response.json()) as T;
  }
}

export const api = new ApiClient();
