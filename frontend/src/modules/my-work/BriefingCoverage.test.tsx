import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nProvider } from '../../platform/i18n/I18nProvider';
import type { FeedResult } from '../briefing/types';
import { BriefingCoverage } from './BriefingCoverage';

beforeEach(() =>
  vi.stubGlobal('window', { navigator: { language: 'en' }, localStorage: { getItem: () => null } }),
);
afterEach(() => vi.unstubAllGlobals());
function render(data: FeedResult) {
  return renderToStaticMarkup(
    <I18nProvider>
      <BriefingCoverage data={data} />
    </I18nProvider>,
  );
}
describe('dashboard briefing coverage', () => {
  it('keeps every returned signal with its source, timestamp, severity and destination', () => {
    const html = render({
      entries: Array.from({ length: 7 }, (_, index) => ({
        kind: 'manual_item',
        severity: 'warning',
        titleKey: 'briefing.feed.manual_item',
        params: { title: `Signal ${index}` },
        source: 'briefing',
        linkPath: `/briefing/${index}`,
        occurredAt: '2026-10-07T08:00:00Z',
      })),
      truncated: {},
      unavailable: [],
    });
    for (let index = 0; index < 7; index++) {
      expect(html).toContain(`Signal ${index}`);
      expect(html).toContain(`href="/briefing/${index}"`);
    }
    expect(html).toContain('dateTime="2026-10-07T08:00:00Z"');
    expect(html).toContain('Warning');
    expect(html).toContain('Briefing');
  });
  it('names unavailable and truncated sources without declaring an empty feed', () => {
    const html = render({
      entries: [],
      truncated: { planning: true },
      unavailable: [{ source: 'security', reason: 'source_timeout' }],
    });
    expect(html).toContain('Security');
    expect(html).toContain('Planning');
    expect(html).toContain('role="status"');
    expect(html).not.toContain('There are no briefing entries.');
  });
});
