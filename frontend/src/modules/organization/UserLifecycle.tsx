import { useState } from 'react';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Alert } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { peopleAdminApi } from './adminApi';
import { reasonsFor, type LifecycleAction, type StatusOperation } from './adminModel';
import type { CredentialLink, PersonRow } from './adminTypes';
import { CredentialResult } from './OneTimeLink';

type Request = { action: LifecycleAction; person: PersonRow };

const statusOperation = (action: LifecycleAction): StatusOperation | undefined =>
  action === 'deactivate' || action === 'reactivate' || action === 'markDeparted'
    ? action
    : undefined;

/** Lifecycle dialogs shared by the Users list and the User detail page. */
export function useLifecycle(onChanged: (updated?: PersonRow) => void) {
  const [request, setRequest] = useState<Request | null>(null);
  const close = () => setRequest(null);
  const dialog = request ? (
    <LifecycleDialog request={request} onClose={close} onChanged={onChanged} />
  ) : null;
  return {
    start: (action: LifecycleAction, person: PersonRow) => setRequest({ action, person }),
    dialog,
  };
}

function LifecycleDialog({
  request,
  onClose,
  onChanged,
}: {
  request: Request;
  onClose: () => void;
  onChanged: (updated?: PersonRow) => void;
}) {
  const operation = statusOperation(request.action);
  return operation ? (
    <StatusDialog
      operation={operation}
      person={request.person}
      onClose={onClose}
      onChanged={onChanged}
    />
  ) : (
    <CredentialDialog
      kind={request.action === 'sendInvitation' ? 'invitation' : 'reset'}
      person={request.person}
      onClose={onClose}
      onChanged={onChanged}
    />
  );
}

const titleKey: Record<StatusOperation, MessageKey> = {
  deactivate: 'lifecycle.deactivate.title',
  reactivate: 'lifecycle.reactivate.title',
  markDeparted: 'lifecycle.markDeparted.title',
};
const introKey: Record<StatusOperation, MessageKey> = {
  deactivate: 'lifecycle.deactivate.intro',
  reactivate: 'lifecycle.reactivate.intro',
  markDeparted: 'lifecycle.markDeparted.intro',
};
const confirmKey: Record<StatusOperation, MessageKey> = {
  deactivate: 'lifecycle.deactivate.confirm',
  reactivate: 'lifecycle.reactivate.confirm',
  markDeparted: 'lifecycle.markDeparted.confirm',
};

function StatusDialog({
  operation,
  person,
  onClose,
  onChanged,
}: {
  operation: StatusOperation;
  person: PersonRow;
  onClose: () => void;
  onChanged: (updated?: PersonRow) => void;
}) {
  const { t } = useI18n();
  const reasons = reasonsFor(operation);
  const [reason, setReason] = useState('');
  return (
    <GuardedActionDialog
      title={t(titleKey[operation], { name: person.displayName })}
      confirmLabel={t(confirmKey[operation])}
      danger={operation !== 'reactivate'}
      disabled={reason === ''}
      run={async () => {
        const call =
          operation === 'deactivate'
            ? peopleAdminApi.deactivate
            : operation === 'reactivate'
              ? peopleAdminApi.reactivate
              : peopleAdminApi.markDeparted;
        onChanged(await call(person.id, person.version, reason));
      }}
      onDone={onClose}
      onClose={onClose}
    >
      <p>{t(introKey[operation], { name: person.displayName })}</p>
      {operation !== 'reactivate' ? <Alert kind="info">{t('lifecycle.sessionsEnd')}</Alert> : null}
      <Select
        label={t('lifecycle.reason')}
        hint={t('lifecycle.reasonHint')}
        value={reason}
        onChange={(event) => setReason(event.target.value)}
        options={[
          { value: '', label: t('lifecycle.reason.choose') },
          ...reasons.map((code) => ({ value: code, label: t(`lifecycle.reason.${code}`) })),
        ]}
        required
      />
    </GuardedActionDialog>
  );
}

function CredentialDialog({
  kind,
  person,
  onClose,
  onChanged,
}: {
  kind: 'invitation' | 'reset';
  person: PersonRow;
  onClose: () => void;
  onChanged: (updated?: PersonRow) => void;
}) {
  const { t } = useI18n();
  const [result, setResult] = useState<CredentialLink | null>(null);
  if (result) {
    return (
      <Dialog
        title={t(
          kind === 'invitation' ? 'credential.invitation.doneTitle' : 'credential.reset.doneTitle',
        )}
        onClose={onClose}
        wide
      >
        <CredentialResult kind={kind} result={result} person={person} />
        <div className="dialog-actions">
          <Button
            variant="primary"
            onClick={() => {
              onChanged();
              onClose();
            }}
          >
            {t('action.close')}
          </Button>
        </div>
      </Dialog>
    );
  }
  return (
    <GuardedActionDialog
      title={t(kind === 'invitation' ? 'credential.invitation.title' : 'credential.reset.title', {
        name: person.displayName,
      })}
      confirmLabel={t(
        kind === 'invitation' ? 'credential.invitation.confirm' : 'credential.reset.confirm',
      )}
      disabled={!person.primaryEmail}
      run={async () => {
        setResult(
          await (kind === 'invitation'
            ? peopleAdminApi.sendInvitation(person.id)
            : peopleAdminApi.resetPassword(person.id)),
        );
      }}
      onDone={() => undefined}
      onClose={onClose}
    >
      <p>
        {t(kind === 'invitation' ? 'credential.invitation.intro' : 'credential.reset.intro', {
          name: person.displayName,
        })}
      </p>
      {person.primaryEmail ? (
        <p>{t('credential.sentTo', { email: person.primaryEmail })}</p>
      ) : (
        <Alert kind="warning">{t('credential.noEmail')}</Alert>
      )}
      {kind === 'reset' ? (
        <Alert kind="info">{t('credential.reset.noLink')}</Alert>
      ) : (
        <Alert kind="info">{t('credential.invitation.linkHint')}</Alert>
      )}
    </GuardedActionDialog>
  );
}
