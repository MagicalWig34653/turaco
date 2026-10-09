import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { useReferenceNames } from '../../platform/ui/query/referenceNames';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { ticketQueuesApi, type QueueUpdateBody } from './api';
import {
  previewReference,
  queueDescriptionMax,
  queueNameMax,
  suggestKey,
  suggestPrefix,
  validateQueueForm,
  type QueueFormIssue,
} from './queueModel';
import {
  priorities,
  type Priority,
  type QueueRoutingMode,
  type QueueVisibility,
  type TicketQueue,
} from './types';

const issueKey: Record<QueueFormIssue, MessageKey> = {
  keyInvalid: 'queues.error.keyInvalid',
  prefixInvalid: 'queues.error.prefixInvalid',
  nameRequired: 'queues.error.nameRequired',
  nameTooLong: 'queues.error.nameTooLong',
};

const visibilities: readonly QueueVisibility[] = ['internal', 'public'];
const routingModes: readonly QueueRoutingMode[] = ['employee_choice', 'automatic', 'both'];

/** A conflict (stale version) asks for a reload; other errors use the shared alert. */
export function QueueError({
  error,
  onReload,
}: {
  error: ApiError;
  onReload?: (() => void) | undefined;
}) {
  const { t } = useI18n();
  if (error.code === 'tickets.version_conflict')
    return (
      <Alert kind="error">
        <p>{t('queues.error.conflict')}</p>
        {onReload ? <Button onClick={onReload}>{t('queues.reload')}</Button> : null}
      </Alert>
    );
  return <ApiErrorAlert error={error} />;
}

type SettingsProps = {
  visibility: QueueVisibility;
  onVisibility: (value: QueueVisibility) => void;
  routingMode: QueueRoutingMode;
  onRouting: (value: QueueRoutingMode) => void;
  priority: Priority;
  onPriority: (value: Priority) => void;
  busy: boolean;
};

function QueueSettingsFields(props: SettingsProps) {
  const { t } = useI18n();
  return (
    <>
      <Select
        label={t('queues.field.visibility')}
        hint={t('queues.field.visibility.hint')}
        value={props.visibility}
        disabled={props.busy}
        onChange={(event) => props.onVisibility(event.target.value as QueueVisibility)}
        options={visibilities.map((value) => ({ value, label: t(`queues.visibility.${value}`) }))}
      />
      <Select
        label={t('queues.field.routingMode')}
        hint={t('queues.field.routingMode.hint')}
        value={props.routingMode}
        disabled={props.busy}
        onChange={(event) => props.onRouting(event.target.value as QueueRoutingMode)}
        options={routingModes.map((value) => ({ value, label: t(`queues.routing.${value}`) }))}
      />
      <Select
        label={t('queues.field.defaultPriority')}
        value={props.priority}
        disabled={props.busy}
        onChange={(event) => props.onPriority(event.target.value as Priority)}
        options={priorities.map((value) => ({ value, label: t(`tickets.priority.${value}`) }))}
      />
    </>
  );
}

export function QueueCreateDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (queue: TicketQueue) => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [key, setKey] = useState('');
  const [prefix, setPrefix] = useState('');
  const [touched, setTouched] = useState({ key: false, prefix: false });
  const [description, setDescription] = useState('');
  const [publicLabel, setPublicLabel] = useState('');
  const [visibility, setVisibility] = useState<QueueVisibility>('internal');
  const [routingMode, setRoutingMode] = useState<QueueRoutingMode>('employee_choice');
  const [priority, setPriority] = useState<Priority>('normal');
  const [padding, setPadding] = useState('4');
  const [team, setTeam] = useState<Assignee | null>(null);
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const issues = validateQueueForm({ key, prefix, name });
  const message = (field: 'key' | 'prefix' | 'name') => {
    const issue = issues[field];
    return submitted && issue ? t(issueKey[issue]) : undefined;
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSubmitted(true);
    if (Object.keys(issues).length > 0 || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      onCreated(
        await ticketQueuesApi.create({
          key,
          prefix,
          name: name.trim(),
          ...(description.trim() ? { description: description.trim() } : {}),
          ...(publicLabel.trim() ? { publicLabel: publicLabel.trim() } : {}),
          visibility,
          routingMode,
          defaultPriority: priority,
          numberPadding: Number(padding),
          ...(team ? { defaultTeamId: team.id } : {}),
        }),
      );
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <Dialog title={t('queues.create.title')} onClose={onClose} wide>
      <form className="form queue-form" onSubmit={(event) => void submit(event)} noValidate>
        {error ? <QueueError error={error} /> : null}
        <TextField
          label={t('queues.field.name')}
          value={name}
          maxLength={queueNameMax}
          required
          autoFocus
          disabled={busy}
          error={message('name')}
          onChange={(event) => {
            const next = event.target.value;
            setName(next);
            if (!touched.key) setKey(suggestKey(next));
            if (!touched.prefix) setPrefix(suggestPrefix(next));
          }}
        />
        <div className="queue-form-pair">
          <TextField
            label={t('queues.field.key')}
            hint={t('queues.field.key.hint')}
            value={key}
            maxLength={31}
            required
            disabled={busy}
            error={message('key')}
            autoCapitalize="none"
            spellCheck={false}
            onChange={(event) => {
              setTouched((current) => ({ ...current, key: true }));
              setKey(event.target.value.toLowerCase());
            }}
          />
          <TextField
            label={t('queues.field.prefix')}
            hint={t('queues.field.prefix.hint')}
            value={prefix}
            maxLength={8}
            required
            disabled={busy}
            error={message('prefix')}
            autoCapitalize="characters"
            spellCheck={false}
            onChange={(event) => {
              setTouched((current) => ({ ...current, prefix: true }));
              setPrefix(event.target.value.toUpperCase());
            }}
          />
        </div>
        <Select
          label={t('queues.field.padding')}
          hint={t('queues.preview', { reference: previewReference(prefix, Number(padding)) })}
          value={padding}
          disabled={busy}
          onChange={(event) => setPadding(event.target.value)}
          options={[3, 4, 5, 6, 7].map((width) => ({
            value: String(width),
            label: t('queues.padding.digits', { count: width }),
          }))}
        />
        <TextArea
          label={t('queues.field.description')}
          value={description}
          rows={2}
          maxLength={queueDescriptionMax}
          disabled={busy}
          onChange={(event) => setDescription(event.target.value)}
        />
        <TextField
          label={t('queues.field.publicLabel')}
          hint={t('queues.field.publicLabel.hint')}
          value={publicLabel}
          maxLength={queueNameMax}
          disabled={busy}
          onChange={(event) => setPublicLabel(event.target.value)}
        />
        <QueueSettingsFields
          visibility={visibility}
          onVisibility={setVisibility}
          routingMode={routingMode}
          onRouting={setRoutingMode}
          priority={priority}
          onPriority={setPriority}
          busy={busy}
        />
        <AssigneePicker
          type="team"
          label={t('queues.field.defaultTeam')}
          hint={t('queues.field.defaultTeam.hint')}
          value={team}
          onChange={setTeam}
        />
        <p className="field-hint">{t('queues.create.permanent')}</p>
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy}>
            {t('queues.create.submit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Rename and settings; key, prefix and padding are fixed once the queue exists. */
export function QueueEditDialog({
  queue,
  onClose,
  onSaved,
  onReload,
}: {
  queue: TicketQueue;
  onClose: () => void;
  onSaved: (queue: TicketQueue) => void;
  onReload: () => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(queue.name);
  const [description, setDescription] = useState(queue.description);
  const [publicLabel, setPublicLabel] = useState(queue.publicLabel ?? '');
  const [visibility, setVisibility] = useState(queue.visibility);
  const [routingMode, setRoutingMode] = useState<QueueRoutingMode>(
    queue.routingMode ?? 'employee_choice',
  );
  const [priority, setPriority] = useState<Priority>(queue.defaultPriority ?? 'normal');
  const [team, setTeam] = useState<Assignee | null>(null);
  const [clearTeam, setClearTeam] = useState(false);
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const teamName = useReferenceNames(
    queue.defaultTeamId ? [{ kind: 'teams', id: queue.defaultTeamId }] : [],
  )('teams', queue.defaultTeamId ?? '');
  const issues = validateQueueForm({ key: queue.key, prefix: queue.prefix, name });
  const nameIssue = submitted && issues.name ? t(issueKey[issues.name]) : undefined;
  // The intake queue must stay public, which the server enforces as well.
  const lockedPublic = queue.isDefault;

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSubmitted(true);
    if (issues.name || busy) return;
    const body: QueueUpdateBody = { expectedVersion: queue.version };
    if (name.trim() !== queue.name) body.name = name.trim();
    if (description.trim() !== queue.description) body.description = description.trim();
    if (publicLabel.trim() !== (queue.publicLabel ?? '')) body.publicLabel = publicLabel.trim();
    if (visibility !== queue.visibility) body.visibility = visibility;
    if (routingMode !== (queue.routingMode ?? 'employee_choice')) body.routingMode = routingMode;
    if (priority !== (queue.defaultPriority ?? 'normal')) body.defaultPriority = priority;
    if (team) body.defaultTeamId = team.id;
    else if (clearTeam) body.defaultTeamId = '';
    if (Object.keys(body).length === 1) {
      onClose();
      return;
    }
    setBusy(true);
    setError(undefined);
    try {
      onSaved(await ticketQueuesApi.update(queue.id, body));
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <Dialog title={t('queues.edit.title', { name: queue.name })} onClose={onClose} wide>
      <form className="form queue-form" onSubmit={(event) => void submit(event)} noValidate>
        {error ? <QueueError error={error} onReload={onReload} /> : null}
        <p className="queue-fixed">
          <span className="incident-reference">{queue.prefix}</span>
          <span>{t('queues.edit.fixed', { key: queue.key })}</span>
        </p>
        <TextField
          label={t('queues.field.name')}
          value={name}
          maxLength={queueNameMax}
          required
          autoFocus
          disabled={busy}
          error={nameIssue}
          onChange={(event) => setName(event.target.value)}
        />
        <TextArea
          label={t('queues.field.description')}
          value={description}
          rows={2}
          maxLength={queueDescriptionMax}
          disabled={busy}
          onChange={(event) => setDescription(event.target.value)}
        />
        <TextField
          label={t('queues.field.publicLabel')}
          hint={t('queues.field.publicLabel.hint')}
          value={publicLabel}
          maxLength={queueNameMax}
          disabled={busy}
          onChange={(event) => setPublicLabel(event.target.value)}
        />
        <QueueSettingsFields
          visibility={lockedPublic ? 'public' : visibility}
          onVisibility={setVisibility}
          routingMode={routingMode}
          onRouting={setRoutingMode}
          priority={priority}
          onPriority={setPriority}
          busy={busy || lockedPublic}
        />
        {lockedPublic ? <p className="field-hint">{t('queues.edit.intakePublic')}</p> : null}
        <AssigneePicker
          type="team"
          label={t('queues.field.defaultTeam')}
          hint={
            queue.defaultTeamId && !clearTeam
              ? t('queues.field.defaultTeam.current', {
                  name: teamName ?? t('query.referenceUnknown'),
                })
              : t('queues.field.defaultTeam.hint')
          }
          value={team}
          onChange={(next) => {
            setTeam(next);
            if (next) setClearTeam(false);
          }}
        />
        {queue.defaultTeamId && !team ? (
          <label className="checkbox queue-clear-team">
            <input
              type="checkbox"
              checked={clearTeam}
              disabled={busy}
              onChange={(event) => setClearTeam(event.target.checked)}
            />
            <span>{t('queues.field.defaultTeam.clear')}</span>
          </label>
        ) : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy}>
            {t('action.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
