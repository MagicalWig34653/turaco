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
import { TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ReasonDialog } from '../../platform/ui/ReasonDialog';
import { problemsApi } from './api';
import { ProblemBadge } from './ProblemsScreen';

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
  const [ticketRef, setTicketRef] = useState('');
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
      await problemsApi.linkTicket(problem.id, ticketRef.trim());
      setTicketRef('');
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
                <Button onClick={() => void run(() => problemsApi.unlinkTicket(problem.id, tk.id))}>
                  {t('problems.unlink')}
                </Button>
              ) : null}
            </li>
          ))}
        </ul>
        {manage && problem.status !== 'closed' ? (
          <form className="filters" onSubmit={link}>
            <TextField
              label={t('problems.linkTicket')}
              hint={t('problems.linkTicket.hint')}
              value={ticketRef}
              onChange={(event) => setTicketRef(event.target.value)}
            />
            <Button type="submit" busy={busy} disabled={ticketRef.trim() === ''}>
              {t('problems.link')}
            </Button>
          </form>
        ) : null}
      </section>
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
