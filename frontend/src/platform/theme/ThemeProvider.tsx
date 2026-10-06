import { createContext, useContext, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import {
  densityKey,
  motionKey,
  readPreference,
  resolveMotion,
  resolveTheme,
  storePreference,
  themeKey,
  type DensityPreference,
  type MotionPreference,
  type ThemePreference,
} from './preferences';

type ThemeContextValue = {
  theme: ThemePreference;
  setTheme: (theme: ThemePreference) => void;
  density: DensityPreference;
  setDensity: (density: DensityPreference) => void;
  motion: MotionPreference;
  setMotion: (motion: MotionPreference) => void;
  reducedMotion: boolean;
};

const ThemeContext = createContext<ThemeContextValue | null>(null);
const themeChoices = ['auto', 'turaco', 'dark', 'cyberpunk'] as const;
const densityChoices = ['comfortable', 'compact'] as const;
const motionChoices = ['auto', 'reduced'] as const;

function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const media = window.matchMedia(query);
    const update = () => setMatches(media.matches);
    update();
    media.addEventListener('change', update);
    return () => media.removeEventListener('change', update);
  }, [query]);
  return matches;
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<ThemePreference>(() =>
    readPreference(themeKey, themeChoices, 'auto'),
  );
  const [density, setDensity] = useState<DensityPreference>(() =>
    readPreference(densityKey, densityChoices, 'comfortable'),
  );
  const [motion, setMotion] = useState<MotionPreference>(() =>
    readPreference(motionKey, motionChoices, 'auto'),
  );
  const dark = useMediaQuery('(prefers-color-scheme: dark)');
  const osReduced = useMediaQuery('(prefers-reduced-motion: reduce)');
  const reducedMotion = resolveMotion(motion, osReduced) === 'reduced';

  useEffect(() => {
    const root = document.documentElement;
    root.dataset.theme = resolveTheme(theme, dark);
    root.style.colorScheme = root.dataset.theme === 'turaco' ? 'light' : 'dark';
    storePreference(themeKey, theme);
  }, [theme, dark]);
  useEffect(() => {
    document.documentElement.dataset.density = density;
    storePreference(densityKey, density);
  }, [density]);
  useEffect(() => {
    document.documentElement.dataset.motion = reducedMotion ? 'reduced' : 'normal';
    storePreference(motionKey, motion);
  }, [motion, reducedMotion]);

  // Decorative theme animation pauses while the tab is hidden (see themes.css).
  useEffect(() => {
    const root = document.documentElement;
    const update = () => {
      root.dataset.visibility = document.visibilityState === 'hidden' ? 'hidden' : 'visible';
    };
    update();
    document.addEventListener('visibilitychange', update);
    return () => document.removeEventListener('visibilitychange', update);
  }, []);

  const value = useMemo(
    () => ({ theme, setTheme, density, setDensity, motion, setMotion, reducedMotion }),
    [theme, density, motion, reducedMotion],
  );
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme() {
  const value = useContext(ThemeContext);
  if (!value) throw new Error('useTheme must be used inside ThemeProvider');
  return value;
}

export function useReducedMotion(): boolean {
  return useTheme().reducedMotion;
}
