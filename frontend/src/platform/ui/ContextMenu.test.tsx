import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nProvider } from '../i18n/I18nProvider';
import { DataTable } from './DataTable';
import { nextMenuIndex, pointerAnchor, positionContextMenu } from './ContextMenu';

describe('context menu navigation', () => {
  it('wraps arrow navigation and supports Home and End', () => {
    expect(nextMenuIndex(2, 3, 'ArrowDown')).toBe(0);
    expect(nextMenuIndex(0, 3, 'ArrowUp')).toBe(2);
    expect(nextMenuIndex(1, 3, 'Home')).toBe(0);
    expect(nextMenuIndex(1, 3, 'End')).toBe(2);
    expect(nextMenuIndex(0, 0, 'ArrowDown')).toBe(-1);
  });

  it('opens below and start-aligned when the menu fits', () => {
    expect(
      positionContextMenu(
        { left: 40, top: 20, right: 72, bottom: 52 },
        { width: 180, height: 100 },
        { width: 400, height: 300 },
      ),
    ).toEqual({ x: 40, y: 56 });
  });

  it('flips end-aligned and above instead of covering the anchor', () => {
    // An ellipsis trigger at the bottom-right corner: the menu ends at its right edge, above it.
    expect(
      positionContextMenu(
        { left: 350, top: 250, right: 382, bottom: 282 },
        { width: 180, height: 100 },
        { width: 400, height: 300 },
      ),
    ).toEqual({ x: 202, y: 146 });
  });

  it('clamps into the viewport when neither side fits', () => {
    expect(
      positionContextMenu(
        { left: -20, top: -10, right: -20, bottom: -10 },
        { width: 180, height: 100 },
        { width: 400, height: 300 },
      ),
    ).toEqual({ x: 8, y: 8 });
  });

  it('keeps a pointer-opened row band uncovered', () => {
    const row = { top: 100, bottom: 166, height: 66 } as DOMRect;
    expect(pointerAnchor({ x: 300, y: 130 }, row)).toEqual({
      left: 300,
      right: 300,
      top: 100,
      bottom: 166,
    });
    const tall = { top: 0, bottom: 600, height: 600 } as DOMRect;
    expect(pointerAnchor({ x: 300, y: 130 }, tall)).toEqual({
      left: 300,
      right: 300,
      top: 130,
      bottom: 130,
    });
  });
});

describe('DataTable row actions', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('adds a visible menu trigger and keyboard row only when actions are supplied', () => {
    vi.stubGlobal('window', {
      navigator: { language: 'en-US' },
      localStorage: { getItem: () => null },
    });
    const props = {
      caption: 'Records',
      columns: [{ key: 'name', header: 'Name', render: (row: { id: string }) => row.id }],
      rows: [{ id: 'AST-001' }],
      rowKey: (row: { id: string }) => row.id,
      emptyText: 'Empty',
    };
    const basic = renderToStaticMarkup(
      <I18nProvider>
        <DataTable {...props} />
      </I18nProvider>,
    );
    const actionable = renderToStaticMarkup(
      <I18nProvider>
        <DataTable
          {...props}
          rowActions={() => [{ id: 'open', label: 'Open', onSelect: () => undefined }]}
        />
      </I18nProvider>,
    );
    expect(basic).not.toContain('table-actions-trigger');
    expect(actionable).toContain('table-actions-trigger');
    expect(actionable).toContain('data-has-row-actions="true"');
    expect(actionable).toContain('aria-haspopup="menu"');
  });
});
