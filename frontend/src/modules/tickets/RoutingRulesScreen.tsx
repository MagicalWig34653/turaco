import { useState, type FormEvent } from 'react';
import { api, type ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/messages.en';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { ConfirmDialog, Dialog } from '../../platform/ui/Dialog';
import { Checkbox, Select, TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Table } from '../../platform/ui/Table';
import { Card, EmptyState, Skeleton } from '../../platform/ui/Workspace';
import { ticketQueuesApi } from './api';
import {
  hasCondition,
  moveRule,
  parseKeywords,
  routingImpacts,
  type RoutingRule,
} from './routingModel';

const base = '/service-desk/routing-rules';

type TestResult = {
  queue: { id: string; key: string; name: string };
  defaultUsed: boolean;
  rule: RoutingRule | null;
};

const rulesApi = {
  list: (signal?: AbortSignal) => api.get<{ items: RoutingRule[] }>(base, { signal }),
  save: (rule: Partial<RoutingRule> & { queueId: string; name: string; enabled: boolean }) =>
    rule.id
      ? api.put<RoutingRule>(`${base}/${encodeURIComponent(rule.id)}`, {
          ...rule,
          expectedVersion: rule.version,
        })
      : api.post<RoutingRule>(base, rule),
  remove: (rule: RoutingRule) =>
    api.delete<void>(`${base}/${encodeURIComponent(rule.id)}`, {
      query: { expectedVersion: rule.version },
    }),
  reorder: (ids: string[]) => api.post<{ items: RoutingRule[] }>(`${base}/reorder`, { ids }),
  test: (body: { title: string; description: string; impact: string; patientImpact: boolean }) =>
    api.post<TestResult>(`${base}/test`, body),
};

/**
 * Queue routing rules (administration, servicedesk.queues.manage): an ordered list; the first enabled rule whose
 * conditions all hold decides the Queue of a new Ticket when nobody chose one. Rules never assign people.
 */
export function RoutingRulesScreen() {
  const { t } = useI18n();
  const rules = useAsync((signal) => rulesApi.list(signal), []);
  const queues = useAsync(async (signal) => (await ticketQueuesApi.list(false, signal)).items, []);
  const [edit, setEdit] = useState<RoutingRule | 'new'>();
  const [drop, setDrop] = useState<RoutingRule>();
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const items = rules.data?.items ?? [];
  const queueName = (id: string) => queues.data?.find((q) => q.id === id)?.name ?? id;

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await fn();
      rules.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  const move = (id: string, delta: -1 | 1) =>
    act(() =>
      rulesApi.reorder(
        moveRule(
          items.map((r) => r.id),
          id,
          delta,
        ),
      ),
    );

  return (
    <>
      <PageHeader
        eyebrow={t('routing.eyebrow')}
        title={t('routing.title')}
        intro={t('routing.intro')}
        actions={<Button onClick={() => setEdit('new')}>{t('routing.new')}</Button>}
      />
      {error ? <ApiErrorAlert error={error} /> : null}
      {rules.error ? (
        <ApiErrorAlert error={rules.error} onRetry={rules.reload} />
      ) : !rules.data ? (
        <Skeleton lines={4} />
      ) : items.length === 0 ? (
        <EmptyState title={t('routing.empty')} />
      ) : (
        <Card title={t('routing.rules')}>
          <Table>
            <caption className="visually-hidden">{t('routing.rules')}</caption>
            <thead>
              <tr>
                <th scope="col">{t('routing.order')}</th>
                <th scope="col">{t('routing.name')}</th>
                <th scope="col">{t('routing.conditions')}</th>
                <th scope="col">{t('routing.queue')}</th>
                <th scope="col">{t('routing.enabled')}</th>
                <th scope="col">
                  <span className="visually-hidden">{t('routing.actions')}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {items.map((rule, index) => (
                <tr key={rule.id}>
                  <td>{index + 1}</td>
                  <td>{rule.name}</td>
                  <td>
                    {[
                      rule.keywords.length > 0
                        ? t('routing.cond.keywords', { words: rule.keywords.join(', ') })
                        : '',
                      rule.impact
                        ? t('routing.cond.impact', {
                            impact: t(`report.impact.${rule.impact}` as MessageKey),
                          })
                        : '',
                      rule.patientImpact !== undefined
                        ? t(
                            rule.patientImpact
                              ? 'routing.cond.patientYes'
                              : 'routing.cond.patientNo',
                          )
                        : '',
                    ]
                      .filter(Boolean)
                      .join(' · ')}
                  </td>
                  <td>{queueName(rule.queueId)}</td>
                  <td>{rule.enabled ? t('routing.on') : t('routing.off')}</td>
                  <td>
                    <Button disabled={busy || index === 0} onClick={() => void move(rule.id, -1)}>
                      {t('routing.up')}
                    </Button>
                    <Button
                      disabled={busy || index === items.length - 1}
                      onClick={() => void move(rule.id, 1)}
                    >
                      {t('routing.down')}
                    </Button>
                    <Button onClick={() => setEdit(rule)}>{t('routing.edit')}</Button>
                    <Button onClick={() => setDrop(rule)}>{t('routing.delete')}</Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Card>
      )}
      <TestCard />
      {edit ? (
        <RuleDialog
          rule={edit === 'new' ? undefined : edit}
          queues={queues.data ?? []}
          onClose={() => setEdit(undefined)}
          onDone={() => {
            setEdit(undefined);
            rules.reload();
          }}
        />
      ) : null}
      {drop ? (
        <ConfirmDialog
          title={t('routing.deleteTitle')}
          message={t('routing.deleteBody', { name: drop.name })}
          confirmLabel={t('routing.delete')}
          danger
          busy={busy}
          onCancel={() => setDrop(undefined)}
          onConfirm={() => {
            const rule = drop;
            setDrop(undefined);
            void act(() => rulesApi.remove(rule));
          }}
        />
      ) : null}
    </>
  );
}

function TestCard() {
  const { t } = useI18n();
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [impact, setImpact] = useState('');
  const [patient, setPatient] = useState(false);
  const [result, setResult] = useState<TestResult>();
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const run = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      setResult(await rulesApi.test({ title, description, impact, patientImpact: patient }));
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title={t('routing.test')}>
      <form className="form" onSubmit={(event) => void run(event)}>
        <p className="field-hint">{t('routing.testHint')}</p>
        <TextField
          label={t('routing.testTitle')}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
        />
        <TextArea
          label={t('routing.testDescription')}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <Select
          label={t('routing.impact')}
          value={impact}
          onChange={(e) => setImpact(e.target.value)}
          options={[
            { value: '', label: t('routing.anyImpact') },
            ...routingImpacts.map((v) => ({
              value: v,
              label: t(`report.impact.${v}` as MessageKey),
            })),
          ]}
        />
        <Checkbox
          label={t('routing.testPatient')}
          checked={patient}
          onChange={(e) => setPatient(e.target.checked)}
        />
        <Button type="submit" busy={busy}>
          {t('routing.runTest')}
        </Button>
        {error ? <ApiErrorAlert error={error} /> : null}
        {result ? (
          <p role="status">
            {result.rule
              ? t('routing.testMatched', { rule: result.rule.name, queue: result.queue.name })
              : t('routing.testDefault', { queue: result.queue.name })}
          </p>
        ) : null}
      </form>
    </Card>
  );
}

function RuleDialog({
  rule,
  queues,
  onClose,
  onDone,
}: {
  rule: RoutingRule | undefined;
  queues: { id: string; name: string; status?: string }[];
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(rule?.name ?? '');
  const [queueId, setQueueId] = useState(rule?.queueId ?? '');
  const [words, setWords] = useState(rule?.keywords.join(', ') ?? '');
  const [impact, setImpact] = useState(rule?.impact ?? '');
  const [patient, setPatient] = useState<'' | 'yes' | 'no'>(
    rule?.patientImpact === undefined ? '' : rule.patientImpact ? 'yes' : 'no',
  );
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const keywords = parseKeywords(words);
  const patientImpact = patient === '' ? undefined : patient === 'yes';
  const valid =
    name.trim() !== '' && queueId !== '' && hasCondition({ keywords, impact, patientImpact });
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!valid) return;
    setBusy(true);
    setError(undefined);
    try {
      await rulesApi.save({
        ...(rule ? { id: rule.id, version: rule.version } : {}),
        queueId,
        name: name.trim(),
        keywords,
        ...(impact ? { impact } : {}),
        ...(patientImpact !== undefined ? { patientImpact } : {}),
        enabled,
      });
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title={rule ? t('routing.editTitle') : t('routing.new')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextField
          label={t('routing.name')}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <Select
          label={t('routing.queue')}
          value={queueId}
          onChange={(e) => setQueueId(e.target.value)}
          options={[
            { value: '', label: t('routing.chooseQueue') },
            ...queues.map((q) => ({ value: q.id, label: q.name })),
          ]}
        />
        <TextArea
          label={t('routing.keywords')}
          hint={t('routing.keywordsHint')}
          value={words}
          onChange={(e) => setWords(e.target.value)}
        />
        <Select
          label={t('routing.impact')}
          value={impact}
          onChange={(e) => setImpact(e.target.value)}
          options={[
            { value: '', label: t('routing.anyImpact') },
            ...routingImpacts.map((v) => ({
              value: v,
              label: t(`report.impact.${v}` as MessageKey),
            })),
          ]}
        />
        <Select
          label={t('routing.patientImpact')}
          value={patient}
          onChange={(e) => setPatient(e.target.value as '' | 'yes' | 'no')}
          options={[
            { value: '', label: t('routing.anyPatient') },
            { value: 'yes', label: t('routing.cond.patientYes') },
            { value: 'no', label: t('routing.cond.patientNo') },
          ]}
        />
        <Checkbox
          label={t('routing.enabled')}
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        {!hasCondition({ keywords, impact, patientImpact }) ? (
          <p className="field-hint">{t('routing.needCondition')}</p>
        ) : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!valid}>
            {t('action.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
