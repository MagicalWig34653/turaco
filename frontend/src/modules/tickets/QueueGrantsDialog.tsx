import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import {
  SubjectChooser,
  useSubjectNames,
  type ChosenSubject,
} from '../../platform/ui/views/SubjectChooser';
import { Skeleton } from '../../platform/ui/Workspace';
import { ticketQueuesApi } from './api';
import { QueueError } from './QueueDialogs';
import {
  abilitiesOf,
  addSubject,
  grantsToMatrix,
  matrixToGrants,
  maxQueueGrants,
  removeSubject,
  sameMatrix,
  setAccess,
  setCreate,
  isEmptyRow,
  type MatrixRow,
} from './queueModel';
import type { TicketQueue } from './types';

const abilityKeys = ['read', 'work', 'internalComments', 'create', 'moveOut', 'moveIn'] as const;

/**
 * Grants matrix of one queue: per user, team or role whether they may read, work (which includes
 * internal comments and moving tickets out) and create (raising tickets and moving tickets in).
 * Saved as a full replacement with the queue's version; access follows at once.
 */
export function QueueGrantsDialog({
  queueId,
  onClose,
  onSaved,
}: {
  queueId: string;
  onClose: () => void;
  onSaved: (queue: TicketQueue) => void;
}) {
  const { t } = useI18n();
  const loaded = useAsync((signal) => ticketQueuesApi.get(queueId, signal), [queueId]);
  return (
    <Dialog
      title={t('queues.grants.title', { name: loaded.data?.name ?? '' })}
      onClose={onClose}
      wide
    >
      {loaded.error ? <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} /> : null}
      {!loaded.data && !loaded.error ? <Skeleton lines={4} /> : null}
      {loaded.data ? (
        <GrantsEditor
          key={`${loaded.data.id}:${loaded.data.version}`}
          queue={loaded.data}
          onClose={onClose}
          onSaved={onSaved}
          onReload={loaded.reload}
        />
      ) : null}
    </Dialog>
  );
}

function GrantsEditor({
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
  const [initial] = useState(() => grantsToMatrix(queue.grants));
  const [rows, setRows] = useState<MatrixRow[]>(initial);
  const [picked, setPicked] = useState<ChosenSubject | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const subjectName = useSubjectNames(
    rows.map((row) => ({ type: row.subjectType, id: row.subjectId })),
  );
  const grants = matrixToGrants(rows);
  const dirty = !sameMatrix(rows, initial);
  const full = grants.length >= maxQueueGrants;

  const save = async () => {
    setBusy(true);
    setError(undefined);
    try {
      onSaved(await ticketQueuesApi.replaceGrants(queue.id, queue.version, grants));
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  const subjectTypeLabel = (type: MatrixRow['subjectType']) => t(`views.share.type.${type}`);

  return (
    <div className="form queue-grants">
      {error ? <QueueError error={error} onReload={onReload} /> : null}
      <p className="field-hint">{t('queues.grants.intro')}</p>
      {queue.visibility === 'public' ? (
        <p className="field-hint">{t('queues.grants.publicHint')}</p>
      ) : null}
      {rows.length === 0 ? (
        <p className="empty">{t('queues.grants.empty')}</p>
      ) : (
        <div className="table-wrap">
          <table className="table table-detail queue-matrix">
            <caption className="visually-hidden">{t('queues.grants.matrix')}</caption>
            <thead>
              <tr>
                <th scope="col">{t('queues.grants.subject')}</th>
                <th scope="col">{t('queues.grants.read')}</th>
                <th scope="col">{t('queues.grants.work')}</th>
                <th scope="col">{t('queues.grants.create')}</th>
                <th scope="col">{t('queues.grants.allows')}</th>
                <th scope="col">
                  <span className="visually-hidden">{t('queues.grants.removeColumn')}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => {
                const label = subjectName(row.subjectType, row.subjectId);
                const abilities = abilitiesOf(row);
                const key = `${row.subjectType}:${row.subjectId}`;
                return (
                  <tr key={key} className={isEmptyRow(row) ? 'queue-matrix-empty' : undefined}>
                    <th scope="row">
                      <span>{label}</span>
                      <small>{subjectTypeLabel(row.subjectType)}</small>
                    </th>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`${label}: ${t('queues.grants.read')}`}
                        checked={row.access !== ''}
                        // Work includes read; switching read off removes work as well.
                        disabled={busy}
                        onChange={(event) =>
                          setRows(setAccess(rows, row, 'view', event.target.checked))
                        }
                      />
                    </td>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`${label}: ${t('queues.grants.work')}`}
                        checked={row.access === 'work' || row.access === 'manage'}
                        disabled={busy || row.access === 'manage'}
                        onChange={(event) =>
                          setRows(setAccess(rows, row, 'work', event.target.checked))
                        }
                      />
                    </td>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`${label}: ${t('queues.grants.create')}`}
                        checked={row.create}
                        disabled={busy}
                        onChange={(event) => setRows(setCreate(rows, row, event.target.checked))}
                      />
                    </td>
                    <td>
                      <ul className="queue-abilities">
                        {abilityKeys
                          .filter((ability) => abilities[ability])
                          .map((ability) => (
                            <li key={ability}>{t(`queues.ability.${ability}`)}</li>
                          ))}
                        {abilityKeys.every((ability) => !abilities[ability]) ? (
                          <li className="queue-ability-none">{t('queues.ability.none')}</li>
                        ) : null}
                      </ul>
                    </td>
                    <td>
                      <Button
                        disabled={busy}
                        aria-label={t('queues.grants.removeSubject', { name: label })}
                        onClick={() => setRows(removeSubject(rows, row))}
                      >
                        <span aria-hidden="true">×</span>
                      </Button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <fieldset className="queue-add-subject" disabled={busy}>
        <legend>{t('queues.grants.add')}</legend>
        <SubjectChooser types={['team', 'user', 'role']} value={picked} onChange={setPicked} />
        <Button
          disabled={!picked || full}
          onClick={() => {
            if (!picked) return;
            setRows(addSubject(rows, { subjectType: picked.type, subjectId: picked.id }));
            setPicked(null);
          }}
        >
          {t('queues.grants.addSubject')}
        </Button>
        {full ? (
          <p className="field-hint">{t('queues.grants.limit', { max: maxQueueGrants })}</p>
        ) : null}
      </fieldset>
      <Alert kind="info">{t('queues.grants.global')}</Alert>
      <div className="dialog-actions">
        <Button onClick={onClose}>{t('action.cancel')}</Button>
        <Button variant="primary" busy={busy} disabled={!dirty} onClick={() => void save()}>
          {t('queues.grants.save')}
        </Button>
      </div>
    </div>
  );
}
