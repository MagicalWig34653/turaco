import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react';
import { useI18n } from '../i18n/I18nProvider';
import {
  calendarWeeks,
  minuteOptions,
  parseLocal,
  toLocalValue,
  weekStartFor,
  type CalendarDay,
  type LocalParts,
} from './dateTimeModel';

type Props = {
  label: string;
  /** `YYYY-MM-DDTHH:mm` in local time, or an empty string. */
  value: string;
  onChange: (value: string) => void;
  hint?: string;
  required?: boolean;
};

const sameDay = (a: Pick<CalendarDay, 'year' | 'month' | 'day'>, b: LocalParts | null) =>
  !!b && a.year === b.year && a.month === b.month && a.day === b.day;

/**
 * Date and time picker formatted in the app locale (not the browser locale). Values keep the
 * `datetime-local` shape so callers convert them exactly as before.
 */
export function DateTimeField({ label, value, onChange, hint, required = false }: Props) {
  const { t, locale } = useI18n();
  const id = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const gridRef = useRef<HTMLDivElement>(null);
  const parts = parseLocal(value);
  const now = new Date();
  const [open, setOpen] = useState(false);
  const [view, setView] = useState({
    year: parts?.year ?? now.getFullYear(),
    month: parts?.month ?? now.getMonth() + 1,
  });
  const weekStart = weekStartFor(locale);
  const weeks = calendarWeeks(view.year, view.month, weekStart);
  const formatted = parts
    ? new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(
        new Date(parts.year, parts.month - 1, parts.day, parts.hour, parts.minute),
      )
    : '';
  const monthLabel = new Intl.DateTimeFormat(locale, { month: 'long', year: 'numeric' }).format(
    new Date(view.year, view.month - 1, 1),
  );
  const weekdayLabels = Array.from({ length: 7 }, (_, index) =>
    new Intl.DateTimeFormat(locale, { weekday: 'short' }).format(
      new Date(2026, 1, 1 + ((index + weekStart) % 7)),
    ),
  );
  const dayLabel = (day: CalendarDay) =>
    new Intl.DateTimeFormat(locale, { dateStyle: 'full' }).format(
      new Date(day.year, day.month - 1, day.day),
    );

  useEffect(() => {
    if (!open) return;
    const onPointer = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener('pointerdown', onPointer);
    requestAnimationFrame(() =>
      gridRef.current
        ?.querySelector<HTMLButtonElement>('[aria-pressed="true"], [data-today="true"]')
        ?.focus(),
    );
    return () => document.removeEventListener('pointerdown', onPointer);
  }, [open]);

  const close = () => {
    setOpen(false);
    triggerRef.current?.focus();
  };
  const choose = (day: CalendarDay) => {
    onChange(
      toLocalValue({
        year: day.year,
        month: day.month,
        day: day.day,
        hour: parts?.hour ?? 9,
        minute: parts?.minute ?? 0,
      }),
    );
    if (!day.inMonth) setView({ year: day.year, month: day.month });
  };
  const setTime = (hour: number, minute: number) => {
    const base = parts ?? {
      year: view.year,
      month: view.month,
      day: view.month === now.getMonth() + 1 && view.year === now.getFullYear() ? now.getDate() : 1,
      hour: 9,
      minute: 0,
    };
    onChange(toLocalValue({ ...base, hour, minute }));
  };
  const shiftMonth = (delta: number) => {
    const date = new Date(view.year, view.month - 1 + delta, 1);
    setView({ year: date.getFullYear(), month: date.getMonth() + 1 });
  };
  const onGridKey = (event: KeyboardEvent<HTMLDivElement>) => {
    const steps: Record<string, number> = {
      ArrowLeft: -1,
      ArrowRight: 1,
      ArrowUp: -7,
      ArrowDown: 7,
    };
    const step = steps[event.key];
    if (step === undefined) return;
    const buttons = [...(gridRef.current?.querySelectorAll<HTMLButtonElement>('button') ?? [])];
    const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
    const next = buttons[index + step];
    if (index >= 0 && next) {
      event.preventDefault();
      next.focus();
    }
  };

  return (
    <div
      className="datetime-field field"
      ref={rootRef}
      onKeyDown={(event) => {
        if (event.key === 'Escape' && open) {
          event.stopPropagation();
          close();
        }
      }}
    >
      <label htmlFor={id}>{label}</label>
      <div className="datetime-control">
        <button
          ref={triggerRef}
          id={id}
          type="button"
          className={`datetime-trigger${parts ? '' : ' is-empty'}`}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-required={required || undefined}
          aria-describedby={hint ? `${id}-hint` : undefined}
          onClick={() => {
            if (parts) setView({ year: parts.year, month: parts.month });
            setOpen(!open);
          }}
        >
          <span>{formatted || t('dateTime.placeholder')}</span>
          <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
            <rect x="3.5" y="5" width="17" height="15" rx="2" fill="none" stroke="currentColor" />
            <path d="M3.5 9.5h17M8 3v4M16 3v4" fill="none" stroke="currentColor" />
          </svg>
        </button>
        {parts && !required ? (
          <button
            type="button"
            className="datetime-clear"
            aria-label={t('dateTime.clear', { label })}
            onClick={() => onChange('')}
          >
            ×
          </button>
        ) : null}
      </div>
      {hint ? (
        <p className="field-hint" id={`${id}-hint`}>
          {hint}
        </p>
      ) : null}
      {open ? (
        <div className="datetime-popover" role="dialog" aria-label={label}>
          <div className="datetime-head">
            <button
              type="button"
              aria-label={t('dateTime.previousMonth')}
              onClick={() => shiftMonth(-1)}
            >
              ‹
            </button>
            <strong aria-live="polite">{monthLabel}</strong>
            <button
              type="button"
              aria-label={t('dateTime.nextMonth')}
              onClick={() => shiftMonth(1)}
            >
              ›
            </button>
          </div>
          <div className="datetime-weekdays" aria-hidden="true">
            {weekdayLabels.map((weekday) => (
              <span key={weekday}>{weekday}</span>
            ))}
          </div>
          <div className="datetime-grid" ref={gridRef} onKeyDown={onGridKey}>
            {weeks.flat().map((day) => {
              const today =
                day.year === now.getFullYear() &&
                day.month === now.getMonth() + 1 &&
                day.day === now.getDate();
              return (
                <button
                  type="button"
                  key={`${day.year}-${day.month}-${day.day}`}
                  className={day.inMonth ? undefined : 'is-outside'}
                  aria-pressed={sameDay(day, parts)}
                  aria-label={dayLabel(day)}
                  data-today={today || undefined}
                  onClick={() => choose(day)}
                >
                  {day.day}
                </button>
              );
            })}
          </div>
          <div className="datetime-time">
            <label>
              {t('dateTime.hour')}
              <select
                value={parts?.hour ?? 9}
                onChange={(event) => setTime(Number(event.target.value), parts?.minute ?? 0)}
              >
                {Array.from({ length: 24 }, (_, hour) => (
                  <option key={hour} value={hour}>
                    {String(hour).padStart(2, '0')}
                  </option>
                ))}
              </select>
            </label>
            <label>
              {t('dateTime.minute')}
              <select
                value={parts?.minute ?? 0}
                onChange={(event) => setTime(parts?.hour ?? 9, Number(event.target.value))}
              >
                {minuteOptions(parts?.minute).map((minute) => (
                  <option key={minute} value={minute}>
                    {String(minute).padStart(2, '0')}
                  </option>
                ))}
              </select>
            </label>
            <button type="button" className="btn btn-primary" onClick={close}>
              {t('dateTime.done')}
            </button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
