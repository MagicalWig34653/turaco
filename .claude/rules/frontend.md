---
paths:
  - "frontend/**/*.{ts,tsx,css,json}"
---
# Frontend rules

- TypeScript strict mode stays enabled.
- User-facing strings go through i18n; English is the fallback locale and German is maintained in parallel.
- Server state belongs in the API/query layer; do not create a giant global store.
- Employee UX hides internal ITSM terminology; IT workspace may expose operational detail.
- Reuse shared platform components for permissions, errors, search, notifications and layout.
- Do not add a component framework without an ADR.
- Accessibility and keyboard navigation are functional requirements.
