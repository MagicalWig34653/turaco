import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList, type PagedState } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { copyContextText, type MenuItem } from '../../platform/ui/ContextMenu';
import { Select, TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { PageHeader } from '../../platform/ui/PageHeader';
import { assetsApi } from './api';
import { assetStatuses, type Asset, type AssetList, type AssetStatus } from './types';

const tone: Record<AssetStatus, 'neutral' | 'success' | 'warning' | 'danger' | 'info'> = {
  received: 'info',
  available: 'success',
  reserved: 'warning',
  assigned: 'info',
  returned: 'neutral',
  in_repair: 'warning',
  retired: 'neutral',
  disposed: 'neutral',
  lost: 'danger',
};

export function AssetStatusBadge({ status }: { status: AssetStatus }) {
  const { t } = useI18n();
  return <Badge tone={tone[status]}>{t(`assets.status.${status}`)}</Badge>;
}

/** The table shared by "My equipment" and the asset list. */
export function AssetTable({
  caption,
  emptyText,
  list,
  productNames,
}: {
  caption: string;
  emptyText: string;
  list: PagedState<Asset>;
  productNames: Record<string, string>;
}) {
  const { t } = useI18n();
  const columns: Column<Asset>[] = [
    {
      key: 'reference',
      header: t('assets.col.reference'),
      render: (a) => <Link to={`/assets/${encodeURIComponent(a.id)}`}>{a.reference}</Link>,
    },
    {
      key: 'product',
      header: t('assets.col.product'),
      render: (a) => productNames[a.productId] ?? '–',
    },
    { key: 'serial', header: t('assets.col.serial'), render: (a) => a.serialNumber ?? '–' },
    { key: 'tag', header: t('assets.col.tag'), render: (a) => a.assetTag ?? '–' },
    {
      key: 'status',
      header: t('assets.col.status'),
      render: (a) => <AssetStatusBadge status={a.status} />,
    },
  ];
  const rowActions = (asset: Asset): MenuItem[] => [
    {
      id: 'open',
      label: t('contextMenu.open'),
      onSelect: () => navigate(`/assets/${encodeURIComponent(asset.id)}`),
    },
    {
      id: 'copy-reference',
      label: t('contextMenu.copyReference'),
      onSelect: () => {
        void copyContextText(asset.reference).then((copied) => {
          if (!copied) window.prompt(t('contextMenu.copyFallback'), asset.reference);
        });
      },
    },
  ];
  return (
    <DataTable
      caption={caption}
      columns={columns}
      rows={list.items}
      rowKey={(a) => a.id}
      rowActions={rowActions}
      loading={list.loading}
      error={list.error}
      onRetry={list.reload}
      emptyText={emptyText}
      hasMore={list.hasMore}
      loadingMore={list.loadingMore}
      loadMoreError={list.loadMoreError}
      onLoadMore={list.loadMore}
    />
  );
}

/** Product names accumulate over the pages of a paged list. */
export function usePagedAssets(
  fetch: (cursor: string | undefined, signal: AbortSignal) => Promise<AssetList>,
  deps: readonly unknown[],
) {
  const [names, setNames] = useState<Record<string, string>>({});
  const list = usePagedList<Asset>(async (cursor, signal) => {
    const page = await fetch(cursor, signal);
    setNames((previous) => ({ ...previous, ...page.productNames }));
    return page;
  }, deps);
  return { list, names };
}

export function MyAssetsScreen() {
  const { t } = useI18n();
  const { list, names } = usePagedAssets((cursor, signal) => assetsApi.mine(cursor, signal), []);
  return (
    <>
      <PageHeader title={t('nav.myAssets')} intro={t('assets.mine.intro')} />
      <AssetTable
        caption={t('nav.myAssets')}
        emptyText={t('assets.mine.empty')}
        list={list}
        productNames={names}
      />
    </>
  );
}

export function AssetsScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [status, setStatus] = useState<AssetStatus | ''>('');
  const [query, setQuery] = useState('');
  const [code, setCode] = useState('');
  const [lookupError, setLookupError] = useState<ApiError | undefined>(undefined);
  const q = useDebouncedValue(query.trim(), 300);
  const { list, names } = usePagedAssets(
    (cursor, signal) => assetsApi.list({ status, q }, cursor, signal),
    [status, q],
  );

  const lookup = async (event: FormEvent) => {
    event.preventDefault();
    setLookupError(undefined);
    try {
      const found = await assetsApi.lookup(code.trim());
      navigate(`/assets/${encodeURIComponent(found.id)}`);
    } catch (cause) {
      setLookupError(asApiError(cause));
    }
  };

  return (
    <>
      <PageHeader
        title={t('nav.assets')}
        intro={t('assets.intro')}
        actions={
          can('assets.manage') ? (
            <Link to="/assets/new" className="btn btn-primary">
              {t('assets.create.action')}
            </Link>
          ) : null
        }
      />
      <form className="filters" role="search" onSubmit={(event) => void lookup(event)}>
        <TextField
          label={t('assets.lookup.label')}
          hint={t('assets.lookup.hint')}
          value={code}
          maxLength={200}
          autoComplete="off"
          onChange={(event) => setCode(event.target.value)}
        />
        <Button type="submit" disabled={code.trim() === ''}>
          {t('assets.lookup.action')}
        </Button>
      </form>
      {lookupError ? <ApiErrorAlert error={lookupError} /> : null}
      <form className="filters" role="search" onSubmit={(event) => event.preventDefault()}>
        <TextField
          label={t('assets.filter.search')}
          type="search"
          value={query}
          maxLength={100}
          autoComplete="off"
          onChange={(event) => setQuery(event.target.value)}
        />
        <Select
          label={t('assets.col.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value as AssetStatus | '')}
          options={[
            { value: '', label: t('assets.filter.anyStatus') },
            ...assetStatuses.map((value) => ({ value, label: t(`assets.status.${value}`) })),
          ]}
        />
      </form>
      <AssetTable
        caption={t('nav.assets')}
        emptyText={t('assets.empty')}
        list={list}
        productNames={names}
      />
    </>
  );
}
