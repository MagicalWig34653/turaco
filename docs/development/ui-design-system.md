# UI design system

The frontend design foundation follows the approved workspace prototype in `turaco-ui-demo-v2`. The app keeps its existing routes, permission checks, data flow and specialist screens while screens migrate incrementally.

## Tokens and themes

`frontend/src/platform/ui/tokens.css` declares semantic CSS custom properties. Use `--surface-canvas`, `--surface-base`, `--surface-raised`, `--surface-muted-token`, `--text-primary`, `--text-secondary`, `--border-token`, `--accent`, `--focus-ring` and the `--state-{critical,warning,info,success,unknown}` foreground/background pairs. Rail, shadow, glow, spacing, radius, typography and motion tokens live alongside them. Existing `app.css` variable names are compatibility aliases to these tokens. Add new UI against semantic tokens, not the aliases or literal colors.

Theme preference is `auto`, `turaco`, `dark` or `cyberpunk`. `auto` follows `prefers-color-scheme` live; light resolves to Turaco and dark to Dark. The resolved theme is `data-theme` on `<html>`, applied before CSS paints by `index.html`. `ThemeProvider` owns preferences and persists them where storage is available. Theme changes presentation only. All themes use the same semantic status labels, routes and permission rules.

Cyberpunk is intentionally expressive: navy/violet surfaces, cyan and magenta highlights, restrained glow, a moving grid and short flicker. Glow never conveys meaning alone. Keep state foreground/background pairs and visible labels; do not recolor warning or critical as brand accents. The contrast test in `tokens.test.ts` checks WCAG AA text pairs numerically. Browser review at zoom and across viewport sizes remains necessary.

## Density and motion

Density (`comfortable` or `compact`) is independent of theme and language. Use `data-density` for row/cell spacing and padding. Do not shrink text or interactive targets below 32px. Coarse pointers and phone layouts may use larger targets.

Motion preference is `auto` or `reduced`; auto follows `prefers-reduced-motion` live. `data-motion='reduced'` zeroes duration tokens and cancels decorative animation. Use transform/opacity for entrance and press effects, keep animations interruptible, and never animate layout or rely on animation to communicate state. JavaScript animation checks the media query and `data-motion` before starting and stops if either requests reduced motion.

## Shell, commands and context menus

`Shell.tsx` retains the route definitions and permission gating. The rail exposes existing navigation groups, the top bar exposes search, create actions and notifications, and the user menu exposes appearance, density, motion and language. Mobile navigation becomes a drawer. The command palette uses the existing routes, filters them by `canViewRoute`, and has a separate seam for future object-search results. Commands are navigation until an authorized operation is explicitly registered.

`ContextMenu` and `useContextMenu` provide pointer, ellipsis and keyboard entry. Pass only actions already appropriate for the current screen; a menu is never authorization. `DataTable` accepts `rowActions(row)` for per-row items. Menu labels must be translated, disabled items need a reason, and the underlying backend still authorizes actions. Do not intercept a browser context menu in an input or for selected text.

## Migrating a screen

1. Preserve its route, permission check, data loading and domain transitions.
2. Replace literal colors with semantic tokens and existing shared controls. Status uses a named semantic pair and a text label.
3. Use compact density to change spacing only, and use motion tokens for new animation.
4. Keep a keyboard path to every action, visible focus, logical heading order, and explicit loading, empty, error and unknown states.
5. Add English and German messages and tests for behavior. Run frontend typecheck, lint, tests and the repository checks.

## Phase 2 shared workspace components

`frontend/src/platform/ui/Workspace.tsx` and `workspace.css` provide `Card`, `MetricCard`, `StatusBadge`, `Toolbar`, `FilterBar`, `EmptyState`, `Skeleton`, `Toast`, `Avatar`, `Tabs` and `SplitPane`. `PageHeader`, `DataTable`, `Button`, `Field` controls and `ContextMenu` remain the other shared primitives. Build screen composition from these components and semantic tokens. `MetricCard` requires a real drill-down route and an honest label: My Work reports the number of currently loaded tasks because the paged API does not return a total. Count-up is decorative; the accessible label always carries the final number and reduced motion displays it immediately. `Skeleton` has a loading status, `Toast` has a live role, and `EmptyState` provides a named empty result.

My Work sorts the existing Task read model by priority and due date and places permission-filtered Briefing feed entries alongside it. The queue's context menu opens the Task or copies its link. The Ticket queue's assign-to-me action is shown only to `tickets.manage` users for an open Ticket assigned to someone else, and calls the existing versioned assignment API. Backend authorization remains authoritative. Ticket detail keeps the existing transition, comment, known-error, runbook and external-sync flows; `SplitPane` places conversation beside context on wide screens and uses a list/context switch on narrow screens. The Briefing page displays computed entries by severity, preserving source and unavailable-source notices.

Native buttons, inputs, selects, date fields, checkboxes, radios, older tables and scrollbars use theme tokens, including when a specialist screen has not yet moved to shared JSX. New lists should use `DataTable`; it centers cell content, tightens compact rows and allows horizontal scrolling on phones. Dates should use `<time>` with a `dateTime` value and tabular numerals. Avoid showing internal UUIDs as visible link text when a localized status or human reference exists. The navigation rail is 64px when collapsed, has a distinct SVG per destination, and becomes a single-column modal drawer on phones. Its backdrop, Escape handling, focus containment and background inert state are required. User identity uses the display name when readable, with a person glyph fallback.

Animations use motion tokens and must stop under `prefers-reduced-motion` and the in-app Motion setting. Cyberpunk uses static, bounded neon glows on specific cards and actions; no route-sized border or view-transition snapshot should flash across the page. Disabled primary actions use semantic muted surface and text tokens, not opacity alone. All strings added by a shared component need English and German entries.

Changes, Security Advisories, Security Findings, Initiatives and Services list screens use `DataTable` and shared filter controls; specialist detail tables use the shared `Table` frame to keep existing row actions and gain the same theme, density and horizontal scroll behavior. Module actions use the shared `Button`; submit types are explicit so existing form behavior is preserved.

## Queue polish: tables, filters, Changes and reporting

`DataTable` is the common queue frame: sticky labelled headers, centered cells, optional natural/numeric sorting through `Column.sortValue`, reference chips, optional zebra rows (off by default), themed hover/selection, skeleton rows, an explicit empty state and an accessible horizontal scroll region. The first column stays pinned on narrow screens. Comfortable rows are 66px and compact rows are 42px; interactive controls stay at least 32px. Specialist `Table` frames share the same spacing and scroll conventions. `TableDate` shows localized relative time, preserves the source timestamp in `dateTime`, exposes the absolute timestamp in its title/accessibility label and refreshes each minute.

Sorting applies to loaded rows only and is labelled accordingly. Do not invent a total from a paged response: show the loaded count unless the endpoint supplies `totalCount`. `filterSummary` can name active filters beside the count. Explicit `rowActions` take precedence; otherwise the table can offer Open/Copy link from an existing rendered module Link. This fallback never constructs a route or mutation. Controlled `selection` exposes checkboxes and a bulk bar only when the caller supplies existing bulk actions. No module acquires a new bulk API operation.

`FilterBar` owns presentation, while screens retain their controlled values, callbacks, requests and permission checks. It accepts primary children, optional secondary controls, removable `activeFilters` and an optional atomic `onClear`. More than four controls go in the keyboard-operable All filters disclosure; submit buttons stay in the primary row. Explicit Apply forms continue to apply on submit. `DateFilter` uses a localized summary instead of an empty native date placeholder; expanding it exposes native keyboard/date editing. `SegmentedFilter` supports counts, which must be labelled as loaded-result counts when no aggregate exists. Never present a missing filter API as a working search box. Filters already initialized from URL query parameters use `useFilterQuery` to replace those parameters without adding one history entry per edit; unrelated query context is preserved.

Changes uses a four-part KPI strip with drilldowns over the currently loaded, server-filtered list. The scope is stated beside the strip; these are not global counts. Lifecycle steps highlight only the actual current state, without inventing historical approvals or completed steps. Failed/rejected/cancelled states have explicit alternatives. Detail actions still come from authoritative `allowedOperations`, with the primary transition above the workspace and secondary operations in a context menu. Approval, execution Tasks, impact and history retain their existing sources and permissions. Create/edit forms have two sections and validate maintenance-window order before submission.

Report a problem uses a guided form with equipment radio cards, the existing debounced knowledge suggestion request, and a persistent success receipt linking to the created Ticket and the existing ticket list. The response must succeed before the receipt appears. Pending controls and a submission guard prevent accidental repeat submission. Equipment icons are generic because the existing response contains product names but no product kinds; no extra data request or inferred product classification is introduced. The responsive layout stacks its help cards on phones. All new surfaces use semantic theme tokens; decorative motion stops for OS and in-app reduced-motion preferences.
