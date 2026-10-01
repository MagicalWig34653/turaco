import { useId } from 'react';
import type {
  InputHTMLAttributes,
  ReactNode,
  Ref,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
} from 'react';

type FieldFrameProps = {
  id: string;
  label: string;
  hint?: string | undefined;
  error?: string | undefined;
  children: ReactNode;
};

function FieldFrame({ id, label, hint, error, children }: FieldFrameProps) {
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {children}
      {hint ? (
        <p className="field-hint" id={`${id}-hint`}>
          {hint}
        </p>
      ) : null}
      {error ? (
        <p className="field-error" id={`${id}-error`}>
          {error}
        </p>
      ) : null}
    </div>
  );
}

function describedBy(id: string, hint?: string, error?: string): string | undefined {
  const ids = [hint ? `${id}-hint` : '', error ? `${id}-error` : ''].filter(Boolean);
  return ids.length ? ids.join(' ') : undefined;
}

type CommonProps = { label: string; hint?: string | undefined; error?: string | undefined };

export function TextField({
  label,
  hint,
  error,
  ref,
  ...rest
}: CommonProps & InputHTMLAttributes<HTMLInputElement> & { ref?: Ref<HTMLInputElement> }) {
  const id = useId();
  return (
    <FieldFrame id={id} label={label} hint={hint} error={error}>
      <input
        {...rest}
        ref={ref}
        id={id}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy(id, hint, error)}
      />
    </FieldFrame>
  );
}

export function TextArea({
  label,
  hint,
  error,
  ...rest
}: CommonProps & TextareaHTMLAttributes<HTMLTextAreaElement>) {
  const id = useId();
  return (
    <FieldFrame id={id} label={label} hint={hint} error={error}>
      <textarea
        {...rest}
        id={id}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy(id, hint, error)}
      />
    </FieldFrame>
  );
}

export type SelectOption = { value: string; label: string };

export function Select({
  label,
  hint,
  error,
  options,
  ...rest
}: CommonProps & SelectHTMLAttributes<HTMLSelectElement> & { options: readonly SelectOption[] }) {
  const id = useId();
  return (
    <FieldFrame id={id} label={label} hint={hint} error={error}>
      <select
        {...rest}
        id={id}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy(id, hint, error)}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    </FieldFrame>
  );
}

export function Checkbox({
  label,
  description,
  ...rest
}: Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> & {
  label: ReactNode;
  description?: ReactNode;
}) {
  const id = useId();
  return (
    <div className="checkbox">
      <input
        {...rest}
        type="checkbox"
        id={id}
        aria-describedby={description ? `${id}-d` : undefined}
      />
      <label htmlFor={id}>{label}</label>
      {description ? (
        <p className="field-hint" id={`${id}-d`}>
          {description}
        </p>
      ) : null}
    </div>
  );
}
