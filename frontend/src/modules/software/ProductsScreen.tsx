import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Button } from '../../platform/ui/Button';
import type { MenuItem } from '../../platform/ui/ContextMenu';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { FilterBar } from '../../platform/ui/FilterBar';
import { Select } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TableDate } from '../../platform/ui/TableDate';
import { useFilterQuery } from '../../platform/ui/useFilterQuery';
import { Card, EmptyState, SplitPane } from '../../platform/ui/Workspace';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Skeleton } from '../../platform/ui/Workspace';
import { softwareApi } from './api';
import { OperationDialog, ProductStatusBadge, VersionStatusBadge } from './components';
import { productOperations } from './helpers';
import {
  productReasons,
  productStatuses,
  type ProductOperation,
  type SoftwareProduct,
} from './types';

const enc = encodeURIComponent;

type PendingOperation = { product: SoftwareProduct; operation: ProductOperation };

function initialParam(name: string): string {
  return new URLSearchParams(window.location.search).get(name) ?? '';
}

/** Approved software: products with their Software Approval Status and a product workspace. */
export function SoftwareProductsScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [status, setStatus] = useState(() => initialParam('status'));
  const [selectedId, setSelectedId] = useState(() => initialParam('product'));
  const [pending, setPending] = useState<PendingOperation>();
  useFilterQuery({ status, product: selectedId });
  const list = usePagedList(
    (cursor, signal) => softwareApi.products(status, cursor, signal),
    [status],
  );
  const selected = list.items.find((product) => product.id === selectedId);
  const canApprove = can('software.approve');

  const operationItems = (product: SoftwareProduct): MenuItem[] =>
    canApprove
      ? productOperations(product.approvalStatus).map((operation) => ({
          id: operation,
          label: t(`software.productOp.${operation}` as MessageKey),
          danger: operation === 'block' || operation === 'retire',
          onSelect: () => setPending({ product, operation }),
        }))
      : [];

  const activeFilters = status
    ? [
        {
          key: 'status',
          label: t(`software.productStatus.${status}` as MessageKey),
          onRemove: () => setStatus(''),
        },
      ]
    : [];

  const columns: Column<SoftwareProduct>[] = [
    {
      key: 'name',
      header: t('software.product.name'),
      sortValue: (product) => product.name,
      render: (product) => (
        <button
          type="button"
          className="software-row-select"
          aria-pressed={product.id === selectedId}
          onClick={() => setSelectedId(product.id)}
        >
          {product.name}
        </button>
      ),
    },
    {
      key: 'publisher',
      header: t('software.publisher'),
      sortValue: (product) => product.publisher ?? '',
      render: (product) => product.publisher ?? '—',
    },
    {
      key: 'status',
      header: t('software.approvalStatus'),
      sortValue: (product) => product.approvalStatus,
      render: (product) => <ProductStatusBadge status={product.approvalStatus} />,
    },
    {
      key: 'updated',
      header: t('software.updatedAt'),
      sortValue: (product) => product.updatedAt,
      render: (product) => <TableDate value={product.updatedAt} />,
    },
  ];

  return (
    <div className="software-workspace">
      <PageHeader
        eyebrow={t('software.eyebrow')}
        title={t('software.products.title')}
        intro={t('software.products.intro')}
        actions={
          <>
            <Link className="btn btn-secondary" to="/software/catalog">
              {t('software.catalog.title')}
            </Link>
            {can('software.package') ? (
              <Link className="btn btn-primary" to="/software/versions/new">
                {t('software.register.title')}
              </Link>
            ) : null}
          </>
        }
      />
      <FilterBar activeFilters={activeFilters} onClear={() => setStatus('')}>
        <Select
          label={t('software.approvalStatus')}
          value={status}
          onChange={(event) => setStatus(event.target.value)}
          options={[
            { value: '', label: t('filters.all') },
            ...productStatuses.map((value) => ({
              value,
              label: t(`software.productStatus.${value}` as MessageKey),
            })),
          ]}
        />
      </FilterBar>
      <SplitPane
        inspectorLabel={t('software.product.workspace')}
        main={
          <DataTable
            caption={t('software.products.title')}
            filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
            columns={columns}
            rows={list.items}
            rowKey={(product) => product.id}
            loading={list.loading}
            error={list.error}
            onRetry={list.reload}
            emptyText={t('software.products.empty')}
            hasMore={list.hasMore}
            loadingMore={list.loadingMore}
            loadMoreError={list.loadMoreError}
            onLoadMore={list.loadMore}
            rowActions={(product) => [
              {
                id: 'select',
                label: t('software.product.show'),
                onSelect: () => setSelectedId(product.id),
              },
              ...operationItems(product),
            ]}
          />
        }
        inspector={
          selected ? (
            <ProductInspector
              key={selected.id}
              product={selected}
              onOperation={(operation) => setPending({ product: selected, operation })}
            />
          ) : (
            <EmptyState
              title={t('software.product.noneSelected')}
              description={t('software.product.noneSelectedHint')}
            />
          )
        }
      />
      {pending ? (
        <ProductOperationDialog
          pending={pending}
          onClose={() => setPending(undefined)}
          onDone={() => {
            setPending(undefined);
            list.reload();
          }}
        />
      ) : null}
    </div>
  );
}

function ProductInspector({
  product,
  onOperation,
}: {
  product: SoftwareProduct;
  onOperation: (operation: ProductOperation) => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const versions = usePagedList(
    (cursor, signal) => softwareApi.versions({ productId: product.id }, cursor, signal),
    [product.id],
  );
  const operations = productOperations(product.approvalStatus);
  return (
    <Card className="software-inspector" title={product.name}>
      <p className="software-eyebrow">{product.publisher ?? t('software.publisherUnknown')}</p>
      <h2>{product.name}</h2>
      <div className="software-inspector-status">
        <ProductStatusBadge status={product.approvalStatus} />
        {product.approvalReason ? (
          <span className="software-muted">
            {t(`software.reasonCode.${product.approvalReason}` as MessageKey)}
          </span>
        ) : null}
      </div>
      <p className="software-muted">
        {t(`software.productStatusHint.${product.approvalStatus}` as MessageKey)}
      </p>
      {can('software.approve') ? (
        <div className="software-inspector-actions">
          {operations.map((operation, index) => (
            <Button
              key={operation}
              variant={
                operation === 'block' || operation === 'retire'
                  ? 'danger'
                  : index === 0
                    ? 'primary'
                    : 'secondary'
              }
              onClick={() => onOperation(operation)}
            >
              {t(`software.productOp.${operation}` as MessageKey)}
            </Button>
          ))}
        </div>
      ) : (
        <p className="software-muted">{t('software.product.approveOnly')}</p>
      )}
      <div className="software-inspector-heading">
        <h3>{t('software.versions.title')}</h3>
        {can('software.package') ? (
          <Link to={`/software/versions/new?productId=${enc(product.id)}`}>
            {t('software.register.short')}
          </Link>
        ) : null}
      </div>
      {versions.error ? <ApiErrorAlert error={versions.error} onRetry={versions.reload} /> : null}
      {versions.loading ? <Skeleton lines={3} /> : null}
      {!versions.loading && !versions.error && versions.items.length === 0 ? (
        <p className="software-muted">{t('software.versions.empty')}</p>
      ) : null}
      <ul className="software-version-list">
        {versions.items.map((version) => (
          <li key={version.id}>
            <Link to={`/software/versions/${enc(version.id)}`}>{version.productVersion}</Link>
            <VersionStatusBadge status={version.approvalStatus} />
          </li>
        ))}
      </ul>
      {versions.hasMore ? (
        <Button onClick={versions.loadMore} busy={versions.loadingMore}>
          {t('action.loadMore')}
        </Button>
      ) : null}
    </Card>
  );
}

function ProductOperationDialog({
  pending,
  onClose,
  onDone,
}: {
  pending: PendingOperation;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const { product, operation } = pending;
  const reasons = operation === 'approve' ? undefined : productReasons[operation];
  return (
    <OperationDialog
      title={t(`software.productOp.${operation}` as MessageKey)}
      description={
        <>
          <p>
            <strong>{product.name}</strong>
          </p>
          <p>{t(`software.productOpHint.${operation}` as MessageKey)}</p>
        </>
      }
      confirmLabel={t(`software.productOp.${operation}` as MessageKey)}
      danger={operation === 'block' || operation === 'retire'}
      {...(reasons ? { reasons } : {})}
      onClose={onClose}
      onSubmit={async (reason) => {
        await softwareApi.productOperation(product.id, operation, product.version, reason);
        onDone();
      }}
    />
  );
}
