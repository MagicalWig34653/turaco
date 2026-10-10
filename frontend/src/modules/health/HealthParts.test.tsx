import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nProvider } from '../../platform/i18n/I18nProvider';
import { CheckCard, NextStepView } from './HealthParts';
import type { HealthResult } from './types';

beforeEach(() =>
  vi.stubGlobal('window', {
    navigator: { language: 'en' },
    localStorage: { getItem: () => null },
    setInterval: () => 0,
    clearInterval: () => {},
    location: { pathname: '/', search: '' },
  }),
);
afterEach(() => vi.unstubAllGlobals());

const render = (node: React.ReactNode) => renderToStaticMarkup(<I18nProvider>{node}</I18nProvider>);

const result: HealthResult = {
  key: 'smtp',
  category: 'integration',
  status: 'not_configured',
  observedAt: '2026-10-10T10:00:00Z',
  errorCode: 'base_url_missing',
  nextStep: {
    kind: 'config',
    configKeys: ['EMAIL_BASE_URL'],
    docsPath: 'docs/integrations/mail.md',
  },
};

describe('CheckCard', () => {
  it('shows not configured as such, with freshness, error text and key names only', () => {
    const html = render(<CheckCard result={result} />);
    expect(html).toContain('Not configured');
    expect(html).not.toContain('>OK<');
    expect(html).toContain('data-status="not_configured"');
    expect(html).toContain('Checked');
    expect(html).toContain('Never');
    expect(html).toContain('base_url_missing');
    expect(html).toContain('The public base URL is not set.');
    expect(html).toContain('<code>EMAIL_BASE_URL</code>');
    expect(html).toContain('/blob/main/docs/integrations/mail.md');
  });
  it('renders an unknown check key without crashing', () => {
    const bare: HealthResult = {
      key: 'brand_new',
      category: 'module',
      status: 'ok',
      observedAt: result.observedAt,
    };
    const html = render(<CheckCard result={bare} />);
    expect(html).toContain('Check brand_new');
  });
});

describe('NextStepView', () => {
  it('renders nothing when there is nothing to show or link', () => {
    expect(render(<NextStepView step={undefined} />)).toBe('');
    expect(render(<NextStepView step={{ kind: 'route', route: '/unknown/page' }} />)).toBe('');
  });
  it('refuses docs paths outside docs/', () => {
    expect(
      render(<NextStepView step={{ kind: 'docs', docsPath: 'https://evil.example/x' }} />),
    ).toBe('');
    expect(render(<NextStepView step={{ kind: 'docs', docsPath: 'docs/../secret' }} />)).toBe('');
  });
  it('links a known route', () => {
    expect(render(<NextStepView step={{ kind: 'route', route: '/admin/modules' }} />)).toContain(
      'href="/admin/modules"',
    );
  });
});
