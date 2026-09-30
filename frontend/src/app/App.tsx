import { useState } from 'react';
import { type Locale, translate, type MessageKey } from '../platform/i18n/i18n';

const cards: Array<{ title: MessageKey; body: MessageKey }> = [
  { title: 'card.today.title', body: 'card.today.body' },
  { title: 'card.briefing.title', body: 'card.briefing.body' },
  { title: 'card.context.title', body: 'card.context.body' },
];

export function App() {
  const [locale, setLocale] = useState<Locale>('de');
  const t = (key: MessageKey) => translate(locale, key);

  return (
    <div className="shell">
      <aside className="sidebar" aria-label="Primary navigation">
        <div className="brand">{t('app.name')}</div>
        <nav>
          <a href="#today">{t('nav.myWork')}</a>
          <a href="#briefing">{t('nav.briefing')}</a>
          <a href="#service-desk">{t('nav.serviceDesk')}</a>
          <a href="#assets">{t('nav.assets')}</a>
        </nav>
      </aside>
      <main className="content">
        <header className="topbar">
          <div>
            <p className="eyebrow">{t('status.foundation')}</p>
            <h1>{t('app.name')}</h1>
            <p className="subtitle">{t('app.subtitle')}</p>
          </div>
          <label className="language">
            {t('language')}
            <select value={locale} onChange={(event) => setLocale(event.target.value as Locale)}>
              <option value="de">Deutsch</option>
              <option value="en">English</option>
            </select>
          </label>
        </header>
        <section className="grid" aria-label="Platform foundations">
          {cards.map((card) => (
            <article className="card" key={card.title}>
              <h2>{t(card.title)}</h2>
              <p>{t(card.body)}</p>
            </article>
          ))}
        </section>
      </main>
    </div>
  );
}
