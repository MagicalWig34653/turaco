import { describe, expect, it } from 'vitest';
import { translate } from './i18n';

describe('translate', () => {
  it('returns German UI text without changing technical names', () => {
    expect(translate('de', 'nav.briefing')).toBe('IT-Briefing');
    expect(translate('de', 'app.name')).toBe('Turaco');
  });
});
