import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { TextArea } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Skeleton } from '../../platform/ui/Workspace';
import { ticketsApi } from './api';

/**
 * A Ticket shared with an external account (ADR-0034): the Ticket and its public comments, and a reply when the
 * sharing allows it. The server sends nothing else (no queue, routing, people or internal notes).
 */
export function ExternalTicketScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const loaded = useAsync((signal) => ticketsApi.get(id, signal), [id]);
  const [body, setBody] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const ticket = loaded.data;
  if (!ticket) return <Skeleton lines={6} />;
  const canComment = ticket.abilities?.comment === true;
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!body.trim() || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.comment(ticket.id, body.trim(), false);
      setBody('');
      loaded.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <PageHeader
        title={ticket.title}
        eyebrow={ticket.reference}
        intro={t(`tickets.status.${ticket.status}`)}
        actions={
          <Link to="/tasks" className="btn btn-secondary">
            {t('external.ticket.back')}
          </Link>
        }
      />
      {ticket.description ? <p className="preline">{ticket.description}</p> : null}
      {ticket.resolution ? (
        <section>
          <h2>{t('tickets.fact.resolution')}</h2>
          <p className="preline">{ticket.resolution}</p>
        </section>
      ) : null}
      <h2>{t('external.ticket.comments')}</h2>
      {ticket.comments.length === 0 ? (
        <p className="empty">{t('external.ticket.noComments')}</p>
      ) : null}
      <ol className="comment-list">
        {ticket.comments.map((comment) => (
          <li key={comment.id}>
            <p>
              <strong>
                {ticket.names[comment.authorId] ?? t('ticketWorkspace.unknownPerson')}
              </strong>{' '}
              <time dateTime={comment.createdAt}>{formatDateTime(locale, comment.createdAt)}</time>
            </p>
            <p className="preline">{comment.body}</p>
          </li>
        ))}
      </ol>
      {canComment ? (
        <form className="form" onSubmit={(event) => void submit(event)}>
          {error ? <ApiErrorAlert error={error} /> : null}
          <TextArea
            label={t('external.ticket.reply')}
            hint={t('external.ticket.replyHint')}
            value={body}
            maxLength={5000}
            rows={4}
            onChange={(event) => setBody(event.target.value)}
          />
          <div className="form-actions">
            <Button type="submit" variant="primary" busy={busy} disabled={!body.trim()}>
              {t('external.ticket.send')}
            </Button>
          </div>
        </form>
      ) : (
        <p className="field-hint">{t('external.ticket.readOnly')}</p>
      )}
    </>
  );
}
