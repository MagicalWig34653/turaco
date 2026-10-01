import { localInputToIso } from '../../platform/format/format';
import type { AuditFilter } from './types';

export type FormState = {
  actionPrefix: string;
  targetType: string;
  targetId: string;
  actorId: string;
  correlationId: string;
  from: string;
  to: string;
};
export const emptyForm: FormState = {
  actionPrefix: '',
  targetType: '',
  targetId: '',
  actorId: '',
  correlationId: '',
  from: '',
  to: '',
};

/** Form values to API filter; empty fields are omitted and datetime-local becomes RFC 3339. */
export function toAuditFilter(form: FormState): AuditFilter {
  const filter: AuditFilter = {};
  if (form.actionPrefix.trim()) filter.actionPrefix = form.actionPrefix.trim();
  if (form.targetType.trim()) filter.targetType = form.targetType.trim();
  if (form.targetId.trim()) filter.targetId = form.targetId.trim();
  if (form.actorId.trim()) filter.actorId = form.actorId.trim();
  if (form.correlationId.trim()) filter.correlationId = form.correlationId.trim();
  const from = localInputToIso(form.from);
  if (from) filter.from = from;
  const to = localInputToIso(form.to);
  if (to) filter.to = to;
  return filter;
}
