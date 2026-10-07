import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { copyContextText } from '../../platform/ui/ContextMenu';
import { Dialog } from '../../platform/ui/Dialog';
import { Select } from '../../platform/ui/Field';
import { organizationApi } from '../organization/api';
import { remoteAccessApi } from './api';
import {
  canCancel,
  canClose,
  canLaunch,
  canRecordConsent,
  isProviderLaunchLink,
  statusTone,
  stepperState,
} from './model';
import { cancelReasons, closeReasons, type RemoteSession } from './types';

export function SessionStatusBadge({ status }: { status: string }) {
  const { t } = useI18n();
  return (
    <Badge tone={statusTone(status)} live={status === 'launched'}>
      {t(`remoteaccess.status.${status}` as MessageKey)}
    </Badge>
  );
}

/** requested -> authorized -> launched -> closed; the state is always written, never only colored. */
export function Stepper({ session }: { session: Pick<RemoteSession, 'status' | 'launchedAt'> }) {
  const { t } = useI18n();
  const { steps, ended, waiting } = stepperState(session);
  return (
    <div className="ra-stepper-wrap">
      <ol className="ra-stepper" aria-label={t('remoteaccess.stepper')}>
        {steps.map(({ step, state }) => (
          <li
            key={step}
            className={`ra-step ra-step-${state}`}
            aria-current={state === 'current' ? 'step' : undefined}
          >
            <span className="ra-step-name">{t(`remoteaccess.step.${step}` as MessageKey)}</span>
            <small>{t(`remoteaccess.stepState.${state}` as MessageKey)}</small>
          </li>
        ))}
      </ol>
      {waiting ? <p className="ra-step-note">{t('remoteaccess.waitingApproval')}</p> : null}
      {ended ? (
        <p className="ra-step-note">
          {t('remoteaccess.endedEarly', {
            status: t(`remoteaccess.status.${ended}` as MessageKey),
          })}
        </p>
      ) : null}
    </div>
  );
}

/** Name of a User when the directory is readable; otherwise a neutral label, never the id. */
export function UserName({ id }: { id: string | null }) {
  const { t } = useI18n();
  const { session, can } = useSession();
  const allowed = can('organization.view');
  const loaded = useAsync(
    (signal) => (id && allowed ? organizationApi.user(id, signal) : Promise.resolve(undefined)),
    [id, allowed],
  );
  if (!id) return <>–</>;
  if (id === session?.userId) return <>{t('remoteaccess.you')}</>;
  return <>{loaded.data?.displayName ?? t('remoteaccess.otherUser')}</>;
}

type ReasonAction = 'close' | 'cancel';

function ReasonCodeDialog({
  action,
  session,
  onClose,
  onDone,
}: {
  action: ReasonAction;
  session: RemoteSession;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const codes = action === 'close' ? closeReasons : cancelReasons;
  const [reason, setReason] = useState<string>(codes[0]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await (action === 'close' ? remoteAccessApi.close : remoteAccessApi.cancel)(
        session.id,
        session.version,
        reason,
      );
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog
      title={t(action === 'close' ? 'remoteaccess.closeSession' : 'remoteaccess.cancelSession')}
      onClose={onClose}
    >
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <Select
          label={t('remoteaccess.reason')}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          options={codes.map((value) => ({
            value,
            label: t(`remoteaccess.${action}Reason.${value}` as MessageKey),
          }))}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy}>
            {t(action === 'close' ? 'remoteaccess.closeSession' : 'remoteaccess.cancelSession')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/**
 * The active session: stepper, consent controls and the launch button. The launch link goes
 * straight from the exchange response to the browser; it is never kept in state or storage.
 */
export function SessionPanel({
  session,
  onChanged,
}: {
  session: RemoteSession;
  onChanged: () => void;
}) {
  const { t } = useI18n();
  const { session: auth } = useSession();
  const me = auth?.userId;
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<ApiError>();
  const [invalidLink, setInvalidLink] = useState(false);
  const [launched, setLaunched] = useState(false);
  const [dialog, setDialog] = useState<ReasonAction | null>(null);
  const [copied, setCopied] = useState(false);

  const run = async (name: string, action: () => Promise<void>) => {
    setBusy(name);
    setError(undefined);
    setInvalidLink(false);
    try {
      await action();
      onChanged();
    } catch (cause) {
      setError(asApiError(cause));
      onChanged();
    } finally {
      setBusy(null);
    }
  };
  const consent = (decision: 'granted' | 'declined') =>
    run(decision, async () => {
      await remoteAccessApi.consent(session.id, session.version, decision);
    });
  const open = () =>
    run('launch', async () => {
      const uri = await remoteAccessApi.launch(session.id, session.version);
      if (!isProviderLaunchLink(uri)) {
        setInvalidLink(true);
        return;
      }
      setLaunched(true);
      window.location.href = uri;
    });

  return (
    <div className="ra-session">
      <p>
        <Link to={`/remote-access/sessions/${encodeURIComponent(session.id)}`}>
          {session.reference}
        </Link>{' '}
        <SessionStatusBadge status={session.status} />
      </p>
      <Stepper session={session} />
      {error ? <ApiErrorAlert error={error} /> : null}
      {invalidLink ? <Alert kind="error">{t('remoteaccess.launch.invalidLink')}</Alert> : null}
      {session.consent !== 'unknown' ? (
        <p>
          {t('remoteaccess.consent')}: {t(`remoteaccess.consent.${session.consent}` as MessageKey)}
        </p>
      ) : null}
      {canRecordConsent(session, me) ? (
        <div className="ra-actions" role="group" aria-label={t('remoteaccess.consent')}>
          <Button busy={busy === 'granted'} onClick={() => void consent('granted')}>
            {t('remoteaccess.consent.agreed')}
          </Button>
          <Button busy={busy === 'declined'} onClick={() => void consent('declined')}>
            {t('remoteaccess.consent.declinedAction')}
          </Button>
        </div>
      ) : null}
      <div className="ra-actions">
        {canLaunch(session, me) ? (
          <Button variant="primary" busy={busy === 'launch'} onClick={() => void open()}>
            {t('remoteaccess.openTool')}
          </Button>
        ) : null}
        {canClose(session, me) ? (
          <Button onClick={() => setDialog('close')}>{t('remoteaccess.closeSession')}</Button>
        ) : null}
        {canCancel(session, me) ? (
          <Button onClick={() => setDialog('cancel')}>{t('remoteaccess.cancelSession')}</Button>
        ) : null}
      </div>
      {launched ? (
        <Alert kind="info">
          <p>{t('remoteaccess.launch.hint', { provider: session.provider })}</p>
          <Button
            onClick={() => void copyContextText(session.reference).then((ok) => setCopied(ok))}
          >
            {t('remoteaccess.copyReference')}
          </Button>
          {copied ? <span role="status"> {t('remoteaccess.copied')}</span> : null}
        </Alert>
      ) : null}
      {dialog ? (
        <ReasonCodeDialog
          action={dialog}
          session={session}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            onChanged();
          }}
        />
      ) : null}
    </div>
  );
}
