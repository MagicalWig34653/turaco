import { de } from './messages.de';
import { en, type MessageKey } from './messages.en';

export type Locale = 'en' | 'de';
export type { MessageKey };
export type MessageParams = Record<string, string | number>;

export const locales: readonly Locale[] = ['de', 'en'];
export const messages: Record<Locale, Record<MessageKey, string>> = { en, de };
export const LOCALE_STORAGE_KEY = 'turaco.locale';

export function isLocale(value: unknown): value is Locale {
  return value === 'en' || value === 'de';
}

/** Replaces {name} placeholders; unknown placeholders are left untouched. */
export function interpolate(template: string, params?: MessageParams): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (match, name: string) => {
    const value = params[name];
    return value === undefined ? match : String(value);
  });
}

export function translate(locale: Locale, key: MessageKey, params?: MessageParams): string {
  // A missing key must never crash the UI; show the key so the gap is visible and reportable.
  const template: string | undefined = messages[locale][key] ?? messages.en[key];
  return template === undefined ? String(key) : interpolate(template, params);
}

/** The first supported language in the browser's preference order; English when none matches. */
export function browserLocale(languages: string | readonly string[]): Locale {
  for (const language of typeof languages === 'string' ? [languages] : languages) {
    const base = language.toLowerCase();
    if (base.startsWith('de')) return 'de';
    if (base.startsWith('en')) return 'en';
  }
  return 'en';
}

/** Stored choice wins; otherwise the browser language(s); English is the fallback locale. */
export function resolveInitialLocale(
  stored: string | null,
  browserLanguages: string | readonly string[],
): Locale {
  return isLocale(stored) ? stored : browserLocale(browserLanguages);
}

export function readStoredLocale(): string | null {
  try {
    return window.localStorage.getItem(LOCALE_STORAGE_KEY);
  } catch {
    return null;
  }
}

export function storeLocale(locale: Locale): void {
  try {
    window.localStorage.setItem(LOCALE_STORAGE_KEY, locale);
  } catch {
    // Storage can be unavailable (private mode, blocked); the choice then lasts for the session.
  }
}
