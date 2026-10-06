import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { PageHeader } from '../../platform/ui/PageHeader';
import { productsApi } from './api';
import type { Manufacturer, Product, ProductCategory } from './types';

type Named = { id: string; name: string };

function ProductDialog({
  product,
  manufacturers,
  categories,
  onClose,
  onSaved,
}: {
  product: Product | null;
  manufacturers: Named[];
  categories: Named[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(product?.name ?? '');
  const [manufacturerId, setManufacturerId] = useState(product?.manufacturerId ?? '');
  const [categoryId, setCategoryId] = useState(product?.categoryId ?? '');
  const [mpn, setMpn] = useState(product?.manufacturerPartNumber ?? '');
  const [ipn, setIpn] = useState(product?.internalPartNumber ?? '');
  const [serialized, setSerialized] = useState(product?.serialized ?? false);
  const [stockManaged, setStockManaged] = useState(product?.stockManaged ?? false);
  const [assetManaged, setAssetManaged] = useState(product?.assetManaged ?? false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      if (product) {
        await productsApi.updateProduct(product.id, {
          expectedVersion: product.version,
          name,
          ...(manufacturerId ? { manufacturerId } : { clearManufacturer: true }),
          ...(categoryId ? { categoryId } : { clearCategory: true }),
          manufacturerPartNumber: mpn,
          internalPartNumber: ipn,
          serialized,
          stockManaged,
          assetManaged,
        });
      } else {
        await productsApi.createProduct({
          name,
          manufacturerId: manufacturerId || null,
          categoryId: categoryId || null,
          manufacturerPartNumber: mpn,
          internalPartNumber: ipn,
          serialized,
          stockManaged,
          assetManaged,
        });
      }
      onSaved();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  const none = { value: '', label: t('products.field.none') };
  return (
    <Dialog title={product ? t('products.edit') : t('products.new')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextField
          label={t('products.field.name')}
          value={name}
          onChange={(event) => setName(event.target.value)}
          maxLength={200}
          required
          autoFocus
        />
        <Select
          label={t('products.field.manufacturer')}
          value={manufacturerId}
          onChange={(event) => setManufacturerId(event.target.value)}
          options={[none, ...manufacturers.map((m) => ({ value: m.id, label: m.name }))]}
        />
        <Select
          label={t('products.field.category')}
          value={categoryId}
          onChange={(event) => setCategoryId(event.target.value)}
          options={[none, ...categories.map((c) => ({ value: c.id, label: c.name }))]}
        />
        <TextField
          label={t('products.field.mpn')}
          value={mpn}
          onChange={(event) => setMpn(event.target.value)}
          maxLength={100}
        />
        <TextField
          label={t('products.field.ipn')}
          value={ipn}
          onChange={(event) => setIpn(event.target.value)}
          maxLength={100}
        />
        <Checkbox
          label={t('products.field.serialized')}
          checked={serialized}
          onChange={(event) => setSerialized(event.target.checked)}
        />
        <Checkbox
          label={t('products.field.stockManaged')}
          checked={stockManaged}
          onChange={(event) => setStockManaged(event.target.checked)}
        />
        <Checkbox
          label={t('products.field.assetManaged')}
          checked={assetManaged}
          onChange={(event) => setAssetManaged(event.target.checked)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={name.trim() === ''}>
            {t('products.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Add form plus plain list for the small master-data lists (manufacturers, categories). */
function NameList({
  title,
  addLabel,
  emptyText,
  items,
  error,
  onRetry,
  onAdd,
}: {
  title: string;
  addLabel: string;
  emptyText: string;
  items: Named[];
  error: ApiError | undefined;
  onRetry: () => void;
  onAdd: (name: string) => Promise<void>;
}) {
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [addError, setAddError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setAddError(undefined);
    try {
      await onAdd(name.trim());
      setName('');
    } catch (cause) {
      setAddError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section>
      <h2>{title}</h2>
      {error ? <ApiErrorAlert error={error} onRetry={onRetry} /> : null}
      {items.length === 0 && !error ? <p className="empty">{emptyText}</p> : null}
      <ul className="plain-list">
        {items.map((item) => (
          <li key={item.id}>{item.name}</li>
        ))}
      </ul>
      <form className="filters" onSubmit={(event) => void submit(event)}>
        <TextField
          label={t('products.add.name')}
          value={name}
          onChange={(event) => setName(event.target.value)}
          maxLength={200}
        />
        <Button type="submit" busy={busy} disabled={name.trim() === ''}>
          {addLabel}
        </Button>
      </form>
      {addError ? <ApiErrorAlert error={addError} /> : null}
    </section>
  );
}

export function ProductsScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const canManage = can('products.manage');
  const [query, setQuery] = useState('');
  const [showInactive, setShowInactive] = useState(false);
  const [editing, setEditing] = useState<Product | 'new' | null>(null);
  const [actionError, setActionError] = useState<ApiError | undefined>(undefined);
  const q = useDebouncedValue(query.trim(), 300);

  const manufacturers = useAsync(
    async (signal) => (await productsApi.manufacturers(undefined, signal)).items,
    [],
  );
  const categories = useAsync(
    async (signal) => (await productsApi.categories(undefined, signal)).items,
    [],
  );
  const list = usePagedList(
    (cursor, signal) =>
      productsApi.products(
        { ...(q ? { q } : {}), ...(showInactive ? {} : { active: true }) },
        cursor,
        signal,
      ),
    [q, showInactive],
  );

  const manufacturerNames = new Map(
    (manufacturers.data ?? []).map((m: Manufacturer) => [m.id, m.name]),
  );
  const categoryNames = new Map(
    (categories.data ?? []).map((c: ProductCategory) => [c.id, c.name]),
  );

  const toggle = async (product: Product) => {
    setActionError(undefined);
    try {
      await productsApi.setProductActive(product.id, !product.active, product.version);
      list.reload();
    } catch (cause) {
      setActionError(asApiError(cause));
    }
  };

  const columns: Column<Product>[] = [
    { key: 'name', header: t('products.col.name'), render: (p) => p.name },
    {
      key: 'manufacturer',
      header: t('products.col.manufacturer'),
      render: (p) => (p.manufacturerId ? (manufacturerNames.get(p.manufacturerId) ?? '–') : '–'),
    },
    {
      key: 'category',
      header: t('products.col.category'),
      render: (p) => (p.categoryId ? (categoryNames.get(p.categoryId) ?? '–') : '–'),
    },
    {
      key: 'part',
      header: t('products.col.partNumber'),
      render: (p) => p.internalPartNumber ?? p.manufacturerPartNumber ?? '–',
    },
    {
      key: 'status',
      header: t('products.col.status'),
      render: (p) => (
        <Badge tone={p.active ? 'success' : 'neutral'}>
          {t(p.active ? 'products.status.active' : 'products.status.inactive')}
        </Badge>
      ),
    },
    ...(canManage
      ? [
          {
            key: 'actions',
            header: '',
            render: (p: Product) => (
              <>
                <Button onClick={() => setEditing(p)}>{t('products.edit')}</Button>{' '}
                <Button onClick={() => void toggle(p)}>
                  {t(p.active ? 'products.deactivate' : 'products.activate')}
                </Button>
              </>
            ),
          },
        ]
      : []),
  ];

  const activeFilters = [
    ...(query
      ? [
          {
            key: 'q',
            label: `${t('products.filter.search')}: ${query}`,
            onRemove: () => {
              setQuery('');
            },
          },
        ]
      : []),
    ...(showInactive
      ? [
          {
            key: 'inactive',
            label: t('products.filter.inactive'),
            onRemove: () => {
              setShowInactive(false);
            },
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t('nav.products')}
        intro={t('products.intro')}
        actions={
          canManage ? (
            <Button variant="primary" onClick={() => setEditing('new')}>
              {t('products.new')}
            </Button>
          ) : null
        }
      />
      <FilterBar
        activeFilters={activeFilters}
        role="search"
        onSubmit={(event) => event.preventDefault()}
      >
        <TextField
          label={t('products.filter.search')}
          type="search"
          value={query}
          maxLength={100}
          autoComplete="off"
          onChange={(event) => setQuery(event.target.value)}
        />
        <Checkbox
          label={t('products.filter.inactive')}
          checked={showInactive}
          onChange={(event) => setShowInactive(event.target.checked)}
        />
      </FilterBar>
      {actionError ? <ApiErrorAlert error={actionError} onRetry={list.reload} /> : null}
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={t('nav.products')}
        columns={columns}
        rows={list.items}
        rowKey={(p) => p.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('products.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {canManage ? (
        <>
          <NameList
            title={t('products.section.manufacturers')}
            addLabel={t('products.add.manufacturer')}
            emptyText={t('products.none.manufacturers')}
            items={manufacturers.data ?? []}
            error={manufacturers.error}
            onRetry={manufacturers.reload}
            onAdd={async (name) => {
              await productsApi.createManufacturer(name);
              manufacturers.reload();
            }}
          />
          <NameList
            title={t('products.section.categories')}
            addLabel={t('products.add.category')}
            emptyText={t('products.none.categories')}
            items={categories.data ?? []}
            error={categories.error}
            onRetry={categories.reload}
            onAdd={async (name) => {
              await productsApi.createCategory(name);
              categories.reload();
            }}
          />
        </>
      ) : null}
      {editing ? (
        <ProductDialog
          key={editing === 'new' ? 'new' : editing.id}
          product={editing === 'new' ? null : editing}
          manufacturers={manufacturers.data ?? []}
          categories={categories.data ?? []}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            list.reload();
          }}
        />
      ) : null}
    </>
  );
}
