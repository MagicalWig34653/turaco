import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { formatDateTime } from '../../platform/format/format';
import { peopleAdminApi } from './adminApi';
import type { ExtendReason, PersonRow, SyncConflictChoice } from './adminTypes';
import {
  extendInstant,
  extendMax,
  extendMin,
  extendReasons,
  type AccessAction,
} from './importModel';

type Request = { action: AccessAction; person: PersonRow };

/** Link-directory-identity and extend-access dialogs of the User detail page. */
export function useAccessDialogs(onChanged: (updated?: PersonRow) => void) {
  const [request, setRequest] = useState<Request | null>(null);
  const close = () => setRequest(null);
  const done = (updated?: PersonRow) => {
    onChanged(updated);
    close();
  };
  let dialog = null;
  if (request?.action === 'linkDirectory')
    dialog = <LinkDirectoryDialog person={request.person} onChanged={done} onClose={close} />;
  if (request?.action === 'extendAccess')
    dialog = <ExtendAccessDialog person={request.person} onChanged={done} onClose={close} />;
  return {
    start: (action: AccessAction, person: PersonRow) => setRequest({ action, person }),
    dialog,
  };
}

const choiceKey = (choice: SyncConflictChoice) => `${choice.runId}|${choice.externalId}`;

/**
 * Attaches a directory identity to a local account (ADR-0034 R5). The identity comes from the conflict list of a
 * synchronization run (its e-mail address belongs to this account); the dialog shows both records and what the
 * server will do atomically. The server refuses while the account holds roles.
 */
function LinkDirectoryDialog({
  person,
  onChanged,
  onClose,
}: {
  person: PersonRow;
  onChanged: (updated?: PersonRow) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const conflicts = useAsync((signal) => peopleAdminApi.syncConflicts(signal), []);
  const [selected, setSelected] = useState('');
  const [understood, setUnderstood] = useState(false);
  const choice = (conflicts.data ?? []).find((entry) => choiceKey(entry) === selected);
  return (
    <GuardedActionDialog
      title={t('people.link.title', { name: person.displayName })}
      confirmLabel={t('people.link.confirm')}
      danger
      wide
      disabled={!choice || !understood}
      run={async () => {
        if (choice)
          onChanged(await peopleAdminApi.linkDirectoryIdentity(person.id, person.version, choice));
      }}
      onDone={() => undefined}
      onClose={onClose}
    >
      <p>{t('people.link.intro')}</p>
      <div className="adm-two-col">
        <section className="adm-card" aria-label={t('people.link.localAccount')}>
          <h3>{t('people.link.localAccount')}</h3>
          <p>{person.displayName}</p>
          <p className="adm-sub">{person.primaryEmail ?? '–'}</p>
          <p className="adm-sub">
            {t(`people.status.${person.status === 'active' ? 'active' : 'inactive'}`)}
          </p>
        </section>
        <section className="adm-card" aria-label={t('people.link.directoryIdentity')}>
          <h3>{t('people.link.directoryIdentity')}</h3>
          {choice ? (
            <>
              <p>{choice.username || choice.externalId}</p>
              <p className="adm-sub">{choice.providerKey}</p>
              <p className="adm-sub">{choice.externalId}</p>
            </>
          ) : (
            <p className="adm-sub">{t('people.link.chooseIdentity')}</p>
          )}
        </section>
      </div>
      {conflicts.error ? (
        <ApiErrorAlert error={conflicts.error} onRetry={conflicts.reload} />
      ) : conflicts.loading ? (
        <p role="status">{t('state.loading')}</p>
      ) : (conflicts.data ?? []).length === 0 ? (
        <Alert kind="warning">{t('people.link.noConflicts')}</Alert>
      ) : (
        <Select
          label={t('people.link.identity')}
          hint={t('people.link.identityHint')}
          value={selected}
          onChange={(event) => setSelected(event.target.value)}
          options={[
            { value: '', label: t('people.link.chooseIdentity') },
            ...(conflicts.data ?? []).map((entry) => ({
              value: choiceKey(entry),
              label: `${entry.username || entry.externalId} · ${entry.providerKey}`,
            })),
          ]}
          required
        />
      )}
      <Alert kind="warning">
        <ul className="adm-impact-list">
          <li>{t('people.link.effect.password')}</li>
          <li>{t('people.link.effect.sessions')}</li>
          <li>{t('people.link.effect.attributes')}</li>
          <li>{t('people.link.effect.roles')}</li>
          <li>{t('people.link.effect.irreversible')}</li>
        </ul>
      </Alert>
      <Checkbox
        label={t('people.link.understood')}
        checked={understood}
        onChange={(event) => setUnderstood(event.target.checked)}
      />
    </GuardedActionDialog>
  );
}

/** Extends the access end of an external account with a closed reason; the server bounds the date. */
function ExtendAccessDialog({
  person,
  onChanged,
  onClose,
}: {
  person: PersonRow;
  onChanged: (updated?: PersonRow) => void;
  onClose: () => void;
}) {
  const { t, locale } = useI18n();
  const [now] = useState(() => new Date());
  const [day, setDay] = useState('');
  const [reason, setReason] = useState<ExtendReason | ''>('');
  const instant = extendInstant(day, person.accessExpiresAt, now);
  return (
    <GuardedActionDialog
      title={t('people.extend.title', { name: person.displayName })}
      confirmLabel={t('people.extend.confirm')}
      disabled={instant === undefined || reason === ''}
      run={async () => {
        if (instant && reason)
          onChanged(await peopleAdminApi.extendAccess(person.id, person.version, instant, reason));
      }}
      onDone={() => undefined}
      onClose={onClose}
    >
      <p>{t('people.extend.intro')}</p>
      <p>
        {t('people.extend.current', {
          time: person.accessExpiresAt ? formatDateTime(locale, person.accessExpiresAt) : '–',
        })}
      </p>
      {person.status === 'inactive' ? (
        <Alert kind="info">{t('people.extend.inactive')}</Alert>
      ) : null}
      <TextField
        label={t('people.extend.until')}
        hint={t('people.extend.untilHint')}
        type="date"
        value={day}
        min={extendMin(person.accessExpiresAt, now)}
        max={extendMax(now)}
        error={day !== '' && instant === undefined ? t('people.extend.untilInvalid') : undefined}
        onChange={(event) => setDay(event.target.value)}
        required
      />
      <Select
        label={t('people.extend.reason')}
        value={reason}
        onChange={(event) => setReason(event.target.value as ExtendReason | '')}
        options={[
          { value: '', label: t('lifecycle.reason.choose') },
          ...extendReasons.map((code) => ({
            value: code,
            label: t(`people.extend.reason.${code}`),
          })),
        ]}
        required
      />
    </GuardedActionDialog>
  );
}
