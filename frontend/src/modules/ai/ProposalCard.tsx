import { useEffect, useState } from 'react';
import { isApiError } from '../../platform/api/client';
import { Button } from '../../platform/ui/Button';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { AiError } from './AiError';
import { aiApi } from './api';
import { canConfirm, proposalOutcomeKey, proposalParams, remainingSeconds } from './model';
import type { Proposal } from './types';

const reloadOn = [
  'ai.proposal_expired',
  'ai.proposal_not_pending',
  'ai.proposal_in_progress',
  'ai.proposal_hash_mismatch',
  'ai.execution_unknown',
];

// Make format and control characters explicit in the exact parameter display. React still escapes the text.
function visibleParameter(value: string): string {
  return value.replace(
    /[\p{Cf}\p{Cc}\p{Zl}\p{Zp}]/gu,
    (char) => `<U+${char.codePointAt(0)!.toString(16).toUpperCase().padStart(4, '0')}>`,
  );
}

/**
 * The confirm dialog of one AI Proposal inside the assistant panel (F12 A-C): the operation, who can see the
 * result, the target record with its version, the complete exact parameters as inert text, the time left, and a
 * hash-bound Confirm plus Dismiss. There is no "confirm all"; after confirmation it shows the real outcome.
 */
export function ProposalCard({
  proposal,
  onChange,
}: {
  proposal: Proposal;
  onChange: (p: Proposal) => void;
}) {
  const { t } = useI18n();
  const [now, setNow] = useState(() => Date.now());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  useEffect(() => {
    if (proposal.state !== 'proposed') return undefined;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [proposal.state]);
  const seconds = remainingSeconds(proposal.expiresAt, now);
  const state = proposal.state === 'proposed' && seconds === 0 ? 'expired' : proposal.state;
  const outcome = proposalOutcomeKey(proposal.outcomeCode);
  const params = proposalParams(proposal);

  async function act(action: () => Promise<Proposal>) {
    if (busy) return;
    setBusy(true);
    setError(undefined);
    try {
      onChange(await action());
    } catch (e) {
      setError(e);
      if (isApiError(e) && reloadOn.includes(e.code)) {
        try {
          onChange(await aiApi.proposal(proposal.id));
        } catch {
          // keep the shown state; the error above explains what happened
        }
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="ai-proposal" aria-label={t('ai.proposal.title')}>
      <strong>{t('ai.proposal.title')}</strong>
      <dl>
        <dt>{t('ai.proposal.tool')}</dt>
        <dd>{proposal.tool}</dd>
        <dt>{t('ai.proposal.effect')}</dt>
        <dd>{t(proposal.effect === 'internal' ? 'ai.effect.internal' : 'ai.effect.unknown')}</dd>
        <dt>{t('ai.proposal.target')}</dt>
        <dd>
          {proposal.target.type === 'ticket' ? t('ai.resource.ticket') : proposal.target.type}{' '}
          {proposal.targetLabel} ({proposal.target.id}) ·{' '}
          {t('ai.proposal.version', { version: proposal.targetVersion })}
        </dd>
      </dl>
      <strong>{t('ai.proposal.parameters')}</strong>
      {params.length ? (
        <dl className="ai-proposal-params">
          {params.map((p) => (
            <div key={p.key}>
              <dt>{p.label ? t(p.label) : p.key}</dt>
              <dd>
                <pre className="ai-text">{visibleParameter(p.value)}</pre>
              </dd>
            </div>
          ))}
        </dl>
      ) : (
        <p>{t('ai.proposal.purged')}</p>
      )}
      <dl>
        <dt>{t('ai.proposal.state')}</dt>
        <dd role="status">{t(`ai.proposalState.${state}`)}</dd>
        {state === 'proposed' && (
          <>
            <dt>{t('ai.proposal.timeLeft')}</dt>
            <dd>
              {t('ai.proposal.remaining', {
                minutes: Math.floor(seconds / 60),
                seconds: seconds % 60,
              })}
            </dd>
          </>
        )}
        {outcome && (
          <>
            <dt>{t('ai.proposal.outcome')}</dt>
            <dd>{t(outcome)}</dd>
          </>
        )}
      </dl>
      {state === 'proposed' && <p>{t('ai.proposal.notice')}</p>}
      <div className="ai-actions">
        <Button
          variant="primary"
          busy={busy}
          disabled={busy || !canConfirm(proposal, now)}
          onClick={() => void act(() => aiApi.confirmProposal(proposal))}
        >
          {t('ai.proposal.confirm')}
        </Button>
        <Button
          disabled={busy || state !== 'proposed'}
          onClick={() => void act(() => aiApi.dismissProposal(proposal.id))}
        >
          {t('ai.proposal.dismiss')}
        </Button>
      </div>
      <AiError error={error} />
    </section>
  );
}
