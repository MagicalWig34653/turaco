import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { catalogApi } from './api';
import type { CatalogForm, CatalogItem } from './types';

type Editing = { kind: 'new' } | { kind: 'edit'; item: CatalogItem };

const emptyDefinition = '{\n  "fields": [],\n  "approval": [],\n  "fulfillment": []\n}';

function ItemDialog({
  editing,
  onClose,
  onSaved,
}: {
  editing: Editing;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const item = editing.kind === 'edit' ? editing.item : undefined;
  const [key, setKey] = useState('');
  const [title, setTitle] = useState(item?.title ?? '');
  const [description, setDescription] = useState(item?.description ?? '');
  const [definition, setDefinition] = useState(item ? '' : emptyDefinition);
  const [jsonError, setJsonError] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [busy, setBusy] = useState(false);

  // The full definition is only loaded for editing; creating starts from an empty template.
  const [loadError, setLoadError] = useState<ApiError | undefined>(undefined);
  const itemId = item?.id;
  useEffect(() => {
    if (!itemId) return undefined;
    const controller = new AbortController();
    catalogApi.form(itemId, controller.signal).then(
      (form: CatalogForm) => setDefinition(JSON.stringify(form.definition ?? {}, null, 2)),
      (cause: unknown) => {
        if (!controller.signal.aborted) setLoadError(asApiError(cause));
      },
    );
    return () => controller.abort();
  }, [itemId]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    let parsed: unknown;
    try {
      parsed = JSON.parse(definition);
    } catch {
      setJsonError(true);
      return;
    }
    setJsonError(false);
    setBusy(true);
    setError(undefined);
    try {
      if (item) {
        await catalogApi.update(item.id, {
          expectedVersion: item.version,
          title,
          description,
          definition: parsed,
        });
      } else {
        await catalogApi.create({ key, title, description, definition: parsed });
      }
      onSaved();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <Dialog title={item ? t('catalogAdmin.edit') : t('catalogAdmin.new')} onClose={onClose} wide>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        {loadError ? <ApiErrorAlert error={loadError} /> : null}
        {!item ? (
          <TextField
            label={t('catalogAdmin.field.key')}
            hint={t('catalogAdmin.field.key.hint')}
            value={key}
            onChange={(event) => setKey(event.target.value)}
            maxLength={64}
            required
          />
        ) : null}
        <TextField
          label={t('catalogAdmin.field.title')}
          value={title}
          onChange={(event) => setTitle(event.target.value)}
          maxLength={200}
          required
        />
        <TextArea
          label={t('catalogAdmin.field.description')}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
          maxLength={2000}
          rows={2}
        />
        <TextArea
          label={t('catalogAdmin.field.definition')}
          hint={t('catalogAdmin.field.definition.hint')}
          error={jsonError ? t('catalogAdmin.error.json') : undefined}
          value={definition}
          onChange={(event) => setDefinition(event.target.value)}
          rows={16}
          spellCheck={false}
          className="code"
          required
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={definition === ''}>
            {t('catalogAdmin.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function CatalogAdminScreen() {
  const { t, locale } = useI18n();
  const [status, setStatus] = useState<'active' | 'inactive' | ''>('');
  const [editing, setEditing] = useState<Editing | null>(null);
  const [actionError, setActionError] = useState<ApiError | undefined>(undefined);
  const list = usePagedList((cursor, signal) => catalogApi.list(status, cursor, signal), [status]);

  const toggle = async (item: CatalogItem) => {
    setActionError(undefined);
    try {
      await catalogApi.setActive(item.id, !item.active, item.version);
      list.reload();
    } catch (cause) {
      setActionError(asApiError(cause));
    }
  };

  const columns: Column<CatalogItem>[] = [
    { key: 'key', header: t('catalogAdmin.col.key'), render: (item) => <code>{item.key}</code> },
    { key: 'title', header: t('catalogAdmin.col.title'), render: (item) => item.title },
    {
      key: 'status',
      header: t('catalogAdmin.col.status'),
      render: (item) => (
        <Badge tone={item.active ? 'success' : 'neutral'}>
          {t(item.active ? 'catalogAdmin.status.active' : 'catalogAdmin.status.inactive')}
        </Badge>
      ),
    },
    {
      key: 'updated',
      header: t('catalogAdmin.col.updated'),
      render: (item) => formatDateTime(locale, item.updatedAt),
    },
    {
      key: 'actions',
      header: '',
      render: (item) => (
        <>
          <Button onClick={() => setEditing({ kind: 'edit', item })}>
            {t('catalogAdmin.edit')}
          </Button>{' '}
          <Button onClick={() => void toggle(item)}>
            {t(item.active ? 'catalogAdmin.deactivate' : 'catalogAdmin.activate')}
          </Button>
        </>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t('nav.catalogAdmin')}
        intro={t('catalogAdmin.intro')}
        actions={
          <Button variant="primary" onClick={() => setEditing({ kind: 'new' })}>
            {t('catalogAdmin.new')}
          </Button>
        }
      />
      <form className="filters" role="search" onSubmit={(event) => event.preventDefault()}>
        <Select
          label={t('catalogAdmin.filter.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value as 'active' | 'inactive' | '')}
          options={[
            { value: '', label: t('catalogAdmin.filter.any') },
            { value: 'active', label: t('catalogAdmin.status.active') },
            { value: 'inactive', label: t('catalogAdmin.status.inactive') },
          ]}
        />
      </form>
      {actionError ? <ApiErrorAlert error={actionError} onRetry={list.reload} /> : null}
      <DataTable
        caption={t('nav.catalogAdmin')}
        columns={columns}
        rows={list.items}
        rowKey={(item) => item.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('catalogAdmin.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {editing ? (
        <ItemDialog
          key={editing.kind === 'edit' ? editing.item.id : 'new'}
          editing={editing}
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
