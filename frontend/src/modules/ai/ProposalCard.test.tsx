import { renderToStaticMarkup } from 'react-dom/server';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { I18nProvider } from '../../platform/i18n/I18nProvider';
import { ProposalCard } from './ProposalCard';
import {
  canConfirm,
  confirmBody,
  errorMessages,
  proposalOutcomeKey,
  proposalParams,
  remainingSeconds,
} from './model';
import type { Proposal } from './types';
import { en } from '../../platform/i18n/messages.en';
import { de } from '../../platform/i18n/messages.de';

const body =
  'Line one\n<img src="https://evil.example/x"> [click](javascript:alert(1)) ' + 'x'.repeat(1800);
const now = Date.parse('2026-10-12T10:00:00Z');
const proposal: Proposal = {
  id: '0199f000-0000-7000-8000-000000000001',
  tool: 'tickets.draft_internal_note',
  effect: 'internal',
  parameters: { ticketId: '0199f000-0000-7000-8000-000000000002', body },
  parametersHash: 'ab'.repeat(32),
  target: { type: 'ticket', id: '0199f000-0000-7000-8000-000000000002' },
  targetVersion: 4,
  targetLabel: 'SD-12 VPN drops',
  state: 'proposed',
  createdAt: '2026-10-12T09:50:00Z',
  expiresAt: '2026-10-12T10:05:00Z',
  confirmedAt: null,
  endedAt: null,
  attempt: 0,
  outcomeCode: null,
  result: null,
};

describe('proposal model', () => {
  it('confirms with the hash only, never with parameters', () => {
    expect(confirmBody(proposal)).toEqual({ parametersHash: proposal.parametersHash });
  });
  it('lists every parameter completely, sorted, without truncation', () => {
    const params = proposalParams(proposal);
    expect(params.map((p) => p.key)).toEqual(['body', 'ticketId']);
    expect(params[0]?.value).toBe(body);
    expect(params[0]?.label).toBe('ai.param.body');
    expect(proposalParams({ ...proposal, parameters: { count: 3 } })[0]).toEqual({
      key: 'count',
      label: null,
      value: '3',
    });
    expect(proposalParams({ ...proposal, parameters: null })).toEqual([]);
  });
  it('counts down to the deadline and stops offering Confirm after it', () => {
    expect(remainingSeconds(proposal.expiresAt, now)).toBe(300);
    expect(remainingSeconds(proposal.expiresAt, now + 301_000)).toBe(0);
    expect(remainingSeconds('garbage', now)).toBe(0);
    expect(canConfirm(proposal, now)).toBe(true);
    expect(canConfirm(proposal, now + 301_000)).toBe(false);
    expect(canConfirm({ ...proposal, state: 'executed' }, now)).toBe(false);
    expect(canConfirm({ ...proposal, state: 'dismissed', parameters: null }, now)).toBe(false);
    // A confirmed proposal with an unknown outcome may be confirmed again (the server never applies it twice).
    expect(canConfirm({ ...proposal, state: 'confirmed' }, now + 3_600_000)).toBe(true);
  });
  it('maps outcomes and errors to existing messages in both catalogs', () => {
    for (const code of [
      'ok',
      'ai.proposal_stale',
      'permission_denied',
      'forbidden',
      'not_found',
      'rejected',
      'parameters_tampered',
      'tool_unavailable',
      'ai.execution_unknown',
      'dismissed',
      'expired',
      'something_new',
    ]) {
      const key = proposalOutcomeKey(code);
      expect(key).not.toBeNull();
      expect(en[key!]).toBeTruthy();
      expect(de[key!]).toBeTruthy();
    }
    expect(proposalOutcomeKey(null)).toBeNull();
    for (const code of [
      'ai.proposal_hash_mismatch',
      'ai.proposal_not_pending',
      'ai.proposal_in_progress',
      'ai.execution_unknown',
    ]) {
      const key = errorMessages[code];
      expect(key).toBeTruthy();
      expect(en[key!]).toBeTruthy();
      expect(de[key!]).toBeTruthy();
    }
  });
});

describe('proposal confirm dialog', () => {
  beforeEach(() =>
    vi.stubGlobal('window', {
      navigator: { language: 'en' },
      localStorage: { getItem: () => null },
      setInterval: () => 0,
      clearInterval: () => undefined,
    }),
  );
  afterEach(() => vi.unstubAllGlobals());
  it('shows the exact content, visibility, target and version as inert text', () => {
    const html = renderToStaticMarkup(
      <I18nProvider>
        <ProposalCard proposal={proposal} onChange={() => undefined} />
      </I18nProvider>,
    );
    expect(html).toContain('x'.repeat(1800));
    expect(html).toContain('&lt;img');
    expect(html).toContain('[click](javascript:alert(1))');
    expect(html).not.toMatch(/<(img|script|a|iframe)\b/);
    expect(html).toContain('SD-12 VPN drops');
    expect(html).toContain(proposal.target.id);
    expect(html).not.toMatch(/confirm all/i);
  });
  it('shows invisible and bidi controls as escaped markers', () => {
    const html = renderToStaticMarkup(
      <I18nProvider>
        <ProposalCard
          proposal={{ ...proposal, parameters: { body: 'ab\u200bcd\u202eEF\u2066gh\u0001' } }}
          onChange={() => undefined}
        />
      </I18nProvider>,
    );
    expect(html).toContain('ab&lt;U+200B&gt;cd&lt;U+202E&gt;EF&lt;U+2066&gt;gh&lt;U+0001&gt;');
    expect(html).not.toMatch(/[\u200b\u202e\u2066\u0001]/u);
  });
  it('shows the real outcome and no enabled Confirm once the proposal ended', () => {
    const html = renderToStaticMarkup(
      <I18nProvider>
        <ProposalCard
          proposal={{ ...proposal, state: 'failed', outcomeCode: 'ai.proposal_stale' }}
          onChange={() => undefined}
        />
      </I18nProvider>,
    );
    const buttons = html.match(/<button[^>]*>/g) ?? [];
    expect(buttons.length).toBe(2);
    for (const b of buttons) expect(b).toMatch(/disabled/);
  });
});
