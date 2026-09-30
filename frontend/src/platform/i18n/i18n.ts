export type Locale = 'en' | 'de';

const messages = {
  en: {
    'app.name': 'Turaco',
    'app.subtitle': 'One workspace for service, assets, infrastructure and operations.',
    'nav.myWork': 'My Work',
    'nav.briefing': 'IT Briefing',
    'nav.serviceDesk': 'Service Desk',
    'nav.assets': 'Assets',
    'status.foundation': 'Foundation repository ready',
    'card.today.title': 'Today',
    'card.today.body': 'Tasks, tickets, approvals and changes will converge here.',
    'card.briefing.title': 'IT Briefing',
    'card.briefing.body': 'Security advisories, incidents, planned work and internal news.',
    'card.context.title': 'Shared context',
    'card.context.body':
      'Users, devices, services and infrastructure are connected instead of duplicated.',
    language: 'Language',
  },
  de: {
    'app.name': 'Turaco',
    'app.subtitle': 'Ein Arbeitsbereich für Service, Assets, Infrastruktur und Betrieb.',
    'nav.myWork': 'Meine Arbeit',
    'nav.briefing': 'IT-Briefing',
    'nav.serviceDesk': 'Service Desk',
    'nav.assets': 'Assets',
    'status.foundation': 'Grundstruktur ist bereit',
    'card.today.title': 'Heute',
    'card.today.body': 'Aufgaben, Tickets, Genehmigungen und Changes laufen hier zusammen.',
    'card.briefing.title': 'IT-Briefing',
    'card.briefing.body': 'Security-Meldungen, Störungen, geplante Arbeiten und interne News.',
    'card.context.title': 'Gemeinsamer Kontext',
    'card.context.body':
      'Benutzer, Geräte, Services und Infrastruktur werden verbunden statt dupliziert.',
    language: 'Sprache',
  },
} as const;

export type MessageKey = keyof (typeof messages)['en'];

export function translate(locale: Locale, key: MessageKey): string {
  return messages[locale][key] ?? messages.en[key];
}
