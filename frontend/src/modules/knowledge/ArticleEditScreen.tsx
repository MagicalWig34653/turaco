import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate, useLocation } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { knowledgeApi } from './api';
import type { Article, Audience } from './types';

function ArticleForm({
  article,
  prefill,
}: {
  article?: Article | undefined;
  prefill?: { title: string; body: string };
}) {
  const { t } = useI18n();
  const [title, setTitle] = useState(article?.title ?? prefill?.title ?? '');
  const [summary, setSummary] = useState(article?.summary ?? '');
  const [body, setBody] = useState(article?.body ?? prefill?.body ?? '');
  const [audience, setAudience] = useState<Audience>(article?.audience ?? 'internal');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    const input = { title: title.trim(), summary: summary.trim(), body: body.trim(), audience };
    try {
      const saved = article
        ? await knowledgeApi.update(article.id, article.version, input)
        : await knowledgeApi.create(input);
      navigate(`/knowledge/${encodeURIComponent(saved.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <form className="form" onSubmit={(event) => void submit(event)}>
      {error ? <ApiErrorAlert error={error} /> : null}
      <TextField
        label={t('knowledge.field.title')}
        value={title}
        maxLength={200}
        required
        onChange={(event) => setTitle(event.target.value)}
      />
      <TextField
        label={t('knowledge.field.summary')}
        value={summary}
        maxLength={500}
        onChange={(event) => setSummary(event.target.value)}
      />
      <TextArea
        label={t('knowledge.field.body')}
        hint={t('knowledge.field.body.hint')}
        value={body}
        rows={14}
        maxLength={20000}
        required
        onChange={(event) => setBody(event.target.value)}
      />
      <Select
        label={t('knowledge.col.audience')}
        hint={t('knowledge.field.audience.hint')}
        value={audience}
        onChange={(event) => setAudience(event.target.value as Audience)}
        options={[
          { value: 'internal', label: t('knowledge.audience.internal') },
          { value: 'employee', label: t('knowledge.audience.employee') },
        ]}
      />
      <div className="form-actions">
        <Button
          type="submit"
          variant="primary"
          busy={busy}
          disabled={title.trim() === '' || body.trim() === ''}
        >
          {t('knowledge.save')}
        </Button>
      </div>
    </form>
  );
}

/** Creates an article (optionally prefilled from a resolved ticket) or edits an existing one. */
export function ArticleEditScreen({ id }: { id?: string }) {
  const { t } = useI18n();
  const { search } = useLocation();
  const params = new URLSearchParams(search);
  const loaded = useAsync(
    (signal) => (id ? knowledgeApi.get(id, signal) : Promise.resolve(undefined)),
    [id],
  );
  return (
    <>
      <PageHeader title={t(id ? 'knowledge.edit' : 'knowledge.new')} />
      <p>
        <Link to={id ? `/knowledge/${encodeURIComponent(id)}` : '/knowledge'}>
          {t('knowledge.back')}
        </Link>
      </p>
      {loaded.error ? <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} /> : null}
      {!loaded.loading && !loaded.error ? (
        <ArticleForm
          key={id ?? 'new'}
          article={loaded.data}
          prefill={{
            title: (params.get('title') ?? '').slice(0, 200),
            body: (params.get('body') ?? '').slice(0, 20000),
          }}
        />
      ) : null}
    </>
  );
}
