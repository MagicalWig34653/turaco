export type ThemePreference = 'auto' | 'turaco' | 'dark' | 'cyberpunk';
export type ResolvedTheme = Exclude<ThemePreference, 'auto'>;
export type DensityPreference = 'comfortable' | 'compact';
export type MotionPreference = 'auto' | 'reduced';

export const themeKey = 'turaco.theme';
export const densityKey = 'turaco.density';
export const motionKey = 'turaco.motion';

export function readPreference<T extends string>(
  key: string,
  choices: readonly T[],
  fallback: T,
): T {
  try {
    const value = localStorage.getItem(key);
    return choices.find((choice) => choice === value) ?? fallback;
  } catch {
    return fallback;
  }
}

export function storePreference(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Preferences remain usable in memory when storage is unavailable.
  }
}

export function resolveTheme(theme: ThemePreference, dark: boolean): ResolvedTheme {
  return theme === 'auto' ? (dark ? 'dark' : 'turaco') : theme;
}

export function resolveMotion(motion: MotionPreference, reduced: boolean): 'normal' | 'reduced' {
  return motion === 'reduced' || reduced ? 'reduced' : 'normal';
}
