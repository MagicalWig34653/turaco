import { useEffect, useMemo, useRef, useState } from 'react';
import type { KeyboardEvent } from 'react';
import { useI18n } from '../../i18n/I18nProvider';
import { navigate } from '../../router/Router';
import { useDebouncedValue } from '../hooks';
import type { PaletteCommand, PaletteSearch } from './paletteCommands';
import { arrangeResults, filterCommands, moveCommandSelection } from './paletteCommands';

const SEARCH_DELAY_MS = 250;

export function CommandPalette({
  open,
  onClose,
  commands,
  recentPaths,
  searchObjects,
}: {
  open: boolean;
  onClose: () => void;
  commands: readonly PaletteCommand[];
  recentPaths: readonly string[];
  /** Object search (tickets by reference, earlier number or title); absent when not available. */
  searchObjects?: PaletteSearch | undefined;
}) {
  const { t } = useI18n();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const results = useMemo(() => filterCommands(commands, query), [commands, query]);
  const recent = useMemo(
    () =>
      recentPaths
        .map((path) => commands.find((command) => command.path === path))
        .filter((item): item is PaletteCommand => Boolean(item)),
    [commands, recentPaths],
  );
  const debounced = useDebouncedValue(query.trim(), SEARCH_DELAY_MS);
  const [objects, setObjects] = useState<{
    query: string;
    items: PaletteCommand[];
    unavailable: boolean;
  }>({ query: '', items: [], unavailable: false });
  const searchRef = useRef(searchObjects);
  searchRef.current = searchObjects;
  const searchEnabled = searchObjects !== undefined;
  useEffect(() => {
    if (!open || !searchEnabled || !debounced) {
      setObjects({ query: '', items: [], unavailable: false });
      return undefined;
    }
    const controller = new AbortController();
    searchRef.current?.(debounced, controller.signal).then(
      (found) => {
        if (!controller.signal.aborted) setObjects({ query: debounced, ...found });
      },
      () => {
        if (!controller.signal.aborted)
          setObjects({ query: debounced, items: [], unavailable: true });
      },
    );
    return () => controller.abort();
  }, [open, searchEnabled, debounced]);
  // Results of an older query are not shown for the current text.
  const searching = searchEnabled && query.trim() !== '' && objects.query !== query.trim();
  const objectItems = searchEnabled && objects.query === query.trim() ? objects.items : [];
  const displayed = query.trim()
    ? arrangeResults(results, objectItems)
    : [
        ...recent,
        ...results.filter((item) => !recent.some((recentItem) => recentItem.id === item.id)),
      ];

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (open && !dialog.open) {
      setQuery('');
      setActive(0);
      dialog.showModal();
      inputRef.current?.focus();
    } else if (!open && dialog.open) dialog.close();
  }, [open]);

  function choose(command: PaletteCommand) {
    onClose();
    if (command.action) command.action();
    else navigate(command.path);
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
      event.preventDefault();
      setActive((current) => moveCommandSelection(current, displayed.length, event.key));
    } else if (event.key === 'Enter' && displayed[active]) {
      event.preventDefault();
      choose(displayed[active]);
    }
  }

  return (
    <dialog
      ref={dialogRef}
      className="turaco-command-dialog"
      aria-label={t('shell.commandTitle')}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        if (event.target === dialogRef.current) onClose();
      }}
    >
      <div className="turaco-command-head">
        <span aria-hidden="true">⌕</span>
        <input
          ref={inputRef}
          role="combobox"
          aria-label={t('shell.search')}
          aria-autocomplete="list"
          aria-expanded="true"
          aria-controls="turaco-command-results"
          aria-activedescendant={
            displayed[active] ? `turaco-command-${displayed[active].id}` : undefined
          }
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setActive(0);
          }}
          onKeyDown={onKeyDown}
          placeholder={t('shell.searchPlaceholder')}
          autoComplete="off"
        />
        <button
          type="button"
          className="turaco-command-close"
          onClick={onClose}
          aria-label={t('action.close')}
        >
          Esc
        </button>
      </div>
      <div className="turaco-command-meta">
        {query.trim() ? t('shell.navigation') : t('shell.recent')}
      </div>
      <div
        id="turaco-command-results"
        role="listbox"
        aria-label={t('shell.navigation')}
        className="turaco-command-results"
      >
        {displayed.length ? (
          displayed.map((command, index) => (
            <div key={command.id} role="presentation">
              {command.group && displayed[index - 1]?.group !== command.group ? (
                <div className="turaco-command-group" role="presentation">
                  {t('shell.group.tickets')}
                </div>
              ) : null}
              <div
                id={`turaco-command-${command.id}`}
                role="option"
                aria-selected={index === active}
                className="turaco-command-option"
                onMouseEnter={() => setActive(index)}
                onClick={() => choose(command)}
              >
                <span className="turaco-command-option-symbol" aria-hidden="true">
                  ↗
                </span>
                <span>{command.label}</span>
                <small>{command.group ? '' : command.path}</small>
              </div>
            </div>
          ))
        ) : searching ? null : (
          <p className="turaco-command-empty">{t('shell.noCommands')}</p>
        )}
        {searching ? (
          <p className="turaco-command-status" role="status">
            {t('shell.searching')}
          </p>
        ) : objects.unavailable && objects.query === query.trim() ? (
          <p className="turaco-command-status" role="status">
            {t('shell.searchUnavailable')}
          </p>
        ) : null}
      </div>
      <div className="turaco-command-footer">{t('shell.commandHint')}</div>
    </dialog>
  );
}
