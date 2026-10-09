import { useState } from 'react';
import type { FormEvent } from 'react';
import { api, type ApiError } from '../../../platform/api/client';
import { asApiError, useAsync } from '../../../platform/api/useAsync';
import { useI18n } from '../../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../../platform/i18n/i18n';
import { ApiErrorAlert } from '../../../platform/ui/ApiErrorAlert';
import { Button } from '../../../platform/ui/Button';
import { Dialog } from '../../../platform/ui/Dialog';
import { Select, TextArea, TextField } from '../../../platform/ui/Field';
import { GroupEditor } from '../../../platform/ui/query/ConditionTree';
import {
  asGroup,
  limitOfError,
  normalize,
  validate,
  type Catalog,
  type Node,
} from '../../../platform/ui/query/filterModel';
import { taskStatuses, type TaskStatus } from '../types';
import { boardsApi } from './api';
import {
  MAX_TITLE,
  canAddColumn,
  canRemoveColumn,
  parseWip,
  sortedColumns,
  validTitle,
} from './model';
import type { ColumnOperation, TaskBoard } from './types';

const statusOptions = (t: (key: MessageKey) => string) =>
  taskStatuses.map((status) => ({ value: status, label: t(`tasks.status.${status}`) }));

/**
 * Board settings: name and description (applied to the Board's Saved View) and the column editor.
 * Every column change is one explicit operation sent with the current Board version.
 */
export function BoardSettingsDialog({
  board,
  onBoard,
  onClose,
}: {
  board: TaskBoard;
  onBoard: (board: TaskBoard) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [working, setWorking] = useState(board);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const [name, setName] = useState(board.name);
  const [description, setDescription] = useState(board.description);
  const [newTitle, setNewTitle] = useState('');
  const [newStatus, setNewStatus] = useState<TaskStatus>('open');
  const columns = sortedColumns(working);

  const run = async (call: () => Promise<TaskBoard>) => {
    setBusy(true);
    setError(undefined);
    try {
      const next = await call();
      setWorking(next);
      onBoard(next);
      return true;
    } catch (cause) {
      const failure = asApiError(cause);
      setError(failure);
      if (failure.code === 'tasks.board_conflict') {
        try {
          const fresh = await boardsApi.get(working.id);
          setWorking(fresh);
          onBoard(fresh);
        } catch {
          // The conflict message already asks the person to reload.
        }
      }
      return false;
    } finally {
      setBusy(false);
    }
  };
  const apply = (operation: ColumnOperation) =>
    run(() => boardsApi.columns(working.id, working.version, operation));

  const saveDetails = (event: FormEvent) => {
    event.preventDefault();
    void run(() =>
      boardsApi.update(working.id, {
        expectedVersion: working.version,
        name: name.trim(),
        description: description.trim(),
      }),
    );
  };
  const detailsChanged = name.trim() !== working.name || description.trim() !== working.description;

  return (
    <Dialog title={t('tasks.board.settings.title')} onClose={onClose} wide>
      {error ? <ApiErrorAlert error={error} /> : null}
      <form className="form" onSubmit={saveDetails}>
        <TextField
          label={t('tasks.board.field.name')}
          value={name}
          maxLength={80}
          required
          onChange={(event) => setName(event.target.value)}
        />
        <TextArea
          label={t('tasks.board.field.description')}
          value={description}
          maxLength={500}
          rows={2}
          onChange={(event) => setDescription(event.target.value)}
        />
        <div className="dialog-actions">
          <Button
            type="submit"
            variant="primary"
            busy={busy}
            disabled={!detailsChanged || name.trim() === ''}
          >
            {t('action.save')}
          </Button>
        </div>
      </form>

      <h3>{t('tasks.board.columns.title')}</h3>
      <p className="field-hint">{t('tasks.board.columns.hint')}</p>
      <ul className="board-col-editor" aria-label={t('tasks.board.columns.title')}>
        {columns.map((column, index) => (
          <li key={`${column.id}:${working.version}`} className="board-col-editor-row">
            <input
              aria-label={t('tasks.board.columns.name', { n: index + 1 })}
              defaultValue={column.title}
              maxLength={MAX_TITLE}
              disabled={busy}
              onBlur={(event) => {
                const title = event.target.value.trim();
                if (title !== column.title && validTitle(title))
                  void apply({ operation: 'rename', columnId: column.id, title });
                else event.target.value = column.title;
              }}
              onKeyDown={(event) => {
                if (event.key === 'Enter') {
                  event.preventDefault();
                  event.currentTarget.blur();
                }
              }}
            />
            <select
              aria-label={t('tasks.board.columns.status', { title: column.title })}
              value={column.mapsTo}
              disabled={busy}
              onChange={(event) =>
                void apply({
                  operation: 'remap',
                  columnId: column.id,
                  mapsTo: event.target.value as TaskStatus,
                })
              }
            >
              {statusOptions(t).map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
            <input
              type="number"
              min={1}
              max={999}
              inputMode="numeric"
              placeholder={t('tasks.board.columns.noLimit')}
              aria-label={t('tasks.board.columns.wip', { title: column.title })}
              defaultValue={column.wipLimit ?? ''}
              disabled={busy}
              onBlur={(event) => {
                const parsed = parseWip(event.target.value);
                if (parsed === 'invalid') {
                  event.target.value = column.wipLimit === null ? '' : String(column.wipLimit);
                } else if (parsed !== column.wipLimit) {
                  void apply({ operation: 'set_wip', columnId: column.id, wipLimit: parsed });
                }
              }}
            />
            <span className="board-col-editor-actions">
              <Button
                disabled={busy || index === 0}
                aria-label={t('tasks.board.columns.up', { title: column.title })}
                onClick={() =>
                  void apply({ operation: 'reorder', columnId: column.id, position: index - 1 })
                }
              >
                <span aria-hidden="true">↑</span>
              </Button>
              <Button
                disabled={busy || index === columns.length - 1}
                aria-label={t('tasks.board.columns.down', { title: column.title })}
                onClick={() =>
                  void apply({ operation: 'reorder', columnId: column.id, position: index + 1 })
                }
              >
                <span aria-hidden="true">↓</span>
              </Button>
              <Button
                variant="danger"
                disabled={busy || !canRemoveColumn(working)}
                aria-label={t('tasks.board.columns.remove', { title: column.title })}
                onClick={() => void apply({ operation: 'remove', columnId: column.id })}
              >
                <span aria-hidden="true">×</span>
              </Button>
            </span>
          </li>
        ))}
      </ul>
      <p className="field-hint">{t('tasks.board.columns.remapHint')}</p>

      <form
        className="board-col-add"
        onSubmit={(event) => {
          event.preventDefault();
          const title = newTitle.trim();
          if (!validTitle(title)) return;
          void apply({ operation: 'add', title, mapsTo: newStatus }).then((ok) => {
            if (ok) setNewTitle('');
          });
        }}
      >
        <TextField
          label={t('tasks.board.columns.addName')}
          value={newTitle}
          maxLength={MAX_TITLE}
          onChange={(event) => setNewTitle(event.target.value)}
        />
        <Select
          label={t('tasks.board.columns.addStatus')}
          value={newStatus}
          onChange={(event) => setNewStatus(event.target.value as TaskStatus)}
          options={statusOptions(t)}
        />
        <Button
          type="submit"
          busy={busy}
          disabled={!canAddColumn(working) || !validTitle(newTitle)}
        >
          {t('tasks.board.columns.add')}
        </Button>
      </form>
      {!canAddColumn(working) ? <p className="field-hint">{t('tasks.board.columns.max')}</p> : null}
      <div className="dialog-actions">
        <Button onClick={onClose}>{t('action.close')}</Button>
      </div>
    </Dialog>
  );
}

/**
 * Edits the filter of the Board's Saved View with the shared condition builder. The new filter
 * applies to everyone who uses the Board; the server decides who may change it.
 */
export function BoardFilterDialog({
  board,
  onBoard,
  onClose,
}: {
  board: TaskBoard;
  onBoard: (board: TaskBoard) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const catalogRead = useAsync((signal) => api.get<Catalog>('/tasks/fields', { signal }), []);
  const [root, setRoot] = useState<Node | undefined>(board.filter?.root);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const catalog = catalogRead.data;
  const normalized = normalize(root);
  const state = {
    filter: normalized ? ({ v: 1, root: normalized } as const) : ({ v: 1 } as const),
    sort: [],
    search: '',
  };
  const problems = catalog ? validate(state, catalog) : [];
  const limits = catalog?.limits ?? {};

  const apply = async () => {
    setBusy(true);
    setError(undefined);
    try {
      onBoard(
        await boardsApi.update(board.id, {
          expectedVersion: board.version,
          filter: normalized ? { v: 1, root: normalized } : null,
        }),
      );
      onClose();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <Dialog title={t('tasks.board.filter.title')} onClose={onClose} wide>
      <p className="field-hint">{t('tasks.board.filter.intro')}</p>
      {catalogRead.error ? (
        <ApiErrorAlert error={catalogRead.error} onRetry={catalogRead.reload} />
      ) : null}
      {error ? <ApiErrorAlert error={error} /> : null}
      {!catalog && !catalogRead.error ? <p role="status">{t('state.loading')}</p> : null}
      {catalog ? (
        <div className="query-panel board-filter-panel">
          <GroupEditor node={asGroup(root)} catalog={catalog} onChange={setRoot} />
          {problems.length > 0 ? (
            <div role="alert" className="query-errors">
              {problems.map((problem) => (
                <p key={problem}>
                  {t(problem as MessageKey, {
                    limit:
                      limits[limitOfError[problem.replace('query.validation.', '')] ?? ''] ?? '',
                  })}
                </p>
              ))}
            </div>
          ) : null}
        </div>
      ) : null}
      <div className="dialog-actions">
        <Button onClick={onClose}>{t('action.cancel')}</Button>
        <Button
          variant="primary"
          busy={busy}
          disabled={!catalog || problems.length > 0}
          onClick={() => void apply()}
        >
          {t('tasks.board.filter.apply')}
        </Button>
      </div>
    </Dialog>
  );
}
