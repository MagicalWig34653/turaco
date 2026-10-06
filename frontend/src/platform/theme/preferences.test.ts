import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  densityKey,
  motionKey,
  readPreference,
  resolveMotion,
  resolveTheme,
  storePreference,
  themeKey,
} from './preferences';

afterEach(() => vi.unstubAllGlobals());

describe('appearance preferences', () => {
  it('resolves auto from the current OS setting, while explicit choices win', () => {
    expect(resolveTheme('auto', false)).toBe('turaco');
    expect(resolveTheme('auto', true)).toBe('dark');
    expect(resolveTheme('cyberpunk', false)).toBe('cyberpunk');
  });

  it('persists theme, density and motion independently', () => {
    const values = new Map<string, string>();
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
    });
    storePreference(themeKey, 'cyberpunk');
    storePreference(densityKey, 'compact');
    storePreference(motionKey, 'reduced');
    expect(readPreference(themeKey, ['auto', 'turaco', 'dark', 'cyberpunk'], 'auto')).toBe(
      'cyberpunk',
    );
    expect(readPreference(densityKey, ['comfortable', 'compact'], 'comfortable')).toBe('compact');
    expect(readPreference(motionKey, ['auto', 'reduced'], 'auto')).toBe('reduced');
    expect(resolveMotion('auto', true)).toBe('reduced');
    expect(resolveMotion('auto', false)).toBe('normal');
    expect(resolveMotion('reduced', false)).toBe('reduced');
  });

  it('falls back when storage is unavailable or invalid', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('blocked');
      },
    });
    expect(readPreference(themeKey, ['auto', 'turaco'], 'auto')).toBe('auto');
    expect(() => storePreference(themeKey, 'turaco')).not.toThrow();
  });
});
