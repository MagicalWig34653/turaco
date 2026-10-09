import { formatDateTime } from '../../../platform/format/format';
import { useI18n } from '../../../platform/i18n/I18nProvider';
import { Link } from '../../../platform/router/Router';
import { Badge } from '../../../platform/ui/Alert';
import { Button } from '../../../platform/ui/Button';
import { Dialog } from '../../../platform/ui/Dialog';
import { isOverdue } from '../actions';
import { assigneeLabel, StatusBadge } from '../TaskTable';
import type { TaskAction } from '../types';
import type { BoardCard } from './types';

/** Side panel with the facts of one card and the lifecycle actions the viewer may run. */
export function CardDrawer({
  card,
  actions,
  canAssign,
  busy,
  onAction,
  onAssign,
  onClose,
}: {
  card: BoardCard;
  actions: readonly TaskAction[];
  canAssign: boolean;
  busy: boolean;
  onAction: (action: TaskAction) => void;
  onAssign: () => void;
  onClose: () => void;
}) {
  const { t, locale } = useI18n();
  const overdue = isOverdue(card, new Date());
  return (
    <Dialog title={card.title} onClose={onClose} drawer>
      <dl className="facts">
        <dt>{t('tasks.col.status')}</dt>
        <dd>
          <StatusBadge status={card.status} />
        </dd>
        {card.statusReason ? (
          <>
            <dt>{t('tasks.fact.reason')}</dt>
            <dd>{card.statusReason}</dd>
          </>
        ) : null}
        <dt>{t('tasks.col.priority')}</dt>
        <dd>{t(`tasks.priority.${card.priority}`)}</dd>
        <dt>{t('tasks.col.assignee')}</dt>
        <dd>{assigneeLabel(card, t('tasks.assignee.none'))}</dd>
        <dt>{t('tasks.col.due')}</dt>
        <dd>
          {card.dueAt ? formatDateTime(locale, card.dueAt) : '–'}{' '}
          {overdue ? <Badge tone="danger">{t('tasks.overdue')}</Badge> : null}
        </dd>
        <dt>{t('tasks.col.updated')}</dt>
        <dd>{formatDateTime(locale, card.updatedAt)}</dd>
      </dl>
      {card.description ? (
        <section>
          <h3>{t('tasks.field.description')}</h3>
          <p className="preline">{card.description}</p>
        </section>
      ) : null}
      <div className="board-drawer-actions">
        {canAssign ? (
          <Button busy={busy} onClick={onAssign}>
            {t('tasks.board.card.assignMe')}
          </Button>
        ) : null}
        {actions.map((action) => (
          <Button
            key={action}
            variant={action === 'complete' || action === 'start' ? 'primary' : 'secondary'}
            disabled={busy}
            onClick={() => onAction(action)}
          >
            {t(`tasks.action.${action}`)}
          </Button>
        ))}
      </div>
      <div className="dialog-actions">
        <Link to={`/tasks/${encodeURIComponent(card.id)}`} className="btn btn-secondary">
          {t('tasks.board.card.openPage')}
        </Link>
        <Button onClick={onClose}>{t('action.close')}</Button>
      </div>
    </Dialog>
  );
}
