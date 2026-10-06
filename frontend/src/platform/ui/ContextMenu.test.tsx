import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nProvider } from '../i18n/I18nProvider';
import { DataTable } from './DataTable';
import { nextMenuIndex, positionContextMenu } from './ContextMenu';

describe('context menu navigation', () => {
  it('wraps arrow navigation and supports Home and End', () => {
    expect(nextMenuIndex(2, 3, 'ArrowDown')).toBe(0);
    expect(nextMenuIndex(0, 3, 'ArrowUp')).toBe(2);
    expect(nextMenuIndex(1, 3, 'Home')).toBe(0);
    expect(nextMenuIndex(1, 3, 'End')).toBe(2);
    expect(nextMenuIndex(0, 0, 'ArrowDown')).toBe(-1);
  });

  it('keeps the menu within the viewport', () => {
    expect(
      positionContextMenu(
        { x: 390, y: 290 },
        { width: 180, height: 100 },
        { width: 400, height: 300 },
      ),
    ).toEqual({ x: 212, y: 192 });
    expect(
      positionContextMenu(
        { x: -20, y: -10 },
        { width: 180, height: 100 },
        { width: 400, height: 300 },
      ),
    ).toEqual({ x: 8, y: 8 });
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
