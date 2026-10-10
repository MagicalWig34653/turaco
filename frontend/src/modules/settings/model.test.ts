import { describe, expect, it } from 'vitest';
import { de } from '../../platform/i18n/messages.de';
import { en } from '../../platform/i18n/messages.en';
import { groupByModule, inputBounds, isDirty, messageOr, parseDraft, toDraft } from './model';
import type { AdminSetting } from './types';

const base = { description: '', notYetActive: false, stored: false, version: 0 };
const duration: AdminSetting = {
  ...base,
  key: 'auth.session_absolute_timeout',
  type: 'duration',
  default: 28800,
  value: 28800,
  min: 3600,
  max: 86400,
  module: 'auth',
};
const enumSetting: AdminSetting = {
  ...base,
  key: 'auth.entra_signout_mode',
  type: 'enum',
  default: 'shared_only',
  value: 'shared_only',
  options: ['never', 'shared_only', 'always'],
  module: 'auth',
  notYetActive: true,
};
const flag: AdminSetting = {
  ...base,
  key: 'teams.personal_default',
  type: 'bool',
  default: false,
  value: false,
  module: 'teams',
};

describe('settings model', () => {
  it('shows durations in minutes and converts back to seconds', () => {
    expect(toDraft(duration, 28800)).toBe('480');
    expect(parseDraft(duration, '120')).toEqual({ ok: true, value: 7200 });
    expect(inputBounds(duration)).toEqual({ min: 60, max: 1440 });
  });

  it('rejects out-of-range, fractional and non-numeric durations', () => {
    for (const draft of ['59', '1441', '1.5', '', 'abc', '-5']) {
      expect(parseDraft(duration, draft).ok, draft).toBe(false);
    }
  });

  it('accepts only listed enum options and parses booleans', () => {
    expect(parseDraft(enumSetting, 'always')).toEqual({ ok: true, value: 'always' });
    expect(parseDraft(enumSetting, 'sometimes').ok).toBe(false);
    expect(parseDraft(flag, 'true')).toEqual({ ok: true, value: true });
  });

  it('detects changes against the effective value', () => {
    expect(isDirty(duration, '480')).toBe(false);
    expect(isDirty(duration, '120')).toBe(true);
    expect(isDirty(flag, 'true')).toBe(true);
  });

  it('groups by module in server order', () => {
    const groups = groupByModule([duration, enumSetting, flag]);
    expect(groups.map((g) => [g.module, g.items.length])).toEqual([
      ['auth', 2],
      ['teams', 1],
    ]);
  });

  it('has English and German text for every code-defined setting', () => {
    for (const s of [duration, enumSetting, flag]) {
      expect(messageOr(`settings.key.${s.key}`, 'settings.key.unknown')).not.toBe(
        'settings.key.unknown',
      );
      expect(Object.hasOwn(de, `settings.key.${s.key}`)).toBe(true);
    }
    for (const o of enumSetting.options ?? []) {
      expect(Object.hasOwn(en, `settings.option.${enumSetting.key}.${o}`)).toBe(true);
      expect(Object.hasOwn(de, `settings.option.${enumSetting.key}.${o}`)).toBe(true);
    }
  });
});
