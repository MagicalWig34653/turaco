import { localInputToIso } from '../../platform/format/format';
import type { AuditActorKind, AuditFilter, AuditVia } from './types';

export type FormState = {
  actionPrefix: string;
  module: string;
  targetType: string;
  targetId: string;
  actorId: string;
  actorKind: string;
  systemActor: string;
  via: string;
  correlationId: string;
  from: string;
  to: string;
};
export const emptyForm: FormState = {
  actionPrefix: '',
  module: '',
  targetType: '',
  targetId: '',
  actorId: '',
  actorKind: '',
  systemActor: '',
  via: '',
  correlationId: '',
  from: '',
  to: '',
};

/** Known non-human actors; the API accepts any name, the list only drives the picker. */
export const systemActors = ['cli', 'directory-sync', 'login', 'access-expiry', 'worker'] as const;
export const viaValues: readonly AuditVia[] = ['ai', 'mcp'];
export const actorKinds: readonly AuditActorKind[] = ['user', 'system'];
/** Module keys that write Audit Events (the action prefix before the first dot). */
export const auditModules = [
  'authorization',
  'organization',
  'platform',
  'modules',
  'catalog',
  'tickets',
  'tasks',
  'endpoints',
  'software',
  'deployments',
  'security',
  'ai',
  'presence',
] as const;

/** Form values to API filter; empty fields are omitted and datetime-local becomes RFC 3339. */
export function toAuditFilter(form: FormState): AuditFilter {
  const filter: AuditFilter = {};
  if (form.actionPrefix.trim()) filter.actionPrefix = form.actionPrefix.trim();
  // The server refuses `module` together with `actionPrefix`; the explicit prefix wins.
  else if (form.module) filter.module = form.module;
  if (form.targetType.trim()) filter.targetType = form.targetType.trim();
  if (form.targetId.trim()) filter.targetId = form.targetId.trim();
  if (form.actorId.trim()) filter.actorId = form.actorId.trim();
  if (form.actorKind === 'user' || form.actorKind === 'system') filter.actorKind = form.actorKind;
  if (form.systemActor.trim()) filter.systemActor = form.systemActor.trim();
  if (form.via === 'ai' || form.via === 'mcp') filter.via = form.via;
  if (form.correlationId.trim()) filter.correlationId = form.correlationId.trim();
  const from = localInputToIso(form.from);
  if (from) filter.from = from;
  const to = localInputToIso(form.to);
  if (to) filter.to = to;
  return filter;
}
