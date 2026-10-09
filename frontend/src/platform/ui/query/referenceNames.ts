import { useEffect, useState } from 'react';
import { api } from '../../api/client';

export type ReferenceKind = 'users' | 'teams' | 'assets';

type NameBody = { displayName?: string; name?: string; reference?: string };

/** Names of picked references, so chips show people and teams instead of identifiers. */
const names = new Map<string, string>();
const pending = new Map<string, Promise<void>>();
const listeners = new Set<() => void>();

const cacheKey = (kind: ReferenceKind, id: string) => `${kind}:${id}`;
const notify = () => listeners.forEach((listener) => listener());

export function rememberReference(kind: ReferenceKind, id: string, label: string): void {
  names.set(cacheKey(kind, id), label);
  notify();
}

export function referenceKind(resource: string | undefined): ReferenceKind | undefined {
  return resource === 'users' || resource === 'teams' || resource === 'assets'
    ? resource
    : undefined;
}

function fetchName(kind: ReferenceKind, id: string): Promise<void> {
  const key = cacheKey(kind, id);
  const existing = pending.get(key);
  if (existing) return existing;
  const request = api
    .get<NameBody>(`/${kind}/${encodeURIComponent(id)}`)
    .then((body) => {
      const label = body.displayName ?? body.name ?? body.reference;
      if (label) names.set(key, label);
    })
    .catch(() => {
      // Missing permission or deleted target: the chip falls back to a neutral label.
    })
    .finally(() => {
      pending.delete(key);
      notify();
    });
  pending.set(key, request);
  return request;
}

/** Resolves the display names of the given references; unknown ones load lazily. */
export function useReferenceNames(wanted: ReadonlyArray<{ kind: ReferenceKind; id: string }>) {
  const [, setVersion] = useState(0);
  const signature = wanted.map((item) => cacheKey(item.kind, item.id)).join('|');
  useEffect(() => {
    const listener = () => setVersion((value) => value + 1);
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);
  useEffect(() => {
    for (const item of signature ? signature.split('|') : []) {
      const [kind, id] = item.split(':') as [ReferenceKind, string];
      if (!names.has(item) && !pending.has(item)) void fetchName(kind, id);
    }
  }, [signature]);
  return (kind: ReferenceKind, id: string): string | undefined => names.get(cacheKey(kind, id));
}
