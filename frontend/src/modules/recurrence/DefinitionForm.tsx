import { useState } from 'react';
import type { FormEvent } from 'react';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Alert } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { AssigneePicker, type Assignee, type AssigneeType } from '../tasks/AssigneePicker';
import { taskPriorities, type TaskPriority } from '../tasks/types';
import {
  browserTimezone,
  defaultRuleForm,
  formToRule,
  isRuleError,
  parseDueAfterHours,
  ruleToForm,
  type RuleForm,
} from './rule';
import {
  frequencies,
  type Frequency,
  type RecurrenceRule,
  type RecurringTaskDefinition,
} from './types';

export type DefinitionFormValues = {
  title: string;
  description: string;
  priority: TaskPriority;
  assignee: { type: AssigneeType; id: string } | null;
  /** True when an existing assignment is to be removed (editing only). */
  clearAssignment: boolean;
  dueAfterHours: number | null;
  rule: RecurrenceRule;
};

type Props = {
  initial?: RecurringTaskDefinition;
  submitLabel: string;
  busy: boolean;
  onSubmit: (values: DefinitionFormValues) => void;
  onCancel: () => void;
};

const weekdays = [1, 2, 3, 4, 5, 6, 7] as const;

/** Shared by creating and editing a recurring task definition; the server validates everything again. */
export function DefinitionForm({ initial, submitLabel, busy, onSubmit, onCancel }: Props) {
  const { t } = useI18n();
  const [title, setTitle] = useState(initial?.title ?? '');
  const [description, setDescription] = useState(initial?.description ?? '');
  const [priority, setPriority] = useState<TaskPriority>(initial?.priority ?? 'normal');
  const [dueAfter, setDueAfter] = useState(
    initial?.dueAfterHours != null ? String(initial.dueAfterHours) : '',
  );
  const [rule, setRule] = useState<RuleForm>(() =>
    initial ? ruleToForm(initial.rule) : defaultRuleForm(new Date(), browserTimezone()),
  );
  const [assigneeType, setAssigneeType] = useState<AssigneeType>('user');
  const [assignee, setAssignee] = useState<Assignee | null>(null);
  const [removeAssignment, setRemoveAssignment] = useState(false);
  const [error, setError] = useState<MessageKey | null>(null);

  const hasAssignment = Boolean(initial?.assignedUserId ?? initial?.assignedTeamId);
  const set = (patch: Partial<RuleForm>) => setRule((previous) => ({ ...previous, ...patch }));

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (title.trim() === '') return;
    const built = formToRule(rule);
    if (isRuleError(built)) {
      setError(`recurrence.error.${built}`);
      return;
    }
    const due = parseDueAfterHours(dueAfter);
    if (due === 'invalid') {
      setError('recurrence.error.dueAfter');
      return;
    }
    setError(null);
    onSubmit({
      title: title.trim(),
      description,
      priority,
      assignee: assignee ? { type: assigneeType, id: assignee.id } : null,
      clearAssignment: removeAssignment && !assignee,
      dueAfterHours: due,
      rule: built,
    });
  };

  return (
    <form className="form" onSubmit={submit}>
      {error ? <Alert kind="error">{t(error)}</Alert> : null}
      <TextField
        label={t('tasks.field.title')}
        value={title}
        onChange={(event) => setTitle(event.target.value)}
        maxLength={200}
        required
        autoFocus
      />
      <TextArea
        label={t('tasks.field.description')}
        value={description}
        onChange={(event) => setDescription(event.target.value)}
        maxLength={10000}
        rows={4}
      />
      <Select
        label={t('tasks.field.priority')}
        value={priority}
        onChange={(event) => setPriority(event.target.value as TaskPriority)}
        options={taskPriorities.map((value) => ({ value, label: t(`tasks.priority.${value}`) }))}
      />
      <TextField
        label={t('recurrence.field.dueAfter')}
        hint={t('recurrence.field.dueAfterHint')}
        inputMode="numeric"
        value={dueAfter}
        onChange={(event) => setDueAfter(event.target.value)}
      />

      <fieldset className="picker">
        <legend>{t('recurrence.field.schedule')}</legend>
        <Select
          label={t('recurrence.field.frequency')}
          value={rule.frequency}
          onChange={(event) => set({ frequency: event.target.value as Frequency })}
          options={frequencies.map((value) => ({
            value,
            label: t(`recurrence.frequency.${value}`),
          }))}
        />
        <TextField
          label={t('recurrence.field.interval')}
          hint={t(`recurrence.field.intervalHint.${rule.frequency}`)}
          inputMode="numeric"
          value={rule.interval}
          onChange={(event) => set({ interval: event.target.value })}
        />
        {rule.frequency === 'weekly' ? (
          <Select
            label={t('recurrence.field.weekday')}
            value={rule.weekday}
            onChange={(event) => set({ weekday: event.target.value })}
            options={weekdays.map((day) => ({
              value: String(day),
              label: t(`recurrence.weekday.${day}`),
            }))}
          />
        ) : null}
        {rule.frequency === 'monthly' ? (
          <TextField
            label={t('recurrence.field.dayOfMonth')}
            hint={t('recurrence.field.dayOfMonthHint')}
            type="number"
            min={1}
            max={31}
            value={rule.dayOfMonth}
            onChange={(event) => set({ dayOfMonth: event.target.value })}
          />
        ) : null}
        <TextField
          label={t('recurrence.field.time')}
          type="time"
          value={rule.timeOfDay}
          onChange={(event) => set({ timeOfDay: event.target.value })}
        />
        <TextField
          label={t('recurrence.field.timezone')}
          hint={t('recurrence.field.timezoneHint')}
          value={rule.timezone}
          onChange={(event) => set({ timezone: event.target.value })}
        />
        <TextField
          label={t('recurrence.field.startsOn')}
          hint={t('recurrence.field.startsOnHint')}
          type="date"
          value={rule.startsOn}
          onChange={(event) => set({ startsOn: event.target.value })}
        />
      </fieldset>

      <fieldset className="picker">
        <legend>{t('recurrence.field.assignment')}</legend>
        <p className="field-hint">
          {initial && hasAssignment ? t('recurrence.assign.keep') : t('recurrence.assign.hint')}
        </p>
        <Select
          label={t('tasks.assign.type')}
          value={assigneeType}
          onChange={(event) => {
            setAssigneeType(event.target.value as AssigneeType);
            setAssignee(null);
          }}
          options={[
            { value: 'user', label: t('tasks.assign.user') },
            { value: 'team', label: t('tasks.assign.team') },
          ]}
        />
        <AssigneePicker
          key={assigneeType}
          type={assigneeType}
          value={assignee}
          onChange={setAssignee}
        />
        {initial && hasAssignment ? (
          <label className="checkbox">
            <input
              type="checkbox"
              checked={removeAssignment}
              onChange={(event) => setRemoveAssignment(event.target.checked)}
            />
            <span>{t('recurrence.assign.remove')}</span>
          </label>
        ) : null}
      </fieldset>

      <div className="form-actions">
        <Button onClick={onCancel}>{t('action.cancel')}</Button>
        <Button type="submit" variant="primary" busy={busy} disabled={title.trim() === ''}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}

export { taskPriorities };
