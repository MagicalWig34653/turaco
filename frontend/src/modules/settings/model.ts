import { en } from '../../platform/i18n/messages.en';
import type { MessageKey } from '../../platform/i18n/messages.en';
import type { AdminSetting, SettingValue } from './types';

/** A catalog key if it exists, otherwise the fallback. */
export function messageOr(key: string, fallback: MessageKey): MessageKey {
  return Object.hasOwn(en, key) ? (key as MessageKey) : fallback;
}

export type SettingGroup = { module: string; items: AdminSetting[] };

/** Groups settings by owning module, keeping the server order of modules and settings. */
export function groupByModule(items: readonly AdminSetting[]): SettingGroup[] {
  const groups: SettingGroup[] = [];
  for (const item of items) {
    let group = groups.find((g) => g.module === item.module);
    if (!group) {
      group = { module: item.module, items: [] };
      groups.push(group);
    }
    group.items.push(item);
  }
  return groups;
}

/** The text shown in the input for a value: durations are edited in minutes. */
export function toDraft(setting: Pick<AdminSetting, 'type'>, value: SettingValue): string {
  if (setting.type === 'duration') return String(Math.round(Number(value) / 60));
  return String(value);
}

export type ParsedDraft = { ok: true; value: SettingValue } | { ok: false };

/** Turns the draft text back into the API value; rejects non-integers and out-of-bounds numbers. */
export function parseDraft(
  setting: Pick<AdminSetting, 'type' | 'min' | 'max' | 'options'>,
  draft: string,
): ParsedDraft {
  switch (setting.type) {
    case 'bool':
      return { ok: true, value: draft === 'true' };
    case 'enum':
      return setting.options?.includes(draft) ? { ok: true, value: draft } : { ok: false };
    case 'int':
    case 'duration': {
      if (!/^\d{1,9}$/.test(draft.trim())) return { ok: false };
      const n = Number(draft.trim()) * (setting.type === 'duration' ? 60 : 1);
      if (setting.min !== undefined && n < setting.min) return { ok: false };
      if (setting.max !== undefined && n > setting.max) return { ok: false };
      return { ok: true, value: n };
    }
  }
}

/** True when saving would change the effective value. */
export function isDirty(setting: AdminSetting, draft: string): boolean {
  return draft !== toDraft(setting, setting.value);
}

/** Bounds in the unit the input uses (minutes for durations). */
export function inputBounds(setting: AdminSetting): { min?: number; max?: number } {
  const factor = setting.type === 'duration' ? 60 : 1;
  return {
    ...(setting.min !== undefined ? { min: setting.min / factor } : {}),
    ...(setting.max !== undefined ? { max: setting.max / factor } : {}),
  };
}
