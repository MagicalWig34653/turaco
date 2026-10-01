import { useI18n } from '../platform/i18n/I18nProvider';
import type { MessageKey } from '../platform/i18n/i18n';

const cards: Array<{ title: MessageKey; body: MessageKey }> = [
  { title: 'card.today.title', body: 'card.today.body' },
  { title: 'card.briefing.title', body: 'card.briefing.body' },
  { title: 'card.context.title', body: 'card.context.body' },
];

export function Home() {
  const { t } = useI18n();
  return (
    <>
      <div className="page-header">
        <div>
          <p className="eyebrow">{t('status.foundation')}</p>
          <h1>{t('app.name')}</h1>
          <p className="subtitle">{t('app.subtitle')}</p>
        </div>
      </div>
      <section className="grid" aria-label={t('home.foundations')}>
        {cards.map((card) => (
          <article className="card" key={card.title}>
            <h2>{t(card.title)}</h2>
            <p>{t(card.body)}</p>
          </article>
        ))}
      </section>
    </>
  );
}
