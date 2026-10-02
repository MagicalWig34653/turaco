import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { PageHeader } from '../../platform/ui/PageHeader';
import { recurrenceApi } from './api';
import { describeRule } from './describe';
import { DefinitionForm, type DefinitionFormValues } from './DefinitionForm';
import type { DefinitionUpdateBody } from './types';

const listPath = '/admin/recurring-tasks';

export function DefinitionCreateScreen() {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);

  const create = async (values: DefinitionFormValues) => {
    setBusy(true);
    setError(undefined);
    try {
      const definition = await recurrenceApi.create({
        title: values.title,
        description: values.description,
        priority: values.priority,
        dueAfterHours: values.dueAfterHours,
        rule: values.rule,
        ...(values.assignee?.type === 'user' ? { assignedUserId: values.assignee.id } : {}),
        ...(values.assignee?.type === 'team' ? { assignedTeamId: values.assignee.id } : {}),
      });
      navigate(`${listPath}/${encodeURIComponent(definition.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <>
      <PageHeader title={t('recurrence.create.title')} intro={t('recurrence.create.intro')} />
      {error ? <ApiErrorAlert error={error} /> : null}
      <DefinitionForm
        submitLabel={t('recurrence.create.action')}
        busy={busy}
        onSubmit={(values) => void create(values)}
        onCancel={() => navigate(listPath)}
      />
    </>
  );
}

export function DefinitionDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const loaded = useAsync((signal) => recurrenceApi.get(id, signal), [id]);
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);

  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const definition = loaded.data;
  if (!definition) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }

  const save = async (values: DefinitionFormValues) => {
    setBusy(true);
    setError(undefined);
    const body: DefinitionUpdateBody = {
      expectedVersion: definition.version,
      title: values.title,
      description: values.description,
      priority: values.priority,
      rule: values.rule,
      ...(values.dueAfterHours === null
        ? { clearDueAfter: true }
        : { dueAfterHours: values.dueAfterHours }),
      ...(values.assignee?.type === 'user' ? { assignedUserId: values.assignee.id } : {}),
      ...(values.assignee?.type === 'team' ? { assignedTeamId: values.assignee.id } : {}),
      ...(values.clearAssignment ? { clearAssignment: true } : {}),
    };
    try {
      await recurrenceApi.update(definition.id, body);
      setEditing(false);
      loaded.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  if (editing) {
    return (
      <>
        <PageHeader title={t('recurrence.edit.title')} />
        {error ? <ApiErrorAlert error={error} /> : null}
        <DefinitionForm
          initial={definition}
          submitLabel={t('action.save')}
          busy={busy}
          onSubmit={(values) => void save(values)}
          onCancel={() => setEditing(false)}
        />
      </>
    );
  }

  return (
    <>
      <PageHeader
        title={definition.title}
        actions={
          <button type="button" className="btn btn-secondary" onClick={() => setEditing(true)}>
            {t('tasks.action.edit')}
          </button>
        }
      />
      <p>
        <Link to={listPath}>{t('recurrence.back')}</Link>
      </p>
      <dl className="facts">
        <dt>{t('recurrence.col.schedule')}</dt>
        <dd>{describeRule(t, definition.rule)}</dd>
        <dt>{t('recurrence.field.startsOn')}</dt>
        <dd>{definition.rule.startsOn}</dd>
        <dt>{t('tasks.col.status')}</dt>
        <dd>{definition.active ? t('recurrence.state.active') : t('recurrence.state.paused')}</dd>
        <dt>{t('recurrence.col.nextRun')}</dt>
        <dd>{definition.nextRunAt ? formatDateTime(locale, definition.nextRunAt) : '–'}</dd>
        <dt>{t('recurrence.fact.lastGenerated')}</dt>
        <dd>
          {definition.lastGeneratedAt ? formatDateTime(locale, definition.lastGeneratedAt) : '–'}
        </dd>
        <dt>{t('tasks.col.priority')}</dt>
        <dd>{t(`tasks.priority.${definition.priority}`)}</dd>
        <dt>{t('recurrence.field.dueAfter')}</dt>
        <dd>{definition.dueAfterHours ?? '–'}</dd>
        <dt>{t('tasks.col.assignee')}</dt>
        <dd>
          {definition.assignedUserId || definition.assignedTeamId
            ? t('recurrence.fact.assigned')
            : t('tasks.assignee.none')}
        </dd>
      </dl>
      {definition.description ? (
        <section>
          <h2>{t('tasks.field.description')}</h2>
          <p className="preline">{definition.description}</p>
        </section>
      ) : null}
    </>
  );
}
