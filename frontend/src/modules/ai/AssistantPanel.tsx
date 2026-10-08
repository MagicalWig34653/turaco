import { useEffect, useRef, useState } from 'react';
import { isApiError } from '../../platform/api/client';
import { AiError } from './AiError';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { aiApi } from './api';
import { consentBody, toolOutcomeKey } from './model';
import type { ResourceRef, ScopeRequest, Status, Turn, UsageCounters } from './types';

/** Intentionally text only: no Markdown, HTML, links or remote resource loading. */
export function InertAnswer({ text }: { text: string }) {
  return <div className="ai-text">{text}</div>;
}
export function Budget({ usage }: { usage: UsageCounters | null }) {
  const { t } = useI18n();
  return usage ? (
    <section aria-label={t('ai.budget')} className="ai-budget">
      <strong>{t('ai.budget')}</strong>
      <p>
        {t('ai.remaining', {
          hour: usage.requestsHourRemaining,
          day: usage.requestsTodayRemaining,
          tokens: usage.tokensTodayRemaining,
          installation: usage.installationTokensRemaining,
        })}
      </p>
    </section>
  ) : (
    <p>{t('ai.usageUnknown')}</p>
  );
}
export function AssistantPanel({
  status,
  visible,
  context,
  sequence,
  onClose,
}: {
  status: Status;
  visible: boolean;
  context?: ResourceRef | undefined;
  sequence: number;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [text, setText] = useState('');
  const [id, setId] = useState<string>();
  const [turns, setTurns] = useState<{ question: string; turn: Turn }[]>([]);
  const [requests, setRequests] = useState<ScopeRequest[]>([]);
  const [retry, setRetry] = useState(false);
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);
  const [error, setError] = useState<unknown>();
  const [usage, setUsage] = useState(status.usage);
  const [restart, setRestart] = useState(false);
  useEffect(() => {
    setUsage(status.usage);
  }, [status.usage]);
  useEffect(() => {
    if (context) setText(t('ai.summarize'));
  }, [sequence, context, t]);
  async function run(action: () => Promise<void>) {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    setError(undefined);
    try {
      await action();
    } catch (e) {
      setError(e);
      if (
        isApiError(e) &&
        ['ai.provider_changed', 'ai.conversation_too_large', 'ai.not_found'].includes(e.code)
      )
        setRestart(true);
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }
  function send(value: string) {
    if (!value.trim() || value.length > 4000 || restart) return;
    void run(async () => {
      const turn = await aiApi.message(value, id, context);
      setId(turn.conversationId);
      setTurns((old) => [...old, { question: value, turn }]);
      setRequests(turn.scopeRequests ?? []);
      setUsage(turn.usage);
      setRetry(false);
      setText('');
    });
  }
  function consent(request: ScopeRequest) {
    if (!id) return;
    const body = consentBody(id, request);
    if (!body) return;
    void run(async () => {
      await aiApi.scope(body);
      setRequests((old) =>
        old.filter(
          (r) => r.resourceId !== request.resourceId || r.resourceType !== request.resourceType,
        ),
      );
      setRetry(true);
    });
  }
  function end() {
    void run(async () => {
      if (id) {
        try {
          await aiApi.end(id);
        } catch (e) {
          if (!isApiError(e) || e.code !== 'ai.not_found') throw e;
        }
      }
      setId(undefined);
      setTurns([]);
      setRequests([]);
      setRetry(false);
      setRestart(false);
      setText('');
    });
  }
  function download() {
    if (!id) return;
    void run(async () => {
      const transcript = await aiApi.transcript(id);
      const content = transcript.items
        .map((item) => `${t(item.role === 'user' ? 'ai.you' : 'ai.assistant')}\n${item.content}`)
        .join('\n\n');
      const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }));
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = 'turaco-transcript.txt';
      anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    });
  }
  if (!visible) return null;
  return (
    <Dialog title={t('ai.assistant')} onClose={onClose}>
      <div className="ai-panel">
        <Button onClick={onClose}>{t('action.close')}</Button>
        {status.provider && (
          <section className="ai-notice">
            <strong>
              {status.provider.displayName} ·{' '}
              {t(status.provider.local ? 'ai.local' : 'ai.external')}
            </strong>
            <p>{t('ai.privacy', { minutes: status.conversationTtlMinutes })}</p>
            <p>
              {t('ai.allowedClasses')}:{' '}
              {status.provider.allowedDataClasses.map((c) => t(`ai.class.${c}`)).join(', ') ||
                t('ai.none')}
            </p>
          </section>
        )}
        {context && (
          <p>
            {t('ai.context')}: {t(`ai.resource.${context.type}`)} {context.id}
          </p>
        )}
        <Budget usage={usage} />
        <div className="ai-messages" role="log" aria-label={t('ai.messages')} aria-live="polite">
          {!turns.length && <p>{t('ai.empty')}</p>}
          {turns.map(({ question, turn }, index) => (
            <article key={index}>
              <strong>{t('ai.you')}</strong>
              <InertAnswer text={question} />
              <strong>{t('ai.assistant')}</strong>
              <InertAnswer text={turn.answer} />
              {turn.stopReason !== 'answer' && (
                <p role="status">{t(`ai.stop.${turn.stopReason}`)}</p>
              )}
              <ul className="ai-tools">
                {(turn.toolsUsed ?? []).map((tool, i) => (
                  <li key={i}>
                    <span>{tool.tool}</span>
                    {tool.target && (
                      <span>
                        {t(`ai.resource.${tool.target.type}`)} {tool.target.id}
                      </span>
                    )}
                    <span>{t('ai.items', { count: tool.itemCount })}</span>
                    <span>{t(toolOutcomeKey(tool.outcome))}</span>
                  </li>
                ))}
              </ul>
              <small>{t('ai.tokens', { input: turn.tokens.in, output: turn.tokens.out })}</small>
            </article>
          ))}
        </div>
        <AiError error={error} />
        {requests.map((request, index) => (
          <section
            className="ai-consent"
            key={`${request.resourceType}:${request.resourceId}:${index}`}
          >
            <p>
              {t('ai.consent', {
                tool: request.tool,
                type:
                  request.resourceType === 'ticket' || request.resourceType === 'device'
                    ? t(`ai.resource.${request.resourceType}`)
                    : request.resourceType,
                id: request.resourceId,
              })}
            </p>
            <Button
              disabled={busy || !consentBody(id ?? '', request)}
              onClick={() => consent(request)}
            >
              {t('ai.allow')}
            </Button>
            <Button
              disabled={busy}
              onClick={() => setRequests((old) => old.filter((r) => r !== request))}
            >
              {t('ai.deny')}
            </Button>
          </section>
        ))}
        {retry && (
          <div>
            <p>{t('ai.retryNotice')}</p>
            <Button disabled={busy || restart} onClick={() => send(turns.at(-1)?.question ?? '')}>
              {t('ai.retry')}
            </Button>
          </div>
        )}
        {restart && (
          <Button disabled={busy} onClick={end}>
            {t('ai.restart')}
          </Button>
        )}
        <form
          onSubmit={(event) => {
            event.preventDefault();
            send(text);
          }}
        >
          <label>
            {t('ai.message')}
            <textarea
              value={text}
              maxLength={4000}
              required
              disabled={busy || restart}
              onChange={(event) => setText(event.target.value)}
            />
          </label>
          <Button type="submit" variant="primary" busy={busy} disabled={!text.trim() || restart}>
            {t('ai.send')}
          </Button>
        </form>
        <div className="ai-actions">
          <Button disabled={!id || busy} onClick={download}>
            {t('ai.download')}
          </Button>
          <Button disabled={!id || busy} onClick={end}>
            {t('ai.end')}
          </Button>
        </div>
        <p>{t('ai.endNotice')}</p>
      </div>
    </Dialog>
  );
}
