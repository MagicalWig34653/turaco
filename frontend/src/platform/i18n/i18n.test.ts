import { describe, expect, it } from 'vitest';
import { de } from './messages.de';
import { en } from './messages.en';
import { interpolate, resolveInitialLocale, translate } from './i18n';

function placeholders(text: string): string[] {
  return [...text.matchAll(/\{(\w+)\}/g)].map((match) => match[1] as string).sort();
}

describe('translate', () => {
  it('returns German UI text without changing technical names', () => {
    expect(translate('de', 'card.briefing.title')).toBe('IT-Briefing');
    expect(translate('de', 'app.name')).toBe('Turaco');
  });

  it('interpolates parameters', () => {
    expect(translate('en', 'login.error.tooManyAttempts', { minutes: 3 })).toContain('3 minute');
    expect(interpolate('a {x} b {y}', { x: 1 })).toBe('a 1 b {y}');
  });
});

describe('message catalogs', () => {
  it('have identical key sets in EN and DE', () => {
    expect(Object.keys(de).sort()).toEqual(Object.keys(en).sort());
  });

  it('have no empty strings', () => {
    for (const catalog of [en, de]) {
      for (const [key, value] of Object.entries(catalog)) {
        expect(value.trim(), key).not.toBe('');
      }
    }
  });

  it('use the same placeholders in EN and DE', () => {
    for (const key of Object.keys(en) as Array<keyof typeof en>) {
      expect(placeholders(de[key]), key).toEqual(placeholders(en[key]));
    }
  });

  it('use the agreed German glossary terms', () => {
    expect(de['nav.roles']).toBe('Rollen');
    expect(de['nav.roleAssignments']).toBe('Rollenzuweisungen');
    expect(de['subject.directory_group']).toBe('Verzeichnisgruppe');
    expect(de['nav.directorySync']).toBe('Verzeichnissynchronisation');
    expect(de['nav.audit']).toBe('Audit-Ereignisse');
  });
});

describe('resolveInitialLocale', () => {
  it('prefers a stored valid choice', () => {
    expect(resolveInitialLocale('en', 'de-DE')).toBe('en');
    expect(resolveInitialLocale('de', 'en-US')).toBe('de');
  });
  it('falls back to the browser language, then English', () => {
    expect(resolveInitialLocale(null, 'de-AT')).toBe('de');
    expect(resolveInitialLocale('xx', 'fr-FR')).toBe('en');
  });
});
