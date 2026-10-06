import { useId, useMemo, useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { ConfirmDialog } from '../../platform/ui/Dialog';
import { Checkbox, Select, TextArea, TextField } from '../../platform/ui/Field';
import { FilterBar } from '../../platform/ui/FilterBar';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TableDate } from '../../platform/ui/TableDate';
import { useFilterQuery } from '../../platform/ui/useFilterQuery';
import { EmptyState, Skeleton, StatusBadge } from '../../platform/ui/Workspace';
import { errorMessageKey } from '../../platform/api/errorMessages';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { targetSetsApi } from './api';
import {
  DevicePicker,
  EvaluationCard,
  ExplainDialog,
  GroupPicker,
  IdChips,
  LocationPicker,
  Section,
  HighImpactBadge,
  canViewDevices,
} from './components';
import {
  availableKinds,
  buildDefinition,
  codeKey,
  definitionLimits,
  definitionToForm,
  emptyRow,
  isAllDevices,
  parseList,
  sameDefinition,
  validateDefinition,
  type DefinitionError,
  type DefinitionForm,
  type FilterKind,
  type FilterRow,
} from './helpers';
import { complianceStates, osPlatforms, ownerships, type TargetSet } from './types';

const enc = encodeURIComponent;

/** Number of set clauses, for the list. */
function clauseCount(set: TargetSet): number {
  const f = set.definition.filters;
  return (
    (f.osVersionPrefix ? 1 : 0) +
    [
      f.platform,
      f.ownership,
      f.compliance,
      f.manufacturer,
      f.model,
      f.groups,
      f.assetLocationIds,
    ].filter((list) => list !== undefined && list.length > 0).length
  );
}

export function TargetSetsScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [includeArchived, setIncludeArchived] = useState(
    () => new URLSearchParams(window.location.search).get('archived') === 'true',
  );
  useFilterQuery({ archived: includeArchived ? 'true' : '' });
  const list = usePagedList(
    (cursor, signal) => targetSetsApi.list(includeArchived, cursor, signal),
    [includeArchived],
  );
  const [archiving, setArchiving] = useState<TargetSet>();
  const [archiveError, setArchiveError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const canManage = can('deployments.manage');

  const archive = async () => {
    if (!archiving) return;
    setBusy(true);
    setArchiveError(undefined);
    try {
      await targetSetsApi.archive(archiving.id, archiving.version);
      setArchiving(undefined);
      list.reload();
    } catch (cause) {
      setArchiveError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  const columns: Column<TargetSet>[] = [
    {
      key: 'name',
      header: t('deployments.ts.name'),
      sortValue: (set) => set.name,
      render: (set) => (
        <span className="deployments-name-cell">
          <Link to={`/target-sets/${enc(set.id)}`}>{set.name}</Link>
          <span className="deployments-ref">{set.reference}</span>
        </span>
      ),
    },
    {
      key: 'scope',
      header: t('deployments.ts.scope'),
      render: (set) =>
        set.allDevices ? (
          <StatusBadge tone="warning">{t('deployments.ts.allDevices')}</StatusBadge>
        ) : (
          <>
            {t('deployments.ts.clauses', { count: clauseCount(set) })}{' '}
            {set.highImpactReason ? <HighImpactBadge /> : null}
          </>
        ),
    },
    {
      key: 'explicit',
      header: t('deployments.ts.explicit'),
      render: (set) =>
        t('deployments.ts.explicitCounts', {
          include: set.includeDeviceCount ?? set.definition.includeDeviceIds.length,
          exclude: set.excludeDeviceCount ?? set.definition.excludeDeviceIds.length,
        }),
    },
    {
      key: 'state',
      header: t('deployments.ts.state'),
      render: (set) =>
        set.archivedAt ? (
          <StatusBadge tone="neutral">{t('deployments.ts.archived')}</StatusBadge>
        ) : (
          <StatusBadge tone="success">{t('deployments.ts.active')}</StatusBadge>
        ),
    },
    {
      key: 'updated',
      header: t('deployments.updatedAt'),
      sortValue: (set) => set.updatedAt,
      render: (set) => <TableDate value={set.updatedAt} />,
    },
  ];

  const activeFilters = includeArchived
    ? [
        {
          key: 'archived',
          label: t('deployments.ts.includeArchived'),
          onRemove: () => setIncludeArchived(false),
        },
      ]
    : [];

  return (
    <div className="deployments-workspace">
      <PageHeader
        eyebrow={t('deployments.eyebrow')}
        title={t('deployments.ts.title')}
        intro={t('deployments.ts.intro')}
        actions={
          canManage ? (
            <Link className="btn btn-primary" to="/target-sets/new">
              {t('deployments.ts.new')}
            </Link>
          ) : undefined
        }
      />
      <FilterBar activeFilters={activeFilters} onClear={() => setIncludeArchived(false)}>
        <Checkbox
          label={t('deployments.ts.includeArchived')}
          checked={includeArchived}
          onChange={(event) => setIncludeArchived(event.target.checked)}
        />
      </FilterBar>
      <DataTable
        caption={t('deployments.ts.title')}
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        columns={columns}
        rows={list.items}
        rowKey={(set) => set.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('deployments.ts.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
        rowActions={(set) => [
          {
            id: 'open',
            label: t('deployments.ts.open'),
            onSelect: () => navigate(`/target-sets/${enc(set.id)}`),
          },
          ...(canManage
            ? [
                {
                  id: 'archive',
                  label: t('deployments.ts.archive'),
                  danger: true,
                  ...(set.archivedAt
                    ? { disabledReason: t('deployments.ts.alreadyArchived') }
                    : {}),
                  onSelect: () => {
                    setArchiveError(undefined);
                    setArchiving(set);
                  },
                },
              ]
            : []),
        ]}
      />
      {archiving ? (
        <ConfirmDialog
          title={t('deployments.ts.archiveTitle', { name: archiving.name })}
          message={t('deployments.ts.archiveText')}
          confirmLabel={t('deployments.ts.archive')}
          danger
          busy={busy}
          error={archiveError ? t(errorMessageKey(archiveError)) : undefined}
          onConfirm={() => void archive()}
          onCancel={() => setArchiving(undefined)}
        />
      ) : null}
    </div>
  );
}

/** Text values entered one by one (manufacturer, model). */
function ValueChips({
  label,
  values,
  onChange,
}: {
  label: string;
  values: string[];
  onChange: (values: string[]) => void;
}) {
  const { t } = useI18n();
  const [text, setText] = useState('');
  const add = () => {
    const parsed = parseList(text);
    if (parsed.length) onChange([...values, ...parsed.filter((value) => !values.includes(value))]);
    setText('');
  };
  return (
    <div className="deployments-picker">
      <div className="deployments-picker-row">
        <TextField
          label={label}
          hint={t('deployments.def.valuesHint', { max: definitionLimits.filterValues })}
          value={text}
          maxLength={definitionLimits.valueLength * 4}
          onChange={(event) => setText(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault();
              add();
            }
          }}
        />
        <Button disabled={!text.trim()} onClick={add}>
          {t('deployments.picker.add')}
        </Button>
      </div>
      <IdChips
        ids={values}
        names={Object.fromEntries(values.map((value) => [value, value]))}
        label={label}
        onRemove={(value) => onChange(values.filter((x) => x !== value))}
      />
    </div>
  );
}

function EnumChecks({
  legend,
  options,
  prefix,
  values,
  onChange,
}: {
  legend: string;
  options: readonly string[];
  prefix: string;
  values: string[];
  onChange: (values: string[]) => void;
}) {
  const { t } = useI18n();
  return (
    <fieldset className="deployments-checks">
      <legend>{legend}</legend>
      {options.map((option) => (
        <Checkbox
          key={option}
          label={t(`${prefix}.${option}` as MessageKey)}
          checked={values.includes(option)}
          onChange={(event) =>
            onChange(
              event.target.checked
                ? [...values, option]
                : values.filter((value) => value !== option),
            )
          }
        />
      ))}
    </fieldset>
  );
}

function FilterRowEditor({
  row,
  names,
  onNames,
  onChange,
  onRemove,
  errors,
}: {
  row: FilterRow;
  names: Record<string, string>;
  onNames: (id: string, name: string) => void;
  onChange: (row: FilterRow) => void;
  onRemove: () => void;
  errors: DefinitionError[];
}) {
  const { t } = useI18n();
  const title = t(`deployments.def.kind.${row.kind}` as MessageKey);
  let body;
  if (row.kind === 'osVersionPrefix') {
    body = (
      <TextField
        label={title}
        hint={t('deployments.def.prefixHint')}
        value={row.text}
        maxLength={definitionLimits.osPrefixLength}
        onChange={(event) => onChange({ kind: row.kind, text: event.target.value })}
      />
    );
  } else if (row.kind === 'groups') {
    body = (
      <>
        <GroupPicker
          onPick={(item) => {
            onNames(item.id, item.label);
            onChange({
              kind: 'groups',
              groups: [
                ...row.groups.filter((group) => group.externalId.trim()),
                { externalId: item.id, includeNested: false },
              ],
            });
          }}
        />
        <ul className="deployments-group-list">
          {row.groups
            .filter((group) => group.externalId.trim())
            .map((group) => (
              <li key={group.externalId}>
                <span title={group.externalId}>{names[group.externalId] ?? group.externalId}</span>
                <Checkbox
                  label={t('deployments.def.includeNested')}
                  checked={group.includeNested}
                  onChange={(event) =>
                    onChange({
                      kind: 'groups',
                      groups: row.groups.map((g) =>
                        g.externalId === group.externalId
                          ? { ...g, includeNested: event.target.checked }
                          : g,
                      ),
                    })
                  }
                />
                <Button
                  aria-label={t('deployments.chip.remove', {
                    name: names[group.externalId] ?? group.externalId,
                  })}
                  onClick={() =>
                    onChange({
                      kind: 'groups',
                      groups: row.groups.filter((g) => g.externalId !== group.externalId),
                    })
                  }
                >
                  ×
                </Button>
              </li>
            ))}
        </ul>
      </>
    );
  } else if (row.kind === 'assetLocationIds') {
    body = (
      <>
        <LocationPicker
          onPick={(item) => {
            onNames(item.id.toLowerCase(), item.label);
            const id = item.id.toLowerCase();
            if (!row.values.includes(id)) onChange({ kind: row.kind, values: [...row.values, id] });
          }}
        />
        <IdChips
          ids={row.values}
          names={names}
          label={title}
          onRemove={(id) =>
            onChange({ kind: row.kind, values: row.values.filter((x) => x !== id) })
          }
        />
      </>
    );
  } else if (row.kind === 'manufacturer' || row.kind === 'model') {
    body = (
      <ValueChips
        label={title}
        values={row.values}
        onChange={(values) => onChange({ kind: row.kind, values })}
      />
    );
  } else {
    const options =
      row.kind === 'platform'
        ? osPlatforms
        : row.kind === 'ownership'
          ? ownerships
          : complianceStates;
    const prefix =
      row.kind === 'platform'
        ? 'deployments.platform'
        : row.kind === 'ownership'
          ? 'deployments.ownership'
          : 'deployments.compliance';
    body = (
      <EnumChecks
        legend={title}
        options={options}
        prefix={prefix}
        values={row.values}
        onChange={(values) => onChange({ kind: row.kind, values })}
      />
    );
  }
  return (
    <li className="deployments-filter-row">
      <div className="deployments-filter-row-head">
        <span className="deployments-filter-and" aria-hidden="true">
          {t('deployments.def.and')}
        </span>
        <strong>{title}</strong>
        <Button onClick={onRemove} aria-label={t('deployments.def.removeFilter', { name: title })}>
          {t('deployments.def.remove')}
        </Button>
      </div>
      {body}
      {errors.map((error) => (
        <p key={error.key} className="field-error">
          {t(error.key, error.params)}
        </p>
      ))}
    </li>
  );
}

/** Structured editor for the strict definition (filters, includes, excludes). */
export function DefinitionEditor({
  form,
  onChange,
  errors,
  savedCounts,
}: {
  form: DefinitionForm;
  onChange: (form: DefinitionForm) => void;
  errors: DefinitionError[];
  /** Explicit list sizes of the saved set (the lists themselves may be withheld). */
  savedCounts?: { include: number; exclude: number } | undefined;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const [names, setNames] = useState<Record<string, string>>({});
  const remember = (id: string, name: string) => setNames((prev) => ({ ...prev, [id]: name }));
  const devicesVisible = canViewDevices(can);
  // Device groups and asset locations need endpoints.view to be saved (403 otherwise).
  const kinds = availableKinds(form.rows).filter(
    (kind) => devicesVisible || (kind !== 'groups' && kind !== 'assetLocationIds'),
  );
  const [nextKind, setNextKind] = useState<FilterKind | ''>('');
  const errorsFor = (field: string) => errors.filter((error) => error.field === field);
  const definition = buildDefinition(form);
  const addRow = () => {
    if (!nextKind) return;
    onChange({ ...form, rows: [...form.rows, emptyRow(nextKind)] });
    setNextKind('');
  };
  const linkDevices = canViewDevices(can);
  return (
    <>
      <Section title={t('deployments.def.filters')}>
        <p className="deployments-muted">{t('deployments.def.filtersIntro')}</p>
        {form.rows.length === 0 ? (
          <p className="deployments-muted">{t('deployments.def.noFilters')}</p>
        ) : (
          <ol className="deployments-filter-rows">
            {form.rows.map((row, index) => (
              <FilterRowEditor
                key={row.kind}
                row={row}
                names={names}
                onNames={remember}
                errors={errorsFor(row.kind)}
                onChange={(next) =>
                  onChange({ ...form, rows: form.rows.map((r, i) => (i === index ? next : r)) })
                }
                onRemove={() =>
                  onChange({ ...form, rows: form.rows.filter((_, i) => i !== index) })
                }
              />
            ))}
          </ol>
        )}
        {kinds.length > 0 ? (
          <div className="deployments-picker-row">
            <Select
              label={t('deployments.def.addFilter')}
              value={nextKind}
              onChange={(event) => setNextKind(event.target.value as FilterKind | '')}
              options={[
                { value: '', label: t('deployments.def.chooseFilter') },
                ...kinds.map((kind) => ({
                  value: kind,
                  label: t(`deployments.def.kind.${kind}` as MessageKey),
                })),
              ]}
            />
            <Button disabled={!nextKind} onClick={addRow}>
              {t('deployments.def.add')}
            </Button>
          </div>
        ) : null}
        {isAllDevices(definition) ? (
          <Alert kind="warning">{t('deployments.def.allDevicesWarning')}</Alert>
        ) : null}
      </Section>
      <Section title={t('deployments.def.explicit')}>
        <p className="deployments-muted">{t('deployments.def.explicitIntro')}</p>
        {!devicesVisible ? (
          <Alert kind="info">
            {t('deployments.def.devicesHidden', {
              include: savedCounts?.include ?? 0,
              exclude: savedCounts?.exclude ?? 0,
            })}
          </Alert>
        ) : null}
        <div className="deployments-two-col" hidden={!devicesVisible}>
          <div>
            <h3>{t('deployments.def.include')}</h3>
            <DevicePicker
              label={t('deployments.def.includeAdd')}
              onPick={(item) => {
                remember(item.id.toLowerCase(), item.label);
                const id = item.id.toLowerCase();
                if (!form.includeDeviceIds.includes(id))
                  onChange({ ...form, includeDeviceIds: [...form.includeDeviceIds, id] });
              }}
            />
            <IdChips
              ids={form.includeDeviceIds}
              names={names}
              linkDevices={linkDevices}
              label={t('deployments.def.include')}
              onRemove={(id) =>
                onChange({
                  ...form,
                  includeDeviceIds: form.includeDeviceIds.filter((x) => x !== id),
                })
              }
            />
            {errorsFor('includeDeviceIds').map((error) => (
              <p key={error.key} className="field-error">
                {t(error.key, error.params)}
              </p>
            ))}
          </div>
          <div>
            <h3>{t('deployments.def.exclude')}</h3>
            <DevicePicker
              label={t('deployments.def.excludeAdd')}
              onPick={(item) => {
                remember(item.id.toLowerCase(), item.label);
                const id = item.id.toLowerCase();
                if (!form.excludeDeviceIds.includes(id))
                  onChange({ ...form, excludeDeviceIds: [...form.excludeDeviceIds, id] });
              }}
            />
            <IdChips
              ids={form.excludeDeviceIds}
              names={names}
              linkDevices={linkDevices}
              label={t('deployments.def.exclude')}
              onRemove={(id) =>
                onChange({
                  ...form,
                  excludeDeviceIds: form.excludeDeviceIds.filter((x) => x !== id),
                })
              }
            />
            {errorsFor('excludeDeviceIds').map((error) => (
              <p key={error.key} className="field-error">
                {t(error.key, error.params)}
              </p>
            ))}
          </div>
        </div>
      </Section>
    </>
  );
}

/** Create (id undefined) or edit a Target Set, with the live evaluation preview once saved. */
export function TargetSetEditorScreen({ id }: { id?: string }) {
  const { t } = useI18n();
  const { can, session } = useSession();
  const formId = useId();
  const detail = useAsync(
    (signal) => (id ? targetSetsApi.get(id, signal) : Promise.resolve(undefined)),
    [id],
  );
  const saved = detail.data;
  const [loadedVersion, setLoadedVersion] = useState<number>();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [owner, setOwner] = useState<Assignee | null>(null);
  const [form, setForm] = useState<DefinitionForm>(() => definitionToForm(undefined));
  const [error, setError] = useState<ApiError>();
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState('');
  const [explain, setExplain] = useState<{ deviceId?: string } | undefined>();
  const [archiving, setArchiving] = useState(false);
  const [triedSubmit, setTriedSubmit] = useState(false);

  // Adopt the loaded record into the form once per version (after save the server state wins).
  if (saved && loadedVersion !== saved.version) {
    setLoadedVersion(saved.version);
    setName(saved.name);
    setDescription(saved.description ?? '');
    setForm(definitionToForm(saved.definition));
  }

  const definition = useMemo(() => buildDefinition(form), [form]);
  const errors = useMemo(() => validateDefinition(definition), [definition]);
  const nameError = triedSubmit && !name.trim() ? t('deployments.ts.nameRequired') : undefined;
  // Without endpoints.view the device lists, groups and locations cannot be saved (and the lists
  // are withheld, so saving would drop them): such a set is shown read-only.
  const needsDevices =
    !!saved &&
    ((saved.includeDeviceCount ?? saved.definition.includeDeviceIds.length) > 0 ||
      (saved.excludeDeviceCount ?? saved.definition.excludeDeviceIds.length) > 0 ||
      !!saved.definition.filters.groups?.length ||
      !!saved.definition.filters.assetLocationIds?.length);
  const devicesLocked = needsDevices && !canViewDevices(can);
  const readOnly = !can('deployments.manage') || !!saved?.archivedAt || devicesLocked;
  const dirty = !!saved && !sameDefinition(saved.definition, definition);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setTriedSubmit(true);
    setNotice('');
    if (!name.trim() || errors.length > 0) return;
    setSaving(true);
    setError(undefined);
    const input = {
      name: name.trim(),
      description: description.trim(),
      definition,
      ...(owner ? { ownerUserId: owner.id } : saved ? { ownerUserId: saved.ownerUserId } : {}),
    };
    try {
      if (saved) {
        await targetSetsApi.update(saved.id, input, saved.version);
        setNotice(t('deployments.ts.saved'));
        detail.reload();
      } else {
        const created = await targetSetsApi.create(input);
        navigate(`/target-sets/${enc(created.id)}`, { replace: true });
      }
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setSaving(false);
    }
  };

  const archive = async () => {
    if (!saved) return;
    setSaving(true);
    setError(undefined);
    try {
      await targetSetsApi.archive(saved.id, saved.version);
      setArchiving(false);
      detail.reload();
    } catch (cause) {
      setError(asApiError(cause));
      setArchiving(false);
    } finally {
      setSaving(false);
    }
  };

  if (id && detail.error) return <ApiErrorAlert error={detail.error} onRetry={detail.reload} />;
  if (id && !saved) return <Skeleton lines={8} />;

  return (
    <div className="deployments-workspace">
      <PageHeader
        eyebrow={saved?.reference ?? t('deployments.eyebrow')}
        title={saved ? saved.name : t('deployments.ts.newTitle')}
        intro={t('deployments.ts.editorIntro')}
        actions={
          <>
            <Link className="btn btn-secondary" to="/target-sets">
              {t('deployments.ts.back')}
            </Link>
            {saved && !readOnly ? (
              <Button variant="danger" onClick={() => setArchiving(true)}>
                {t('deployments.ts.archive')}
              </Button>
            ) : null}
            {!readOnly ? (
              <Button type="submit" form={formId} variant="primary" busy={saving}>
                {saved ? t('action.save') : t('deployments.ts.create')}
              </Button>
            ) : null}
          </>
        }
      />
      {saved?.archivedAt ? <Alert kind="info">{t('deployments.ts.archivedNotice')}</Alert> : null}
      {devicesLocked && can('deployments.manage') && !saved?.archivedAt ? (
        <Alert kind="info">{t('deployments.ts.devicesLocked')}</Alert>
      ) : null}
      {saved?.allDevices || saved?.highImpactReason ? (
        <Alert kind="warning">
          {t(
            codeKey(
              'deployments.tsHighImpact',
              saved.highImpactReason ?? 'all_devices',
              'deployments.tsHighImpact.unknown',
            ),
          )}
        </Alert>
      ) : null}
      {error ? <ApiErrorAlert error={error} /> : null}
      {notice ? <Alert kind="success">{notice}</Alert> : null}
      {triedSubmit && errors.length > 0 ? (
        <Alert kind="error">{t('deployments.def.fixErrors')}</Alert>
      ) : null}
      <div className="deployments-detail-grid">
        <form
          id={formId}
          className="deployments-detail-main"
          onSubmit={(e) => void submit(e)}
          noValidate
        >
          <fieldset className="deployments-fieldset" disabled={readOnly}>
            <legend className="visually-hidden">{t('deployments.ts.details')}</legend>
            <Section title={t('deployments.ts.details')}>
              <TextField
                label={t('deployments.ts.name')}
                value={name}
                required
                maxLength={150}
                error={nameError}
                onChange={(event) => setName(event.target.value)}
              />
              <TextArea
                label={t('deployments.ts.description')}
                value={description}
                maxLength={1000}
                rows={2}
                onChange={(event) => setDescription(event.target.value)}
              />
              {!readOnly ? (
                <AssigneePicker
                  type="user"
                  value={owner}
                  onChange={setOwner}
                  label={t('deployments.field.owner')}
                  hint={
                    saved
                      ? t('deployments.field.ownerKeep')
                      : t('deployments.field.ownerDefault', { name: session?.displayName ?? '' })
                  }
                />
              ) : null}
            </Section>
            <DefinitionEditor
              form={form}
              onChange={setForm}
              errors={errors}
              savedCounts={
                saved
                  ? {
                      include: saved.includeDeviceCount ?? saved.definition.includeDeviceIds.length,
                      exclude: saved.excludeDeviceCount ?? saved.definition.excludeDeviceIds.length,
                    }
                  : undefined
              }
            />
          </fieldset>
        </form>
        <aside className="deployments-detail-side" aria-label={t('deployments.eval.title')}>
          {saved ? (
            <EvaluationCard
              targetSetId={saved.id}
              dirty={dirty}
              onExplain={(deviceId) => setExplain(deviceId ? { deviceId } : {})}
            />
          ) : (
            <Section title={t('deployments.eval.title')}>
              <EmptyState
                title={t('deployments.eval.saveFirst')}
                description={t('deployments.eval.saveFirstHint')}
              />
            </Section>
          )}
        </aside>
      </div>
      {explain && saved ? (
        <ExplainDialog
          targetSetId={saved.id}
          deviceId={explain.deviceId}
          onClose={() => setExplain(undefined)}
        />
      ) : null}
      {archiving && saved ? (
        <ConfirmDialog
          title={t('deployments.ts.archiveTitle', { name: saved.name })}
          message={t('deployments.ts.archiveText')}
          confirmLabel={t('deployments.ts.archive')}
          danger
          busy={saving}
          onConfirm={() => void archive()}
          onCancel={() => setArchiving(false)}
        />
      ) : null}
    </div>
  );
}
