import { useState } from 'react';
import type { ApiError } from '../../api/client';
import { asApiError, useAsync } from '../../api/useAsync';
import { useI18n } from '../../i18n/I18nProvider';
import type { MessageKey } from '../../i18n/i18n';
import { ApiErrorAlert } from '../ApiErrorAlert';
import { Button } from '../Button';
import { Dialog } from '../Dialog';
import { Select } from '../Field';
import { viewsApi, type SavedView } from './api';
import { sidebarGroupOf } from './model';
import { SubjectChooser, useSubjectNames, type ChosenSubject } from './SubjectChooser';

/**
 * Pin a View in the sidebar of every member of a Team or role. Needs views.pin_for_groups, which no
 * other permission implies; a rule grants no access, so members who may not use the View never see it.
 */
export function PinRulesDialog({ view, onClose }: { view: SavedView; onClose: () => void }) {
  const { t } = useI18n();
  const rules = useAsync((signal) => viewsApi.pinRules(view.id, signal), [view.id]);
  const names = useSubjectNames(
    (rules.data?.items ?? []).map((rule) => ({ type: rule.subjectType, id: rule.subjectId })),
  );
  const own = sidebarGroupOf(view.resource);
  const [picked, setPicked] = useState<ChosenSubject | null>(null);
  const [groupKey, setGroupKey] = useState(own);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
      rules.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  const groupLabel = (key: string) => t(`views.group.${key}` as MessageKey);
  return (
    <Dialog title={t('views.pinRules.title', { name: view.name })} onClose={onClose} wide>
      <p className="field-hint">{t('views.pinRules.intro')}</p>
      {rules.error ? <ApiErrorAlert error={rules.error} onRetry={rules.reload} /> : null}
      {rules.loading && !rules.data ? <p role="status">{t('state.loading')}</p> : null}
      {rules.data && rules.data.items.length === 0 ? (
        <p className="empty">{t('views.pinRules.none')}</p>
      ) : null}
      <ul className="view-share-list">
        {(rules.data?.items ?? []).map((rule) => {
          const name = names(rule.subjectType, rule.subjectId);
          return (
            <li key={rule.id}>
              <span className="view-share-name">
                <strong>{name}</strong>
                <small>
                  {t(`views.share.type.${rule.subjectType}` as MessageKey)} ·{' '}
                  {groupLabel(rule.groupKey)}
                </small>
              </span>
              <Button
                disabled={busy}
                aria-label={t('views.pinRules.remove', { name })}
                onClick={() => void run(() => viewsApi.deletePinRule(view.id, rule.id))}
              >
                {t('views.share.removeShort')}
              </Button>
            </li>
          );
        })}
      </ul>
      <h3>{t('views.pinRules.add')}</h3>
      <div className="view-share-add">
        <SubjectChooser types={['team', 'role']} value={picked} onChange={setPicked} />
        <Select
          label={t('views.pinRules.group')}
          value={groupKey}
          options={[...new Set([own, 'work'])].map((key) => ({
            value: key,
            label: groupLabel(key),
          }))}
          onChange={(event) => setGroupKey(event.target.value)}
        />
        <Button
          variant="primary"
          disabled={!picked || picked.type === 'user'}
          busy={busy}
          onClick={() => {
            if (!picked || picked.type === 'user') return;
            const { type, id } = picked;
            void run(async () => {
              await viewsApi.createPinRule(view.id, { subjectType: type, subjectId: id, groupKey });
              setPicked(null);
            });
          }}
        >
          {t('views.pinRules.addShort')}
        </Button>
      </div>
      {error ? <ApiErrorAlert error={error} /> : null}
      <div className="dialog-actions">
        <Button onClick={onClose} autoFocus>
          {t('action.close')}
        </Button>
      </div>
    </Dialog>
  );
}
