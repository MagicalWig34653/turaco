import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { knowledgeApi } from './api';

/** Articles that may already answer what the employee is typing, shown before a ticket is sent. */
export function Suggestions({ text }: { text: string }) {
  const { t } = useI18n();
  const q = useDebouncedValue(text.trim(), 400);
  const found = useAsync(
    async (signal) =>
      q.length < 4 ? [] : (await knowledgeApi.list(q, 'published', undefined, signal, 5)).items,
    [q],
  );
  if (!found.data || found.data.length === 0) return null;
  return (
    <aside className="field" aria-label={t('knowledge.suggestions')}>
      <p>
        <strong>{t('knowledge.suggestions')}</strong>
      </p>
      <ul className="plain-list">
        {found.data.map((a) => (
          <li key={a.id}>
            <Link to={`/knowledge/${encodeURIComponent(a.id)}`}>{a.title}</Link>
          </li>
        ))}
      </ul>
    </aside>
  );
}
