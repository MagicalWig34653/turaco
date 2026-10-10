import type { MessageKey } from '../../platform/i18n/i18n';

/** The task screen and API need one of these; a requester without them sees a plain title. */
export const taskViewPermissions = ['tasks.view', 'tasks.manage', 'tasks.work'] as const;

export function canOpenTasks(can: (permission: string) => boolean): boolean {
  return taskViewPermissions.some((permission) => can(permission));
}

/** Staff see "Required"; a requester who cannot open the task sees a plain-language wording. */
export function taskHintKey(mandatory: boolean, linked: boolean): MessageKey {
  if (!mandatory) return 'requests.task.optional';
  return linked ? 'requests.task.mandatory' : 'requests.task.mandatoryPlain';
}
