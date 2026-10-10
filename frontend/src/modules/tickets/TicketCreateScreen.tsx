import { ModuleFeature } from '../../platform/modules/ModulesProvider';
import { useRef, useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { Card, Skeleton } from '../../platform/ui/Workspace';
import { useSession } from '../../platform/session/SessionProvider';
import { assetsApi } from '../assets/api';
import { organizationApi } from '../organization/api';
import { Alert } from '../../platform/ui/Alert';
import { useDebouncedValue } from '../../platform/ui/hooks';
import {
  describeImpactInText,
  duplicateCandidates,
  impactChoices,
  impactFields,
  impactHintKey,
  impactLabelKey,
  mergeDevices,
  withDeviceNote,
  type ImpactChoice,
} from './reportModel';
import { type Assignee } from '../tasks/AssigneePicker';
import { PersonLookup } from '../organization/PersonLookup';
import { IncidentBanner } from '../incidents/IncidentBanner';
import { Suggestions } from '../knowledge/Suggestions';
import { ticketQueuesApi, ticketsApi } from './api';
import { intakeChoice, queueChoiceLabel } from './queueModel';
import type { Ticket } from './types';

function ReportIcon({ kind }: { kind: 'device' | 'message' | 'book' | 'check' }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {kind === 'device' ? (
        <>
          <rect x="3" y="4" width="18" height="13" rx="2" />
          <path d="M8 21h8M12 17v4" />
        </>
      ) : kind === 'book' ? (
        <>
          <path d="M12 5v16M3 4h5a4 4 0 0 1 4 2 4 4 0 0 1 4-2h5v15h-5a4 4 0 0 0-4 2 4 4 0 0 0-4-2H3z" />
        </>
      ) : kind === 'check' ? (
        <path d="m5 12 4 4L19 6" />
      ) : (
        <path d="M5 4h14a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H9l-6 3V6a2 2 0 0 1 2-2Z" />
      )}
    </svg>
  );
}

function DeviceChoice({
  asset,
  name,
  selected,
  onSelect,
}: {
  asset: { id: string; reference: string };
  name: string;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <label className={`report-device ${selected ? 'is-selected' : ''}`}>
      <input
        type="radio"
        name="report-device"
        value={asset.id}
        checked={selected}
        onChange={onSelect}
      />
      <span className="report-device-icon">
        <ReportIcon kind="device" />
      </span>
      <span>
        <strong>{name}</strong>
        <small className="report-reference">{asset.reference}</small>
      </span>
    </label>
  );
}

/** Raising a ticket asks for little: what is wrong, and optionally which of my devices. */
export function TicketCreateScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const staff = can('tickets.manage');
  const [onBehalf, setOnBehalf] = useState<Assignee | null>(null);
  const { session } = useSession();
  // Devices of the affected person: the caller's own, or (with asset access) those of the person the
  // ticket is created for. Shared devices belong to that person's primary Location.
  const canListAssets = can('assets.view') || can('assets.manage');
  const affectedId = onBehalf?.id ?? session?.userId;
  const devices = useAsync(
    async (signal) => {
      if (!onBehalf) return assetsApi.mine(undefined, signal);
      if (!canListAssets) return null;
      return assetsApi.list({ assigneeId: onBehalf.id }, undefined, signal);
    },
    [onBehalf?.id, canListAssets],
  );
  const canReadUsers = can('organization.view');
  const location = useAsync(
    async (signal) =>
      canReadUsers && affectedId
        ? ((await organizationApi.user(affectedId, signal)).primaryLocationId ?? null)
        : null,
    [affectedId, canReadUsers],
  );
  // Shared devices (carts, printers) are assigned to a Location. For myself the server answers from my
  // own primary Location, needing no permission; for another person it takes asset access.
  const sharedDevices = useAsync(
    async (signal) => {
      if (!onBehalf) return assetsApi.sharedMine(undefined, signal);
      return canListAssets && location.data
        ? await assetsApi.list({ assigneeId: location.data }, undefined, signal)
        : null;
    },
    [onBehalf?.id, location.data, canListAssets],
  );
  const mine = useAsync(
    async (signal) => (await ticketsApi.list('mine', { open: true }, undefined, signal)).items,
    [],
  );
  // A Queue is asked for only when more than one is on offer; otherwise the server (or the single
  // offered Queue) decides silently. A failed read falls back to the intake Queue.
  const queues = useAsync(async (signal) => (await ticketQueuesApi.list(true, signal)).items, []);
  const choice = intakeChoice(queues.data);
  const [pickedQueue, setPickedQueue] = useState('');
  const chosenQueueId =
    choice.mode === 'single'
      ? choice.queue.id
      : choice.mode === 'choose'
        ? choice.queues.some((queue) => queue.id === pickedQueue)
          ? pickedQueue
          : choice.initial.id
        : undefined;
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [assetId, setAssetId] = useState('');
  const [impact, setImpact] = useState<ImpactChoice | ''>('');
  const [deviceNote, setDeviceNote] = useState('');
  const debouncedTitle = useDebouncedValue(title, 400);
  const duplicates = duplicateCandidates(debouncedTitle, mine.data ?? []);
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);
  const [created, setCreated] = useState<Ticket>();
  const [error, setError] = useState<ApiError | undefined>(undefined);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (submitting.current || !title.trim()) return;
    submitting.current = true;
    setBusy(true);
    setError(undefined);
    try {
      const text = withDeviceNote(description, t('report.deviceNote.label'), deviceNote);
      const body = {
        title: title.trim(),
        description: text,
        ...(assetId ? { assetId } : {}),
        ...(onBehalf ? { affectedUserId: onBehalf.id } : {}),
        ...(chosenQueueId ? { queueId: chosenQueueId } : {}),
      };
      try {
        setCreated(await ticketsApi.create({ ...body, ...impactFields(impact) }));
      } catch (cause) {
        // A server that does not know the impact fields refuses them (400): keep the signal in the text.
        if (impact && asApiError(cause).status === 400) {
          setCreated(
            await ticketsApi.create({
              ...body,
              description: describeImpactInText(text, t(impactLabelKey[impact])),
            }),
          );
        } else throw cause;
      }
    } catch (cause) {
      setError(asApiError(cause));
      submitting.current = false;
      setBusy(false);
    }
  };

  const names = { ...sharedDevices.data?.productNames, ...devices.data?.productNames };
  const offered = mergeDevices(
    (devices.data?.items ?? []).filter((device) => !onBehalf || device.status === 'assigned'),
    (sharedDevices.data?.items ?? []).filter((device) => device.status === 'assigned'),
  );
  if (created)
    return (
      <div className="report-workspace report-success">
        <Card className="report-success-card">
          <span className="report-success-icon">
            <ReportIcon kind="check" />
          </span>
          <p className="report-eyebrow">{t('reportPolish.sent')}</p>
          <h1
            tabIndex={-1}
            ref={(node) => {
              node?.focus();
            }}
          >
            {t('reportPolish.successTitle')}
          </h1>
          <p>{t('reportPolish.successIntro')}</p>
          <div className="report-receipt">
            <span className="report-reference">{created.reference}</span>
            <strong>{created.title}</strong>
          </div>
          <Link className="btn btn-primary" to={`/support/${encodeURIComponent(created.id)}`}>
            {t('reportPolish.openTicket')} <span aria-hidden="true">→</span>
          </Link>
        </Card>
        <Card className="report-next">
          <h2>{t('reportPolish.next')}</h2>
          <ol>
            <li>{t('reportPolish.nextReview')}</li>
            <li>{t('reportPolish.nextFollow')}</li>
          </ol>
          <Link to="/support">
            {t('tickets.back')} <span aria-hidden="true">→</span>
          </Link>
        </Card>
      </div>
    );

  return (
    <div className="report-workspace">
      <header className="report-heading">
        <div>
          <p className="report-eyebrow">{t('reportPolish.eyebrow')}</p>
          <h1>{t('reportPolish.title')}</h1>
          <p className="subtitle">{t('reportPolish.intro')}</p>
        </div>
        <Link to="/support" className="back-link">
          <span aria-hidden="true">←</span> {t('tickets.back')}
        </Link>
      </header>
      <IncidentBanner compact />
      <div className="report-layout">
        <form className="report-form" onSubmit={(event) => void submit(event)}>
          {error ? <ApiErrorAlert error={error} /> : null}
          <Card className="report-section">
            <div className="report-section-heading">
              <span className="report-step" aria-hidden="true">
                01
              </span>
              <div>
                <h2>{t('reportPolish.describe')}</h2>
                <p>{t('reportPolish.describeHint')}</p>
              </div>
            </div>
            <TextField
              label={t('tickets.field.title')}
              hint={t('reportPolish.titleHint')}
              placeholder={t('reportPolish.titlePlaceholder')}
              value={title}
              maxLength={200}
              required
              autoFocus
              disabled={busy}
              onChange={(event) => setTitle(event.target.value)}
            />
            {duplicates.length > 0 ? (
              <Alert kind="info">
                <p>{t('report.duplicate.title')}</p>
                <ul className="plain-list">
                  {duplicates.map((ticket) => (
                    <li key={ticket.id}>
                      <Link to={`/support/${encodeURIComponent(ticket.id)}`}>
                        {ticket.reference} · {ticket.title}
                      </Link>
                    </li>
                  ))}
                </ul>
                <p>{t('report.duplicate.hint')}</p>
              </Alert>
            ) : null}
            <TextArea
              label={t('tickets.field.description')}
              hint={`${t('tickets.field.description.hint')} · ${t('reportPolish.characters', { count: description.length, max: 5000 })}`}
              placeholder={t('reportPolish.descriptionPlaceholder')}
              value={description}
              rows={8}
              maxLength={5000}
              disabled={busy}
              onChange={(event) => setDescription(event.target.value)}
            />
          </Card>
          {staff ? (
            <Card className="report-section">
              <details open={onBehalf !== null}>
                <summary>{t('tickets.onBehalf.title')}</summary>
                <p className="field-hint">{t('tickets.onBehalf.hint')}</p>
                <PersonLookup
                  label={t('tickets.onBehalf.search')}
                  value={onBehalf}
                  onChange={(next) => {
                    setOnBehalf(next);
                    setAssetId('');
                  }}
                />
                {onBehalf ? (
                  <Button
                    onClick={() => {
                      setOnBehalf(null);
                      setAssetId('');
                    }}
                  >
                    {t('tickets.onBehalf.clear')}
                  </Button>
                ) : null}
              </details>
            </Card>
          ) : null}
          {choice.mode === 'choose' ? (
            <Card className="report-section">
              <div className="report-section-heading">
                <span className="report-step" aria-hidden="true">
                  02
                </span>
                <div>
                  <h2>{t(staff ? 'tickets.queue.pick' : 'reportPolish.queuePick')}</h2>
                  <p>{t(staff ? 'tickets.queue.pickHint' : 'reportPolish.queuePickHint')}</p>
                </div>
              </div>
              {staff ? (
                <Select
                  label={t('tickets.fact.queue')}
                  value={chosenQueueId ?? ''}
                  disabled={busy}
                  onChange={(event) => setPickedQueue(event.target.value)}
                  options={choice.queues.map((queue) => ({
                    value: queue.id,
                    label: queueChoiceLabel(queue, true),
                  }))}
                />
              ) : (
                <fieldset className="report-devices report-queues" disabled={busy}>
                  <legend className="report-sr-only">{t('reportPolish.queuePick')}</legend>
                  {choice.queues.map((queue) => (
                    <label
                      key={queue.id}
                      className={`report-device ${chosenQueueId === queue.id ? 'is-selected' : ''}`}
                    >
                      <input
                        type="radio"
                        name="report-queue"
                        value={queue.id}
                        checked={chosenQueueId === queue.id}
                        onChange={() => setPickedQueue(queue.id)}
                      />
                      <span className="report-device-icon">
                        <ReportIcon kind="message" />
                      </span>
                      <span>
                        <strong>{queueChoiceLabel(queue, false)}</strong>
                        {queue.description ? <small>{queue.description}</small> : null}
                      </span>
                    </label>
                  ))}
                </fieldset>
              )}
            </Card>
          ) : null}
          <Card className="report-section">
            <div className="report-section-heading">
              <div>
                <h2>{t('report.impact.title')}</h2>
                <p>{t('report.impact.intro')}</p>
              </div>
              <span className="report-optional">{t('reportPolish.optional')}</span>
            </div>
            <fieldset className="report-devices report-impacts" disabled={busy}>
              <legend className="report-sr-only">{t('report.impact.title')}</legend>
              <label className={`report-device ${impact === '' ? 'is-selected' : ''}`}>
                <input
                  type="radio"
                  name="report-impact"
                  value=""
                  checked={impact === ''}
                  onChange={() => setImpact('')}
                />
                <span>
                  <strong>{t('report.impact.none')}</strong>
                  <small>{t('report.impact.none.hint')}</small>
                </span>
              </label>
              {impactChoices.map((choiceKey) => (
                <label
                  key={choiceKey}
                  className={`report-device ${impact === choiceKey ? 'is-selected' : ''}`}
                >
                  <input
                    type="radio"
                    name="report-impact"
                    value={choiceKey}
                    checked={impact === choiceKey}
                    onChange={() => setImpact(choiceKey)}
                  />
                  <span>
                    <strong>{t(impactLabelKey[choiceKey])}</strong>
                    <small>{t(impactHintKey[choiceKey])}</small>
                  </span>
                </label>
              ))}
            </fieldset>
            <p className="field-hint">{t('report.impact.note')}</p>
          </Card>
          <Card className="report-section">
            <div className="report-section-heading">
              <span className="report-step" aria-hidden="true">
                {choice.mode === 'choose' ? '03' : '02'}
              </span>
              <div>
                <h2>{t('reportPolish.equipment')}</h2>
                <p>{t('tickets.field.device.hint')}</p>
              </div>
              <span className="report-optional">{t('reportPolish.optional')}</span>
            </div>
            {onBehalf && !canListAssets ? (
              <p className="report-device-note" role="status">
                {t('tickets.onBehalf.noDevice', { name: onBehalf.label })}
              </p>
            ) : onBehalf ? (
              <p className="report-device-note" role="status">
                {t('report.devices.forPerson', { name: onBehalf.label })}
              </p>
            ) : null}
            {devices.loading ? <Skeleton lines={2} /> : null}
            {devices.error ? (
              <p className="report-device-note" role="status">
                {t('reportPolish.devicesUnavailable')}
              </p>
            ) : null}
            <fieldset className="report-devices" disabled={busy}>
              <legend className="report-sr-only">{t('tickets.field.device')}</legend>
              <label className={`report-device ${assetId === '' ? 'is-selected' : ''}`}>
                <input
                  type="radio"
                  name="report-device"
                  value=""
                  checked={assetId === ''}
                  onChange={() => setAssetId('')}
                />
                <span className="report-device-icon">
                  <ReportIcon kind="message" />
                </span>
                <span>
                  <strong>{t('tickets.field.device.none')}</strong>
                </span>
              </label>
              {offered.own.map((a) => (
                <DeviceChoice
                  key={a.id}
                  asset={a}
                  name={names[a.productId] ?? a.reference}
                  selected={assetId === a.id}
                  onSelect={() => setAssetId(a.id)}
                />
              ))}
            </fieldset>
            {offered.shared.length > 0 ? (
              <fieldset className="report-devices" disabled={busy}>
                <legend className="report-device-legend">{t('report.devices.shared')}</legend>
                {offered.shared.map((a) => (
                  <DeviceChoice
                    key={a.id}
                    asset={a}
                    name={names[a.productId] ?? a.reference}
                    selected={assetId === a.id}
                    onSelect={() => setAssetId(a.id)}
                  />
                ))}
              </fieldset>
            ) : null}
            <TextField
              label={t('report.deviceNote.field')}
              hint={t('report.deviceNote.hint')}
              value={deviceNote}
              maxLength={200}
              disabled={busy}
              onChange={(event) => setDeviceNote(event.target.value)}
            />
          </Card>
          <div className="report-submit">
            <p>{t('reportPolish.submitHint')}</p>
            <Button
              type="submit"
              variant="primary"
              busy={busy}
              disabled={!title.trim() || queues.loading}
            >
              {t(busy ? 'reportPolish.sending' : 'reportPolish.submit')}{' '}
              <span aria-hidden="true">→</span>
            </Button>
          </div>
        </form>
        <aside className="report-help" aria-label={t('reportPolish.help')}>
          <Card className="report-help-card">
            <span className="report-help-icon">
              <ReportIcon kind="book" />
            </span>
            <p className="report-eyebrow">{t('reportPolish.help')}</p>
            <h2>{t('reportPolish.helpTitle')}</h2>
            <p>{t('reportPolish.helpIntro')}</p>
            <div className="report-suggestions">
              <ModuleFeature module="knowledge">
                <Suggestions text={title} />
              </ModuleFeature>
            </div>
            <Link className="report-card-footer" to="/knowledge">
              {t('reportPolish.browse')} <span aria-hidden="true">→</span>
            </Link>
          </Card>
          <Card className="report-tips">
            <h2>{t('reportPolish.tips')}</h2>
            <ul>
              <li>{t('reportPolish.tipWhen')}</li>
              <li>{t('reportPolish.tipTried')}</li>
              <li>{t('reportPolish.tipPrivacy')}</li>
            </ul>
          </Card>
        </aside>
      </div>
    </div>
  );
}
