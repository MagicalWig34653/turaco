import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { Badge } from './Alert';

describe('Badge', () => {
  it('keeps a distinct glyph and visible label for each tone', () => {
    const html = renderToStaticMarkup(<Badge tone="danger">Critical</Badge>);
    expect(html).toContain('badge badge-danger');
    expect(html).toContain('Critical');
    expect(html).not.toContain('badge-live');
  });

  it('marks ongoing states as live without changing the label', () => {
    const html = renderToStaticMarkup(
      <Badge tone="info" live>
        In progress
      </Badge>,
    );
    expect(html).toContain('badge badge-info badge-live');
    expect(html).toContain('In progress');
  });
});
