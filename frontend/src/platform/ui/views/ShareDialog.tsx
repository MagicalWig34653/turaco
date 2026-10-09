import { useState } from 'react';
import type { ApiError } from '../../api/client';
import { asApiError, useAsync } from '../../api/useAsync';
import { useI18n } from '../../i18n/I18nProvider';
import type { MessageKey } from '../../i18n/i18n';
import { useSession } from '../../session/SessionProvider';
import { Alert } from '../Alert';
import { ApiErrorAlert } from '../ApiErrorAlert';
import { Button } from '../Button';
import { Dialog } from '../Dialog';
import { Checkbox, Select } from '../Field';
import { viewsApi, type SavedView, type ShareLevel } from './api';
import {
  hasEditShare,
  removeShare,
  shareProblems,
  shareRecipientTypes,
  sharesChanged,
  toDraft,
  upsertShare,
  type ShareDraft,
} from './model';
import { SubjectChooser, useSubjectNames, type ChosenSubject } from './SubjectChooser';

const problemKey: Record<ReturnType<typeof shareProblems>[number], MessageKey> = {
  limit: 'views.share.problem.limit',
  everyoneEdit: 'views.share.problem.everyoneEdit',
  needShare: 'views.share.problem.needShare',
  needPublish: 'views.share.problem.needPublish',
};

/**
 * Replaces the share set of a View: everyone (global), users, Teams and roles with use or edit.
 * Loads the View fresh so the version and share list are current; the server re-checks every right.
 */
export function ShareDialog({
  view,
  onClose,
  onChanged,
}: {
  view: SavedView;
  onClose: () => void;
  onChanged: (view: SavedView) => void;
}) {
  const { t } = useI18n();
  const fresh = useAsync((signal) => viewsApi.get(view.id, signal), [view.id]);
  return (
    <Dialog title={t('views.share.title', { name: view.name })} onClose={onClose} wide>
      {fresh.error ? <ApiErrorAlert error={fresh.error} onRetry={fresh.reload} /> : null}
      {fresh.loading && !fresh.data ? <p role="status">{t('state.loading')}</p> : null}
      {fresh.data ? (
        <ShareEditor
          key={`${fresh.data.id}:${fresh.data.version}`}
          view={fresh.data}
          onClose={onClose}
          onChanged={onChanged}
          onReload={fresh.reload}
        />
      ) : (
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
        </div>
      )}
    </Dialog>
  );
}

function ShareEditor({
  view,
  onClose,
  onChanged,
  onReload,
}: {
  view: SavedView;
  onClose: () => void;
  onChanged: (view: SavedView) => void;
  onReload: () => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const saved = toDraft(view.shares);
  const [draft, setDraft] = useState<ShareDraft[]>(saved);
  const [picked, setPicked] = useState<ChosenSubject | null>(null);
  const [level, setLevel] = useState<ShareLevel>('use');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const names = useSubjectNames(
    draft.map((share) => ({ type: share.subjectType, id: share.subjectId })),
  );
  const everyone = draft.some((share) => share.subjectType === 'everyone');
  const problems = shareProblems(saved, draft, can);
  const changed = sharesChanged(saved, draft);
  const recipientTypes = shareRecipientTypes(can);
  const ownTeamsOnly = !can('views.share');
  const typeLabel = (type: string) => t(`views.share.type.${type}` as MessageKey);

  const save = async () => {
    setBusy(true);
    setError(undefined);
    try {
      const result = await viewsApi.setShares(
        view.id,
        view.version,
        draft.map(({ subjectType, subjectId, level: shareLevel }) => ({
          subjectType,
          ...(subjectId ? { subjectId } : {}),
          level: shareLevel,
        })),
      );
      onChanged(result);
      onClose();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="view-share"
      onSubmit={(event) => {
        event.preventDefault();
        if (changed && problems.length === 0) void save();
      }}
    >
      <p className="field-hint">{t('views.share.intro')}</p>
      <Checkbox
        label={t('views.share.everyone')}
        description={t('views.share.everyoneHint')}
        checked={everyone}
        disabled={!can('views.publish') && !everyone}
        onChange={(event) =>
          setDraft(
            event.target.checked
              ? upsertShare(draft, { subjectType: 'everyone', level: 'use' })
              : removeShare(draft, { subjectType: 'everyone' }),
          )
        }
      />

      <h3>{t('views.share.current')}</h3>
      {draft.filter((share) => share.subjectType !== 'everyone').length === 0 ? (
        <p className="empty">{t('views.share.none')}</p>
      ) : (
        <ul className="view-share-list">
          {draft
            .filter((share) => share.subjectType !== 'everyone')
            .map((share) => {
              const name = names(share.subjectType, share.subjectId);
              return (
                <li key={`${share.subjectType}:${share.subjectId}`}>
                  <span className="view-share-name">
                    <strong>{name}</strong>
                    <small>{typeLabel(share.subjectType)}</small>
                  </span>
                  <Select
                    label={t('views.share.levelFor', { name })}
                    value={share.level}
                    options={[
                      { value: 'use', label: t('views.share.level.use') },
                      { value: 'edit', label: t('views.share.level.edit') },
                    ]}
                    onChange={(event) =>
                      setDraft(
                        upsertShare(draft, { ...share, level: event.target.value as ShareLevel }),
                      )
                    }
                  />
                  <Button
                    aria-label={t('views.share.remove', { name })}
                    onClick={() => setDraft(removeShare(draft, share))}
                  >
                    {t('views.share.removeShort')}
                  </Button>
                </li>
              );
            })}
        </ul>
      )}

      <h3>{t('views.share.add')}</h3>
      {ownTeamsOnly ? <p className="field-hint">{t('views.share.ownTeamsOnly')}</p> : null}
      <div className="view-share-add">
        <SubjectChooser types={recipientTypes} value={picked} onChange={setPicked} />
        <Select
          label={t('views.share.level')}
          value={level}
          options={[
            { value: 'use', label: t('views.share.level.use') },
            { value: 'edit', label: t('views.share.level.edit') },
          ]}
          onChange={(event) => setLevel(event.target.value as ShareLevel)}
        />
        <Button
          disabled={!picked}
          onClick={() => {
            if (!picked) return;
            setDraft(upsertShare(draft, { subjectType: picked.type, subjectId: picked.id, level }));
            setPicked(null);
          }}
        >
          {t('views.share.addShort')}
        </Button>
      </div>

      {hasEditShare(draft) ? <Alert kind="info">{t('views.share.editWarning')}</Alert> : null}
      {problems.length > 0 ? (
        <Alert kind="warning">
          {problems.map((problem) => (
            <p key={problem}>{t(problemKey[problem])}</p>
          ))}
        </Alert>
      ) : null}
      {error ? (
        <>
          <ApiErrorAlert error={error} />
          {error.code === 'views.conflict' ? (
            <Button onClick={onReload}>{t('views.reload')}</Button>
          ) : null}
        </>
      ) : null}
      <div className="dialog-actions">
        <Button onClick={onClose}>{t('action.cancel')}</Button>
        <Button
          type="submit"
          variant="primary"
          busy={busy}
          disabled={!changed || problems.length > 0}
        >
          {t('views.share.save')}
        </Button>
      </div>
    </form>
  );
}
