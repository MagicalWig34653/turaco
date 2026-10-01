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
  return interpolate(messages[locale][key] ?? messages.en[key], params);
}

/** Stored choice wins; otherwise the browser language; English is the fallback locale. */
export function resolveInitialLocale(stored: string | null, browserLanguage: string): Locale {
  if (isLocale(stored)) return stored;
  return browserLanguage.toLowerCase().startsWith('de') ? 'de' : 'en';
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
