import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Dialog } from '../../platform/ui/Dialog';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { ReasonDialog } from '../../platform/ui/ReasonDialog';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { PersonLookup } from '../organization/PersonLookup';
import { OwnerName } from './OwnerName';
import { problemsApi } from './api';
import { ProblemBadge } from './ProblemsScreen';
import { TicketPicker } from '../tickets/TicketPicker';
import type { TicketHit } from '../tickets/ticketLookup';

const needsText: Record<string, MessageKey> = {
  identify_cause: 'problems.field.cause',
  mark_known_error: 'problems.field.workaround',
  resolve: 'problems.field.resolution',
};

export function ProblemDetailScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const loaded = useAsync((signal) => problemsApi.get(id, signal), [id]);
  const [op, setOp] = useState<string | null>(null);
  const [picked, setPicked] = useState<TicketHit[]>([]);
  const [unlinking, setUnlinking] = useState<{ id: string; label: string } | null>(null);
  const [ownerOpen, setOwnerOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const problem = loaded.data;
  if (!problem) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }
  const manage = can('problems.manage');
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
      loaded.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  const link = (event: FormEvent) => {
    event.preventDefault();
    void run(async () => {
      for (const hit of picked) await problemsApi.linkTicket(problem.id, hit.id);
      setPicked([]);
    });
  };
  return (
    <>
      <PageHeader
        title={`${problem.reference} · ${problem.title}`}
        actions={
          manage
            ? problem.allowedOperations.map((operation) => (
                <Button
                  key={operation}
                  variant={operation === 'resolve' ? 'primary' : 'secondary'}
                  busy={busy}
                  onClick={() =>
                    operation in needsText
                      ? setOp(operation)
                      : void run(() => problemsApi.operate(problem.id, operation, problem.version))
                  }
                >
                  {t(`problems.action.${operation}` as MessageKey)}
                </Button>
              ))
            : null
        }
      />
      <p>
        <Link to="/problems">{t('problems.back')}</Link>
      </p>
      {error ? <ApiErrorAlert error={error} /> : null}
      <p>
        <ProblemBadge status={problem.status} />
      </p>
      {problem.description ? <p className="preline">{problem.description}</p> : null}
      <dl className="facts">
        <dt>{t('problems.owner')}</dt>
        <dd>
          <OwnerName ownerId={problem.ownerId} />{' '}
          {manage && problem.status !== 'closed' ? (
            <Button onClick={() => setOwnerOpen(true)}>
              {t(problem.ownerId ? 'problems.owner.change' : 'problems.owner.set')}
            </Button>
          ) : null}
        </dd>
        {problem.cause ? (
          <>
            <dt>{t('problems.field.cause')}</dt>
            <dd className="preline">{problem.cause}</dd>
          </>
        ) : null}
        {problem.workaround ? (
          <>
            <dt>{t('problems.field.workaround')}</dt>
            <dd className="preline">{problem.workaround}</dd>
          </>
        ) : null}
        {problem.resolution ? (
          <>
            <dt>{t('problems.field.resolution')}</dt>
            <dd className="preline">{problem.resolution}</dd>
          </>
        ) : null}
      </dl>
      <section>
        <h2>{t('problems.tickets')}</h2>
        {problem.tickets.length === 0 ? (
          <p className="empty">{t('problems.tickets.none')}</p>
        ) : null}
        <ul className="plain-list">
          {problem.tickets.map((tk) => (
            <li key={tk.id}>
              <Link to={`/support/${encodeURIComponent(tk.id)}`}>
                {tk.reference} · {tk.title}
              </Link>{' '}
              {manage ? (
                <Button
                  onClick={() =>
                    setUnlinking({ id: tk.id, label: `${tk.reference} · ${tk.title}` })
                  }
                >
                  {t('problems.unlink')}
                </Button>
              ) : null}
            </li>
          ))}
        </ul>
        {manage && problem.status !== 'closed' ? (
          <form className="link-panel" onSubmit={link}>
            <TicketPicker
              label={t('problems.linkTicket')}
              hint={t('problems.linkTicket.hint')}
              multiple
              selected={picked}
              excludeIds={problem.tickets.map((tk) => tk.id)}
              disabled={busy}
              onChange={setPicked}
            />
            <Button type="submit" busy={busy} disabled={picked.length === 0}>
              {t('problems.link')}
            </Button>
          </form>
        ) : null}
      </section>
      {unlinking ? (
        <GuardedActionDialog
          title={t('problems.unlink.title')}
          confirmLabel={t('problems.unlink')}
          danger
          run={() => problemsApi.unlinkTicket(problem.id, unlinking.id)}
          onDone={() => {
            setUnlinking(null);
            loaded.reload();
          }}
          onClose={() => setUnlinking(null)}
        >
          <p>{t('problems.unlink.confirm', { ticket: unlinking.label })}</p>
        </GuardedActionDialog>
      ) : null}
      {ownerOpen ? (
        <OwnerDialog
          problemId={problem.id}
          version={problem.version}
          onClose={() => setOwnerOpen(false)}
          onDone={() => {
            setOwnerOpen(false);
            loaded.reload();
          }}
        />
      ) : null}
      {op && op in needsText ? (
        <ReasonDialog
          title={t(`problems.action.${op}` as MessageKey)}
          label={t(needsText[op] as MessageKey)}
          confirmLabel={t(`problems.action.${op}` as MessageKey)}
          onClose={() => setOp(null)}
          onSubmit={async (text) => {
            await problemsApi.operate(problem.id, op, problem.version, text);
            setOp(null);
            loaded.reload();
          }}
        />
      ) : null}
    </>
  );
}

function OwnerDialog({
  problemId,
  version,
  onClose,
  onDone,
}: {
  problemId: string;
  version: number;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const [owner, setOwner] = useState<Assignee | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!owner) return;
    setBusy(true);
    setError(undefined);
    try {
      await problemsApi.setOwner(problemId, owner.id, version);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('problems.owner.set')} onClose={onClose} wide>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        {can('organization.view') ? (
          <AssigneePicker
            type="user"
            label={t('problems.owner')}
            value={owner}
            onChange={setOwner}
          />
        ) : (
          <PersonLookup label={t('problems.owner')} value={owner} onChange={setOwner} />
        )}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!owner}>
            {t('problems.owner.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
