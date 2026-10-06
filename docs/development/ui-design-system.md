# UI design system

The frontend design foundation follows the approved workspace prototype in `turaco-ui-demo-v2`. The app keeps its existing routes, permission checks, data flow and specialist screens while screens migrate incrementally.

## Tokens and themes

`frontend/src/platform/ui/tokens.css` declares semantic CSS custom properties. Use `--surface-canvas`, `--surface-base`, `--surface-raised`, `--surface-muted-token`, `--text-primary`, `--text-secondary`, `--border-token`, `--accent`, `--focus-ring` and the `--state-{critical,warning,info,success,unknown}` foreground/background pairs. Rail, shadow, glow, spacing, radius, typography and motion tokens live alongside them. Existing `app.css` variable names are compatibility aliases to these tokens. Add new UI against semantic tokens, not the aliases or literal colors.

Theme preference is `auto`, `turaco`, `dark` or `cyberpunk`. `auto` follows `prefers-color-scheme` live; light resolves to Turaco and dark to Dark. The resolved theme is `data-theme` on `<html>`, applied before CSS paints by `index.html`. `ThemeProvider` owns preferences and persists them where storage is available. Theme changes presentation only. All themes use the same semantic status labels, routes and permission rules.

Cyberpunk is intentionally expressive: navy/violet surfaces, cyan and magenta highlights, restrained glow, a moving grid and short flicker. Glow never conveys meaning alone. Keep state foreground/background pairs and visible labels; do not recolor warning or critical as brand accents. The contrast test in `tokens.test.ts` checks WCAG AA text pairs numerically. Browser review at zoom and across viewport sizes remains necessary.

## Density and motion

Density (`comfortable` or `compact`) is independent of theme and language. Use `data-density` for row/cell spacing and padding. Do not shrink text or interactive targets below 32px. Coarse pointers and phone layouts may use larger targets.

Motion preference is `auto` or `reduced`; auto follows `prefers-reduced-motion` live. `data-motion='reduced'` zeroes duration tokens and cancels decorative animation. Use transform/opacity for entrance and press effects, keep animations interruptible, and never animate layout or rely on animation to communicate state. The `useReducedMotion` hook is available where JavaScript animation is needed.

## Shell, commands and context menus

`Shell.tsx` retains the route definitions and permission gating. The rail exposes existing navigation groups, the top bar exposes search, create actions and notifications, and the user menu exposes appearance, density, motion and language. Mobile navigation becomes a drawer. The command palette uses the existing routes, filters them by `canViewRoute`, and has a separate seam for future object-search results. Commands are navigation until an authorized operation is explicitly registered.

`ContextMenu` and `useContextMenu` provide pointer, ellipsis and keyboard entry. Pass only actions already appropriate for the current screen; a menu is never authorization. `DataTable` accepts `rowActions(row)` for per-row items. Menu labels must be translated, disabled items need a reason, and the underlying backend still authorizes actions. Do not intercept a browser context menu in an input or for selected text.

## Migrating a screen

1. Preserve its route, permission check, data loading and domain transitions.
2. Replace literal colors with semantic tokens and existing shared controls. Status uses a named semantic pair and a text label.
3. Use compact density to change spacing only, and use motion tokens for new animation.
4. Keep a keyboard path to every action, visible focus, logical heading order, and explicit loading, empty, error and unknown states.
5. Add English and German messages and tests for behavior. Run frontend typecheck, lint, tests and the repository checks.
