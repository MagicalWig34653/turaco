import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nProvider } from '../i18n/I18nProvider';
import { Link } from '../router/Router';
import { DataTable } from './DataTable';
import { FilterBar } from './FilterBar';
import { Button } from './Button';
import { TextField } from './Field';

beforeEach(() =>
  vi.stubGlobal('window', { navigator: { language: 'en' }, localStorage: { getItem: () => null } }),
);
afterEach(() => vi.unstubAllGlobals());
const rows = [{ id: 'AST-001' }];
const props = {
  caption: 'Assets',
  rows,
  rowKey: (row: { id: string }) => row.id,
  emptyText: 'No assets',
  columns: [
    {
      key: 'reference',
      header: 'Reference',
      render: (row: { id: string }) => row.id,
      sortValue: (row: { id: string }) => row.id,
    },
  ],
};

describe('collection accessibility', () => {
  it('keeps column labels and an announced loading state while skeletons are decorative', () => {
    const html = renderToStaticMarkup(
      <I18nProvider>
        <DataTable {...props} loading />
      </I18nProvider>,
    );
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain('aria-label="Assets"');
    expect(html).toContain('aria-sort="none"');
    expect(html).toContain('table-skeleton');
    expect(html).not.toContain('AST-001');
  });
  it('shows an honest loaded count and retains table headers when empty', () => {
    const html = renderToStaticMarkup(
      <I18nProvider>
        <DataTable {...props} rows={[]} />
      </I18nProvider>,
    );
    expect(html).toContain('0 loaded records');
    expect(html).toContain('No assets');
    expect(html).toContain('Reference');
  });
  it('renders controlled selection and only the supplied bulk actions', () => {
    const html = renderToStaticMarkup(
      <I18nProvider>
        <DataTable
          {...props}
          selection={{
            keys: new Set(['AST-001']),
            onChange: () => {},
            label: (row) => row.id,
            actions: <Button>Existing action</Button>,
          }}
        />
      </I18nProvider>,
    );
    expect(html).toContain('1 selected');
    expect(html).toContain('data-selected="true"');
    expect(html).toContain('Existing action');
    expect(html).toContain('Select all loaded records');
  });
  it('adds navigation menus only from existing visible links', () => {
    const html = renderToStaticMarkup(
      <I18nProvider>
        <DataTable
          {...props}
          columns={[
            {
              key: 'ref',
              header: 'Reference',
              render: (row) => <Link to={`/assets/${row.id}`}>{row.id}</Link>,
            },
          ]}
        />
      </I18nProvider>,
    );
    expect(html).toContain('aria-haspopup="menu"');
    expect(html).toContain('table-reference');
  });
  it('keeps submit accessible while secondary controls are collapsed', () => {
    const html = renderToStaticMarkup(
      <I18nProvider>
        <FilterBar
          primaryCount={1}
          activeFilters={[{ key: 'q', label: 'Laptop', onRemove: () => {} }]}
        >
          <TextField label="Search" />
          <TextField label="Secondary" />
          <div className="form-actions">
            <Button type="submit">Apply</Button>
          </div>
        </FilterBar>
      </I18nProvider>,
    );
    expect(html.indexOf('Apply')).toBeLessThan(html.indexOf('<details'));
    expect(html).toContain('Remove filter: Laptop');
    expect(html).toContain('All filters');
  });
});
