import { useState } from 'react';
import type { FormEvent } from 'react';
import { asApiError, useAsync } from '../../../platform/api/useAsync';
import { useI18n } from '../../../platform/i18n/I18nProvider';
import type { ApiError } from '../../../platform/api/client';
import { ApiErrorAlert } from '../../../platform/ui/ApiErrorAlert';
import { Button } from '../../../platform/ui/Button';
import { Dialog } from '../../../platform/ui/Dialog';
import { Select, TextArea, TextField } from '../../../platform/ui/Field';
import { viewsApi } from '../../../platform/ui/views/api';
import { boardsApi } from './api';
import { filterFromView, presetColumns } from './model';
import type { TaskBoard } from './types';

/**
 * Creates a Board either blank or from a saved Task view (its condition tree becomes the Board
 * filter), with the preset columns To do, In progress, Blocked and Done.
 */
export function BoardCreateDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (board: TaskBoard) => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [source, setSource] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const views = useAsync(
    (signal) => viewsApi.list({ resource: 'tasks', scope: 'all' }, signal),
    [],
  );
  const choices = (views.data?.items ?? []).filter((view) => view.kind !== 'board');

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const view = choices.find((candidate) => candidate.id === source);
      const filter = filterFromView(view?.definition);
      onCreated(
        await boardsApi.create({
          name: name.trim(),
          ...(description.trim() ? { description: description.trim() } : {}),
          ...(filter ? { filter } : {}),
          columns: presetColumns({
            open: t('tasks.board.preset.todo'),
            in_progress: t('tasks.board.preset.doing'),
            blocked: t('tasks.board.preset.blocked'),
            completed: t('tasks.board.preset.done'),
          }),
        }),
      );
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <Dialog title={t('tasks.board.create.title')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextField
          label={t('tasks.board.field.name')}
          value={name}
          maxLength={80}
          required
          autoFocus
          onChange={(event) => setName(event.target.value)}
        />
        <TextArea
          label={t('tasks.board.field.description')}
          value={description}
          maxLength={500}
          rows={2}
          onChange={(event) => setDescription(event.target.value)}
        />
        <Select
          label={t('tasks.board.create.source')}
          hint={t('tasks.board.create.sourceHint')}
          value={source}
          onChange={(event) => setSource(event.target.value)}
          options={[
            { value: '', label: t('tasks.board.create.blank') },
            ...choices.map((view) => ({ value: view.id, label: view.name })),
          ]}
        />
        {views.error ? <ApiErrorAlert error={views.error} onRetry={views.reload} /> : null}
        <p className="field-hint">{t('tasks.board.create.columnsHint')}</p>
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={name.trim() === ''}>
            {t('tasks.board.create.action')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
